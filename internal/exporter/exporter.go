// Package exporter ties together the FoxESS client and the InfluxDB writer,
// running the poll loops for real-time and report data.
package exporter

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/sathyabhat/foxess-exporter/internal/config"
	"github.com/sathyabhat/foxess-exporter/internal/foxess"
	"github.com/sathyabhat/foxess-exporter/internal/influx"
)

// foxESSClient is the subset of the FoxESS Cloud API consumed by the Exporter.
// *foxess.Client satisfies this interface.
type foxESSClient interface {
	ListDevices() ([]foxess.Device, error)
	RealTimeData(sns []string, variables []string) ([]foxess.RealQueryResult, error)
	DailyReport(sn string, t time.Time) ([]foxess.ReportQueryResult, error)
	HistoryData(sn string, variables []string, begin, end int64) (*foxess.HistoryQueryResult, error)
}

// metricsWriter is the subset of the InfluxDB writer consumed by the Exporter.
// *influx.Writer satisfies this interface.
type metricsWriter interface {
	WriteRealtime(ctx context.Context, p influx.RealtimePoint) error
	WriteReport(ctx context.Context, points []influx.ReportPoint) error
	// LastTimestamp returns the timestamp of the most recent realtime point
	// stored for the given device, or a zero time.Time if none exists.
	LastTimestamp(ctx context.Context, deviceSN string) (time.Time, error)
}

// Exporter orchestrates polling FoxESS and writing to InfluxDB.
type Exporter struct {
	cfg    *config.Config
	fox    foxESSClient
	influx metricsWriter
	log    *zap.Logger

	// resolved at startup
	deviceSN    string
	stationName string
}

// New creates a new Exporter.  Call Run to start polling.
// The concrete *foxess.Client and *influx.Writer satisfy the internal interfaces.
func New(cfg *config.Config, fox *foxess.Client, iw *influx.Writer, log *zap.Logger) *Exporter {
	return NewWithDeps(cfg, fox, iw, log)
}

// NewWithDeps is like New but accepts interface types, enabling test injection
// of mock FoxESS clients and InfluxDB writers without a live server.
func NewWithDeps(cfg *config.Config, fox foxESSClient, iw metricsWriter, log *zap.Logger) *Exporter {
	return &Exporter{
		cfg:    cfg,
		fox:    fox,
		influx: iw,
		log:    log,
	}
}

// Run starts the exporter and blocks until ctx is cancelled.
func (e *Exporter) Run(ctx context.Context) error {
	if err := e.resolveDevice(ctx); err != nil {
		return err
	}

	e.log.Info("exporter started",
		zap.String("device_sn", e.deviceSN),
		zap.String("station_name", e.stationName),
		zap.Duration("realtime_interval", e.cfg.Exporter.RealtimeInterval),
		zap.Duration("report_interval", e.cfg.Exporter.ReportInterval),
	)

	// Kick off backfill before the live poll loops so gaps are filled
	// before new data starts arriving.
	if e.cfg.Exporter.BackfillEnabled {
		e.backfill(ctx)
	}

	// Kick off both loops.
	realtimeTicker := time.NewTicker(e.cfg.Exporter.RealtimeInterval)
	reportTicker := time.NewTicker(e.cfg.Exporter.ReportInterval)
	defer realtimeTicker.Stop()
	defer reportTicker.Stop()

	// Run immediately on startup, then on each tick.
	e.collectRealtime(ctx)
	e.collectReport(ctx)

	for {
		select {
		case <-ctx.Done():
			e.log.Info("exporter shutting down")
			return ctx.Err()
		case <-realtimeTicker.C:
			e.collectRealtime(ctx)
		case <-reportTicker.C:
			e.collectReport(ctx)
		}
	}
}

// --------------------------------------------------------------------------
// Device resolution
// --------------------------------------------------------------------------

func (e *Exporter) resolveDevice(ctx context.Context) error {
	if e.cfg.FoxESS.DeviceSN != "" {
		e.deviceSN = e.cfg.FoxESS.DeviceSN
		e.log.Info("using configured device SN", zap.String("sn", e.deviceSN))
		// Still try to get the station name for tagging.
		_ = e.resolveStationName(ctx)
		return nil
	}

	e.log.Info("no device_sn configured, auto-discovering devices…")
	devices, err := e.fox.ListDevices()
	if err != nil {
		return fmt.Errorf("auto-discover devices: %w", err)
	}
	if len(devices) == 0 {
		return fmt.Errorf("no devices found on this FoxESS account")
	}
	if len(devices) > 1 {
		sns := make([]string, len(devices))
		for i, d := range devices {
			sns[i] = d.DeviceSN
		}
		e.log.Warn("multiple devices found, using first; set foxess.device_sn to pick one",
			zap.Strings("available", sns))
	}
	e.deviceSN = devices[0].DeviceSN
	e.stationName = devices[0].StationName
	e.log.Info("resolved device",
		zap.String("sn", e.deviceSN),
		zap.String("station", e.stationName),
		zap.String("product_type", devices[0].ProductType),
	)
	return nil
}

