package foxess_test

// Black-box tests for the FoxESS client.  All network calls are intercepted
// with httptest.Server so no real credentials are needed.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sathyabhat/foxess-exporter/internal/foxess"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// apiResp wraps a result in the standard FoxESS envelope.
func apiResp(errno int, result any) any {
	return map[string]any{"errno": errno, "msg": "", "result": result}
}

// newServer creates a test server that responds to a single path with the
// given status code and JSON body.
func newServer(t *testing.T, path string, status int, body any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --------------------------------------------------------------------------
// Constructor
// --------------------------------------------------------------------------

func TestNew_DefaultBaseURL(t *testing.T) {
	c := foxess.New("key", "")
	assert.NotNil(t, c)
	// We can't read the baseURL field directly (unexported), but we can verify
	// it doesn't panic and that calls to an explicit URL work in other tests.
}

// --------------------------------------------------------------------------
// Request headers
// --------------------------------------------------------------------------

func TestClient_Headers_ContainsRequiredFields(t *testing.T) {
	var capturedHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResp(0, []foxess.Device{}))
	}))
	t.Cleanup(srv.Close)

	// Use ListDevices as a convenient endpoint to trigger a real request.
	// We need a pageResult wrapper the client expects.
	// Re-register with the right shape:
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResp(0, map[string]any{
			"devices": []any{}, "currentPage": 1, "pageSize": 20, "total": 0,
		}))
	}))
	t.Cleanup(srv2.Close)

	client := foxess.New("my-test-api-key", srv2.URL)
	_, err := client.ListDevices()
	require.NoError(t, err)

	assert.Equal(t, "my-test-api-key", capturedHeader.Get("token"), "token header")
	assert.Equal(t, "en", capturedHeader.Get("lang"), "lang header")
	assert.Equal(t, "application/json", capturedHeader.Get("Content-Type"), "Content-Type header")

	ts := capturedHeader.Get("timestamp")
	require.NotEmpty(t, ts, "timestamp header must be set")
	tsVal, err := strconv.ParseInt(ts, 10, 64)
	require.NoError(t, err, "timestamp must be a valid integer")
	assert.Greater(t, tsVal, int64(0), "timestamp must be positive")

	sig := capturedHeader.Get("signature")
	require.Len(t, sig, 32, "signature must be 32-char MD5 hex")
}

// --------------------------------------------------------------------------
// ListDevices
// --------------------------------------------------------------------------

func TestClient_ListDevices_SinglePage(t *testing.T) {
	devices := []map[string]any{
		{"deviceSN": "SN001", "moduleSN": "M001", "productType": "H3", "status": 1, "stationID": "ST1", "stationName": "My Home"},
		{"deviceSN": "SN002", "moduleSN": "M002", "productType": "H1", "status": 1, "stationID": "ST1", "stationName": "My Home"},
	}
	body := apiResp(0, map[string]any{
		"devices": devices, "currentPage": 1, "pageSize": 20, "total": 2,
	})
	srv := newServer(t, "/op/v0/device/list", http.StatusOK, body)

	got, err := foxess.New("key", srv.URL).ListDevices()
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "SN001", got[0].DeviceSN)
	assert.Equal(t, "My Home", got[0].StationName)
	assert.Equal(t, "H3", got[0].ProductType)
}

func TestClient_ListDevices_Pagination(t *testing.T) {
	// Server returns page 1 with 2 devices, total=3; page 2 with 1 device.
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&callCount, 1))
		w.Header().Set("Content-Type", "application/json")

		var result map[string]any
		switch n {
		case 1:
			result = map[string]any{
				"devices":     []map[string]any{{"deviceSN": "SN001"}, {"deviceSN": "SN002"}},
				"currentPage": 1, "pageSize": 2, "total": 3,
			}
		default:
			result = map[string]any{
				"devices":     []map[string]any{{"deviceSN": "SN003"}},
				"currentPage": 2, "pageSize": 2, "total": 3,
			}
		}
		_ = json.NewEncoder(w).Encode(apiResp(0, result))
	}))
	t.Cleanup(srv.Close)

	got, err := foxess.New("key", srv.URL).ListDevices()
	require.NoError(t, err)
	require.Len(t, got, 3, "should collect devices across all pages")
	assert.Equal(t, "SN001", got[0].DeviceSN)
	assert.Equal(t, "SN002", got[1].DeviceSN)
	assert.Equal(t, "SN003", got[2].DeviceSN)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount), "should have made 2 page requests")
}

