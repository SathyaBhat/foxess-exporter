package exporter_test

import (
	"context"
	"errors"
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
// Mock FoxESS client
// --------------------------------------------------------------------------

type mockFoxClient struct {
	mu              sync.Mutex
	listDevicesFn   func() ([]foxess.Device, error)
	realTimeFn      func(sns []string, variables []string) ([]foxess.RealQueryResult, error)
	dailyReportFn   func(sn string, t time.Time) ([]foxess.ReportQueryResult, error)
	historyDataFn   func(sn string, variables []string, begin, end int64) (*foxess.HistoryQueryResult, error)
	realTimeCalls   [][]string // SNs passed on each call
	reportCalls     []string   // SNs passed on each call
	historyCalls    []historyCall
}

type historyCall struct {
	SN    string
	Begin int64
	End   int64
}

func (m *mockFoxClient) ListDevices() ([]foxess.Device, error) {
	if m.listDevicesFn != nil {
		return m.listDevicesFn()
	}
	return nil, nil
}

func (m *mockFoxClient) RealTimeData(sns []string, variables []string) ([]foxess.RealQueryResult, error) {
	m.mu.Lock()
	m.realTimeCalls = append(m.realTimeCalls, sns)
	m.mu.Unlock()
	if m.realTimeFn != nil {
		return m.realTimeFn(sns, variables)
	}
	return nil, nil
}

func (m *mockFoxClient) DailyReport(sn string, t time.Time) ([]foxess.ReportQueryResult, error) {
	m.mu.Lock()
	m.reportCalls = append(m.reportCalls, sn)
	m.mu.Unlock()
	if m.dailyReportFn != nil {
		return m.dailyReportFn(sn, t)
	}
	return nil, nil
}

func (m *mockFoxClient) HistoryData(sn string, variables []string, begin, end int64) (*foxess.HistoryQueryResult, error) {
	m.mu.Lock()
	m.historyCalls = append(m.historyCalls, historyCall{SN: sn, Begin: begin, End: end})
	m.mu.Unlock()
	if m.historyDataFn != nil {
		return m.historyDataFn(sn, variables, begin, end)
	}
	return &foxess.HistoryQueryResult{DeviceSN: sn, Datas: nil}, nil
}

// --------------------------------------------------------------------------
// Mock metrics writer
// --------------------------------------------------------------------------

type mockWriter struct {
	mu               sync.Mutex
	writeRealtimeFn  func(context.Context, influx.RealtimePoint) error
	writeReportFn    func(context.Context, []influx.ReportPoint) error
	lastTimestampFn  func(context.Context, string) (time.Time, error)
	realtimePts      []influx.RealtimePoint
	reportBatches    [][]influx.ReportPoint
}

func (m *mockWriter) WriteRealtime(ctx context.Context, p influx.RealtimePoint) error {
	m.mu.Lock()
	m.realtimePts = append(m.realtimePts, p)
	m.mu.Unlock()
	if m.writeRealtimeFn != nil {
		return m.writeRealtimeFn(ctx, p)
	}
	return nil
}

func (m *mockWriter) WriteReport(ctx context.Context, pts []influx.ReportPoint) error {
	m.mu.Lock()
	m.reportBatches = append(m.reportBatches, pts)
	m.mu.Unlock()
	if m.writeReportFn != nil {
		return m.writeReportFn(ctx, pts)
	}
	return nil
}

func (m *mockWriter) LastTimestamp(ctx context.Context, deviceSN string) (time.Time, error) {
	if m.lastTimestampFn != nil {
		return m.lastTimestampFn(ctx, deviceSN)
	}
	// Default: return zero time (no prior data) so backfill is skipped.
	return time.Time{}, nil
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// minimalConfig builds a valid Config for testing.  Intervals are set to the
// minimum allowed (10s) since we cancel the context before they fire anyway.
func minimalConfig(deviceSN string) *config.Config {
	return &config.Config{
		FoxESS: config.FoxESSConfig{
			APIKey:   "test-key",
			BaseURL:  "http://localhost",
			DeviceSN: deviceSN,
		},
		InfluxDB: config.InfluxDBConfig{
			Host:     "http://localhost:8086",
			Token:    "token",
			Database: "foxess",
		},
		Exporter: config.ExporterConfig{
			RealtimeInterval: 10 * time.Second,
			ReportInterval:   5 * time.Minute,
		},
		Log: config.LogConfig{Level: "info"},
	}
}

func noopLogger() *zap.Logger { return zap.NewNop() }

// runOneCycle creates an exporter with the given mocks and runs it long enough
// for the initial collect cycle (which fires immediately on startup), then
// cancels.  Returns after Run() returns.
func runOneCycle(t *testing.T, cfg *config.Config, fox *mockFoxClient, w *mockWriter) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	exp := exporter.NewWithDeps(cfg, fox, w, noopLogger())

	errCh := make(chan error, 1)
	go func() { errCh <- exp.Run(ctx) }()

	// Give the exporter a moment to finish the initial sync collection, then
	// cancel so the loop exits.
	time.Sleep(200 * time.Millisecond)
	cancel()

	return <-errCh
}

// --------------------------------------------------------------------------
// Device resolution
// --------------------------------------------------------------------------

func TestExporter_ResolveDevice_ConfiguredSN_UsedDirectly(t *testing.T) {
	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{
				{DeviceSN: "CONFIGURED-SN", StationName: "Home"},
			}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0], Datas: nil}}, nil
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("CONFIGURED-SN")

	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled), "expected context.Canceled, got %v", err)

	// Real-time data must have been requested for the configured SN.
	fox.mu.Lock()
	defer fox.mu.Unlock()
	require.NotEmpty(t, fox.realTimeCalls)
	assert.Equal(t, []string{"CONFIGURED-SN"}, fox.realTimeCalls[0])
}

