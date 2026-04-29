package recorder

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestRecorder(t *testing.T) (*Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := New(Config{RecordingsDir: dir, Keep: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r, dir
}

func TestArmDisarm_CreatesSavedFile(t *testing.T) {
	r, dir := newTestRecorder(t)
	r.OnArm("BigTalon", "/path/to/bigtalon.yml")
	time.Sleep(5 * time.Millisecond) // ensure non-zero session duration
	saved := r.OnDisarm()
	if saved == "" {
		t.Fatal("OnDisarm returned empty path")
	}
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("saved file does not exist: %v", err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Errorf("expected 1 saved file, got %d", len(files))
	}
}

func TestArmDisarm_FilenameContainsModel(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.OnArm("Big Talon!@#", "/x.yml")
	saved := r.OnDisarm()
	base := filepath.Base(saved)
	if !strings.Contains(base, "Big-Talon") {
		t.Errorf("expected sanitised model name in filename, got %q", base)
	}
	if !strings.HasSuffix(base, ".db") {
		t.Errorf("expected .db extension, got %q", base)
	}
}

func TestEvents_RecordedAndRetained(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.OnArm("M", "/m.yml")
	r.LogEvent("audio", "armed.1x", "critical", nil)
	r.LogEvent("alarm", "bat-low", "warning", map[string]string{"reason": "voltage"})
	saved := r.OnDisarm()

	db, err := sql.Open("sqlite", saved)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Expected: armed (auto), audio (manual), alarm (manual), disarmed (auto) = 4
	if n != 4 {
		t.Errorf("expected 4 events, got %d", n)
	}

	// Check the alarm row got its detail JSON.
	var detail string
	if err := db.QueryRow(`SELECT detail FROM events WHERE kind='alarm'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "voltage") {
		t.Errorf("expected detail JSON with 'voltage', got %q", detail)
	}
}

func TestTelemetry_RecordedAndThrottled(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.OnArm("M", "/m.yml")

	v := 14.8
	sample := TelemetrySample{BatVolts: &v}

	// Push 10 samples in quick succession; throttle should drop most.
	for i := 0; i < 10; i++ {
		r.LogTelemetry(sample)
	}
	// Sleep past the throttle window and push one more.
	time.Sleep(220 * time.Millisecond)
	r.LogTelemetry(sample)

	saved := r.OnDisarm()
	db, _ := sql.Open("sqlite", saved)
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM telemetry`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// First push always lets through; subsequent ones throttled. After
	// the sleep, one more goes through. So we expect 2.
	if n != 2 {
		t.Errorf("expected 2 telemetry rows after throttling, got %d", n)
	}
}

func TestTelemetry_PreArmDropped(t *testing.T) {
	r, _ := newTestRecorder(t)
	v := 14.8
	r.LogTelemetry(TelemetrySample{BatVolts: &v}) // before OnArm
	r.OnArm("M", "/m.yml")
	saved := r.OnDisarm()
	db, _ := sql.Open("sqlite", saved)
	defer db.Close()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM telemetry`).Scan(&n)
	if n != 0 {
		t.Errorf("pre-arm telemetry should not appear, got %d rows", n)
	}
}

func TestRotate_NextFlightIndependent(t *testing.T) {
	r, dir := newTestRecorder(t)

	// Flight 1
	r.OnArm("Plane1", "/p1.yml")
	r.LogEvent("audio", "armed.1x", "critical", nil)
	r.OnDisarm()

	// Flight 2 — should not see flight 1's events
	r.OnArm("Plane2", "/p2.yml")
	saved2 := r.OnDisarm()

	db, _ := sql.Open("sqlite", saved2)
	defer db.Close()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	// Expected: armed, disarmed = 2 (flight 1's audio event must NOT appear)
	if n != 2 {
		t.Errorf("expected 2 events in flight 2, got %d", n)
	}

	files, _ := os.ReadDir(dir)
	if len(files) != 2 {
		t.Errorf("expected 2 saved recordings, got %d", len(files))
	}
}

func TestCleanup_KeepsOnlyN(t *testing.T) {
	r, dir := newTestRecorder(t) // Keep=3
	for i := 0; i < 6; i++ {
		r.OnArm("X", "/x.yml")
		// Spread mod times so cleanup can pick by recency.
		time.Sleep(15 * time.Millisecond)
		r.OnDisarm()
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 3 {
		t.Errorf("expected 3 recordings (Keep=3), got %d", len(files))
	}
}

func TestRecordings_ListsNewestFirst(t *testing.T) {
	r, _ := newTestRecorder(t)
	for i := 0; i < 3; i++ {
		r.OnArm("X", "/x.yml")
		time.Sleep(15 * time.Millisecond)
		r.OnDisarm()
	}
	recs, err := r.Recordings()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("expected 3 recordings, got %d", len(recs))
	}
	// Newest first: ModTime descending lexicographically (RFC3339 sorts well).
	for i := 1; i < len(recs); i++ {
		if recs[i-1].ModTime < recs[i].ModTime {
			t.Errorf("recordings not sorted newest-first")
		}
	}
}

func TestNoArmIsNoSave(t *testing.T) {
	r, dir := newTestRecorder(t)
	saved := r.OnDisarm() // no preceding arm
	if saved != "" {
		t.Errorf("OnDisarm without OnArm should return empty, got %q", saved)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Errorf("expected no files, got %d", len(files))
	}
}

func TestDoubleArmNoOp(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.OnArm("X", "/x.yml")
	r.OnArm("Y", "/y.yml") // should be no-op (still armed)
	saved := r.OnDisarm()
	db, _ := sql.Open("sqlite", saved)
	defer db.Close()
	var name string
	db.QueryRow(`SELECT model_name FROM sessions LIMIT 1`).Scan(&name)
	if name != "X" {
		t.Errorf("expected model X (first arm), got %q", name)
	}
}

func TestSanitiseFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"BigTalon", "BigTalon"},
		{"Big Talon", "Big-Talon"},
		{"Big!@# Talon", "Big-Talon"},
		{"---weird---", "weird"},
		{"", "session"},
		{"!!!", "session"},
		{strings.Repeat("a", 50), strings.Repeat("a", 40)},
	}
	for _, c := range cases {
		got := sanitiseFilename(c.in)
		if got != c.want {
			t.Errorf("sanitiseFilename(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNoOpRecorder_Interface(t *testing.T) {
	var r Interface = NoOpRecorder{}
	r.OnArm("X", "/x.yml")
	r.LogEvent("audio", "x", "info", nil)
	r.LogTelemetry(TelemetrySample{})
	if got := r.OnDisarm(); got != "" {
		t.Errorf("NoOp OnDisarm should return empty, got %q", got)
	}
	recs, err := r.Recordings()
	if err != nil || recs != nil {
		t.Errorf("NoOp Recordings should be (nil, nil), got (%v, %v)", recs, err)
	}
	r.Close()
}