func (e *Exporter) resolveStationName(ctx context.Context) error {
	devices, err := e.fox.ListDevices()
	if err != nil {
		return err
	}
	for _, d := range devices {
		if d.DeviceSN == e.deviceSN {
			e.stationName = d.StationName
			return nil
		}
	}
	return nil
}

// --------------------------------------------------------------------------
// Real-time poll
// --------------------------------------------------------------------------

func (e *Exporter) collectRealtime(ctx context.Context) {
	results, err := e.fox.RealTimeData([]string{e.deviceSN}, foxess.WatchedVariables)
	if err != nil {
		e.log.Error("real-time query failed", zap.Error(err))
		return
	}

	ts := time.Now().UTC()
	for _, r := range results {
		fields := make(map[string]float64, len(r.Datas))
		for _, d := range r.Datas {
			fields[d.Variable] = d.Value
		}

		if err := e.influx.WriteRealtime(ctx, influx.RealtimePoint{
			DeviceSN:    r.DeviceSN,
			StationName: e.stationName,
			Timestamp:   ts,
			Fields:      fields,
		}); err != nil {
			e.log.Error("write realtime failed", zap.Error(err))
			return
		}

		// Log a summary of the key metrics.
		e.log.Info("realtime collected",
			zap.String("device_sn", r.DeviceSN),
			zap.Float64("pv_kw", fields["pvPower"]),
			zap.Float64("soc_pct", fields["SoC"]),
			zap.Float64("bat_charge_kw", fields["batChargePower"]),
			zap.Float64("bat_discharge_kw", fields["batDischargePower"]),
			zap.Float64("load_kw", fields["loadsPower"]),
			zap.Float64("feedin_kw", fields["feedinPower"]),
			zap.Float64("grid_import_kw", fields["gridConsumptionPower"]),
		)
	}
}

// --------------------------------------------------------------------------
// Report poll  (daily energy totals, broken down by hour)
// --------------------------------------------------------------------------

func (e *Exporter) collectReport(ctx context.Context) {
	now := time.Now()
	results, err := e.fox.DailyReport(e.deviceSN, now)
	if err != nil {
		e.log.Error("report query failed", zap.Error(err))
		return
	}

	// The report endpoint returns per-hour buckets for the day.
	// We store each non-zero bucket as a separate InfluxDB point timestamped
	// to the start of that hour.
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var pts []influx.ReportPoint
	for _, r := range results {
		for i, v := range r.Values {
			if v == 0 {
				continue
			}
			hourTs := date.Add(time.Duration(i) * time.Hour)
			pts = append(pts, influx.ReportPoint{
				DeviceSN:    e.deviceSN,
				StationName: e.stationName,
				Timestamp:   hourTs,
				Variable:    r.Variable,
				Value:       v,
				Unit:        r.Unit,
			})
		}
	}

	if err := e.influx.WriteReport(ctx, pts); err != nil {
		e.log.Error("write report failed", zap.Error(err))
		return
	}

	// Log summary totals for the day so far.
	totals := make(map[string]float64)
	for _, r := range results {
		var sum float64
		for _, v := range r.Values {
			sum += v
		}
		totals[r.Variable] = sum
	}
	e.log.Info("report collected",
		zap.String("device_sn", e.deviceSN),
		zap.String("date", now.Format("2006-01-02")),
		zap.Float64("generation_kwh", totals["generation"]),
		zap.Float64("feedin_kwh", totals["feedin"]),
		zap.Float64("grid_import_kwh", totals["gridConsumption"]),
		zap.Float64("battery_charged_kwh", totals["chargeEnergyToTal"]),
		zap.Float64("battery_discharged_kwh", totals["dischargeEnergyToTal"]),
	)

}

// --------------------------------------------------------------------------
// Backfill
// --------------------------------------------------------------------------

