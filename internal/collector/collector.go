package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/continuity"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/traffic"
)

type State string

const (
	StateUnavailable State = "unavailable"
	StateConnecting  State = "connecting"
	StateConnected   State = "connected"
)

type Reason string

const (
	ReasonNotConnected         Reason = "not_connected"
	ReasonConnecting           Reason = "connecting"
	ReasonConnected            Reason = "connected"
	ReasonInvalidConfiguration Reason = "invalid_configuration"
	ReasonUnreachable          Reason = "unreachable"
	ReasonAuthenticationFailed Reason = "authentication_failed"
	ReasonIncompatibleVersion  Reason = "incompatible_version"
	ReasonInvalidSchema        Reason = "invalid_schema"
	ReasonDisconnected         Reason = "disconnected"
	ReasonStorageFailed        Reason = "storage_failed"
)

type TrafficSink interface {
	AddTraffic([]traffic.Record) error
	OpenCollectionGap(continuity.Reason) error
	AcceptSample(continuity.State, []traffic.Record) (continuity.Acceptance, error)
}

type Config struct {
	ControllerURL    string
	ControllerSecret string
	SampleInterval   time.Duration
	TrafficSink      TrafficSink
	Now              func() time.Time
}

type Snapshot struct {
	State                  State      `json:"state"`
	Reason                 Reason     `json:"reason"`
	Message                string     `json:"message"`
	ControllerVersion      string     `json:"controllerVersion,omitempty"`
	LastSample             *time.Time `json:"lastSample"`
	UploadBytesPerSecond   int64      `json:"uploadBytesPerSecond"`
	DownloadBytesPerSecond int64      `json:"downloadBytesPerSecond"`
	ActiveConnections      int        `json:"activeConnections"`
}

type Collector struct {
	configuration Config
	httpClient    *http.Client

	mutex       sync.RWMutex
	snapshot    Snapshot
	subscribers map[chan Snapshot]struct{}
}

type collectionError struct {
	reason  Reason
	message string
}

func (failure collectionError) Error() string { return failure.message }

type wireSnapshot struct {
	UploadTotal   *int64           `json:"uploadTotal"`
	DownloadTotal *int64           `json:"downloadTotal"`
	Connections   []wireConnection `json:"connections"`
}

type wireConnection struct {
	ID       string          `json:"id"`
	Upload   *int64          `json:"upload"`
	Download *int64          `json:"download"`
	Metadata json.RawMessage `json:"metadata"`
}

func New(configuration Config) *Collector {
	return &Collector{
		configuration: configuration,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		snapshot: Snapshot{
			State:   StateUnavailable,
			Reason:  ReasonNotConnected,
			Message: "Waiting for Mihomo External Controller collection.",
		},
		subscribers: make(map[chan Snapshot]struct{}),
	}
}

func (collector *Collector) Snapshot() Snapshot {
	collector.mutex.RLock()
	defer collector.mutex.RUnlock()
	return cloneSnapshot(collector.snapshot)
}

func (collector *Collector) Subscribe() (<-chan Snapshot, func()) {
	updates := make(chan Snapshot, 1)
	collector.mutex.Lock()
	collector.subscribers[updates] = struct{}{}
	updates <- cloneSnapshot(collector.snapshot)
	collector.mutex.Unlock()
	return updates, func() {
		collector.mutex.Lock()
		if _, exists := collector.subscribers[updates]; exists {
			delete(collector.subscribers, updates)
			close(updates)
		}
		collector.mutex.Unlock()
	}
}

