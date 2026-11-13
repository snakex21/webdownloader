//go:build windows

package wv2host

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestStartExposesPrivateDevTools starts the hidden instance, checks that
// its DevTools endpoint lists one page and that Close removes the profile.
func TestStartExposesPrivateDevTools(t *testing.T) {
	if testing.Short() || !Available() {
		t.Skip("WebView2 runtime not available (or -short)")
	}
	h, err := Start(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.DevToolsURL, "ws://127.0.0.1:") {
		h.Close()
		t.Fatalf("DevToolsURL = %q", h.DevToolsURL)
	}
	base := "http://" + strings.TrimPrefix(strings.SplitN(h.DevToolsURL, "/devtools/", 2)[0], "ws://")
	resp, err := http.Get(base + "/json/list")
	if err != nil {
		h.Close()
		t.Fatal(err)
	}
	var targets []struct{ Type string }
	_ = json.NewDecoder(resp.Body).Decode(&targets)
	resp.Body.Close()
	if len(targets) == 0 || targets[0].Type != "page" {
		t.Errorf("targets = %+v", targets)
	}
	profile := h.userDataDir
	h.Close()
	h.Close() // idempotent
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Errorf("profile %s not removed (err=%v)", profile, err)
	}
}