// backfill queries InfluxDB for the most recent stored timestamp and, if a
// meaningful gap is detected, fills it using the FoxESS history endpoint.
// It is intentionally synchronous so the live poll loops only start after the
// gap is closed.  A context cancellation aborts mid-backfill cleanly.
func (e *Exporter) backfill(ctx context.Context) {
	last, err := e.influx.LastTimestamp(ctx, e.deviceSN)
	if err != nil {
		e.log.Warn("could not query last stored timestamp; skipping backfill",
			zap.Error(err))
		return
	}

	if last.IsZero() {
		e.log.Info("no existing data found in InfluxDB; skipping backfill (fresh start)")
		return
	}

	now := time.Now().UTC()
	gap := now.Sub(last)

	// Only backfill when we've genuinely missed at least two poll cycles.
	// A clean restart within one interval means no data was lost.
	minGap := 2 * e.cfg.Exporter.RealtimeInterval
	if gap <= minGap {
		e.log.Debug("gap too small to warrant backfill",
			zap.Duration("gap", gap),
			zap.Duration("min_gap", minGap))
		return
	}

	maxAge := e.cfg.Exporter.BackfillMaxAge
	from := last
	if now.Sub(from) > maxAge {
		from = now.Add(-maxAge)
		e.log.Info("gap exceeds backfill_max_age; clamping start",
			zap.Time("clamped_from", from),
			zap.Duration("max_age", maxAge),
		)
	}

	e.log.Info("starting backfill",
		zap.Time("from", from),
		zap.Time("to", now),
		zap.Duration("gap", gap),
	)

	// FoxESS history API allows at most 24 h per request; chunk accordingly.
	const historyChunk = 24 * time.Hour
	chunks := 0
	for chunkStart := from; chunkStart.Before(now); chunkStart = chunkStart.Add(historyChunk) {
		if ctx.Err() != nil {
			e.log.Info("backfill interrupted by context cancellation")
			return
		}

		chunkEnd := chunkStart.Add(historyChunk)
		if chunkEnd.After(now) {
			chunkEnd = now
		}

		written, err := e.backfillChunk(ctx, chunkStart, chunkEnd)
		if err != nil {
			e.log.Error("backfill chunk failed; aborting remaining backfill",
				zap.Time("chunk_from", chunkStart),
				zap.Time("chunk_to", chunkEnd),
				zap.Error(err))
			return
		}
		chunks++
		e.log.Info("backfill chunk complete",
			zap.Time("chunk_from", chunkStart),
			zap.Time("chunk_to", chunkEnd),
			zap.Int("points_written", written),
		)

		// Respect the 1 req/sec rate limit between history calls.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}

	e.log.Info("backfill complete", zap.Int("chunks", chunks))
}

// backfillChunk fetches one ≤24 h window of history and writes it to InfluxDB.
// It groups the data by timestamp so each distinct sample time becomes a single
// multi-field InfluxDB point, matching the shape of the live realtime points.
// Returns the number of timestamps written.
func (e *Exporter) backfillChunk(ctx context.Context, from, to time.Time) (int, error) {
	result, err := e.fox.HistoryData(
		e.deviceSN,
		foxess.WatchedVariables,
		from.UnixMilli(),
		to.UnixMilli(),
	)
	if err != nil {
		return 0, fmt.Errorf("history query [%s, %s]: %w", from.Format(time.RFC3339), to.Format(time.RFC3339), err)
	}

	// Group all variables' data points by their timestamp string.
	// FoxESS returns each variable separately; we merge them into one point per
	// timestamp so Grafana queries stay efficient.
	type fieldMap = map[string]float64
	byTime := make(map[string]fieldMap)
	for _, hv := range result.Datas {
		for _, dp := range hv.Data {
			if _, ok := byTime[dp.Time]; !ok {
				byTime[dp.Time] = make(fieldMap)
			}
			byTime[dp.Time][hv.Variable] = dp.Value
		}
	}

	written := 0
	for tsStr, fields := range byTime {
		ts, err := parseHistoryTime(tsStr)
		if err != nil {
			e.log.Warn("skipping history point with unparseable timestamp",
				zap.String("raw_time", tsStr), zap.Error(err))
			continue
		}
		if err := e.influx.WriteRealtime(ctx, influx.RealtimePoint{
			DeviceSN:    e.deviceSN,
			StationName: e.stationName,
			Timestamp:   ts,
			Fields:      fields,
		}); err != nil {
			return written, fmt.Errorf("write backfill point at %s: %w", tsStr, err)
		}
		written++
	}
	return written, nil
}

// parseHistoryTime parses the FoxESS history timestamp format
// "2024-01-12 10:05:00" which is always in UTC.
func parseHistoryTime(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
}