func TestClient_ListDevices_Empty(t *testing.T) {
	body := apiResp(0, map[string]any{
		"devices": []any{}, "currentPage": 1, "pageSize": 20, "total": 0,
	})
	srv := newServer(t, "/op/v0/device/list", http.StatusOK, body)

	got, err := foxess.New("key", srv.URL).ListDevices()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestClient_ListDevices_APIError(t *testing.T) {
	body := apiResp(40257, map[string]any{})
	// Override msg:
	body = map[string]any{"errno": 40257, "msg": "invalid parameters", "result": nil}
	srv := newServer(t, "/op/v0/device/list", http.StatusOK, body)

	_, err := foxess.New("key", srv.URL).ListDevices()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "40257")
}

func TestClient_ListDevices_HTTPError(t *testing.T) {
	srv := newServer(t, "/op/v0/device/list", http.StatusUnauthorized, "unauthorized")

	_, err := foxess.New("key", srv.URL).ListDevices()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestClient_ListDevices_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json {{"))
	}))
	t.Cleanup(srv.Close)

	_, err := foxess.New("key", srv.URL).ListDevices()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

// --------------------------------------------------------------------------
// RealTimeData
// --------------------------------------------------------------------------

func TestClient_RealTimeData_Success(t *testing.T) {
	result := []map[string]any{
		{
			"deviceSN": "SN001",
			"datas": []map[string]any{
				{"variable": "pvPower", "unit": "kW", "name": "PVPower", "value": 3.5},
				{"variable": "SoC", "unit": "%", "name": "SoC", "value": 82.0},
				{"variable": "loadsPower", "unit": "kW", "name": "Load Power", "value": 1.2},
			},
		},
	}
	srv := newServer(t, "/op/v1/device/real/query", http.StatusOK, apiResp(0, result))

	got, err := foxess.New("key", srv.URL).RealTimeData([]string{"SN001"}, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "SN001", got[0].DeviceSN)
	require.Len(t, got[0].Datas, 3)

	byVar := make(map[string]foxess.RealDatum)
	for _, d := range got[0].Datas {
		byVar[d.Variable] = d
	}
	assert.InDelta(t, 3.5, byVar["pvPower"].Value, 1e-9)
	assert.InDelta(t, 82.0, byVar["SoC"].Value, 1e-9)
	assert.Equal(t, "kW", byVar["pvPower"].Unit)
}

func TestClient_RealTimeData_SendsSpecifiedVariables(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResp(0, []any{}))
	}))
	t.Cleanup(srv.Close)

	vars := []string{"pvPower", "SoC"}
	_, err := foxess.New("key", srv.URL).RealTimeData([]string{"SN001"}, vars)
	require.NoError(t, err)

	gotSNs, _ := capturedBody["sns"].([]any)
	require.Len(t, gotSNs, 1)
	assert.Equal(t, "SN001", gotSNs[0])

	gotVars, _ := capturedBody["variables"].([]any)
	require.Len(t, gotVars, 2)
	assert.Contains(t, gotVars, "pvPower")
	assert.Contains(t, gotVars, "SoC")
}

func TestClient_RealTimeData_APIError(t *testing.T) {
	srv := newServer(t, "/op/v1/device/real/query", http.StatusOK,
		map[string]any{"errno": 40400, "msg": "too many requests", "result": nil})

	_, err := foxess.New("key", srv.URL).RealTimeData([]string{"SN001"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "40400")
}

// --------------------------------------------------------------------------
// HistoryData
// --------------------------------------------------------------------------

func TestClient_HistoryData_Success(t *testing.T) {
	result := []map[string]any{
		{
			"deviceSN": "SN001",
			"datas": []map[string]any{
				{
					"variable": "pvPower", "unit": "kW", "name": "PVPower",
					"data": []map[string]any{
						{"value": 1.1, "time": "2024-01-12 10:00:00"},
						{"value": 2.2, "time": "2024-01-12 10:05:00"},
					},
				},
			},
		},
	}
	srv := newServer(t, "/op/v0/device/history/query", http.StatusOK, apiResp(0, result))

	begin := int64(1704153600000)
	end := begin + 3600000
	got, err := foxess.New("key", srv.URL).HistoryData("SN001", []string{"pvPower"}, begin, end)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "SN001", got.DeviceSN)
	require.Len(t, got.Datas, 1)
	assert.Equal(t, "pvPower", got.Datas[0].Variable)
	require.Len(t, got.Datas[0].Data, 2)
	assert.InDelta(t, 1.1, got.Datas[0].Data[0].Value, 1e-9)
	assert.Equal(t, "2024-01-12 10:00:00", got.Datas[0].Data[0].Time)
}

