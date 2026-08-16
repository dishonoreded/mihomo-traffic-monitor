package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/collector"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/continuity"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
)

type liveSource interface {
	Snapshot() collector.Snapshot
	Subscribe() (<-chan collector.Snapshot, func())
}

type server struct {
	configuration config.Config
	store         *storage.Store
	assets        fs.FS
	monitor       liveSource
	keepalive     time.Duration
}

const liveKeepaliveInterval = 15 * time.Second

func NewHandler(configuration config.Config, store *storage.Store, assets fs.FS) http.Handler {
	return NewHandlerWithCollector(configuration, store, assets, nil)
}

func NewHandlerWithCollector(configuration config.Config, store *storage.Store, assets fs.FS, monitor liveSource) http.Handler {
	return newHandler(configuration, store, assets, monitor, liveKeepaliveInterval)
}

func newHandler(configuration config.Config, store *storage.Store, assets fs.FS, monitor liveSource, keepalive time.Duration) http.Handler {
	srv := &server{configuration: configuration, store: store, assets: assets, monitor: monitor, keepalive: keepalive}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/status", getOnly(http.HandlerFunc(srv.status)))
	mux.Handle("/api/v1/summary", getOnly(http.HandlerFunc(srv.summary)))
	mux.Handle("/api/v1/series", getOnly(http.HandlerFunc(srv.series)))
	mux.Handle("/api/v1/rankings", getOnly(http.HandlerFunc(srv.rankings)))
	mux.Handle("/api/v1/dimensions", getOnly(http.HandlerFunc(srv.dimensions)))
	mux.Handle("/api/v1/gaps", getOnly(http.HandlerFunc(srv.gaps)))
	mux.Handle("/api/v1/live/events", getOnly(http.HandlerFunc(srv.liveEvents)))
	mux.Handle("/api/v1/openapi.json", getOnly(http.HandlerFunc(srv.openAPIDocument)))
	mux.HandleFunc("/api/", srv.apiNotFound)
	mux.Handle("/", getOnly(srv.singlePageApplication()))
	return securityHeaders(mux)
}

func getOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeJSON(response, http.StatusMethodNotAllowed, map[string]any{
				"error": map[string]string{
					"code":    "method_not_allowed",
					"message": "The local API is read-only; use GET.",
				},
			})
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (srv *server) status(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, srv.statusPayload())
}

type summaryRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type summaryLeaders struct {
	Apps  []storage.Leader `json:"apps"`
	Hosts []storage.Leader `json:"hosts"`
}

type summaryResponse struct {
	APIVersion string                    `json:"apiVersion"`
	Scope      storage.TrafficScope      `json:"scope"`
	Range      summaryRange              `json:"range"`
	Upload     storage.AttributionTotals `json:"upload"`
	Download   storage.AttributionTotals `json:"download"`
	Total      storage.AttributionTotals `json:"total"`
	Coverage   float64                   `json:"coverage"`
	Leaders    summaryLeaders            `json:"leaders"`
}

type gapsResponse struct {
	APIVersion string           `json:"apiVersion"`
	Range      summaryRange     `json:"range"`
	Gaps       []continuity.Gap `json:"gaps"`
}

type seriesRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type seriesResponse struct {
	APIVersion  string                `json:"apiVersion"`
	Scope       storage.TrafficScope  `json:"scope"`
	Granularity storage.Granularity   `json:"granularity"`
	PointLimit  int                   `json:"pointLimit"`
	TimeZone    string                `json:"timeZone"`
	Range       seriesRange           `json:"range"`
	Points      []storage.SeriesPoint `json:"points"`
}

func (srv *server) summary(response http.ResponseWriter, request *http.Request) {
	start, end, valid := requestedSummaryTimeRange(request)
	if !valid {
		writeJSON(response, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "invalid_time_range",
				"message": "Provide from and to as RFC3339 timestamps with to after from; the range is [from, to).",
			},
		})
		return
	}
	filter, valid := requestedTrafficFilter(request)
	if !valid {
		writeInvalidFilter(response)
		return
	}
	result, err := srv.store.FilteredSummary(start, end, filter)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"code":    "database_query_failed",
				"message": "Traffic history could not be read from the local database.",
			},
		})
		return
	}
	writeJSON(response, http.StatusOK, summaryResponse{
		APIVersion: "v1",
		Scope:      result.Scope,
		Range:      summaryRange{Start: start.Format(time.RFC3339Nano), End: end.Format(time.RFC3339Nano)},
		Upload:     result.Upload,
		Download:   result.Download,
		Total:      result.Total,
		Coverage:   result.Coverage,
		Leaders:    summaryLeaders{Apps: result.Apps, Hosts: result.Hosts},
	})
}

