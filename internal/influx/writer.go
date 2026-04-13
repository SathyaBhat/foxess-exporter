// Package influx wraps the InfluxDB v3 Go client for writing FoxESS metrics.
package influx

import (
	"context"
	"fmt"
	"time"

	influxdb3 "github.com/InfluxCommunity/influxdb3-go/influxdb3"
	"github.com/apache/arrow/go/v15/arrow"
	"go.uber.org/zap"
)

const (
	// MeasurementRealtime is the InfluxDB measurement name for real-time data.
	MeasurementRealtime = "inverter_realtime"
	// MeasurementReport is the measurement name for daily energy report totals.
	MeasurementReport = "inverter_report"
)

// Writer wraps an InfluxDB v3 client and provides typed write helpers.
type Writer struct {
	client *influxdb3.Client
	log    *zap.Logger
}

// New creates a Writer connected to an InfluxDB v3 instance.
func New(host, token, database string, log *zap.Logger) (*Writer, error) {
	client, err := influxdb3.New(influxdb3.ClientConfig{
		Host:     host,
		Token:    token,
		Database: database,
	})
	if err != nil {
		return nil, fmt.Errorf("create influxdb3 client: %w", err)
	}
	return &Writer{client: client, log: log}, nil
}

// Close releases resources held by the underlying client.
func (w *Writer) Close() error {
	return w.client.Close()
}

// RealtimePoint represents a single real-time sample to write.
type RealtimePoint struct {
	DeviceSN    string
	StationName string
	Timestamp   time.Time
	// Fields is a map of variableName → value.
	Fields map[string]float64
}

// WriteRealtime writes a real-time sample as a single line-protocol point.
// All variables from one poll cycle share the same timestamp so they land
// in the same InfluxDB series row, making cross-field queries efficient.
func (w *Writer) WriteRealtime(ctx context.Context, p RealtimePoint) error {
	if len(p.Fields) == 0 {
		return nil
	}

	point := influxdb3.NewPoint(MeasurementRealtime,
		map[string]string{
			"device_sn":    p.DeviceSN,
			"station_name": p.StationName,
		},
		fieldsToAny(p.Fields),
		p.Timestamp,
	)

	if err := w.client.WritePoints(ctx, []*influxdb3.Point{point}); err != nil {
		return fmt.Errorf("write realtime point: %w", err)
	}
	w.log.Debug("wrote realtime point",
		zap.String("device_sn", p.DeviceSN),
		zap.Int("fields", len(p.Fields)),
		zap.Time("ts", p.Timestamp),
	)
	return nil
}

// ReportPoint represents a single energy-report data point.
type ReportPoint struct {
	DeviceSN    string
	StationName string
	Timestamp   time.Time   // start of the reporting period
	Variable    string      // e.g. "generation", "feedin"
	Value       float64     // kWh
	Unit        string
}

// WriteReport writes daily report totals.  Each variable gets its own point
// so partial updates (e.g. writing just "generation") are straightforward.
func (w *Writer) WriteReport(ctx context.Context, points []ReportPoint) error {
	if len(points) == 0 {
		return nil
	}

	influxPoints := make([]*influxdb3.Point, 0, len(points))
	for _, p := range points {
		pt := influxdb3.NewPoint(MeasurementReport,
			map[string]string{
				"device_sn":    p.DeviceSN,
				"station_name": p.StationName,
				"variable":     p.Variable,
				"unit":         p.Unit,
			},
			map[string]any{"value_kwh": p.Value},
			p.Timestamp,
		)
		influxPoints = append(influxPoints, pt)
	}

	if err := w.client.WritePoints(ctx, influxPoints); err != nil {
		return fmt.Errorf("write report points: %w", err)
	}
	w.log.Debug("wrote report points", zap.Int("count", len(influxPoints)))
	return nil
}

func fieldsToAny(m map[string]float64) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// LastTimestamp queries InfluxDB for the most recent data point stored for
// the given device in the realtime measurement.  Returns a zero time.Time
// (and no error) when no data has been written yet — i.e. first run.
func (w *Writer) LastTimestamp(ctx context.Context, deviceSN string) (time.Time, error) {
	query := fmt.Sprintf(
		`SELECT time FROM "inverter_realtime" WHERE "device_sn" = '%s' ORDER BY time DESC LIMIT 1`,
		deviceSN,
	)

	iter, err := w.client.Query(ctx, query)
	if err != nil {
		return time.Time{}, fmt.Errorf("query last timestamp: %w", err)
	}

	if !iter.Next() {
		// No rows — database is empty for this device.
		return time.Time{}, nil
	}

	row := iter.Value()
	raw, ok := row["time"]
	if !ok {
		return time.Time{}, nil
	}

	// influxdb3-go returns Arrow timestamps as arrow.Timestamp (nanosecond precision).
	switch v := raw.(type) {
	case arrow.Timestamp:
		return v.ToTime(arrow.Nanosecond).UTC(), nil
	case time.Time:
		return v.UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("unexpected time column type %T", raw)
	}
}