func (collector *Collector) Run(ctx context.Context) {
	retryDelay := max(collector.configuration.SampleInterval, time.Second)
	if err := collector.openGap(continuity.ReasonMonitorRestart); err != nil {
		collector.publish(collector.transitionSnapshot(StateUnavailable, ReasonStorageFailed, err.Error()))
	}
	for ctx.Err() == nil {
		collector.publish(collector.transitionSnapshot(StateConnecting, ReasonConnecting, "Connecting to Mihomo External Controller."))
		failure := collector.collect(ctx)
		if ctx.Err() != nil {
			return
		}
		var classified collectionError
		if !errors.As(failure, &classified) {
			classified = collectionError{reason: ReasonUnreachable, message: "Mihomo External Controller is unavailable. Check its URL and that External Controller is enabled."}
		}
		if err := collector.openGap(continuityReason(classified.reason)); err != nil {
			classified = collectionError{reason: ReasonStorageFailed, message: err.Error()}
		}
		collector.publish(collector.transitionSnapshot(StateUnavailable, classified.reason, classified.message))
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (collector *Collector) transitionSnapshot(state State, reason Reason, message string) Snapshot {
	snapshot := collector.Snapshot()
	snapshot.State = state
	snapshot.Reason = reason
	snapshot.Message = message
	snapshot.UploadBytesPerSecond = 0
	snapshot.DownloadBytesPerSecond = 0
	snapshot.ActiveConnections = 0
	return snapshot
}

func (collector *Collector) collect(ctx context.Context) (result error) {
	version, err := collector.probeVersion(ctx)
	if err != nil {
		return err
	}
	connection, response, err := websocket.Dial(ctx, collector.connectionsURL(), &websocket.DialOptions{
		HTTPClient: collector.httpClient,
		HTTPHeader: collector.authorizationHeader(),
	})
	if err != nil {
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			return collectionError{reason: ReasonAuthenticationFailed, message: "Mihomo rejected the Controller secret. Update MIHOMO_MONITOR_CONTROLLER_SECRET and restart."}
		}
		return collectionError{reason: ReasonUnreachable, message: "Cannot open Mihomo's connections stream. Check External Controller access."}
	}
	defer connection.CloseNow()
	connection.SetReadLimit(16 << 20)
	reconciler := traffic.NewReconciler()
	defer func() {
		if err := collector.persist(reconciler.Flush()); err != nil {
			result = err
		}
	}()

	var previous wireSnapshot
	var previousAt time.Time
	baseline := true
	for {
		_, data, err := connection.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return collectionError{reason: ReasonDisconnected, message: "Mihomo's connections stream disconnected. Reconnecting automatically."}
		}
		var current wireSnapshot
		if err := json.Unmarshal(data, &current); err != nil {
			return collectionError{reason: ReasonInvalidSchema, message: "Mihomo returned an incompatible connections payload: invalid JSON or field types"}
		}
		if err := validateSnapshot(current); err != nil {
			return collectionError{reason: ReasonInvalidSchema, message: "Mihomo returned an incompatible connections payload: " + err.Error()}
		}
		now := collector.now().UTC()
		trafficSample, err := toTrafficSample(current, now)
		if err != nil {
			return collectionError{reason: ReasonInvalidSchema, message: "Mihomo returned an incompatible connections payload: " + err.Error()}
		}
		if err := collector.acceptSample(trafficSample, reconciler.Add(trafficSample)); err != nil {
			return err
		}
		live := Snapshot{
			State:             StateConnected,
			Reason:            ReasonConnected,
			Message:           "Live traffic collection is active.",
			ControllerVersion: version,
			LastSample:        &now,
			ActiveConnections: len(current.Connections),
		}
		if !baseline {
			elapsed := now.Sub(previousAt).Seconds()
			if elapsed > 0 {
				live.UploadBytesPerSecond = rate(*current.UploadTotal, *previous.UploadTotal, elapsed)
				live.DownloadBytesPerSecond = rate(*current.DownloadTotal, *previous.DownloadTotal, elapsed)
			}
		}
		collector.publish(live)
		baseline = false
		previous = current
		previousAt = now
	}
}

func (collector *Collector) now() time.Time {
	if collector.configuration.Now != nil {
		return collector.configuration.Now()
	}
	return time.Now()
}

func (collector *Collector) persist(records []traffic.Record) error {
	if collector.configuration.TrafficSink == nil || len(records) == 0 {
		return nil
	}
	if err := collector.configuration.TrafficSink.AddTraffic(records); err != nil {
		return collectionError{reason: ReasonStorageFailed, message: "Traffic history could not be written to the local database. Check database status and available disk space."}
	}
	return nil
}

func (collector *Collector) acceptSample(sample traffic.Sample, records []traffic.Record) error {
	if collector.configuration.TrafficSink == nil {
		return nil
	}
	if _, err := collector.configuration.TrafficSink.AcceptSample(continuity.State{
		SampledAt: sample.At, UploadTotal: sample.UploadTotal, DownloadTotal: sample.DownloadTotal,
	}, records); err != nil {
		return collectionError{reason: ReasonStorageFailed, message: "Traffic history could not be written to the local database. Check database status and available disk space."}
	}
	return nil
}

func (collector *Collector) openGap(reason continuity.Reason) error {
	if collector.configuration.TrafficSink == nil {
		return nil
	}
	if err := collector.configuration.TrafficSink.OpenCollectionGap(reason); err != nil {
		return collectionError{reason: ReasonStorageFailed, message: "Collection continuity could not be written to the local database. Check database status and available disk space."}
	}
	return nil
}

func continuityReason(reason Reason) continuity.Reason {
	switch reason {
	case ReasonAuthenticationFailed:
		return continuity.ReasonAuthenticationFailed
	case ReasonIncompatibleVersion:
		return continuity.ReasonIncompatibleVersion
	case ReasonInvalidSchema:
		return continuity.ReasonInvalidSchema
	case ReasonDisconnected:
		return continuity.ReasonDisconnected
	case ReasonStorageFailed:
		return continuity.ReasonStorageFailed
	default:
		return continuity.ReasonUnreachable
	}
}