func (srv *server) series(response http.ResponseWriter, request *http.Request) {
	from, to, valid := requestedCanonicalTimeRange(request)
	if !valid {
		writeJSON(response, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "invalid_time_range",
				"message": "Provide from and to as RFC3339 timestamps with to after from; the range is [from, to).",
			},
		})
		return
	}
	locationName := request.URL.Query().Get("timeZone")
	location, err := time.LoadLocation(locationName)
	if err != nil || locationName == "" {
		writeJSON(response, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "invalid_time_zone",
				"message": "Provide timeZone as an IANA time zone name such as America/New_York.",
			},
		})
		return
	}
	granularity := storage.Granularity(request.URL.Query().Get("granularity"))
	if granularity != storage.GranularityMinute && granularity != storage.GranularityHour && granularity != storage.GranularityDay && granularity != storage.GranularityAuto {
		writeJSON(response, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "invalid_granularity",
				"message": "Provide granularity as minute, hour, day, or auto.",
			},
		})
		return
	}
	filter, valid := requestedTrafficFilter(request)
	if !valid {
		writeInvalidFilter(response)
		return
	}
	result, err := srv.store.Series(storage.SeriesOptions{Start: from, End: to, Granularity: granularity, Location: location, Filter: filter})
	if errors.Is(err, storage.ErrAutoPointLimitExceeded) {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]string{
				"code":    "series_point_limit_exceeded",
				"message": "The requested range exceeds the automatic 400-point limit even at day granularity.",
			},
		})
		return
	}
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"code":    "database_query_failed",
				"message": "Traffic history could not be read from the local database.",
			},
		})
		return
	}
	writeJSON(response, http.StatusOK, seriesResponse{
		APIVersion:  "v1",
		Scope:       result.Scope,
		Granularity: result.Granularity,
		PointLimit:  storage.AutoPointLimit,
		TimeZone:    locationName,
		Range:       seriesRange{From: from.Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano)},
		Points:      result.Points,
	})
}

type dimensionsResponse struct {
	APIVersion string   `json:"apiVersion"`
	Query      string   `json:"query"`
	Limit      int      `json:"limit"`
	Apps       []string `json:"apps"`
	Hosts      []string `json:"hosts"`
	Domains    []string `json:"domains"`
}

func (srv *server) dimensions(response http.ResponseWriter, request *http.Request) {
	limit, valid := requestedLimit(request, 20)
	if !valid {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": map[string]string{
			"code": "invalid_limit", "message": "Provide limit as an integer from 1 through 100.",
		}})
		return
	}
	query := request.URL.Query().Get("q")
	result, err := srv.store.Dimensions(query, limit)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]any{"error": map[string]string{
			"code": "database_query_failed", "message": "Traffic dimensions could not be read from the local database.",
		}})
		return
	}
	writeJSON(response, http.StatusOK, dimensionsResponse{
		APIVersion: "v1", Query: query, Limit: limit, Apps: result.Apps, Hosts: result.Hosts, Domains: result.Domains,
	})
}

type rankingsResponse struct {
	APIVersion string                   `json:"apiVersion"`
	Scope      storage.TrafficScope     `json:"scope"`
	Range      seriesRange              `json:"range"`
	Dimension  storage.RankingDimension `json:"dimension"`
	Direction  storage.RankingDirection `json:"direction"`
	Limit      int                      `json:"limit"`
	Items      []storage.Leader         `json:"items"`
}

