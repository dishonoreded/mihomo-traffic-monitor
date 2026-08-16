package api_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/api"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/continuity"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/traffic"
)

func TestStatusReportsPrivateLocalObservatoryState(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Config{
		ControllerURL:    "http://127.0.0.1:9090",
		ControllerSecret: "never-return-this-secret",
		DashboardAddress: "127.0.0.1:9091",
		SampleInterval:   time.Second,
		DatabasePath:     databasePath,
	}
	handler := api.NewHandler(cfg, store, testAssets(t))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got, want := response.Code, http.StatusOK; got != want {
		t.Fatalf("status code = %d, want %d: %s", got, want, response.Body.String())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("same-origin API must not enable permissive CORS")
	}
	if strings.Contains(response.Body.String(), cfg.ControllerSecret) {
		t.Fatal("status response exposed the Controller secret")
	}

	var body struct {
		APIVersion string `json:"apiVersion"`
		Collector  struct {
			State string `json:"state"`
		} `json:"collector"`
		Database struct {
			Healthy       bool  `json:"healthy"`
			SizeBytes     int64 `json:"sizeBytes"`
			SchemaVersion int   `json:"schemaVersion"`
		} `json:"database"`
		Configuration struct {
			ControllerURL            string `json:"controllerUrl"`
			DashboardAddress         string `json:"dashboardAddress"`
			SampleInterval           string `json:"sampleInterval"`
			ControllerAuthentication string `json:"controllerAuthentication"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.APIVersion != "v1" || body.Collector.State != "unavailable" {
		t.Fatalf("unexpected API/collector state: %+v", body)
	}
	if !body.Database.Healthy || body.Database.SchemaVersion != 3 || body.Database.SizeBytes <= 0 {
		t.Fatalf("unexpected database state: %+v", body.Database)
	}
	if body.Configuration.ControllerURL != cfg.ControllerURL || body.Configuration.DashboardAddress != cfg.DashboardAddress || body.Configuration.SampleInterval != "1s" {
		t.Fatalf("unexpected safe configuration: %+v", body.Configuration)
	}
	if body.Configuration.ControllerAuthentication != "configured" {
		t.Fatalf("authentication diagnostic = %q, want configured", body.Configuration.ControllerAuthentication)
	}
}

func TestGapsReturnsBoundedOpenAndClosedCollectionGaps(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	if _, err := store.AcceptSample(continuity.State{SampledAt: start, UploadTotal: 100, DownloadTotal: 200}, nil); err != nil {
		t.Fatalf("accept baseline: %v", err)
	}
	if err := store.OpenCollectionGap("disconnected"); err != nil {
		t.Fatalf("open gap: %v", err)
	}
	reconnectedAt := start.Add(5 * time.Minute)
	if _, err := store.AcceptSample(continuity.State{SampledAt: reconnectedAt, UploadTotal: 130, DownloadTotal: 260}, nil); err != nil {
		t.Fatalf("close recovered gap: %v", err)
	}
	if err := store.OpenCollectionGap("authentication_failed"); err != nil {
		t.Fatalf("open current gap: %v", err)
	}

	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))
	queryStart := start.Add(-time.Minute)
	queryEnd := reconnectedAt.Add(time.Minute)
	path := "/api/v1/gaps?start=" + url.QueryEscape(queryStart.Format(time.RFC3339)) + "&end=" + url.QueryEscape(queryEnd.Format(time.RFC3339))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("gaps status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		APIVersion string           `json:"apiVersion"`
		Range      summaryRangeJSON `json:"range"`
		Gaps       []continuity.Gap `json:"gaps"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode gaps: %v", err)
	}
	if payload.APIVersion != "v1" || payload.Range.Start != queryStart.Format(time.RFC3339) || payload.Range.End != queryEnd.Format(time.RFC3339) {
		t.Fatalf("gaps range = %+v, API = %q", payload.Range, payload.APIVersion)
	}
	if len(payload.Gaps) != 2 || !payload.Gaps[0].Open || payload.Gaps[0].Reason != "authentication_failed" || payload.Gaps[1].Disposition != continuity.DispositionRecovered || payload.Gaps[1].RecoveredUpload != 30 || payload.Gaps[1].RecoveredDownload != 60 {
		t.Fatalf("gaps payload = %+v", payload.Gaps)
	}
	statusResult := httptest.NewRecorder()
	handler.ServeHTTP(statusResult, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	var statusPayload struct {
		Collection struct {
			CurrentGap *continuity.Gap  `json:"currentGap"`
			RecentGaps []continuity.Gap `json:"recentGaps"`
			Error      *string          `json:"error"`
		} `json:"collection"`
	}
	if err := json.Unmarshal(statusResult.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("decode status Collection diagnostics: %v", err)
	}
	if statusPayload.Collection.CurrentGap == nil || statusPayload.Collection.CurrentGap.Reason != "authentication_failed" || len(statusPayload.Collection.RecentGaps) != 1 || statusPayload.Collection.Error != nil {
		t.Fatalf("status Collection diagnostics = %+v", statusPayload.Collection)
	}
}

type summaryRangeJSON struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func TestSummaryReturnsHalfOpenAttributionTotalsCoverageAndLeaders(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 14, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	if err := store.AddTraffic([]traffic.Record{
		{Minute: start, Class: traffic.Observed, App: "Safari", Host: "example.com", RegistrableDomain: "example.com", Upload: 20, Download: 80},
		{Minute: start, Class: traffic.Residual, Upload: 5, Download: 15},
		{Minute: start.Add(time.Minute), Class: traffic.GapRecovered, Upload: 7, Download: 13},
	}); err != nil {
		t.Fatalf("seed traffic: %v", err)
	}
	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))
	path := "/api/v1/summary?start=" + url.QueryEscape(start.Format(time.RFC3339)) + "&end=" + url.QueryEscape(start.Add(time.Minute).Format(time.RFC3339))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("summary status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		APIVersion string `json:"apiVersion"`
		Range      struct {
			Start string `json:"start"`
			End   string `json:"end"`
		} `json:"range"`
		Upload   storage.AttributionTotals `json:"upload"`
		Download storage.AttributionTotals `json:"download"`
		Total    storage.AttributionTotals `json:"total"`
		Coverage float64                   `json:"coverage"`
		Leaders  struct {
			Apps  []storage.Leader `json:"apps"`
			Hosts []storage.Leader `json:"hosts"`
		} `json:"leaders"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if payload.APIVersion != "v1" || payload.Range.Start != start.Format(time.RFC3339) || payload.Range.End != start.Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("summary range = %+v, API = %q", payload.Range, payload.APIVersion)
	}
	if payload.Total != (storage.AttributionTotals{Observed: 100, Residual: 20, Total: 120}) {
		t.Fatalf("summary total = %+v", payload.Total)
	}
	if payload.Coverage != float64(100)/120 || len(payload.Leaders.Apps) != 1 || payload.Leaders.Apps[0].Name != "Safari" || len(payload.Leaders.Hosts) != 1 {
		t.Fatalf("summary coverage/leaders = %f %+v", payload.Coverage, payload.Leaders)
	}
}

func TestSummaryRejectsMissingMalformedAndEmptyRanges(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))
	for _, path := range []string{
		"/api/v1/summary",
		"/api/v1/summary?start=not-a-time&end=2026-08-14T01:00:00Z",
		"/api/v1/summary?start=2026-08-14T01:00:00Z&end=2026-08-14T01:00:00Z",
		"/api/v1/gaps",
		"/api/v1/gaps?start=not-a-time&end=2026-08-14T01:00:00Z",
		"/api/v1/gaps?start=2026-08-14T01:00:00Z&end=2026-08-14T01:00:00Z",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode range error: %v", err)
		}
		if response.Code != http.StatusBadRequest || payload.Error.Code != "invalid_time_range" {
			t.Fatalf("%s = status %d code %q", path, response.Code, payload.Error.Code)
		}
	}
}

func TestOpenAPIDescribesStatusWithoutSecretMaterial(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Config{ControllerSecret: "never-document-this-secret", DatabasePath: databasePath}
	handler := api.NewHandler(cfg, store, testAssets(t))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI status = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(strings.ToLower(response.Body.String()), "controllersecret") || strings.Contains(response.Body.String(), cfg.ControllerSecret) {
		t.Fatal("OpenAPI contains secret material")
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	statusSchema := schemas["Status"].(map[string]any)
	properties := statusSchema["properties"].(map[string]any)
	for _, requiredProperty := range []string{"apiVersion", "collector", "live", "database", "configuration"} {
		if _, ok := properties[requiredProperty]; !ok {
			t.Fatalf("status schema is missing %q", requiredProperty)
		}
	}
	paths := document["paths"].(map[string]any)
	if _, ok := paths["/api/v1/live/events"]; !ok {
		t.Fatal("OpenAPI is missing the live event stream")
	}
	if _, ok := paths["/api/v1/summary"]; !ok {
		t.Fatal("OpenAPI is missing the traffic summary")
	}
	if _, ok := paths["/api/v1/gaps"]; !ok {
		t.Fatal("OpenAPI is missing Collection gaps")
	}
	if _, ok := schemas["Gap"]; !ok {
		t.Fatal("OpenAPI is missing the Gap schema")
	}
	if _, ok := schemas["Summary"]; !ok {
		t.Fatal("OpenAPI is missing the Summary schema")
	}

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	var statusPayload any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("decode status for contract validation: %v", err)
	}
	assertJSONMatchesSchema(t, statusPayload, statusSchema)
}

func TestAPIErrorsUseTheStableJSONShape(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))

	for _, scenario := range []struct {
		method string
		path   string
		status int
		code   string
	}{
		{method: http.MethodPost, path: "/api/v1/status", status: http.StatusMethodNotAllowed, code: "method_not_allowed"},
		{method: http.MethodGet, path: "/api/v1/missing", status: http.StatusNotFound, code: "not_found"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(scenario.method, scenario.path, nil))
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s %s error: %v (%q)", scenario.method, scenario.path, err, response.Body.String())
		}
		if response.Code != scenario.status || payload.Error.Code != scenario.code {
			t.Fatalf("%s %s = status %d code %q", scenario.method, scenario.path, response.Code, payload.Error.Code)
		}
	}
}

func testAssets(t *testing.T) fs.FS {
	t.Helper()
	assets, err := fs.Sub(fstest.MapFS{
		"dist/index.html": {Data: []byte("<!doctype html><title>Mihomo Monitor</title>")},
	}, "dist")
	if err != nil {
		t.Fatalf("create test assets: %v", err)
	}
	return assets
}

func assertJSONMatchesSchema(t *testing.T, value any, schema map[string]any) {
	t.Helper()
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("contract expected object, got %T", value)
		}
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, name := range required {
				if _, present := object[name.(string)]; !present {
					t.Fatalf("contract response is missing required property %q", name)
				}
			}
		}
		for name, propertyValue := range object {
			propertySchema, documented := properties[name].(map[string]any)
			if !documented {
				t.Fatalf("contract response contains undocumented property %q", name)
			}
			assertJSONMatchesSchema(t, propertyValue, propertySchema)
		}
	case "string":
		if _, ok := value.(string); !ok {
			t.Fatalf("contract expected string, got %T", value)
		}
	case "integer":
		if _, ok := value.(float64); !ok {
			t.Fatalf("contract expected integer, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			t.Fatalf("contract expected boolean, got %T", value)
		}
	default:
		if allowedTypes, ok := schema["type"].([]any); ok {
			if value == nil {
				for _, allowed := range allowedTypes {
					if allowed == "null" {
						return
					}
				}
				t.Fatal("contract does not allow null")
			}
			for _, allowed := range allowedTypes {
				if allowed == "string" {
					if _, ok := value.(string); ok {
						return
					}
				}
			}
			t.Fatalf("contract value %T does not match allowed types %v", value, allowedTypes)
		}
	}
}
