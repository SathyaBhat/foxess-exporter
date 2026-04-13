package exporter_test

// Integration tests wire up a real foxess.Client and a real influx.Writer
// against httptest servers that mimic FoxESS Cloud and InfluxDB v3.
// Run with: go test ./internal/exporter/ -run Integration -v
// Or skip via: go test -short ./...

import (
	"context"
	"encoding/json"
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

	"github.com/sathyabhat/foxess-exporter/internal/config"
	"github.com/sathyabhat/foxess-exporter/internal/exporter"
	"github.com/sathyabhat/foxess-exporter/internal/foxess"
	"github.com/sathyabhat/foxess-exporter/internal/influx"
)

// --------------------------------------------------------------------------
// Fake FoxESS server
// --------------------------------------------------------------------------

type fakeFoxESS struct {
	mu sync.Mutex

	// Canned responses keyed by path.
	devices    []foxess.Device
	realtime   []foxess.RealQueryResult
	reportData []foxess.ReportQueryResult

	// Counts of calls received.
	listCalls     int
	realtimeCalls int
	reportCalls   int

	// done is closed once at least one realtime AND one report write have landed
	// on the fake InfluxDB side (set externally).
	done chan struct{}
}

func (f *fakeFoxESS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	type envelope struct {
		Errno  int    `json:"errno"`
		Msg    string `json:"msg"`
		Result any    `json:"result"`
	}

	write := func(result any) {
		_ = json.NewEncoder(w).Encode(envelope{Errno: 0, Result: result})
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.URL.Path {
	case "/op/v0/device/list":
		f.listCalls++
		write(map[string]any{
			"devices":     f.devices,
			"currentPage": 1,
			"pageSize":    20,
			"total":       len(f.devices),
		})

	case "/op/v1/device/real/query":
		f.realtimeCalls++
		write(f.realtime)

	case "/op/v0/device/report/query":
		f.reportCalls++
		write(f.reportData)

	default:
		http.NotFound(w, r)
	}
}

// --------------------------------------------------------------------------
// Fake InfluxDB server
// --------------------------------------------------------------------------

type fakeInfluxWrite struct {
	mu     sync.Mutex
	bodies []string

	// Notify once we have received at least realtimeWant+reportWant writes.
	realtimeWant int
	reportWant   int
	got          int
	notify       chan struct{}
	once         sync.Once
}

func newFakeInfluxWrite(realtimeWant, reportWant int) *fakeInfluxWrite {
	return &fakeInfluxWrite{
		realtimeWant: realtimeWant,
		reportWant:   reportWant,
		notify:       make(chan struct{}),
	}
}

func (f *fakeInfluxWrite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v2/write" {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, string(body))
	f.got++
	want := f.realtimeWant + f.reportWant
	if f.got >= want {
		f.once.Do(func() { close(f.notify) })
	}
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeInfluxWrite) allBodies() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.bodies, "\n")
}

// --------------------------------------------------------------------------
// Integration test
// --------------------------------------------------------------------------