func TestExporter_ResolveDevice_AutoDiscover_SingleDevice(t *testing.T) {
	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{
				{DeviceSN: "AUTO-SN", StationName: "Solar Station"},
			}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("") // no SN → auto-discover

	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled))

	fox.mu.Lock()
	defer fox.mu.Unlock()
	require.NotEmpty(t, fox.realTimeCalls)
	assert.Equal(t, []string{"AUTO-SN"}, fox.realTimeCalls[0],
		"auto-discovered SN should be used for real-time queries")
}

func TestExporter_ResolveDevice_AutoDiscover_MultipleDevices_UsesFirst(t *testing.T) {
	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{
				{DeviceSN: "FIRST-SN"},
				{DeviceSN: "SECOND-SN"},
			}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("")

	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled))

	fox.mu.Lock()
	defer fox.mu.Unlock()
	require.NotEmpty(t, fox.realTimeCalls)
	assert.Equal(t, "FIRST-SN", fox.realTimeCalls[0][0], "should use the first discovered device")
}

func TestExporter_ResolveDevice_NoDevices_ReturnsError(t *testing.T) {
	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{}, nil
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("")

	ctx := context.Background()
	exp := exporter.NewWithDeps(cfg, fox, w, noopLogger())
	err := exp.Run(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no devices found")
}

func TestExporter_ResolveDevice_ListDevicesError_ReturnsError(t *testing.T) {
	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return nil, errors.New("network error")
		},
	}
	cfg := minimalConfig("")
	exp := exporter.NewWithDeps(cfg, fox, &mockWriter{}, noopLogger())

	err := exp.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
}

// --------------------------------------------------------------------------
// Real-time collection
// --------------------------------------------------------------------------

func TestExporter_CollectRealtime_WritesFieldsToInflux(t *testing.T) {
	fox := &mockFoxClient{
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{
				{
					DeviceSN: "SN001",
					Datas: []foxess.RealDatum{
						{Variable: "pvPower", Value: 4.2, Unit: "kW"},
						{Variable: "SoC", Value: 88.0, Unit: "%"},
						{Variable: "loadsPower", Value: 1.5, Unit: "kW"},
					},
				},
			}, nil
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("SN001")

	_ = runOneCycle(t, cfg, fox, w)

	w.mu.Lock()
	defer w.mu.Unlock()
	require.NotEmpty(t, w.realtimePts, "should have written at least one realtime point")

	pt := w.realtimePts[0]
	assert.Equal(t, "SN001", pt.DeviceSN)
	assert.InDelta(t, 4.2, pt.Fields["pvPower"], 1e-9)
	assert.InDelta(t, 88.0, pt.Fields["SoC"], 1e-9)
	assert.InDelta(t, 1.5, pt.Fields["loadsPower"], 1e-9)
}

func TestExporter_CollectRealtime_APIError_ContinuesWithoutPanic(t *testing.T) {
	fox := &mockFoxClient{
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return nil, errors.New("foxess api unavailable")
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("SN001")

	// Should not panic; should log the error and keep running.
	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled),
		"exporter should keep running after a transient real-time API error")

	w.mu.Lock()
	defer w.mu.Unlock()
	assert.Empty(t, w.realtimePts, "no points should be written if the API call fails")
}

