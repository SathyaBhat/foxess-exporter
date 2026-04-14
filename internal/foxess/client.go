// Package foxess provides a client for the FoxESS Cloud Open API.
//
// Authentication uses the private-token flow:
//   - token header  : your API key
//   - timestamp     : current Unix milliseconds
//   - signature     : MD5( path + "\r\n" + token + "\r\n" + timestamp )
//
// Reference: https://www.foxesscloud.com/public/i18n/en/OpenApiDocument.html
package foxess

import (
	"bytes"
	"crypto/md5" //nolint:gosec // FoxESS mandates MD5 for request signing
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	pathDeviceList    = "/op/v0/device/list"
	pathRealQuery     = "/op/v1/device/real/query"
	pathHistoryQuery  = "/op/v0/device/history/query"
	pathReportQuery   = "/op/v0/device/report/query"
	pathVariableGet   = "/op/v0/device/variable/get"
)

// Client is a FoxESS Cloud API client.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New creates a new Client.  baseURL defaults to "https://www.foxesscloud.com".
func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://www.foxesscloud.com"
	}
	return &Client{
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// --------------------------------------------------------------------------
// Request / response helpers
// --------------------------------------------------------------------------

func sign(path, token string, tsMs int64) string {
	// FoxESS docs show a Python fr-string: fr'{path}\r\n{token}\r\n{timestamp}'
	// The raw-string prefix means \r\n is the literal 4-char sequence "\r\n",
	// NOT actual CRLF bytes.
	raw := path + `\r\n` + token + `\r\n` + strconv.FormatInt(tsMs, 10)
	//nolint:gosec // FoxESS mandates MD5
	return fmt.Sprintf("%x", md5.Sum([]byte(raw)))
}

func (c *Client) headers(path string) http.Header {
	tsMs := time.Now().UnixMilli()
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("token", c.apiKey)
	h.Set("timestamp", strconv.FormatInt(tsMs, 10))
	h.Set("signature", sign(path, c.apiKey, tsMs))
	h.Set("lang", "en")
	h.Set("User-Agent", "foxess-exporter/1.0")
	return h
}

type apiResponse[T any] struct {
	Errno  int    `json:"errno"`
	Msg    string `json:"msg"`
	Result T      `json:"result"`
}

func doRequest[T any](c *Client, method, path string, body any) (T, error) {
	var zero T

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return zero, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return zero, fmt.Errorf("create request: %w", err)
	}
	req.Header = c.headers(path)

	resp, err := c.http.Do(req)
	if err != nil {
		return zero, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var parsed apiResponse[T]
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return zero, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Errno != 0 {
		return zero, fmt.Errorf("API error %d: %s", parsed.Errno, parsed.Msg)
	}
	return parsed.Result, nil
}

// --------------------------------------------------------------------------
// Device list
// --------------------------------------------------------------------------

type DeviceListRequest struct {
	CurrentPage int `json:"currentPage"`
	PageSize    int `json:"pageSize"`
}

type DeviceListResult struct {
	Devices    []Device `json:"data"`
	CurrentPage int     `json:"currentPage"`
	PageSize    int     `json:"pageSize"`
	Total       int     `json:"total"`
}

type Device struct {
	DeviceSN    string `json:"deviceSN"`
	ModuleSN    string `json:"moduleSN"`
	ProductType string `json:"productType"`
	Status      int    `json:"status"`
	StationID   string `json:"stationID"`
	StationName string `json:"stationName"`
}

// ListDevices returns all devices (inverters) on the account.
func (c *Client) ListDevices() ([]Device, error) {
	type pageResult struct {
		Devices    []Device `json:"data"`
		CurrentPage int     `json:"currentPage"`
		PageSize    int     `json:"pageSize"`
		Total       int     `json:"total"`
	}

	var all []Device
	page := 1
	for {
		result, err := doRequest[pageResult](c, http.MethodPost, pathDeviceList, DeviceListRequest{
			CurrentPage: page,
			PageSize:    20,
		})
		if err != nil {
			return nil, fmt.Errorf("list devices page %d: %w", page, err)
		}
		all = append(all, result.Devices...)
		if len(all) >= result.Total || len(result.Devices) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// --------------------------------------------------------------------------
// Real-time data  (v1 – batch capable)
// --------------------------------------------------------------------------

type RealQueryRequest struct {
	SNs       []string `json:"sns"`
	Variables []string `json:"variables,omitempty"`
}

type RealQueryResult struct {
	DeviceSN string      `json:"deviceSN"`
	Datas    []RealDatum `json:"datas"`
}

type RealDatum struct {
	Variable string  `json:"variable"`
	Unit     string  `json:"unit"`
	Name     string  `json:"name"`
	Value    float64 `json:"value"`
}

// RealTimeData fetches the latest real-time values for the given device SNs
// and variables.  Pass nil variables to get all available variables.
func (c *Client) RealTimeData(sns []string, variables []string) ([]RealQueryResult, error) {
	return doRequest[[]RealQueryResult](c, http.MethodPost, pathRealQuery, RealQueryRequest{
		SNs:       sns,
		Variables: variables,
	})
}

// --------------------------------------------------------------------------
// History data
// --------------------------------------------------------------------------

type HistoryQueryRequest struct {
	SN        string   `json:"sn"`
	Variables []string `json:"variables,omitempty"`
	Begin     int64    `json:"begin,omitempty"` // Unix milliseconds
	End       int64    `json:"end,omitempty"`   // Unix milliseconds
}

type HistoryQueryResult struct {
	DeviceSN string         `json:"deviceSN"`
	Datas    []HistoryVar   `json:"datas"`
}

type HistoryVar struct {
	Variable string         `json:"variable"`
	Unit     string         `json:"unit"`
	Name     string         `json:"name"`
	Data     []HistoryPoint `json:"data"`
}

type HistoryPoint struct {
	Value float64 `json:"value"`
	Time  string  `json:"time"` // UTC RFC3339-ish: "2024-01-12 10:05:00"
}

// HistoryData fetches historical data for a single device.
// begin/end are Unix milliseconds; pass zero values to get the last 3 days.
func (c *Client) HistoryData(sn string, variables []string, begin, end int64) (*HistoryQueryResult, error) {
	results, err := doRequest[[]HistoryQueryResult](c, http.MethodPost, pathHistoryQuery, HistoryQueryRequest{
		SN:        sn,
		Variables: variables,
		Begin:     begin,
		End:       end,
	})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no history data returned for device %s", sn)
	}
	return &results[0], nil
}

// --------------------------------------------------------------------------
// Report / daily energy totals
// --------------------------------------------------------------------------

// ReportDimension specifies the aggregation period for the report endpoint.
type ReportDimension string

const (
	DimensionDay   ReportDimension = "day"
	DimensionMonth ReportDimension = "month"
	DimensionYear  ReportDimension = "year"
)

type ReportQueryRequest struct {
	SN        string          `json:"sn"`
	Year      int             `json:"year"`
	Month     int             `json:"month"`
	Day       int             `json:"day,omitempty"`
	Dimension ReportDimension `json:"dimension"`
	Variables []string        `json:"variables"`
}

type ReportQueryResult struct {
	Variable string    `json:"variable"`
	Unit     string    `json:"unit"`
	Name     string    `json:"name"`
	Values   []float64 `json:"values"` // one entry per hour (index = hour-of-day)
}

// ReportVariables are the energy-total variables supported by the report endpoint.
var ReportVariables = []string{
	"generation",          // PV generation (kWh)
	"feedin",             // Feed-in to grid (kWh)
	"gridConsumption",    // Imported from grid (kWh)
	"chargeEnergyToTal",  // Battery charged (kWh)
	"dischargeEnergyToTal", // Battery discharged (kWh)
}

// DailyReport fetches today's hourly energy totals for the given device.
func (c *Client) DailyReport(sn string, t time.Time) ([]ReportQueryResult, error) {
	return doRequest[[]ReportQueryResult](c, http.MethodPost, pathReportQuery, ReportQueryRequest{
		SN:        sn,
		Year:      t.Year(),
		Month:     int(t.Month()),
		Day:       t.Day(),
		Dimension: DimensionDay,
		Variables: ReportVariables,
	})
}

// --------------------------------------------------------------------------
// Available variables
// --------------------------------------------------------------------------

// GetVariables returns the full variable catalogue for the account.
// The result is a map of variableName → VariableMeta.
func (c *Client) GetVariables() (map[string]VariableMeta, error) {
	// The API returns a raw map; each key is the variable name.
	raw, err := doRequest[map[string]VariableMeta](c, http.MethodGet, pathVariableGet, nil)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

type VariableMeta struct {
	Unit string `json:"unit"`
	Name struct {
		EN string `json:"en"`
	} `json:"name"`
}
