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

func TestSummaryAcceptsCanonicalFromToAndRetainsStartEndCompatibility(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	minute := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	if err := store.AddTraffic([]traffic.Record{{Minute: minute, Class: traffic.Residual, Upload: 4, Download: 6}}); err != nil {
		t.Fatalf("seed summary traffic: %v", err)
	}
	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))
	for _, query := range []string{
		"from=" + url.QueryEscape(minute.Format(time.RFC3339)) + "&to=" + url.QueryEscape(minute.Add(time.Minute).Format(time.RFC3339)),
		"start=" + url.QueryEscape(minute.Format(time.RFC3339)) + "&end=" + url.QueryEscape(minute.Add(time.Minute).Format(time.RFC3339)),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/summary?"+query, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("summary query %q status = %d: %s", query, response.Code, response.Body.String())
		}
		var payload struct {
			Total storage.AttributionTotals `json:"total"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode summary: %v", err)
		}
		if payload.Total.Total != 10 {
			t.Fatalf("summary query %q total = %+v", query, payload.Total)
		}
	}
}

func TestSeriesReturnsCalendarBucketsAndDirectionalAttributionTotals(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	second := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	if err := store.AddTraffic([]traffic.Record{
		{Minute: first, Class: traffic.Observed, App: "Safari", Host: "example.com", Upload: 10, Download: 20},
		{Minute: first, Class: traffic.Residual, Upload: 3, Download: 4},
		{Minute: second, Class: traffic.GapRecovered, Upload: 5, Download: 6},
	}); err != nil {
		t.Fatalf("seed series traffic: %v", err)
	}
	from := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	path := "/api/v1/series?from=" + url.QueryEscape(from.Format(time.RFC3339)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339)) + "&timeZone=America%2FNew_York&granularity=hour"
	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("series status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		APIVersion  string              `json:"apiVersion"`
		Granularity storage.Granularity `json:"granularity"`
		PointLimit  int                 `json:"pointLimit"`
		TimeZone    string              `json:"timeZone"`
		Range       struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"range"`
		Points []struct {
			Start    string                    `json:"start"`
			Upload   storage.AttributionTotals `json:"upload"`
			Download storage.AttributionTotals `json:"download"`
			Total    storage.AttributionTotals `json:"total"`
		} `json:"points"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode series: %v", err)
	}
	if payload.APIVersion != "v1" || payload.Granularity != storage.GranularityHour || payload.PointLimit != storage.AutoPointLimit || payload.TimeZone != "America/New_York" || payload.Range.From != from.Format(time.RFC3339) || payload.Range.To != to.Format(time.RFC3339) {
		t.Fatalf("series metadata = %+v", payload)
	}
	if len(payload.Points) != 2 || payload.Points[0].Start != "2026-11-01T01:00:00-04:00" || payload.Points[1].Start != "2026-11-01T01:00:00-05:00" {
		t.Fatalf("series points = %+v", payload.Points)
	}
	if payload.Points[0].Total != (storage.AttributionTotals{Observed: 30, Residual: 7, Total: 37}) || payload.Points[1].Total != (storage.AttributionTotals{GapRecovered: 11, Total: 11}) {
		t.Fatalf("series totals = %+v", payload.Points)
	}
}

func TestSeriesRejectsInvalidRangesTimeZonesAndGranularities(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))
	validRange := "from=2026-08-14T00%3A00%3A00Z&to=2026-08-14T01%3A00%3A00Z"
	tests := []struct {
		path string
		code string
	}{
		{path: "/api/v1/series", code: "invalid_time_range"},
		{path: "/api/v1/series?from=bad&to=2026-08-14T01%3A00%3A00Z&timeZone=UTC&granularity=auto", code: "invalid_time_range"},
		{path: "/api/v1/series?from=2026-08-14T01%3A00%3A00Z&to=2026-08-14T01%3A00%3A00Z&timeZone=UTC&granularity=auto", code: "invalid_time_range"},
		{path: "/api/v1/series?" + validRange + "&timeZone=Mars%2FOlympus&granularity=auto", code: "invalid_time_zone"},
		{path: "/api/v1/series?" + validRange + "&timeZone=UTC&granularity=week", code: "invalid_granularity"},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s error: %v", test.path, err)
		}
		if response.Code != http.StatusBadRequest || payload.Error.Code != test.code {
			t.Fatalf("%s = status %d code %q, want 400 %q", test.path, response.Code, payload.Error.Code, test.code)
		}
	}
}

func TestSeriesReportsWhenAutoCannotFitTheDocumentedPointLimit(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	from := time.Date(2018, 1, 1, 12, 0, 0, 0, time.UTC)
	records := make([]traffic.Record, 0, storage.AutoPointLimit+1)
	for index := 0; index <= storage.AutoPointLimit; index++ {
		records = append(records, traffic.Record{Minute: from.AddDate(0, 0, index), Class: traffic.Residual, Download: 1})
	}
	if err := store.AddTraffic(records); err != nil {
		t.Fatalf("seed oversized daily history: %v", err)
	}
	to := from.AddDate(0, 0, storage.AutoPointLimit+1)
	path := "/api/v1/series?from=" + url.QueryEscape(from.Format(time.RFC3339)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339)) + "&timeZone=UTC&granularity=auto"
	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode oversized series error: %v", err)
	}
	if response.Code != http.StatusUnprocessableEntity || payload.Error.Code != "series_point_limit_exceeded" {
		t.Fatalf("oversized series = status %d code %q", response.Code, payload.Error.Code)
	}
}

func TestFilterDimensionAndRankingAPIsExposeObservedScopeAndRepeatedExactFilters(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	if err := store.AddTraffic([]traffic.Record{
		{Minute: start, Class: traffic.Observed, App: "Safari", Host: "api.example.com", RegistrableDomain: "example.com", Upload: 10, Download: 20},
		{Minute: start, Class: traffic.Observed, App: "curl", Host: "cdn.example.com", RegistrableDomain: "example.com", Upload: 30, Download: 40},
		{Minute: start, Class: traffic.Observed, App: "Mail", Host: "mail.example.com", RegistrableDomain: "example.com", Upload: 50, Download: 60},
		{Minute: start.Add(time.Minute), Class: traffic.Observed, App: "Safari", Host: "api.example.net", RegistrableDomain: "example.net", Upload: 70, Download: 80},
		{Minute: start, Class: traffic.Residual, Upload: 100, Download: 200},
	}); err != nil {
		t.Fatalf("seed API traffic: %v", err)
	}
	handler := api.NewHandler(config.Config{DatabasePath: databasePath}, store, testAssets(t))

	dimensionsResponse := httptest.NewRecorder()
	handler.ServeHTTP(dimensionsResponse, httptest.NewRequest(http.MethodGet, "/api/v1/dimensions?q=EXAMPLE&limit=2", nil))
	var dimensions struct {
		APIVersion string   `json:"apiVersion"`
		Apps       []string `json:"apps"`
		Hosts      []string `json:"hosts"`
		Domains    []string `json:"domains"`
	}
	if err := json.Unmarshal(dimensionsResponse.Body.Bytes(), &dimensions); err != nil {
		t.Fatalf("decode dimensions: %v", err)
	}
	if dimensionsResponse.Code != http.StatusOK || dimensions.APIVersion != "v1" || len(dimensions.Hosts) != 2 || len(dimensions.Domains) != 2 {
		t.Fatalf("dimensions response = status %d %+v", dimensionsResponse.Code, dimensions)
	}

	filter := "&app=Safari&app=curl&domain=example.com"
	summaryPath := "/api/v1/summary?from=" + url.QueryEscape(start.Format(time.RFC3339)) + "&to=" + url.QueryEscape(start.Add(2*time.Minute).Format(time.RFC3339)) + filter
	summaryResponse := httptest.NewRecorder()
	handler.ServeHTTP(summaryResponse, httptest.NewRequest(http.MethodGet, summaryPath, nil))
	var summary struct {
		Scope string                    `json:"scope"`
		Total storage.AttributionTotals `json:"total"`
	}
	if err := json.Unmarshal(summaryResponse.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode filtered summary: %v", err)
	}
	if summaryResponse.Code != http.StatusOK || summary.Scope != "observed" || summary.Total != (storage.AttributionTotals{Observed: 100, Total: 100}) {
		t.Fatalf("filtered summary = status %d %+v", summaryResponse.Code, summary)
	}

	seriesPath := "/api/v1/series?from=" + url.QueryEscape(start.Format(time.RFC3339)) + "&to=" + url.QueryEscape(start.Add(2*time.Minute).Format(time.RFC3339)) + "&timeZone=UTC&granularity=minute" + filter
	seriesResponse := httptest.NewRecorder()
	handler.ServeHTTP(seriesResponse, httptest.NewRequest(http.MethodGet, seriesPath, nil))
	var series struct {
		Scope  string                `json:"scope"`
		Points []storage.SeriesPoint `json:"points"`
	}
	if err := json.Unmarshal(seriesResponse.Body.Bytes(), &series); err != nil {
		t.Fatalf("decode filtered series: %v", err)
	}
	if seriesResponse.Code != http.StatusOK || series.Scope != "observed" || len(series.Points) != 1 || series.Points[0].Total.Total != 100 {
		t.Fatalf("filtered series = status %d %+v", seriesResponse.Code, series)
	}

	rankingPath := "/api/v1/rankings?from=" + url.QueryEscape(start.Format(time.RFC3339)) + "&to=" + url.QueryEscape(start.Add(2*time.Minute).Format(time.RFC3339)) + "&dimension=host&direction=upload&limit=2" + filter
	rankingResponse := httptest.NewRecorder()
	handler.ServeHTTP(rankingResponse, httptest.NewRequest(http.MethodGet, rankingPath, nil))
	var rankings struct {
		Scope     string                   `json:"scope"`
		Dimension storage.RankingDimension `json:"dimension"`
		Direction storage.RankingDirection `json:"direction"`
		Limit     int                      `json:"limit"`
		Items     []storage.Leader         `json:"items"`
	}
	if err := json.Unmarshal(rankingResponse.Body.Bytes(), &rankings); err != nil {
		t.Fatalf("decode rankings: %v", err)
	}
	if rankingResponse.Code != http.StatusOK || rankings.Scope != "observed" || rankings.Dimension != storage.DimensionHost || rankings.Direction != storage.DirectionUpload || rankings.Limit != 2 || len(rankings.Items) != 2 || rankings.Items[0].Name != "cdn.example.com" {
		t.Fatalf("rankings response = status %d %+v", rankingResponse.Code, rankings)
	}
}

func TestDimensionAndRankingAPIsRejectInvalidOptions(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := api.NewHandler(config.Config{}, store, testAssets(t))
	for _, scenario := range []struct {
		path string
		code string
	}{
		{path: "/api/v1/dimensions?limit=0", code: "invalid_limit"},
		{path: "/api/v1/rankings?from=2026-08-14T00%3A00%3A00Z&to=2026-08-14T01%3A00%3A00Z&dimension=rule&direction=total&limit=10", code: "invalid_dimension"},
		{path: "/api/v1/rankings?from=2026-08-14T00%3A00%3A00Z&to=2026-08-14T01%3A00%3A00Z&dimension=app&direction=sideways&limit=10", code: "invalid_direction"},
		{path: "/api/v1/rankings?from=2026-08-14T00%3A00%3A00Z&to=2026-08-14T01%3A00%3A00Z&dimension=app&direction=total&limit=101", code: "invalid_limit"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, scenario.path, nil))
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v", scenario.path, err)
		}
		if response.Code != http.StatusBadRequest || payload.Error.Code != scenario.code {
			t.Fatalf("%s = status %d code %q", scenario.path, response.Code, payload.Error.Code)
		}
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
	seriesPath, ok := paths["/api/v1/series"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI is missing the traffic series")
	}
	seriesOperation := seriesPath["get"].(map[string]any)
	parameters := seriesOperation["parameters"].([]any)
	parameterNames := map[string]bool{}
	for _, rawParameter := range parameters {
		parameter := rawParameter.(map[string]any)
		parameterNames[parameter["name"].(string)] = true
	}
	for _, name := range []string{"from", "to", "timeZone", "granularity"} {
		if !parameterNames[name] {
			t.Fatalf("series OpenAPI is missing %q query parameter", name)
		}
	}
	for _, path := range []string{"/api/v1/dimensions", "/api/v1/rankings"} {
		if _, ok := paths[path]; !ok {
			t.Fatalf("OpenAPI is missing %s", path)
		}
	}
	for _, schema := range []string{"Dimensions", "Rankings"} {
		if _, ok := schemas[schema]; !ok {
			t.Fatalf("OpenAPI is missing the %s schema", schema)
		}
	}
	for _, path := range []string{"/api/v1/summary", "/api/v1/series", "/api/v1/rankings"} {
		operation := paths[path].(map[string]any)["get"].(map[string]any)
		parameters := operation["parameters"].([]any)
		names := map[string]bool{}
		for _, raw := range parameters {
			names[raw.(map[string]any)["name"].(string)] = true
		}
		for _, name := range []string{"app", "host", "domain"} {
			if !names[name] {
				t.Fatalf("%s OpenAPI is missing repeated %q filters", path, name)
			}
		}
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
	seriesSchema, ok := schemas["Series"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI is missing the Series schema")
	}
	seriesProperties := seriesSchema["properties"].(map[string]any)
	pointLimit := seriesProperties["pointLimit"].(map[string]any)
	if pointLimit["const"] != float64(storage.AutoPointLimit) && pointLimit["const"] != storage.AutoPointLimit {
		t.Fatalf("series pointLimit contract = %v, want %d", pointLimit["const"], storage.AutoPointLimit)
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
