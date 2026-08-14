package api_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/api"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
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
	if !body.Database.Healthy || body.Database.SchemaVersion != 1 || body.Database.SizeBytes <= 0 {
		t.Fatalf("unexpected database state: %+v", body.Database)
	}
	if body.Configuration.ControllerURL != cfg.ControllerURL || body.Configuration.DashboardAddress != cfg.DashboardAddress || body.Configuration.SampleInterval != "1s" {
		t.Fatalf("unexpected safe configuration: %+v", body.Configuration)
	}
	if body.Configuration.ControllerAuthentication != "configured" {
		t.Fatalf("authentication diagnostic = %q, want configured", body.Configuration.ControllerAuthentication)
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