func (srv *server) rankings(response http.ResponseWriter, request *http.Request) {
	from, to, valid := requestedCanonicalTimeRange(request)
	if !valid {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": map[string]string{
			"code": "invalid_time_range", "message": "Provide from and to as RFC3339 timestamps with to after from; the range is [from, to).",
		}})
		return
	}
	dimension := storage.RankingDimension(request.URL.Query().Get("dimension"))
	if dimension != storage.DimensionApp && dimension != storage.DimensionHost && dimension != storage.DimensionDomain {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": map[string]string{
			"code": "invalid_dimension", "message": "Provide dimension as app, host, or domain.",
		}})
		return
	}
	direction := storage.RankingDirection(request.URL.Query().Get("direction"))
	if direction != storage.DirectionUpload && direction != storage.DirectionDownload && direction != storage.DirectionTotal {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": map[string]string{
			"code": "invalid_direction", "message": "Provide direction as upload, download, or total.",
		}})
		return
	}
	limit, valid := requestedLimit(request, 10)
	if !valid {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": map[string]string{
			"code": "invalid_limit", "message": "Provide limit as an integer from 1 through 100.",
		}})
		return
	}
	filter, valid := requestedTrafficFilter(request)
	if !valid {
		writeInvalidFilter(response)
		return
	}
	result, err := srv.store.Rankings(storage.RankingOptions{
		Start: from, End: to, Dimension: dimension, Direction: direction, Limit: limit, Filter: filter,
	})
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]any{"error": map[string]string{
			"code": "database_query_failed", "message": "Traffic rankings could not be read from the local database.",
		}})
		return
	}
	writeJSON(response, http.StatusOK, rankingsResponse{
		APIVersion: "v1", Scope: result.Scope, Range: seriesRange{From: from.Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano)},
		Dimension: dimension, Direction: direction, Limit: limit, Items: result.Items,
	})
}

func requestedLimit(request *http.Request, fallback int) (int, bool) {
	raw := request.URL.Query().Get("limit")
	if raw == "" {
		return fallback, true
	}
	limit, err := strconv.Atoi(raw)
	return limit, err == nil && limit >= 1 && limit <= 100
}

func requestedTrafficFilter(request *http.Request) (storage.TrafficFilter, bool) {
	query := request.URL.Query()
	filter := storage.TrafficFilter{Apps: query["app"], Hosts: query["host"], Domains: query["domain"]}
	for _, values := range [][]string{filter.Apps, filter.Hosts, filter.Domains} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return storage.TrafficFilter{}, false
			}
		}
	}
	return filter, true
}

func writeInvalidFilter(response http.ResponseWriter) {
	writeJSON(response, http.StatusBadRequest, map[string]any{"error": map[string]string{
		"code": "invalid_filter", "message": "App, Host, and domain filters must be non-empty exact values.",
	}})
}

func (srv *server) gaps(response http.ResponseWriter, request *http.Request) {
	start, end, valid := requestedTimeRange(request)
	if !valid {
		writeJSON(response, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "invalid_time_range",
				"message": "Provide start and end as RFC3339 timestamps with end after start; the range is [start, end).",
			},
		})
		return
	}
	gaps, err := srv.store.CollectionGaps(start, end)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"code":    "database_query_failed",
				"message": "Collection gap history could not be read from the local database.",
			},
		})
		return
	}
	writeJSON(response, http.StatusOK, gapsResponse{
		APIVersion: "v1",
		Range:      summaryRange{Start: start.Format(time.RFC3339Nano), End: end.Format(time.RFC3339Nano)},
		Gaps:       gaps,
	})
}

func requestedTimeRange(request *http.Request) (time.Time, time.Time, bool) {
	start, startErr := time.Parse(time.RFC3339, request.URL.Query().Get("start"))
	end, endErr := time.Parse(time.RFC3339, request.URL.Query().Get("end"))
	return start, end, startErr == nil && endErr == nil && end.After(start)
}

func requestedSummaryTimeRange(request *http.Request) (time.Time, time.Time, bool) {
	query := request.URL.Query()
	if query.Get("from") != "" || query.Get("to") != "" {
		return requestedCanonicalTimeRange(request)
	}
	return requestedTimeRange(request)
}

func requestedCanonicalTimeRange(request *http.Request) (time.Time, time.Time, bool) {
	from, fromErr := time.Parse(time.RFC3339, request.URL.Query().Get("from"))
	to, toErr := time.Parse(time.RFC3339, request.URL.Query().Get("to"))
	return from, to, fromErr == nil && toErr == nil && to.After(from)
}

