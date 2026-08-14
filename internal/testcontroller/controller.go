package testcontroller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

type Options struct {
	RequiredSecret    string
	Version           string
	VersionStatus     int
	ConnectionsStatus int
	OnConnections     func(*http.Request, *websocket.Conn)
}

type Controller struct {
	URL               string
	VersionRequests   atomic.Int32
	ConnectionStreams atomic.Int32
}

func Start(t testing.TB, options Options) *Controller {
	t.Helper()
	controller := &Controller{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if options.RequiredSecret != "" && request.Header.Get("Authorization") != "Bearer "+options.RequiredSecret {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/version":
			controller.VersionRequests.Add(1)
			if options.VersionStatus != 0 && options.VersionStatus != http.StatusOK {
				http.Error(response, http.StatusText(options.VersionStatus), options.VersionStatus)
				return
			}
			version := options.Version
			if version == "" {
				version = "v1.19.0"
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"version": version})
		case "/connections":
			controller.ConnectionStreams.Add(1)
			if options.ConnectionsStatus != 0 && options.ConnectionsStatus != http.StatusOK {
				http.Error(response, http.StatusText(options.ConnectionsStatus), options.ConnectionsStatus)
				return
			}
			connection, err := websocket.Accept(response, request, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer connection.CloseNow()
			if options.OnConnections != nil {
				options.OnConnections(request, connection)
				return
			}
			<-request.Context().Done()
		default:
			http.NotFound(response, request)
		}
	}))
	controller.URL = server.URL
	t.Cleanup(server.Close)
	return controller
}

func DisconnectedURL() string {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	return url
}

func WriteSnapshot(ctx context.Context, connection *websocket.Conn, uploadTotal, downloadTotal, upload, download int64) error {
	payload, err := json.Marshal(map[string]any{
		"uploadTotal":   uploadTotal,
		"downloadTotal": downloadTotal,
		"connections": []any{
			map[string]any{
				"id":       "connection-1",
				"upload":   upload,
				"download": download,
				"metadata": map[string]any{"process": "Safari", "host": "example.com", "destinationIP": "203.0.113.10"},
			},
		},
	})
	if err != nil {
		return err
	}
	return connection.Write(ctx, websocket.MessageText, payload)
}