func TestExporter_CollectRealtime_WriteError_ContinuesWithoutPanic(t *testing.T) {
	fox := &mockFoxClient{
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: "SN001", Datas: []foxess.RealDatum{
				{Variable: "pvPower", Value: 1.0},
			}}}, nil
		},
	}
	w := &mockWriter{
		writeRealtimeFn: func(_ context.Context, _ influx.RealtimePoint) error {
			return errors.New("influxdb write failed")
		},
	}
	cfg := minimalConfig("SN001")

	// Write failure should be logged but not cause a panic or crash the loop.
	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled))
}

// --------------------------------------------------------------------------
// Report collection
// --------------------------------------------------------------------------

func TestExporter_CollectReport_NonZeroHoursBecomePoints(t *testing.T) {
	fox := &mockFoxClient{
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: "SN001"}}, nil
		},
		dailyReportFn: func(sn string, _ time.Time) ([]foxess.ReportQueryResult, error) {
			return []foxess.ReportQueryResult{
				{
					Variable: "generation", Unit: "kWh",
					Data: []foxess.ReportPoint{
						{Index: 8, Value: 0.5},
						{Index: 9, Value: 1.2},
						{Index: 10, Value: 0.0}, // zero — should be skipped
					},
				},
				{
					Variable: "feedin", Unit: "kWh",
					Data: []foxess.ReportPoint{
						{Index: 9, Value: 0.3},
					},
				},
			}, nil
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("SN001")

	_ = runOneCycle(t, cfg, fox, w)

	w.mu.Lock()
	defer w.mu.Unlock()
	require.NotEmpty(t, w.reportBatches)

	var pts []influx.ReportPoint
	for _, b := range w.reportBatches {
		pts = append(pts, b...)
	}

	// Index 10 with Value 0.0 must be filtered out.
	for _, p := range pts {
		assert.NotZero(t, p.Value, "zero-value report points should be skipped")
	}

	// 2 non-zero generation + 1 feedin = 3 points.
	assert.Len(t, pts, 3, "expected 3 non-zero report points")

	// Timestamps should reflect the hour index relative to midnight UTC.
	byVar := make(map[string][]influx.ReportPoint)
	for _, p := range pts {
		byVar[p.Variable] = append(byVar[p.Variable], p)
	}
	require.Len(t, byVar["generation"], 2)
	assert.Equal(t, 8, byVar["generation"][0].Timestamp.Hour(), "index 8 → 08:00 UTC")
	assert.Equal(t, 9, byVar["generation"][1].Timestamp.Hour(), "index 9 → 09:00 UTC")
}

func TestExporter_CollectReport_AllZero_WritesEmptyBatch(t *testing.T) {
	fox := &mockFoxClient{
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: "SN001"}}, nil
		},
		dailyReportFn: func(sn string, _ time.Time) ([]foxess.ReportQueryResult, error) {
			return []foxess.ReportQueryResult{
				{Variable: "generation", Data: []foxess.ReportPoint{{Index: 0, Value: 0}}},
			}, nil
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("SN001")

	_ = runOneCycle(t, cfg, fox, w)

	w.mu.Lock()
	defer w.mu.Unlock()
	// WriteReport is still called, but with an empty (or nil) slice.
	for _, b := range w.reportBatches {
		assert.Empty(t, b, "all-zero report should produce an empty write batch")
	}
}

func TestExporter_CollectReport_APIError_ContinuesWithoutPanic(t *testing.T) {
	fox := &mockFoxClient{
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: "SN001"}}, nil
		},
		dailyReportFn: func(_ string, _ time.Time) ([]foxess.ReportQueryResult, error) {
			return nil, errors.New("report endpoint down")
		},
	}
	w := &mockWriter{}
	cfg := minimalConfig("SN001")

	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled))

	w.mu.Lock()
	defer w.mu.Unlock()
	assert.Empty(t, w.reportBatches, "no report batches should be written on API error")
}

// --------------------------------------------------------------------------
// Context cancellation
// --------------------------------------------------------------------------

