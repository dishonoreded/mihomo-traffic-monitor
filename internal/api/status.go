package api

import (
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/collector"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/continuity"
)

type controllerAuthentication string

const (
	authenticationConfigured    controllerAuthentication = "configured"
	authenticationNotConfigured controllerAuthentication = "not_configured"
)

type statusResponse struct {
	APIVersion    string              `json:"apiVersion"`
	Timestamp     string              `json:"timestamp"`
	Collector     collectorStatus     `json:"collector"`
	Live          liveStatus          `json:"live"`
	Database      databaseStatus      `json:"database"`
	Collection    collectionStatus    `json:"collection"`
	Configuration configurationStatus `json:"configuration"`
}

type collectionStatus struct {
	CurrentGap *continuity.Gap  `json:"currentGap"`
	RecentGaps []continuity.Gap `json:"recentGaps"`
	Error      *string          `json:"error"`
}

type collectorStatus struct {
	State             collector.State  `json:"state"`
	Reason            collector.Reason `json:"reason"`
	Message           string           `json:"message"`
	ControllerVersion *string          `json:"controllerVersion"`
	LastSample        *string          `json:"lastSample"`
}

type liveStatus struct {
	UploadBytesPerSecond   int64 `json:"uploadBytesPerSecond"`
	DownloadBytesPerSecond int64 `json:"downloadBytesPerSecond"`
	ActiveConnections      int   `json:"activeConnections"`
}

type databaseStatus struct {
	Healthy       bool    `json:"healthy"`
	SizeBytes     int64   `json:"sizeBytes"`
	SchemaVersion int     `json:"schemaVersion"`
	JournalMode   string  `json:"journalMode"`
	Error         *string `json:"error"`
}

type configurationStatus struct {
	ControllerURL            string                   `json:"controllerUrl"`
	ControllerAuthentication controllerAuthentication `json:"controllerAuthentication"`
	DashboardAddress         string                   `json:"dashboardAddress"`
	SampleInterval           string                   `json:"sampleInterval"`
	DatabasePath             string                   `json:"databasePath"`
}