func (srv *server) statusPayload() statusResponse {
	payload := srv.baseStatusPayload()
	if srv.monitor != nil {
		applyCollectorSnapshot(&payload, srv.monitor.Snapshot())
	}
	return payload
}

func (srv *server) baseStatusPayload() statusResponse {
	database := srv.store.Info()
	now := time.Now().UTC()
	authentication := authenticationNotConfigured
	if srv.configuration.ControllerSecret != "" {
		authentication = authenticationConfigured
	}
	payload := statusResponse{
		APIVersion: "v1",
		Timestamp:  now.Format(time.RFC3339Nano),
		Collector: collectorStatus{
			State:   collector.StateUnavailable,
			Reason:  collector.ReasonNotConnected,
			Message: "Waiting for Mihomo External Controller collection.",
		},
		Live: liveStatus{},
		Database: databaseStatus{
			Healthy:       database.Healthy,
			SizeBytes:     database.SizeBytes,
			SchemaVersion: database.SchemaVersion,
			JournalMode:   database.JournalMode,
			Error:         optionalString(database.Error),
		},
		Collection: collectionStatus{RecentGaps: []continuity.Gap{}},
		Configuration: configurationStatus{
			ControllerURL:            srv.configuration.ControllerURL,
			ControllerAuthentication: authentication,
			DashboardAddress:         srv.configuration.DashboardAddress,
			SampleInterval:           srv.configuration.SampleInterval.String(),
			DatabasePath:             srv.configuration.DatabasePath,
		},
	}
	gaps, err := srv.store.CollectionGaps(now.Add(-24*time.Hour), now.Add(time.Second))
	if err != nil {
		payload.Collection.Error = optionalString("Collection gap history could not be read from the local database.")
	} else {
		for index := range gaps {
			gap := gaps[index]
			if gap.Open {
				payload.Collection.CurrentGap = &gap
				continue
			}
			if len(payload.Collection.RecentGaps) < 5 {
				payload.Collection.RecentGaps = append(payload.Collection.RecentGaps, gap)
			}
		}
	}
	return payload
}

func applyCollectorSnapshot(payload *statusResponse, snapshot collector.Snapshot) {
	payload.Collector.State = snapshot.State
	payload.Collector.Reason = snapshot.Reason
	payload.Collector.Message = snapshot.Message
	payload.Collector.ControllerVersion = optionalString(snapshot.ControllerVersion)
	if snapshot.LastSample != nil {
		formatted := snapshot.LastSample.UTC().Format(time.RFC3339Nano)
		payload.Collector.LastSample = &formatted
	}
	payload.Live.UploadBytesPerSecond = snapshot.UploadBytesPerSecond
	payload.Live.DownloadBytesPerSecond = snapshot.DownloadBytesPerSecond
	payload.Live.ActiveConnections = snapshot.ActiveConnections
}

func (srv *server) liveEvents(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	if request.Method == http.MethodHead {
		response.WriteHeader(http.StatusOK)
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeJSON(response, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"code": "streaming_unsupported", "message": "Live event streaming is unavailable."},
		})
		return
	}

	if srv.monitor == nil {
		if err := writeStatusEvent(response, srv.statusPayload()); err != nil {
			return
		}
		flusher.Flush()
		<-request.Context().Done()
		return
	}

	updates, unsubscribe := srv.monitor.Subscribe()
	defer unsubscribe()
	keepalive := time.NewTicker(srv.keepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case snapshot, open := <-updates:
			if !open {
				return
			}
			payload := srv.baseStatusPayload()
			applyCollectorSnapshot(&payload, snapshot)
			if err := writeStatusEvent(response, payload); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := response.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeStatusEvent(response http.ResponseWriter, payload statusResponse) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = response.Write(append(append([]byte("event: status\ndata: "), encoded...), []byte("\n\n")...))
	return err
}

func (srv *server) openAPIDocument(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, openAPISpecification())
}

func (srv *server) apiNotFound(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusNotFound, map[string]any{
		"error": map[string]string{
			"code":    "not_found",
			"message": "The requested API resource does not exist.",
		},
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