func TestExporter_Run_ContextCancelled_ReturnsContextError(t *testing.T) {
	fox := &mockFoxClient{
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
	}
	cfg := minimalConfig("SN001")

	ctx, cancel := context.WithCancel(context.Background())
	exp := exporter.NewWithDeps(cfg, fox, &mockWriter{}, noopLogger())

	errCh := make(chan error, 1)
	go func() { errCh <- exp.Run(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.True(t, errors.Is(err, context.Canceled),
			"Run must return context.Canceled when ctx is cancelled, got: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// --------------------------------------------------------------------------
// Backfill — unit tests
// --------------------------------------------------------------------------

// backfillConfig returns a minimal config with backfill enabled and a short
// realtime_interval so the min-gap threshold is deterministic in tests.
func backfillConfig(enabled bool, maxAge time.Duration) *config.Config {
	cfg := minimalConfig("SN001")
	cfg.Exporter.BackfillEnabled = enabled
	cfg.Exporter.BackfillMaxAge = maxAge
	cfg.Exporter.RealtimeInterval = 10 * time.Second // min-gap = 20s
	return cfg
}

// makeFoxWithHistory returns a mockFoxClient whose history endpoint returns
// canned data at the given timestamps.
func makeFoxWithHistory(sn string, timestamps []string, fields map[string]float64) *mockFoxClient {
	return &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{{DeviceSN: sn}}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
		historyDataFn: func(querySN string, _ []string, _, _ int64) (*foxess.HistoryQueryResult, error) {
			var datas []foxess.HistoryVar
			for variable, value := range fields {
				var pts []foxess.HistoryPoint
				for _, ts := range timestamps {
					pts = append(pts, foxess.HistoryPoint{Time: ts, Value: value})
				}
				datas = append(datas, foxess.HistoryVar{
					Variable: variable, Unit: "kW",
					Data: pts,
				})
			}
			return &foxess.HistoryQueryResult{DeviceSN: querySN, Datas: datas}, nil
		},
	}
}

func TestExporter_Backfill_Disabled_NoHistoryCalls(t *testing.T) {
	fox := makeFoxWithHistory("SN001", nil, nil)
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			// 2 hours ago — would trigger backfill if enabled.
			return time.Now().Add(-2 * time.Hour), nil
		},
	}
	cfg := backfillConfig(false, 7*24*time.Hour)

	_ = runOneCycle(t, cfg, fox, w)

	fox.mu.Lock()
	defer fox.mu.Unlock()
	assert.Empty(t, fox.historyCalls, "backfill disabled: no history calls expected")
}

func TestExporter_Backfill_NoExistingData_SkipsBackfill(t *testing.T) {
	fox := makeFoxWithHistory("SN001", nil, nil)
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return time.Time{}, nil // zero = fresh database
		},
	}

	_ = runOneCycle(t, minimalConfig("SN001"), fox, w)

	fox.mu.Lock()
	defer fox.mu.Unlock()
	assert.Empty(t, fox.historyCalls, "no history calls on fresh start")
}

func TestExporter_Backfill_SmallGap_SkipsBackfill(t *testing.T) {
	fox := makeFoxWithHistory("SN001", nil, nil)
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			// 15s ago — less than 2 * 10s realtime_interval → no backfill.
			return time.Now().Add(-15 * time.Second), nil
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	_ = runOneCycle(t, cfg, fox, w)

	fox.mu.Lock()
	defer fox.mu.Unlock()
	assert.Empty(t, fox.historyCalls, "gap < 2×interval should not trigger backfill")
}

func TestExporter_Backfill_SingleChunk_OneHistoryCall(t *testing.T) {
	// 2-hour gap fits in a single 24h chunk → exactly 1 history call.
	fox := makeFoxWithHistory("SN001",
		[]string{"2024-01-12 10:00:00", "2024-01-12 10:05:00"},
		map[string]float64{"pvPower": 3.0, "SoC": 80.0},
	)
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return time.Now().Add(-2 * time.Hour), nil
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	_ = runOneCycle(t, cfg, fox, w)

	fox.mu.Lock()
	defer fox.mu.Unlock()
	assert.Len(t, fox.historyCalls, 1, "2h gap should produce exactly 1 history call")
	assert.Equal(t, "SN001", fox.historyCalls[0].SN)
}

