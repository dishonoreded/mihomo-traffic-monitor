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

type TrafficConnection struct {
	ID            string
	Upload        int64
	Download      int64
	Process       string
	SniffHost     string
	Host          string
	DestinationIP string
}

type TrafficSnapshot struct {
	UploadTotal   int64
	DownloadTotal int64
	Connections   []TrafficConnection
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
	return WriteTrafficSnapshot(ctx, connection, TrafficSnapshot{
		UploadTotal: uploadTotal, DownloadTotal: downloadTotal,
		Connections: []TrafficConnection{{
			ID: "connection-1", Upload: upload, Download: download,
			Process: "Safari", Host: "example.com", DestinationIP: "203.0.113.10",
		}},
	})
}

func WriteTrafficSnapshot(ctx context.Context, connection *websocket.Conn, snapshot TrafficSnapshot) error {
	connections := make([]any, 0, len(snapshot.Connections))
	for _, item := range snapshot.Connections {
		connections = append(connections, map[string]any{
			"id": item.ID, "upload": item.Upload, "download": item.Download,
			"metadata": map[string]any{
				"process": item.Process, "sniffHost": item.SniffHost, "host": item.Host, "destinationIP": item.DestinationIP,
			},
		})
	}
	payload, err := json.Marshal(map[string]any{
		"uploadTotal": snapshot.UploadTotal, "downloadTotal": snapshot.DownloadTotal, "connections": connections,
	})
	if err != nil {
		return err
	}
	return connection.Write(ctx, websocket.MessageText, payload)
}
