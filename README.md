# foxess-exporter

A Go exporter that polls the [FoxESS Cloud Open API](https://www.foxesscloud.com/public/i18n/en/OpenApiDocument.html), writes metrics to **InfluxDB v3**, and ships a pre-built **Grafana** dashboard.

## What it tracks

| Panel | Variables polled |
|---|---|
| ☀️ PV Power | `pvPower`, `pv1–4Power`, `generationPower`, `todayYield` |
| 🔋 Battery SoC | `SoC` |
| 🔋 Battery Power | `batChargePower`, `batDischargePower`, `invBatPower` |
| 🏠 House Load | `loadsPower` |
| 🔌 Grid | `feedinPower`, `gridConsumptionPower`, `meterPower` |
| 🌡️ Health | `batTemperature`, `invTemperat`, `batVolt`, `batCurrent` |
| 📊 Daily Energy | `generation`, `feedin`, `gridConsumption`, `chargeEnergyToTal`, `dischargeEnergyToTal` |

### InfluxDB measurements

| Measurement | Description |
|---|---|
| `inverter_realtime` | Real-time samples, one point per poll cycle, tagged with `device_sn` + `station_name` |
| `inverter_report` | Hourly energy totals from the report endpoint, tagged with `variable` |

---

## Quick start (Docker Compose)

```bash
# 1. Clone and enter the repo
git clone https://github.com/sathyabhat/foxess-exporter
cd foxess-exporter

# 2. Create your config
cp config.yaml.example config.yaml
$EDITOR config.yaml        # set api_key, influxdb.token, optionally device_sn

# 3. Launch everything
docker compose up -d

# 4. Open Grafana
open http://localhost:3000   # admin / admin
```

Grafana auto-provisions the **FoxESS Solar Dashboard** — no manual import needed.

---

## Configuration

All settings can be provided via `config.yaml` **or** environment variables (prefix `FOXESS_`).  
Environment variables always take precedence over the file.

### config.yaml

```yaml
foxess:
  api_key: "YOUR_FOXESS_API_KEY"   # required
  base_url: "https://www.foxesscloud.com"
  device_sn: ""                    # leave empty to auto-discover

influxdb:
  host: "http://localhost:8086"    # required
  token: "YOUR_INFLUXDB_TOKEN"     # required
  database: "foxess"

exporter:
  realtime_interval: 60s           # min 10s; 60s = safe for the 1440/day limit
  report_interval: 5m

log:
  level: "info"                    # debug | info | warn | error
```

### Environment variables

| Variable | Equivalent config key |
|---|---|
| `FOXESS_API_KEY` | `foxess.api_key` |
| `FOXESS_DEVICE_SN` | `foxess.device_sn` |
| `FOXESS_BASE_URL` | `foxess.base_url` |
| `INFLUXDB_HOST` | `influxdb.host` |
| `INFLUXDB_TOKEN` | `influxdb.token` |
| `INFLUXDB_DATABASE` | `influxdb.database` |
| `EXPORTER_REALTIME_INTERVAL` | `exporter.realtime_interval` |
| `EXPORTER_REPORT_INTERVAL` | `exporter.report_interval` |
| `EXPORTER_BACKFILL_ENABLED` | `exporter.backfill_enabled` |
| `EXPORTER_BACKFILL_MAX_AGE` | `exporter.backfill_max_age` |
| `LOG_LEVEL` | `log.level` |

---

## Building locally

```bash
# Run directly
go run ./cmd/exporter -config config.yaml

# Build binary
go build -o foxess-exporter ./cmd/exporter

# Build Docker image
docker build -t foxess-exporter .
```

---

## FoxESS API rate limits

| Limit | Value |
|---|---|
| Real-time / history endpoints | 1 request/second |
| Setter endpoints | 1 request/2 seconds |
| Daily budget per inverter | 1 440 calls/day |

`realtime_interval: 60s` uses exactly 1 440 calls/day — the safe maximum.  
Use `120s` if you share the quota with other apps (e.g. Home Assistant).

---

## Grafana dashboard

The provisioned dashboard (`grafana/dashboards/foxess.json`) contains:

- **Live stat tiles** — PV power, Battery SoC, charge/discharge, house load, grid import
- **Power flow time-series** — all key power streams overlaid on one graph
- **Battery SoC time-series** — coloured thresholds (red < 20 %, yellow < 50 %, green ≥ 80 %)
- **Hourly energy bar chart** — generation, feed-in, grid import, battery charge/discharge
- **Temperature & voltage history**

A `$device_sn` variable at the top lets you switch inverters if you have more than one.

---

## Project structure

```
foxess-exporter/
├── cmd/exporter/main.go          # entrypoint, signal handling, wiring
├── internal/
│   ├── config/config.go          # Viper-backed config with env var support
│   ├── foxess/
│   │   ├── client.go             # FoxESS API client (auth, all endpoints)
│   │   └── variables.go          # Curated variable list + friendly names
│   ├── influx/writer.go          # InfluxDB v3 writer (realtime + report)
│   └── exporter/exporter.go      # Poll loops, device resolution, logging
├── grafana/
│   ├── provisioning/             # Auto-wired datasource + dashboard provider
│   └── dashboards/foxess.json    # Pre-built Grafana dashboard
├── config.yaml.example
├── docker-compose.yml
├── Dockerfile
└── go.mod
```