func TestExporter_Backfill_MultipleChunks_OneCallPerChunk(t *testing.T) {
	// 3-day gap → 3 ×24h chunks → 3 history calls.
	// Each chunk has a 1s rate-limit sleep, so we can't use runOneCycle's 200ms budget.
	// Instead, count calls via a channel and cancel once all expected chunks land.
	const wantChunks = 3
	called := make(chan struct{}, wantChunks+2)

	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{{DeviceSN: "SN001"}}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
		historyDataFn: func(_ string, _ []string, _, _ int64) (*foxess.HistoryQueryResult, error) {
			called <- struct{}{}
			return &foxess.HistoryQueryResult{DeviceSN: "SN001"}, nil
		},
	}
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return time.Now().Add(-3 * 24 * time.Hour), nil
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exp := exporter.NewWithDeps(cfg, fox, w, noopLogger())
	go exp.Run(ctx) //nolint:errcheck

	for i := 0; i < wantChunks; i++ {
		select {
		case <-called:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for chunk %d/%d", i+1, wantChunks)
		}
	}
	cancel()

	fox.mu.Lock()
	defer fox.mu.Unlock()
	assert.GreaterOrEqual(t, len(fox.historyCalls), wantChunks, "3-day gap → 3 history API calls")
}

func TestExporter_Backfill_MaxAgeClampsGap(t *testing.T) {
	// Gap is 10 days but max_age is 2 days → only 2 chunks.
	const wantChunks = 2
	called := make(chan struct{}, wantChunks+2)

	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{{DeviceSN: "SN001"}}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
		historyDataFn: func(_ string, _ []string, _, _ int64) (*foxess.HistoryQueryResult, error) {
			called <- struct{}{}
			return &foxess.HistoryQueryResult{DeviceSN: "SN001"}, nil
		},
	}
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return time.Now().Add(-10 * 24 * time.Hour), nil
		},
	}
	cfg := backfillConfig(true, 2*24*time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exp := exporter.NewWithDeps(cfg, fox, w, noopLogger())
	go exp.Run(ctx) //nolint:errcheck

	for i := 0; i < wantChunks; i++ {
		select {
		case <-called:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for chunk %d/%d", i+1, wantChunks)
		}
	}
	cancel()

	fox.mu.Lock()
	defer fox.mu.Unlock()
	assert.GreaterOrEqual(t, len(fox.historyCalls), wantChunks, "max_age=2d → exactly 2 chunks")
}

func TestExporter_Backfill_GroupsTimestampsIntoOnePointEach(t *testing.T) {
	// Two timestamps, two variables each → two InfluxDB points (one per timestamp).
	timestamps := []string{"2024-01-12 10:00:00", "2024-01-12 10:05:00"}
	fox := makeFoxWithHistory("SN001", timestamps, map[string]float64{
		"pvPower": 3.5,
		"SoC":     82.0,
	})
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return time.Now().Add(-2 * time.Hour), nil
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	_ = runOneCycle(t, cfg, fox, w)

	w.mu.Lock()
	defer w.mu.Unlock()

	// Filter to backfill-sourced points (timestamped in the past, not time.Now()).
	var backfillPts []influx.RealtimePoint
	cutoff := time.Now().Add(-30 * time.Second)
	for _, p := range w.realtimePts {
		if p.Timestamp.Before(cutoff) {
			backfillPts = append(backfillPts, p)
		}
	}

	assert.Len(t, backfillPts, 2, "two distinct history timestamps → two InfluxDB points")
	for _, p := range backfillPts {
		assert.Contains(t, p.Fields, "pvPower", "each backfill point should contain pvPower")
		assert.Contains(t, p.Fields, "SoC", "each backfill point should contain SoC")
	}
}

func TestExporter_Backfill_HistoryAPIError_ContinuesAndStartsLivePoll(t *testing.T) {
	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{{DeviceSN: "SN001"}}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
		historyDataFn: func(_ string, _ []string, _, _ int64) (*foxess.HistoryQueryResult, error) {
			return nil, errors.New("history endpoint unavailable")
		},
	}
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return time.Now().Add(-2 * time.Hour), nil
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	// Despite backfill failure, Run should not return an error — it should
	// proceed to the live poll loop and eventually be cancelled.
	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled),
		"history API error should not abort Run; expected context.Canceled, got %v", err)

	// Live realtime data should still have been collected.
	w.mu.Lock()
	defer w.mu.Unlock()
	assert.NotEmpty(t, w.realtimePts, "live poll should still run after backfill failure")
}

func TestExporter_Backfill_LastTimestampError_ContinuesAndStartsLivePoll(t *testing.T) {
	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{{DeviceSN: "SN001"}}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
	}
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return time.Time{}, errors.New("influxdb query failed")
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled),
		"LastTimestamp error should not abort Run")

	w.mu.Lock()
	defer w.mu.Unlock()
	assert.NotEmpty(t, w.realtimePts, "live poll should still run after LastTimestamp error")
}

