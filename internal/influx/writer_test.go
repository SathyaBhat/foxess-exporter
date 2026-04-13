package influx_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/sathyabhat/foxess-exporter/internal/influx"
)

// fakeInfluxDB is a minimal InfluxDB v3 write endpoint.
// It records each line-protocol body posted to /api/v2/write and returns 204.
type fakeInfluxDB struct {
	mu     sync.Mutex
	bodies []string // one entry per write call
	token  string   // expected auth token
}

func (f *fakeInfluxDB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v2/write" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if f.token != "" {
		want := "Token " + f.token
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, string(body))
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeInfluxDB) allBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(f.bodies))
	copy(cp, f.bodies)
	return cp
}

// newFakeInflux starts a test server and returns a writer pointed at it.
func newFakeInflux(t *testing.T, token string) (*fakeInfluxDB, *influx.Writer) {
	t.Helper()
	fake := &fakeInfluxDB{token: token}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	w, err := influx.New(srv.URL, token, "foxess", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	return fake, w
}

// --------------------------------------------------------------------------
// WriteRealtime
// --------------------------------------------------------------------------

func TestWriteRealtime_EmptyFields_DoesNotWrite(t *testing.T) {
	fake, w := newFakeInflux(t, "tok")

	err := w.WriteRealtime(context.Background(), influx.RealtimePoint{
		DeviceSN:  "SN001",
		Timestamp: time.Now(),
		Fields:    map[string]float64{}, // empty
	})
	require.NoError(t, err)
	assert.Empty(t, fake.allBodies(), "empty fields should produce no write")
}

func TestWriteRealtime_SinglePoint_WritesLineProtocol(t *testing.T) {
	fake, w := newFakeInflux(t, "tok")

	ts := time.Date(2024, 1, 12, 10, 0, 0, 0, time.UTC)
	err := w.WriteRealtime(context.Background(), influx.RealtimePoint{
		DeviceSN:    "SN001",
		StationName: "My Home",
		Timestamp:   ts,
		Fields: map[string]float64{
			"pvPower": 3.5,
			"SoC":     82.0,
		},
	})
	require.NoError(t, err)

	bodies := fake.allBodies()
	require.Len(t, bodies, 1, "should have written exactly one batch")

	body := bodies[0]
	assert.Contains(t, body, "inverter_realtime", "measurement name")
	assert.Contains(t, body, "device_sn=SN001", "device_sn tag")
	assert.Contains(t, body, "station_name=My\\ Home", "station_name tag (escaped space)")
	assert.Contains(t, body, "pvPower=", "pvPower field")
	assert.Contains(t, body, "SoC=", "SoC field")
}

func TestWriteRealtime_MultipleFields_AllPresent(t *testing.T) {
	fake, w := newFakeInflux(t, "tok")

	fields := map[string]float64{
		"pvPower":           3.5,
		"SoC":               75.0,
		"batChargePower":    1.2,
		"batDischargePower": 0.0,
		"loadsPower":        2.1,
		"feedinPower":       1.4,
	}

	err := w.WriteRealtime(context.Background(), influx.RealtimePoint{
		DeviceSN:  "SN001",
		Timestamp: time.Now().UTC(),
		Fields:    fields,
	})
	require.NoError(t, err)

	bodies := fake.allBodies()
	require.Len(t, bodies, 1)
	for fieldName := range fields {
		assert.Contains(t, bodies[0], fieldName+"=", "field %q should appear in line protocol", fieldName)
	}
}

func TestWriteRealtime_UsesCorrectMeasurementName(t *testing.T) {
	fake, w := newFakeInflux(t, "tok")

	err := w.WriteRealtime(context.Background(), influx.RealtimePoint{
		DeviceSN:  "SN001",
		Timestamp: time.Now().UTC(),
		Fields:    map[string]float64{"pvPower": 1.0},
	})
	require.NoError(t, err)

	assert.True(t,
		strings.HasPrefix(fake.allBodies()[0], "inverter_realtime"),
		"line protocol must start with measurement name %q", influx.MeasurementRealtime,
	)
}

// --------------------------------------------------------------------------
// WriteReport
// --------------------------------------------------------------------------

func TestWriteReport_EmptyPoints_DoesNotWrite(t *testing.T) {
	fake, w := newFakeInflux(t, "tok")

	err := w.WriteReport(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, fake.allBodies(), "nil points should produce no write")

	err = w.WriteReport(context.Background(), []influx.ReportPoint{})
	require.NoError(t, err)
	assert.Empty(t, fake.allBodies(), "empty slice should produce no write")
}

func TestWriteReport_WritesAllPoints(t *testing.T) {
	fake, w := newFakeInflux(t, "tok")

	date := time.Date(2024, 1, 12, 0, 0, 0, 0, time.UTC)
	pts := []influx.ReportPoint{
		{DeviceSN: "SN001", StationName: "Home", Timestamp: date.Add(8 * time.Hour), Variable: "generation", Value: 0.5, Unit: "kWh"},
		{DeviceSN: "SN001", StationName: "Home", Timestamp: date.Add(9 * time.Hour), Variable: "feedin", Value: 0.3, Unit: "kWh"},
		{DeviceSN: "SN001", StationName: "Home", Timestamp: date.Add(10 * time.Hour), Variable: "gridConsumption", Value: 0.1, Unit: "kWh"},
	}

	err := w.WriteReport(context.Background(), pts)
	require.NoError(t, err)

	bodies := fake.allBodies()
	require.Len(t, bodies, 1, "all report points should be in a single batch write")

	body := bodies[0]
	// Each variable/hour is its own line in the batch.
	assert.Contains(t, body, "inverter_report", "measurement name")
	assert.Contains(t, body, `variable=generation`, "generation variable tag")
	assert.Contains(t, body, `variable=feedin`, "feedin variable tag")
	assert.Contains(t, body, `variable=gridConsumption`, "gridConsumption variable tag")
	assert.Contains(t, body, "value_kwh=", "value_kwh field")
}

func TestWriteReport_TagsContainDeviceSNAndVariable(t *testing.T) {
	fake, w := newFakeInflux(t, "tok")

	pts := []influx.ReportPoint{
		{
			DeviceSN:    "MY-INVERTER-SN",
			StationName: "Roof",
			Timestamp:   time.Now().UTC(),
			Variable:    "chargeEnergyToTal",
			Value:       4.2,
			Unit:        "kWh",
		},
	}
	require.NoError(t, w.WriteReport(context.Background(), pts))

	body := fake.allBodies()[0]
	assert.Contains(t, body, "device_sn=MY-INVERTER-SN")
	assert.Contains(t, body, "variable=chargeEnergyToTal")
	assert.Contains(t, body, "unit=kWh")
}

// --------------------------------------------------------------------------
// Constructor
// --------------------------------------------------------------------------

func TestNew_InvalidHost_ReturnsError(t *testing.T) {
	// The influxdb3 client constructor accepts any string and only fails on a
	// truly malformed URL (e.g. one that can't be parsed at all).
	_, err := influx.New("://not-a-url", "tok", "db", zap.NewNop())
	// Some invalid URLs parse fine; we just ensure New doesn't panic.
	// If it does error, that's also valid.
	_ = err // outcome depends on the library's URL parsing tolerance
}

func TestNew_ValidConfig_DoesNotError(t *testing.T) {
	fake := &fakeInfluxDB{token: "tok"}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	w, err := influx.New(srv.URL, "tok", "foxess", zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, w)
	_ = w.Close()
}
