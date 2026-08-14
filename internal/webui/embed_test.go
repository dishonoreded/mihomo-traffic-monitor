package webui_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/webui"
)

func TestProductionAssetsAreEmbeddedAndOffline(t *testing.T) {
	index, err := fs.ReadFile(webui.Assets(), "index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	page := string(index)
	if !strings.Contains(page, "mihomo-monitor") {
		t.Fatal("embedded production index is not the observatory shell")
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Fatal("embedded production index references an external asset")
	}
}
