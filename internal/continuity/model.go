package continuity

import "time"

type Reason string

const (
	ReasonMonitorRestart       Reason = "monitor_restart"
	ReasonCounterReset         Reason = "counter_reset"
	ReasonUnreachable          Reason = "unreachable"
	ReasonAuthenticationFailed Reason = "authentication_failed"
	ReasonIncompatibleVersion  Reason = "incompatible_version"
	ReasonInvalidSchema        Reason = "invalid_schema"
	ReasonDisconnected         Reason = "disconnected"
	ReasonStorageFailed        Reason = "storage_failed"
)

type Disposition string

const (
	DispositionOpen      Disposition = "open"
	DispositionRecovered Disposition = "recovered"
	DispositionNoGrowth  Disposition = "no_growth"
	DispositionReset     Disposition = "reset"
)

type State struct {
	SampledAt     time.Time
	UploadTotal   int64
	DownloadTotal int64
}

type Gap struct {
	ID                int64       `json:"id"`
	StartedAt         time.Time   `json:"startedAt"`
	EndedAt           *time.Time  `json:"endedAt"`
	Open              bool        `json:"open"`
	Reason            Reason      `json:"reason"`
	Disposition       Disposition `json:"disposition"`
	RecoveredUpload   int64       `json:"recoveredUpload"`
	RecoveredDownload int64       `json:"recoveredDownload"`
}

type Acceptance struct {
	Gap *Gap
}

type Recovery struct {
	Disposition Disposition
	Upload      int64
	Download    int64
}

func EvaluateRecovery(previous, current State) Recovery {
	if current.UploadTotal < previous.UploadTotal || current.DownloadTotal < previous.DownloadTotal {
		return Recovery{Disposition: DispositionReset}
	}
	recovery := Recovery{
		Disposition: DispositionNoGrowth,
		Upload:      current.UploadTotal - previous.UploadTotal,
		Download:    current.DownloadTotal - previous.DownloadTotal,
	}
	if recovery.Upload > 0 || recovery.Download > 0 {
		recovery.Disposition = DispositionRecovered
	}
	return recovery
}