func TestIntegration_FullCycle_WritesRealtimeAndReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// ── Set up fake FoxESS server ────────────────────────────────────────────
	fakeFox := &fakeFoxESS{
		devices: []foxess.Device{
			{DeviceSN: "INTEG-SN-001", StationName: "Integration Station", ProductType: "H3"},
		},
		realtime: []foxess.RealQueryResult{
			{
				DeviceSN: "INTEG-SN-001",
				Datas: []foxess.RealDatum{
					{Variable: "pvPower", Unit: "kW", Name: "PVPower", Value: 5.5},
					{Variable: "SoC", Unit: "%", Name: "SoC", Value: 91.0},
					{Variable: "batChargePower", Unit: "kW", Name: "Charge Power", Value: 2.1},
					{Variable: "loadsPower", Unit: "kW", Name: "Load Power", Value: 1.8},
					{Variable: "feedinPower", Unit: "kW", Name: "Feed-in", Value: 1.6},
					{Variable: "gridConsumptionPower", Unit: "kW", Name: "Grid Import", Value: 0.0},
				},
			},
		},
		reportData: []foxess.ReportQueryResult{
			{
				Variable: "generation", Unit: "kWh",
				Data: []foxess.ReportPoint{
					{Index: 8, Value: 1.5},
					{Index: 9, Value: 3.2},
					{Index: 10, Value: 2.8},
				},
			},
			{
				Variable: "feedin", Unit: "kWh",
				Data: []foxess.ReportPoint{
					{Index: 9, Value: 0.8},
					{Index: 10, Value: 1.1},
				},
			},
			{
				Variable: "gridConsumption", Unit: "kWh",
				Data: []foxess.ReportPoint{{Index: 7, Value: 0.2}},
			},
		},
	}
	foxSrv := httptest.NewServer(fakeFox)
	defer foxSrv.Close()

	// ── Set up fake InfluxDB server ──────────────────────────────────────────
	// We expect exactly 2 write calls: 1 realtime + 1 report batch.
	fakeInflux := newFakeInfluxWrite(1, 1)
	influxSrv := httptest.NewServer(fakeInflux)
	defer influxSrv.Close()

	// ── Wire up real client + writer ─────────────────────────────────────────
	foxClient := foxess.New("integ-test-key", foxSrv.URL)

	influxWriter, err := influx.New(influxSrv.URL, "integ-influx-token", "foxess", zap.NewNop())
	require.NoError(t, err)
	defer influxWriter.Close()

	cfg := &config.Config{
		FoxESS: config.FoxESSConfig{
			APIKey:   "integ-test-key",
			BaseURL:  foxSrv.URL,
			DeviceSN: "", // auto-discover
		},
		InfluxDB: config.InfluxDBConfig{
			Host:     influxSrv.URL,
			Token:    "integ-influx-token",
			Database: "foxess",
		},
		Exporter: config.ExporterConfig{
			RealtimeInterval: 10 * time.Minute, // long — only initial cycle fires
			ReportInterval:   10 * time.Minute,
		},
		Log: config.LogConfig{Level: "info"},
	}

	exp := exporter.New(cfg, foxClient, influxWriter, zap.NewNop())

	// ── Run exporter in background; cancel once both writes land ────────────
	ctx, cancel := newCancelOnNotify(fakeInflux.notify)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- exp.Run(ctx) }()

	select {
	case <-fakeInflux.notify:
		cancel() // both writes received — shut down the exporter
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for writes to reach fake InfluxDB")
	}

	// Wait for Run to return.
	select {
	case err := <-runErr:
		// context.Canceled is expected.
		if err != nil && !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("unexpected Run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	// ── Assertions ───────────────────────────────────────────────────────────

	// FoxESS server received expected calls.
	fakeFox.mu.Lock()
	assert.GreaterOrEqual(t, fakeFox.listCalls, 1, "ListDevices should have been called")
	assert.GreaterOrEqual(t, fakeFox.realtimeCalls, 1, "RealTimeData should have been called")
	assert.GreaterOrEqual(t, fakeFox.reportCalls, 1, "DailyReport should have been called")
	fakeFox.mu.Unlock()

	// InfluxDB received both write payloads.
	allWritten := fakeInflux.allBodies()

	assert.Contains(t, allWritten, "inverter_realtime",
		"realtime measurement should be present in InfluxDB writes")
	assert.Contains(t, allWritten, "device_sn=INTEG-SN-001",
		"device_sn tag should be written")
	assert.Contains(t, allWritten, "pvPower=",
		"pvPower field should be present")
	assert.Contains(t, allWritten, "SoC=",
		"SoC field should be present")

	assert.Contains(t, allWritten, "inverter_report",
		"report measurement should be present in InfluxDB writes")
	assert.Contains(t, allWritten, "variable=generation",
		"generation report variable tag should be written")
	assert.Contains(t, allWritten, "variable=feedin",
		"feedin report variable tag should be written")
	assert.Contains(t, allWritten, "value_kwh=",
		"value_kwh field should be present in report writes")
}

// --------------------------------------------------------------------------
// Integration test: auto-discovery picks the right device
// --------------------------------------------------------------------------

func TestIntegration_AutoDiscover_UsesFirstDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fakeFox := &fakeFoxESS{
		devices: []foxess.Device{
			{DeviceSN: "FIRST-DEVICE", StationName: "Station A"},
			{DeviceSN: "SECOND-DEVICE", StationName: "Station B"},
		},
		realtime: []foxess.RealQueryResult{
			{DeviceSN: "FIRST-DEVICE", Datas: []foxess.RealDatum{
				{Variable: "pvPower", Value: 1.0},
			}},
		},
		reportData: []foxess.ReportQueryResult{
			{Variable: "generation", Data: []foxess.ReportPoint{{Index: 9, Value: 0.5}}},
		},
	}
	foxSrv := httptest.NewServer(fakeFox)
	defer foxSrv.Close()

	fakeInflux := newFakeInfluxWrite(1, 1)
	influxSrv := httptest.NewServer(fakeInflux)
	defer influxSrv.Close()

	foxClient := foxess.New("key", foxSrv.URL)
	influxWriter, err := influx.New(influxSrv.URL, "tok", "foxess", zap.NewNop())
	require.NoError(t, err)
	defer influxWriter.Close()

	cfg := &config.Config{
		FoxESS:   config.FoxESSConfig{APIKey: "key", BaseURL: foxSrv.URL, DeviceSN: ""},
		InfluxDB: config.InfluxDBConfig{Host: influxSrv.URL, Token: "tok", Database: "foxess"},
		Exporter: config.ExporterConfig{RealtimeInterval: 10 * time.Minute, ReportInterval: 10 * time.Minute},
		Log:      config.LogConfig{Level: "info"},
	}

	exp := exporter.New(cfg, foxClient, influxWriter, zap.NewNop())
	ctx, cancel := newCancelOnNotify(fakeInflux.notify)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- exp.Run(ctx) }()

	select {
	case <-fakeInflux.notify:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for writes")
	}
	<-runErr

	// The writes should reference FIRST-DEVICE, not SECOND-DEVICE.
	allWritten := fakeInflux.allBodies()
	assert.Contains(t, allWritten, "device_sn=FIRST-DEVICE",
		"auto-discovery should pick the first device")
	assert.NotContains(t, allWritten, "SECOND-DEVICE",
		"second device should not appear in writes")
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// newCancelOnNotify returns a context + cancel that is also automatically
// cancelled when the done channel is closed.
func newCancelOnNotify(done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// --------------------------------------------------------------------------
// Backfill integration test
// --------------------------------------------------------------------------

// TestIntegration_Backfill_HistoryDataWrittenToInflux wires up a real
// foxess.Client against a fake FoxESS server that serves history data, and a
// hybrid setup where LastTimestamp is mocked (returns a past time to trigger
// backfill) while writes go to the real fake InfluxDB HTTP server.
func TestIntegration_Backfill_HistoryDataWrittenToInflux(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// ── Fake FoxESS server (history + realtime + list) ───────────────────────
	historyData := []foxess.HistoryVar{
		{
			Variable: "pvPower", Unit: "kW",
			Data: []foxess.HistoryPoint{
				{Time: "2024-01-12 08:00:00", Value: 0.2},
				{Time: "2024-01-12 08:05:00", Value: 1.5},
				{Time: "2024-01-12 08:10:00", Value: 2.8},
			},
		},
		{
			Variable: "SoC", Unit: "%",
			Data: []foxess.HistoryPoint{
				{Time: "2024-01-12 08:00:00", Value: 65.0},
				{Time: "2024-01-12 08:05:00", Value: 68.0},
				{Time: "2024-01-12 08:10:00", Value: 71.0},
			},
		},
	}

	backfillFox := &fakeFoxESS{
		devices: []foxess.Device{
			{DeviceSN: "BACKFILL-SN", StationName: "Backfill Station"},
		},
		realtime: []foxess.RealQueryResult{
			{DeviceSN: "BACKFILL-SN", Datas: []foxess.RealDatum{
				{Variable: "pvPower", Value: 3.0},
			}},
		},
		reportData: []foxess.ReportQueryResult{
			{Variable: "generation", Data: []foxess.ReportPoint{{Index: 8, Value: 1.2}}},
		},
	}

	var historyMu sync.Mutex
	var historyCalled int
	foxHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/op/v0/device/history/query" {
			historyMu.Lock()
			historyCalled++
			historyMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			type envelope struct {
				Errno  int    `json:"errno"`
				Msg    string `json:"msg"`
				Result any    `json:"result"`
			}
			_ = json.NewEncoder(w).Encode(envelope{Result: []map[string]any{{
				"deviceSN": "BACKFILL-SN",
				"datas":    historyData,
			}}})
			return
		}
		backfillFox.ServeHTTP(w, r)
	})
	foxSrv := httptest.NewServer(foxHandler)
	defer foxSrv.Close()

	// ── Fake InfluxDB: wait for 3 backfill writes + 1 live realtime write ────
	fakeIDB := newFakeInfluxWrite(4, 0)
	influxSrv := httptest.NewServer(fakeIDB)
	defer influxSrv.Close()

	// ── Wire up real client + writer + LastTimestamp wrapper ─────────────────
	foxClient := foxess.New("backfill-key", foxSrv.URL)

	realWriter, err := influx.New(influxSrv.URL, "tok", "foxess", zap.NewNop())
	require.NoError(t, err)
	defer realWriter.Close()

	// Wrap the real writer so LastTimestamp returns a controlled past time.
	wrapped := &backfillWriterWrapper{
		Writer: realWriter,
		lastTS: time.Now().Add(-2 * time.Hour), // 2h gap → triggers backfill
	}

	cfg := &config.Config{
		FoxESS:  config.FoxESSConfig{APIKey: "backfill-key", BaseURL: foxSrv.URL},
		InfluxDB: config.InfluxDBConfig{Host: influxSrv.URL, Token: "tok", Database: "foxess"},
		Exporter: config.ExporterConfig{
			RealtimeInterval: 10 * time.Minute,
			ReportInterval:   10 * time.Minute,
			BackfillEnabled:  true,
			BackfillMaxAge:   7 * 24 * time.Hour,
		},
		Log: config.LogConfig{Level: "info"},
	}

	exp := exporter.NewWithDeps(cfg, foxClient, wrapped, zap.NewNop())
	ctx, cancel := newCancelOnNotify(fakeIDB.notify)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- exp.Run(ctx) }()

	select {
	case <-fakeIDB.notify:
		cancel()
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for backfill + live writes to land")
	}
	<-runErr

	// ── Assertions ───────────────────────────────────────────────────────────
	historyMu.Lock()
	assert.GreaterOrEqual(t, historyCalled, 1, "history API must have been called for backfill")
	historyMu.Unlock()

	allWritten := fakeIDB.allBodies()
	assert.Contains(t, allWritten, "inverter_realtime", "realtime measurement present")
	assert.Contains(t, allWritten, "device_sn=BACKFILL-SN", "device tag present")
	assert.Contains(t, allWritten, "pvPower=", "pvPower field present")
	assert.Contains(t, allWritten, "SoC=", "SoC field present")
}

// backfillWriterWrapper wraps a real *influx.Writer but overrides LastTimestamp
// so integration tests can inject a controlled gap without a queryable InfluxDB.
type backfillWriterWrapper struct {
	*influx.Writer
	lastTS time.Time
}

func (b *backfillWriterWrapper) LastTimestamp(_ context.Context, _ string) (time.Time, error) {
	return b.lastTS, nil
}