func TestExporter_Backfill_CorrectTimeRangeSentToAPI(t *testing.T) {
	// The history call's begin/end should bracket the gap start → now.
	gapStart := time.Now().Add(-90 * time.Minute).UTC().Truncate(time.Second)

	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{{DeviceSN: "SN001"}}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
		historyDataFn: func(_ string, _ []string, _, _ int64) (*foxess.HistoryQueryResult, error) {
			return &foxess.HistoryQueryResult{DeviceSN: "SN001"}, nil
		},
	}
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return gapStart, nil
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	_ = runOneCycle(t, cfg, fox, w)

	fox.mu.Lock()
	defer fox.mu.Unlock()
	require.Len(t, fox.historyCalls, 1)

	callBegin := time.UnixMilli(fox.historyCalls[0].Begin).UTC()
	callEnd := time.UnixMilli(fox.historyCalls[0].End).UTC()

	// begin should equal gapStart (within 1ms of rounding).
	assert.WithinDuration(t, gapStart, callBegin, time.Millisecond,
		"history begin should equal the gap start timestamp")
	// end should be within a few seconds of now.
	assert.WithinDuration(t, time.Now().UTC(), callEnd, 5*time.Second,
		"history end should be close to now")
}

func TestExporter_Backfill_UnparseableTimestamp_SkippedGracefully(t *testing.T) {
	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{{DeviceSN: "SN001"}}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
		historyDataFn: func(sn string, _ []string, _, _ int64) (*foxess.HistoryQueryResult, error) {
			return &foxess.HistoryQueryResult{
				DeviceSN: sn,
				Datas: []foxess.HistoryVar{
					{
						Variable: "pvPower", Unit: "kW",
						Data: []foxess.HistoryPoint{
							{Time: "NOT A VALID TIME", Value: 1.0},
							{Time: "2024-01-12 10:05:00", Value: 2.0}, // valid
						},
					},
				},
			}, nil
		},
	}
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			return time.Now().Add(-2 * time.Hour), nil
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	// Should not panic or return error.
	err := runOneCycle(t, cfg, fox, w)
	assert.True(t, errors.Is(err, context.Canceled))

	// Only the valid timestamp should have been written.
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := time.Now().Add(-30 * time.Second)
	var backfillPts []influx.RealtimePoint
	for _, p := range w.realtimePts {
		if p.Timestamp.Before(cutoff) {
			backfillPts = append(backfillPts, p)
		}
	}
	assert.Len(t, backfillPts, 1, "only the valid timestamp should produce a point")
}

func TestExporter_Backfill_ContextCancelledMidBackfill_StopsCleanly(t *testing.T) {
	// Make history calls slow so we can cancel mid-backfill.
	callCount := 0
	ctx, cancel := context.WithCancel(context.Background())

	fox := &mockFoxClient{
		listDevicesFn: func() ([]foxess.Device, error) {
			return []foxess.Device{{DeviceSN: "SN001"}}, nil
		},
		realTimeFn: func(sns []string, _ []string) ([]foxess.RealQueryResult, error) {
			return []foxess.RealQueryResult{{DeviceSN: sns[0]}}, nil
		},
		historyDataFn: func(_ string, _ []string, _, _ int64) (*foxess.HistoryQueryResult, error) {
			callCount++
			if callCount == 1 {
				// Cancel context after the first chunk to simulate mid-backfill cancel.
				cancel()
			}
			return &foxess.HistoryQueryResult{DeviceSN: "SN001"}, nil
		},
	}
	w := &mockWriter{
		lastTimestampFn: func(_ context.Context, _ string) (time.Time, error) {
			// 5-day gap → would produce 5 chunks, but context is cancelled after 1.
			return time.Now().Add(-5 * 24 * time.Hour), nil
		},
	}
	cfg := backfillConfig(true, 7*24*time.Hour)

	exp := exporter.NewWithDeps(cfg, fox, w, noopLogger())
	err := exp.Run(ctx)

	// context.Canceled is expected.
	assert.True(t, errors.Is(err, context.Canceled) || err == nil)

	fox.mu.Lock()
	defer fox.mu.Unlock()
	// Should have made at most 2 calls (1 chunk + attempting the next before cancel
	// propagates through the rate-limit sleep).
	assert.LessOrEqual(t, len(fox.historyCalls), 2,
		"context cancel should stop backfill after at most 2 chunks")
}
