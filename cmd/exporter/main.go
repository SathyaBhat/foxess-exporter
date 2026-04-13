package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/sathyabhat/foxess-exporter/internal/config"
	"github.com/sathyabhat/foxess-exporter/internal/exporter"
	"github.com/sathyabhat/foxess-exporter/internal/foxess"
	"github.com/sathyabhat/foxess-exporter/internal/influx"
)

func main() {
	cfgFile := flag.String("config", "", "path to config.yaml (default: ./config.yaml)")
	flag.Parse()

	// Load config first with a temporary plain logger so we can report errors.
	tmpLog, _ := zap.NewDevelopment()

	cfg, err := config.Load(*cfgFile)
	if err != nil {
		tmpLog.Fatal("failed to load config", zap.Error(err))
	}

	log := buildLogger(cfg.Log.Level)
	defer log.Sync() //nolint:errcheck

	log.Info("foxess-exporter starting",
		zap.String("foxess_base_url", cfg.FoxESS.BaseURL),
		zap.String("influxdb_host", cfg.InfluxDB.Host),
		zap.String("influxdb_database", cfg.InfluxDB.Database),
	)

	foxClient := foxess.New(cfg.FoxESS.APIKey, cfg.FoxESS.BaseURL)

	influxWriter, err := influx.New(
		cfg.InfluxDB.Host,
		cfg.InfluxDB.Token,
		cfg.InfluxDB.Database,
		log,
	)
	if err != nil {
		log.Fatal("failed to create InfluxDB writer", zap.Error(err))
	}
	defer influxWriter.Close()

	exp := exporter.New(cfg, foxClient, influxWriter, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := exp.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal("exporter exited with error", zap.Error(err))
	}
	log.Info("exporter stopped cleanly")
}

func buildLogger(level string) *zap.Logger {
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(level)); err != nil {
		zapLevel = zapcore.InfoLevel
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	logCfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapLevel),
		Development:      false,
		Encoding:         "json",
		EncoderConfig:    encCfg,
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	log, err := logCfg.Build()
	if err != nil {
		// Fallback – should never happen.
		fmt.Fprintf(os.Stderr, "failed to build logger: %v\n", err)
		log, _ = zap.NewProduction()
	}
	return log
}