func TestClient_HistoryData_EmptyResult_ReturnsError(t *testing.T) {
	// API returns an empty slice — HistoryData should surface an error.
	srv := newServer(t, "/op/v0/device/history/query", http.StatusOK, apiResp(0, []any{}))

	_, err := foxess.New("key", srv.URL).HistoryData("SN001", nil, 0, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no history data")
}

func TestClient_HistoryData_SendsTimeRange(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResp(0, []map[string]any{
			{"deviceSN": "SN001", "datas": []any{}},
		}))
	}))
	t.Cleanup(srv.Close)

	begin := int64(1704067200000)
	end := int64(1704153600000)
	_, _ = foxess.New("key", srv.URL).HistoryData("SN001", nil, begin, end)

	assert.Equal(t, "SN001", capturedBody["sn"])
	assert.InDelta(t, float64(begin), capturedBody["begin"], 1.0)
	assert.InDelta(t, float64(end), capturedBody["end"], 1.0)
}

// --------------------------------------------------------------------------
// DailyReport
// --------------------------------------------------------------------------

func TestClient_DailyReport_Success(t *testing.T) {
	result := []map[string]any{
		{
			"variable": "generation", "unit": "kWh", "name": "Generation",
			"data": []map[string]any{
				{"index": 8, "value": 0.5},
				{"index": 9, "value": 1.2},
				{"index": 10, "value": 2.1},
			},
		},
		{
			"variable": "feedin", "unit": "kWh", "name": "Feed-in",
			"data": []map[string]any{
				{"index": 9, "value": 0.3},
			},
		},
	}
	srv := newServer(t, "/op/v0/device/report/query", http.StatusOK, apiResp(0, result))

	t0 := time.Date(2024, 1, 12, 0, 0, 0, 0, time.UTC)
	got, err := foxess.New("key", srv.URL).DailyReport("SN001", t0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "generation", got[0].Variable)
	assert.Equal(t, "kWh", got[0].Unit)
	require.Len(t, got[0].Data, 3)
	assert.Equal(t, 8, got[0].Data[0].Index)
	assert.InDelta(t, 0.5, got[0].Data[0].Value, 1e-9)
}

func TestClient_DailyReport_SendsCorrectDateAndVariables(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResp(0, []any{}))
	}))
	t.Cleanup(srv.Close)

	t0 := time.Date(2024, 3, 15, 12, 30, 0, 0, time.UTC)
	_, _ = foxess.New("key", srv.URL).DailyReport("SN001", t0)

	assert.Equal(t, "SN001", capturedBody["sn"])
	assert.Equal(t, float64(2024), capturedBody["year"])
	assert.Equal(t, float64(3), capturedBody["month"])
	assert.Equal(t, float64(15), capturedBody["day"])
	assert.Equal(t, "day", capturedBody["dimension"])

	// All ReportVariables must be requested.
	vars, _ := capturedBody["variables"].([]any)
	varSet := make(map[string]bool, len(vars))
	for _, v := range vars {
		varSet[v.(string)] = true
	}
	for _, expected := range foxess.ReportVariables {
		assert.True(t, varSet[expected], "expected report variable %q to be requested", expected)
	}
}

// --------------------------------------------------------------------------
// GetVariables
// --------------------------------------------------------------------------

func TestClient_GetVariables_Success(t *testing.T) {
	result := map[string]any{
		"pvPower": map[string]any{
			"unit": "kW",
			"name": map[string]any{"en": "PVPower", "zh_CN": "PV功率"},
		},
		"SoC": map[string]any{
			"unit": "%",
			"name": map[string]any{"en": "SoC", "zh_CN": "SoC"},
		},
	}
	srv := newServer(t, "/op/v0/device/variable/get", http.StatusOK, apiResp(0, result))

	got, err := foxess.New("key", srv.URL).GetVariables()
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "kW", got["pvPower"].Unit)
	assert.Equal(t, "SoC", got["SoC"].Name.EN)
}

func TestClient_GetVariables_APIError(t *testing.T) {
	body := map[string]any{"errno": 40256, "msg": "missing headers", "result": nil}
	srv := newServer(t, "/op/v0/device/variable/get", http.StatusOK, body)

	_, err := foxess.New("key", srv.URL).GetVariables()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "40256")
}
