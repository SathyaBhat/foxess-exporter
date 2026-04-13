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
// variables (prefix FOXESS_).  Environment variables override file values.
//
// Supported env vars (all optional if set in config file):
//
//	FOXESS_FOXESS_API_KEY
//	FOXESS_FOXESS_BASE_URL
//	FOXESS_FOXESS_DEVICE_SN
//	FOXESS_INFLUXDB_HOST
//	FOXESS_INFLUXDB_TOKEN
//	FOXESS_INFLUXDB_DATABASE
//	FOXESS_EXPORTER_REALTIME_INTERVAL
//	FOXESS_EXPORTER_REPORT_INTERVAL
//	FOXESS_LOG_LEVEL
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

	v.SetEnvPrefix("FOXESS")
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
		return fmt.Errorf("foxess.api_key (or FOXESS_FOXESS_API_KEY) is required")
	}
	if c.InfluxDB.Host == "" {
		return fmt.Errorf("influxdb.host (or FOXESS_INFLUXDB_HOST) is required")
	}
	if c.InfluxDB.Token == "" {
		return fmt.Errorf("influxdb.token (or FOXESS_INFLUXDB_TOKEN) is required")
	}
	if c.Exporter.RealtimeInterval < 10*time.Second {
		return fmt.Errorf("exporter.realtime_interval must be >= 10s (FoxESS rate limits)")
	}
	return nil
}