func toTrafficSample(snapshot wireSnapshot, at time.Time) (traffic.Sample, error) {
	result := traffic.Sample{At: at, UploadTotal: *snapshot.UploadTotal, DownloadTotal: *snapshot.DownloadTotal}
	result.Connections = make([]traffic.Connection, 0, len(snapshot.Connections))
	for _, connection := range snapshot.Connections {
		var metadata struct {
			Process       string `json:"process"`
			SniffHost     string `json:"sniffHost"`
			Host          string `json:"host"`
			DestinationIP string `json:"destinationIP"`
		}
		if err := json.Unmarshal(connection.Metadata, &metadata); err != nil {
			return traffic.Sample{}, fmt.Errorf("connection metadata is invalid")
		}
		result.Connections = append(result.Connections, traffic.Connection{
			ID:            connection.ID,
			Upload:        *connection.Upload,
			Download:      *connection.Download,
			Process:       metadata.Process,
			SniffHost:     metadata.SniffHost,
			Host:          metadata.Host,
			DestinationIP: metadata.DestinationIP,
		})
	}
	return result, nil
}

func (collector *Collector) probeVersion(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(collector.configuration.ControllerURL, "/")+"/version", nil)
	if err != nil {
		return "", collectionError{reason: ReasonInvalidConfiguration, message: "The Mihomo Controller URL is invalid."}
	}
	request.Header = collector.authorizationHeader()
	response, err := collector.httpClient.Do(request)
	if err != nil {
		return "", collectionError{reason: ReasonUnreachable, message: "Mihomo External Controller is unavailable. Check its URL and that External Controller is enabled."}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return "", collectionError{reason: ReasonAuthenticationFailed, message: "Mihomo rejected the Controller secret. Update MIHOMO_MONITOR_CONTROLLER_SECRET and restart."}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", collectionError{reason: ReasonUnreachable, message: fmt.Sprintf("Mihomo version probe returned HTTP %d.", response.StatusCode)}
	}
	var payload struct {
		Version string `json:"version"`
	}
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&payload); err != nil || payload.Version == "" {
		return "", collectionError{reason: ReasonInvalidSchema, message: "Mihomo returned an invalid version response."}
	}
	if !strings.HasPrefix(payload.Version, "v1.") && !strings.HasPrefix(payload.Version, "1.") {
		return "", collectionError{reason: ReasonIncompatibleVersion, message: "This Mihomo version is not supported: " + payload.Version}
	}
	return payload.Version, nil
}

func (collector *Collector) connectionsURL() string {
	controllerURL, _ := url.Parse(collector.configuration.ControllerURL)
	if controllerURL.Scheme == "https" {
		controllerURL.Scheme = "wss"
	} else {
		controllerURL.Scheme = "ws"
	}
	controllerURL.Path = "/connections"
	query := controllerURL.Query()
	interval := max(collector.configuration.SampleInterval.Milliseconds(), int64(1))
	query.Set("interval", fmt.Sprintf("%d", interval))
	controllerURL.RawQuery = query.Encode()
	return controllerURL.String()
}

func (collector *Collector) authorizationHeader() http.Header {
	header := make(http.Header)
	if collector.configuration.ControllerSecret != "" {
		header.Set("Authorization", "Bearer "+collector.configuration.ControllerSecret)
	}
	return header
}

func (collector *Collector) publish(snapshot Snapshot) {
	collector.mutex.Lock()
	collector.snapshot = cloneSnapshot(snapshot)
	for subscriber := range collector.subscribers {
		select {
		case subscriber <- cloneSnapshot(snapshot):
		default:
			select {
			case <-subscriber:
			default:
			}
			subscriber <- cloneSnapshot(snapshot)
		}
	}
	collector.mutex.Unlock()
}

func validateSnapshot(snapshot wireSnapshot) error {
	if snapshot.UploadTotal == nil || snapshot.DownloadTotal == nil || snapshot.Connections == nil {
		return fmt.Errorf("required totals or connections are missing")
	}
	if *snapshot.UploadTotal < 0 || *snapshot.DownloadTotal < 0 {
		return fmt.Errorf("global counters must be non-negative")
	}
	for _, connection := range snapshot.Connections {
		if connection.ID == "" || connection.Upload == nil || connection.Download == nil || len(connection.Metadata) == 0 {
			return fmt.Errorf("connection identity, counters, or metadata are missing")
		}
		if *connection.Upload < 0 || *connection.Download < 0 || connection.Metadata[0] != '{' {
			return fmt.Errorf("connection counters or metadata are invalid")
		}
	}
	return nil
}

func rate(current, previous int64, elapsedSeconds float64) int64 {
	if current < previous {
		return 0
	}
	return int64(float64(current-previous) / elapsedSeconds)
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.LastSample != nil {
		lastSample := *snapshot.LastSample
		snapshot.LastSample = &lastSample
	}
	return snapshot
}
