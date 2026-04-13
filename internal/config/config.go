package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the exporter.
type Config struct {
	FoxESS   FoxESSConfig   `mapstructure:"foxess"`
	InfluxDB  InfluxDBConfig `mapstructure:"influxdb"`
	Exporter  ExporterConfig `mapstructure:"exporter"`
	Log      LogConfig      `mapstructure:"log"`
}

type FoxESSConfig struct {
	// APIKey is the private token from FoxESS Cloud → Personal Centre → API Management.
	APIKey  string `mapstructure:"api_key"`
	// BaseURL defaults to https://www.foxesscloud.com
	BaseURL string `mapstructure:"base_url"`
	// DeviceSN is the inverter serial number. Leave empty to auto-discover the
	// first device on the account.
	DeviceSN string `mapstructure:"device_sn"`
}

type InfluxDBConfig struct {
	// Host is the InfluxDB v3 host, e.g. "https://us-east-1-1.aws.cloud2.influxdata.com"
	// or "http://localhost:8086" for a local instance.
	Host     string `mapstructure:"host"`
	Token    string `mapstructure:"token"`
	Database string `mapstructure:"database"`
}

type ExporterConfig struct {
	// RealtimeInterval is how often to poll the real-time data endpoint.
	// FoxESS allows 1 req/sec per endpoint; 1440 calls/day = 1 call/min max sustainable.
	// Defaults to 60s.
	RealtimeInterval time.Duration `mapstructure:"realtime_interval"`
	// ReportInterval is how often to poll the daily report endpoint.
	// Defaults to 5 minutes.
	ReportInterval time.Duration `mapstructure:"report_interval"`
	// BackfillEnabled controls whether the exporter fills gaps in InfluxDB on
	// startup by querying the FoxESS history endpoint. Defaults to true.
	BackfillEnabled bool `mapstructure:"backfill_enabled"`
	// BackfillMaxAge is the furthest back the backfill will look.
	// Capped because the FoxESS daily call budget is shared with live polling.
	// Defaults to 168h (7 days).
	BackfillMaxAge time.Duration `mapstructure:"backfill_max_age"`
}

type LogConfig struct {
	// Level is one of debug, info, warn, error. Defaults to info.
	Level string `mapstructure:"level"`
}

// Load reads configuration from the given file path and merges environment
// variables.  Environment variables override file values.
//
// The env var name is the config key with dots replaced by underscores,
// uppercased — no extra prefix.  For example:
//
//	FOXESS_API_KEY          → foxess.api_key
//	FOXESS_BASE_URL         → foxess.base_url
//	FOXESS_DEVICE_SN        → foxess.device_sn
//	INFLUXDB_HOST           → influxdb.host
//	INFLUXDB_TOKEN          → influxdb.token
//	INFLUXDB_DATABASE       → influxdb.database
//	EXPORTER_REALTIME_INTERVAL → exporter.realtime_interval
//	EXPORTER_REPORT_INTERVAL   → exporter.report_interval
//	EXPORTER_BACKFILL_ENABLED  → exporter.backfill_enabled
//	EXPORTER_BACKFILL_MAX_AGE  → exporter.backfill_max_age
//	LOG_LEVEL               → log.level
func Load(cfgFile string) (*Config, error) {
	v := viper.New()

	// Defaults
	v.SetDefault("foxess.base_url", "https://www.foxesscloud.com")
	v.SetDefault("exporter.realtime_interval", 60*time.Second)
	v.SetDefault("exporter.report_interval", 5*time.Minute)
	v.SetDefault("exporter.backfill_enabled", true)
	v.SetDefault("exporter.backfill_max_age", 7*24*time.Hour)
	v.SetDefault("log.level", "info")
	v.SetDefault("influxdb.database", "foxess")

	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.foxess-exporter")
		v.AddConfigPath("/etc/foxess-exporter")
	}

	// No prefix: the top-level section name (foxess, influxdb, exporter, log)
	// already acts as the natural namespace, e.g. FOXESS_API_KEY, INFLUXDB_HOST.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		// A missing config file is fine – env vars alone are sufficient.
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	if c.FoxESS.APIKey == "" {
		return fmt.Errorf("foxess.api_key (or FOXESS_API_KEY) is required")
	}
	if c.InfluxDB.Host == "" {
		return fmt.Errorf("influxdb.host (or INFLUXDB_HOST) is required")
	}
	if c.InfluxDB.Token == "" {
		return fmt.Errorf("influxdb.token (or INFLUXDB_TOKEN) is required")
	}
	if c.Exporter.RealtimeInterval < 10*time.Second {
		return fmt.Errorf("exporter.realtime_interval must be >= 10s (FoxESS rate limits)")
	}
	return nil
}


