package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
)

type server struct {
	configuration config.Config
	store         *storage.Store
	assets        fs.FS
}

func NewHandler(configuration config.Config, store *storage.Store, assets fs.FS) http.Handler {
	srv := &server{configuration: configuration, store: store, assets: assets}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/status", getOnly(http.HandlerFunc(srv.status)))
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
	database := srv.store.Info()
	authentication := authenticationNotConfigured
	if srv.configuration.ControllerSecret != "" {
		authentication = authenticationConfigured
	}
	payload := statusResponse{
		APIVersion: "v1",
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Collector: collectorStatus{
			State:   collectorUnavailable,
			Reason:  "not_connected",
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
	writeJSON(response, http.StatusOK, payload)
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
