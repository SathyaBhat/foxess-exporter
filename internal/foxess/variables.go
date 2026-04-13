package foxess

// WatchedVariables is the set of real-time variables polled by the exporter.
// These map to the key metrics visible on the FoxESS dashboard.
var WatchedVariables = []string{
	// ── Solar ────────────────────────────────────────────────────────────────
	"pvPower",           // Total PV power (sum of all strings)   [kW]
	"pv1Power",          // PV string 1 power                     [kW]
	"pv2Power",          // PV string 2 power                     [kW]
	"pv3Power",          // PV string 3 power                     [kW]
	"pv4Power",          // PV string 4 power                     [kW]
	"generationPower",   // Inverter output power                 [kW]
	"todayYield",        // Today's yield so far                  [kWh]

	// ── Battery ──────────────────────────────────────────────────────────────
	"SoC",               // State of charge                       [%]
	"batChargePower",    // Battery charge power (positive = charging) [kW]
	"batDischargePower", // Battery discharge power               [kW]
	"invBatPower",       // Inverter-side battery power (+/-)     [kW]
	"batTemperature",    // Battery temperature                   [°C]
	"batVolt",           // Battery voltage                       [V]
	"batCurrent",        // Battery current                       [A]

	// ── Grid ─────────────────────────────────────────────────────────────────
	"feedinPower",            // Power exported to grid (positive = export) [kW]
	"gridConsumptionPower",   // Power imported from grid                   [kW]
	"meterPower",             // Grid meter power (+feed-in / -import)      [kW]

	// ── Load ─────────────────────────────────────────────────────────────────
	"loadsPower",        // Total house load                      [kW]

	// ── Inverter misc ────────────────────────────────────────────────────────
	"RVolt",             // AC voltage (single / R-phase)         [V]
	"RFreq",             // AC frequency                          [Hz]
	"invTemperat",       // Inverter temperature                  [°C]
}

// FriendlyName maps a FoxESS variable name to a human-readable label
// used as a Grafana legend and InfluxDB field alias.
var FriendlyName = map[string]string{
	"pvPower":              "PV Power Total",
	"pv1Power":             "PV String 1 Power",
	"pv2Power":             "PV String 2 Power",
	"pv3Power":             "PV String 3 Power",
	"pv4Power":             "PV String 4 Power",
	"generationPower":      "Inverter Output Power",
	"todayYield":           "Today Yield",
	"SoC":                  "Battery SoC",
	"batChargePower":       "Battery Charge Power",
	"batDischargePower":    "Battery Discharge Power",
	"invBatPower":          "Battery Power (inv side)",
	"batTemperature":       "Battery Temperature",
	"batVolt":              "Battery Voltage",
	"batCurrent":           "Battery Current",
	"feedinPower":          "Feed-in Power",
	"gridConsumptionPower": "Grid Consumption Power",
	"meterPower":           "Grid Meter Power",
	"loadsPower":           "House Load Power",
	"RVolt":                "AC Voltage",
	"RFreq":                "AC Frequency",
	"invTemperat":          "Inverter Temperature",
}
