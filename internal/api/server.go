package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/collector"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
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

func (srv *server) statusPayload() statusResponse {
	payload := srv.baseStatusPayload()
	if srv.monitor != nil {
		applyCollectorSnapshot(&payload, srv.monitor.Snapshot())
	}
	return payload
}

func (srv *server) baseStatusPayload() statusResponse {
	database := srv.store.Info()
	authentication := authenticationNotConfigured
	if srv.configuration.ControllerSecret != "" {
		authentication = authenticationConfigured
	}
	payload := statusResponse{
		APIVersion: "v1",
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
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
		Configuration: configurationStatus{
			ControllerURL:            srv.configuration.ControllerURL,
			ControllerAuthentication: authentication,
			DashboardAddress:         srv.configuration.DashboardAddress,
			SampleInterval:           srv.configuration.SampleInterval.String(),
			DatabasePath:             srv.configuration.DatabasePath,
		},
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
