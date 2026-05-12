package hdmihealth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a fake DRM tree at t.TempDir() / "drm" with the
// given connector statuses, and returns the glob pattern matching it.
func fixture(t *testing.T, connectors map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "drm")
	for name, status := range connectors {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status+"\n"), 0o644); err != nil {
			t.Fatalf("write status: %v", err)
		}
	}
	return filepath.Join(root, "card*-HDMI-*")
}

// TestScan_TwoConnected: both HDMI ports report connected.
// Operationally: the Pi 400's normal state with both kiosks wired.
func TestScan_TwoConnected(t *testing.T) {
	pat := fixture(t, map[string]string{
		"card1-HDMI-A-1": "connected",
		"card1-HDMI-A-2": "connected",
	})
	r, err := Scan(pat)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if r.Connected != 2 || r.Total != 2 {
		t.Errorf("counts: got %d/%d, want 2/2", r.Connected, r.Total)
	}
	if len(r.Detail) != 2 {
		t.Errorf("Detail len: got %d, want 2", len(r.Detail))
	}
}

// TestScan_OneConnected: one cable plugged in, one not. The
// failure mode we explicitly block on: <2 means flight is gated.
func TestScan_OneConnected(t *testing.T) {
	pat := fixture(t, map[string]string{
		"card1-HDMI-A-1": "connected",
		"card1-HDMI-A-2": "disconnected",
	})
	r, _ := Scan(pat)
	if r.Connected != 1 || r.Total != 2 {
		t.Errorf("counts: got %d/%d, want 1/2", r.Connected, r.Total)
	}
}

// TestScan_NoneConnected: both cables out (or no kiosks attached).
func TestScan_NoneConnected(t *testing.T) {
	pat := fixture(t, map[string]string{
		"card1-HDMI-A-1": "disconnected",
		"card1-HDMI-A-2": "disconnected",
	})
	r, _ := Scan(pat)
	if r.Connected != 0 || r.Total != 2 {
		t.Errorf("counts: got %d/%d, want 0/2", r.Connected, r.Total)
	}
}

// TestScan_NoMatches: the dev-machine case. No HDMI connectors in
// the fixture path at all. Scan returns zero Connected / zero Total
// and a nil error.
func TestScan_NoMatches(t *testing.T) {
	pat := filepath.Join(t.TempDir(), "doesnotexist/card*-HDMI-*")
	r, err := Scan(pat)
	if err != nil {
		t.Errorf("Scan on missing path should not error, got: %v", err)
	}
	if r.Connected != 0 || r.Total != 0 {
		t.Errorf("counts: got %d/%d, want 0/0", r.Connected, r.Total)
	}
}

// TestScan_DetailFormat: Detail entries are 'name: status'. Verify
// the format so the status page can display them consistently and
// the FirstError on a down device is human-readable.
func TestScan_DetailFormat(t *testing.T) {
	pat := fixture(t, map[string]string{
		"card1-HDMI-A-1": "connected",
		"card1-HDMI-A-2": "disconnected",
	})
	r, _ := Scan(pat)
	for _, d := range r.Detail {
		if !strings.Contains(d, ": ") {
			t.Errorf("detail line missing 'name: status' format: %q", d)
		}
	}
}

// TestScan_TrailingWhitespaceTrimmed: sysfs files end with newline.
// Our Scan must trim that before comparing, or "connected\n" !=
// "connected" and counts would silently be zero.
func TestScan_TrailingWhitespaceTrimmed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "drm")
	dir := filepath.Join(root, "card1-HDMI-A-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write with all of \r\n, trailing space, multiple newlines.
	// Real kernels just append \n, but defensive whitespace handling
	// costs nothing.
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte("connected  \r\n\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, _ := Scan(filepath.Join(root, "card*-HDMI-*"))
	if r.Connected != 1 {
		t.Errorf("Connected: got %d, want 1 (whitespace not trimmed)", r.Connected)
	}
}
