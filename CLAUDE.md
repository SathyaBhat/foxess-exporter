# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run directly (requires config.yaml or env vars)
go run ./cmd/exporter -config config.yaml

# Build binary
go build -o foxess-exporter ./cmd/exporter

# Run all tests
go test ./...

# Run tests for a specific package
go test ./internal/foxess/...
go test ./internal/exporter/...

# Run a single test
go test ./internal/foxess/ -run TestSign_Format

# Build Docker image
docker build -t foxess-exporter .

# Run full stack (exporter + InfluxDB + Grafana)
docker compose up -d
```

## Architecture

This is a Go service that polls the FoxESS Cloud Open API and writes metrics to InfluxDB v3, with a pre-wired Grafana dashboard.

**Data flow:**
```
FoxESS Cloud API → foxess.Client → exporter.Exporter → influx.Writer → InfluxDB v3 → Grafana
```

**Package responsibilities:**
- `cmd/exporter/main.go` — entrypoint: loads config, wires up the three main components, handles OS signals
- `internal/config/` — Viper-backed config loading; no `FOXESS_` prefix is used — the section names (`foxess`, `influxdb`, etc.) act as the natural namespace for env vars (e.g. `FOXESS_API_KEY`, `INFLUXDB_HOST`)
- `internal/foxess/` — FoxESS Cloud API client; auth uses MD5 signature of `path + "\r\n" + token + "\r\n" + timestamp`; the `\r\n` is the **literal 4-character string** backslash-r-backslash-n (not CRLF bytes) — as shown by the FoxESS docs Python example using `fr'...\r\n...'` raw strings
- `internal/influx/` — InfluxDB v3 writer; two measurements: `inverter_realtime` (all poll fields in one multi-field point per cycle) and `inverter_report` (hourly energy totals, one point per variable)
- `internal/exporter/` — orchestrates poll loops (realtime + report tickers), device resolution, and backfill on startup

**Dependency injection for testing:** `exporter.New` accepts concrete `*foxess.Client` and `*influx.Writer`. `exporter.NewWithDeps` accepts the `foxESSClient` and `metricsWriter` interfaces for test injection without a live server.

**Backfill:** On startup, the exporter checks the newest timestamp in InfluxDB and fills any gap using the FoxESS history endpoint (max 24 h per API call). Each 24 h window costs one API call from the 1440/day budget.

**Rate limits:** FoxESS enforces 1 req/sec and 1440 calls/day per inverter. The default `realtime_interval: 60s` hits exactly the daily limit. Use `120s` if sharing the quota with other apps (e.g. Home Assistant).
