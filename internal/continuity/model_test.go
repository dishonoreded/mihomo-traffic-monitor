package continuity_test

import (
	"testing"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/continuity"
)

func TestEvaluateRecoveryTreatsBothDirectionsAsANewEpochAfterAnyReset(t *testing.T) {
	t.Parallel()

	previous := continuity.State{UploadTotal: 100, DownloadTotal: 200}
	current := continuity.State{UploadTotal: 3, DownloadTotal: 260}
	got := continuity.EvaluateRecovery(previous, current)
	if got != (continuity.Recovery{Disposition: continuity.DispositionReset}) {
		t.Fatalf("mixed-epoch recovery = %+v", got)
	}
}

func TestEvaluateRecoveryReturnsMonotonicDirectionalGrowth(t *testing.T) {
	t.Parallel()

	previous := continuity.State{UploadTotal: 100, DownloadTotal: 200}
	tests := []struct {
		name    string
		current continuity.State
		want    continuity.Recovery
	}{
		{name: "growth", current: continuity.State{UploadTotal: 130, DownloadTotal: 260}, want: continuity.Recovery{Disposition: continuity.DispositionRecovered, Upload: 30, Download: 60}},
		{name: "no growth", current: previous, want: continuity.Recovery{Disposition: continuity.DispositionNoGrowth}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := continuity.EvaluateRecovery(previous, test.current); got != test.want {
				t.Fatalf("recovery = %+v, want %+v", got, test.want)
			}
		})
	}
}
