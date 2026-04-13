package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sathyabhat/foxess-exporter/internal/config"
)

// writeTempConfig writes a YAML config file in a temp dir and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// --------------------------------------------------------------------------
// Happy-path load from file
// --------------------------------------------------------------------------

func TestLoad_FromFile_AllFieldsSet(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "test-api-key"
  base_url: "https://custom.foxess.example.com"
  device_sn: "ABCDEF123456"

influxdb:
  host: "http://influx:8086"
  token: "influx-token"
  database: "my_foxess"

exporter:
  realtime_interval: 120s
  report_interval: 10m

log:
  level: "debug"
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "test-api-key", cfg.FoxESS.APIKey)
	assert.Equal(t, "https://custom.foxess.example.com", cfg.FoxESS.BaseURL)
	assert.Equal(t, "ABCDEF123456", cfg.FoxESS.DeviceSN)

	assert.Equal(t, "http://influx:8086", cfg.InfluxDB.Host)
	assert.Equal(t, "influx-token", cfg.InfluxDB.Token)
	assert.Equal(t, "my_foxess", cfg.InfluxDB.Database)

	assert.Equal(t, 120*time.Second, cfg.Exporter.RealtimeInterval)
	assert.Equal(t, 10*time.Minute, cfg.Exporter.ReportInterval)

	assert.Equal(t, "debug", cfg.Log.Level)
}

// --------------------------------------------------------------------------
// Default values
// --------------------------------------------------------------------------

func TestLoad_Defaults_AreApplied(t *testing.T) {
	// Minimal valid config — only required fields.
	path := writeTempConfig(t, `
foxess:
  api_key: "key"
influxdb:
  host: "http://localhost:8086"
  token: "token"
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "https://www.foxesscloud.com", cfg.FoxESS.BaseURL, "default base_url")
	assert.Equal(t, "foxess", cfg.InfluxDB.Database, "default database")
	assert.Equal(t, 60*time.Second, cfg.Exporter.RealtimeInterval, "default realtime_interval")
	assert.Equal(t, 5*time.Minute, cfg.Exporter.ReportInterval, "default report_interval")
	assert.Equal(t, "info", cfg.Log.Level, "default log level")
}

// --------------------------------------------------------------------------
// Validation errors
// --------------------------------------------------------------------------

func TestLoad_MissingAPIKey_ReturnsError(t *testing.T) {
	path := writeTempConfig(t, `
influxdb:
  host: "http://localhost:8086"
  token: "token"
`)
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key")
}

func TestLoad_MissingInfluxHost_ReturnsError(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "key"
influxdb:
  token: "token"
`)
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "influxdb.host")
}

func TestLoad_MissingInfluxToken_ReturnsError(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "key"
influxdb:
  host: "http://localhost:8086"
`)
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "influxdb.token")
}

func TestLoad_RealtimeIntervalTooShort_ReturnsError(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "key"
influxdb:
  host: "http://localhost:8086"
  token: "token"
exporter:
  realtime_interval: 5s
`)
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "realtime_interval")
	assert.Contains(t, err.Error(), "10s")
}

func TestLoad_RealtimeIntervalExactly10s_IsValid(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "key"
influxdb:
  host: "http://localhost:8086"
  token: "token"
exporter:
  realtime_interval: 10s
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, cfg.Exporter.RealtimeInterval)
}

func TestLoad_InvalidConfigFile_ReturnsError(t *testing.T) {
	path := writeTempConfig(t, `:::not valid yaml:::`)
	_, err := config.Load(path)
	require.Error(t, err)
}

func TestLoad_NonExistentFile_ReturnsError(t *testing.T) {
	_, err := config.Load("/tmp/does-not-exist-foxess-12345.yaml")
	// Viper returns an error when SetConfigFile points to a missing file.
	require.Error(t, err)
}

// --------------------------------------------------------------------------
// Environment variable overrides
// --------------------------------------------------------------------------

func TestLoad_EnvVars_OverrideFileValues(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "file-api-key"
  device_sn: "file-sn"
influxdb:
  host: "http://file-influx:8086"
  token: "file-token"
  database: "file_db"
`)
	t.Setenv("FOXESS_FOXESS_API_KEY", "env-api-key")
	t.Setenv("FOXESS_FOXESS_DEVICE_SN", "env-sn")
	t.Setenv("FOXESS_INFLUXDB_HOST", "http://env-influx:8086")
	t.Setenv("FOXESS_INFLUXDB_TOKEN", "env-token")
	t.Setenv("FOXESS_INFLUXDB_DATABASE", "env_db")
	t.Setenv("FOXESS_LOG_LEVEL", "warn")

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "env-api-key", cfg.FoxESS.APIKey, "env should override file api_key")
	assert.Equal(t, "env-sn", cfg.FoxESS.DeviceSN, "env should override file device_sn")
	assert.Equal(t, "http://env-influx:8086", cfg.InfluxDB.Host, "env should override file influxdb.host")
	assert.Equal(t, "env-token", cfg.InfluxDB.Token, "env should override file influxdb.token")
	assert.Equal(t, "env_db", cfg.InfluxDB.Database, "env should override file influxdb.database")
	assert.Equal(t, "warn", cfg.Log.Level, "env should override file log level")
}

func TestLoad_EnvVars_RealtimeInterval(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "key"
influxdb:
  host: "http://localhost:8086"
  token: "token"
`)
	t.Setenv("FOXESS_EXPORTER_REALTIME_INTERVAL", "90s")

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, cfg.Exporter.RealtimeInterval)
}

func TestLoad_EnvVars_BackfillSettings(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "key"
influxdb:
  host: "http://localhost:8086"
  token: "token"
exporter:
  backfill_enabled: true
  backfill_max_age: 72h
`)
	// Override both backfill settings via env vars.
	t.Setenv("FOXESS_EXPORTER_BACKFILL_ENABLED", "false")
	t.Setenv("FOXESS_EXPORTER_BACKFILL_MAX_AGE", "48h")

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.False(t, cfg.Exporter.BackfillEnabled, "env should override backfill_enabled")
	assert.Equal(t, 48*time.Hour, cfg.Exporter.BackfillMaxAge, "env should override backfill_max_age")
}

func TestLoad_BackfillDefaults(t *testing.T) {
	path := writeTempConfig(t, `
foxess:
  api_key: "key"
influxdb:
  host: "http://localhost:8086"
  token: "token"
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.True(t, cfg.Exporter.BackfillEnabled, "backfill_enabled should default to true")
	assert.Equal(t, 7*24*time.Hour, cfg.Exporter.BackfillMaxAge, "backfill_max_age should default to 168h")
}
