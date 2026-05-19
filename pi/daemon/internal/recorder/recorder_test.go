package recorder

import (
	"database/sql"
	"errors"
	"io/fs"
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

// TestTelemetry_AttitudeRecorded verifies attitude_roll/pitch/yaw
// columns are populated when the sample carries them. Replay needs
// these to drive the HUD artificial horizon and GLCD pitch ladder;
// without this round-trip working, the replay would always show
// level flight.
func TestTelemetry_AttitudeRecorded(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.OnArm("M", "/m.yml")

	roll, pitch, yaw := 12.5, -8.0, 173.2
	r.LogTelemetry(TelemetrySample{
		AttitudeRoll:  &roll,
		AttitudePitch: &pitch,
		AttitudeYaw:   &yaw,
	})

	saved := r.OnDisarm()
	db, _ := sql.Open("sqlite", saved)
	defer db.Close()

	var gotRoll, gotPitch, gotYaw float64
	err := db.QueryRow(
		`SELECT attitude_roll, attitude_pitch, attitude_yaw FROM telemetry LIMIT 1`,
	).Scan(&gotRoll, &gotPitch, &gotYaw)
	if err != nil {
		t.Fatalf("query attitude: %v", err)
	}
	if gotRoll != roll || gotPitch != pitch || gotYaw != yaw {
		t.Errorf("attitude mismatch: got (%g, %g, %g), want (%g, %g, %g)",
			gotRoll, gotPitch, gotYaw, roll, pitch, yaw)
	}
}

// TestTelemetry_AttitudeNilStaysNull verifies that omitting attitude
// pointers leaves the columns NULL in the database. Distinguishes
// "no data" from "zero degrees" -- a level aircraft with 0/0/0
// attitude should still get a non-NULL row when telemetry is fresh.
func TestTelemetry_AttitudeNilStaysNull(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.OnArm("M", "/m.yml")

	v := 14.8
	r.LogTelemetry(TelemetrySample{BatVolts: &v}) // no attitude

	saved := r.OnDisarm()
	db, _ := sql.Open("sqlite", saved)
	defer db.Close()

	// Use sql.NullFloat64 because the columns are NULL when omitted;
	// scanning into a plain float64 would error.
	var roll, pitch, yaw sql.NullFloat64
	err := db.QueryRow(
		`SELECT attitude_roll, attitude_pitch, attitude_yaw FROM telemetry LIMIT 1`,
	).Scan(&roll, &pitch, &yaw)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if roll.Valid || pitch.Valid || yaw.Valid {
		t.Errorf("expected all attitude columns NULL when omitted, got roll=%v pitch=%v yaw=%v",
			roll, pitch, yaw)
	}
}

// TestTelemetry_AttitudeZeroPreserved is the partner to the nil
// test: an explicit zero must NOT be treated as missing. Pointer
// types in TelemetrySample exist specifically to make this
// distinction; a level aircraft with attitude {0, 0, 0} must
// produce non-NULL columns.
func TestTelemetry_AttitudeZeroPreserved(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.OnArm("M", "/m.yml")

	zero := 0.0
	r.LogTelemetry(TelemetrySample{
		AttitudeRoll:  &zero,
		AttitudePitch: &zero,
		AttitudeYaw:   &zero,
	})

	saved := r.OnDisarm()
	db, _ := sql.Open("sqlite", saved)
	defer db.Close()

	var roll, pitch, yaw sql.NullFloat64
	if err := db.QueryRow(
		`SELECT attitude_roll, attitude_pitch, attitude_yaw FROM telemetry LIMIT 1`,
	).Scan(&roll, &pitch, &yaw); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !roll.Valid || !pitch.Valid || !yaw.Valid {
		t.Errorf("explicit zero attitude was stored as NULL")
	}
	if roll.Float64 != 0 || pitch.Float64 != 0 || yaw.Float64 != 0 {
		t.Errorf("expected (0,0,0), got (%g,%g,%g)",
			roll.Float64, pitch.Float64, yaw.Float64)
	}
}

func TestPreserve_WritesSidecar(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.OnArm("X", "/x.yml")
	r.PreserveCurrentSession("failsafe")
	time.Sleep(5 * time.Millisecond)
	saved := r.OnDisarm()
	if saved == "" {
		t.Fatal("OnDisarm returned empty path")
	}
	sidecar := saved + preserveSuffix
	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "failsafe" {
		t.Errorf("sidecar content = %q, want %q", got, "failsafe")
	}
}

func TestPreserve_FlagResetsBetweenSessions(t *testing.T) {
	r, dir := newTestRecorder(t)

	// Session 1: preserved.
	r.OnArm("A", "/a.yml")
	r.PreserveCurrentSession("failsafe")
	time.Sleep(5 * time.Millisecond)
	first := r.OnDisarm()
	if _, err := os.Stat(first + preserveSuffix); err != nil {
		t.Fatalf("first session sidecar missing: %v", err)
	}

	// Session 2: NOT preserved -- the preserve flag from session 1
	// must NOT carry over.
	r.OnArm("B", "/b.yml")
	time.Sleep(5 * time.Millisecond)
	second := r.OnDisarm()
	if _, err := os.Stat(second + preserveSuffix); err == nil {
		t.Errorf("second session erroneously has a sidecar (preserve leaked across sessions)")
	}
	_ = dir
}

func TestCleanup_SkipsPreserved(t *testing.T) {
	r, dir := newTestRecorder(t) // Keep=3

	// Six sessions, second one preserved. Without the preserve flag
	// the second-oldest would be deleted; with it, the second-oldest
	// survives and instead one of the others gets aged out.
	var preservedPath string
	for i := 0; i < 6; i++ {
		r.OnArm("X", "/x.yml")
		if i == 1 {
			r.PreserveCurrentSession("test")
		}
		time.Sleep(15 * time.Millisecond)
		saved := r.OnDisarm()
		if i == 1 {
			preservedPath = saved
		}
	}

	// Confirm preserved .db still exists.
	if _, err := os.Stat(preservedPath); err != nil {
		t.Fatalf("preserved recording was deleted: %v", err)
	}

	// Count: 3 non-preserved newest + 1 preserved = 4 total .db files.
	files, _ := os.ReadDir(dir)
	dbCount := 0
	preserveCount := 0
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".db") {
			dbCount++
		}
		if strings.HasSuffix(f.Name(), preserveSuffix) {
			preserveCount++
		}
	}
	if dbCount != 4 {
		t.Errorf("expected 4 .db files (3 kept + 1 preserved), got %d", dbCount)
	}
	if preserveCount != 1 {
		t.Errorf("expected 1 .preserve sidecar, got %d", preserveCount)
	}
}

// === SetPreserved (post-flight operator action) ===

// helper: arm-then-disarm yields one saved .db whose basename the
// test can address via SetPreserved. Returns the basename plus the
// recordings dir for direct sidecar inspection.
func armDisarmOnce(t *testing.T) (*Recorder, string, string) {
	t.Helper()
	r, dir := newTestRecorder(t)
	r.OnArm("BigTalon", "/x.yml")
	time.Sleep(5 * time.Millisecond)
	saved := r.OnDisarm()
	if saved == "" {
		t.Fatalf("OnDisarm: no saved path")
	}
	return r, dir, filepath.Base(saved)
}

func TestSetPreserved_WritesSidecarWithReason(t *testing.T) {
	r, dir, name := armDisarmOnce(t)
	if err := r.SetPreserved(name, "operator", true); err != nil {
		t.Fatalf("SetPreserved(true): %v", err)
	}
	sidecar := filepath.Join(dir, name+preserveSuffix)
	body, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if string(body) != "operator\n" {
		t.Errorf("sidecar content = %q, want %q", string(body), "operator\n")
	}
}

func TestSetPreserved_RemovesSidecar(t *testing.T) {
	r, dir, name := armDisarmOnce(t)
	if err := r.SetPreserved(name, "operator", true); err != nil {
		t.Fatalf("preserve: %v", err)
	}
	if err := r.SetPreserved(name, "", false); err != nil {
		t.Fatalf("unpreserve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name+preserveSuffix)); !os.IsNotExist(err) {
		t.Errorf("sidecar still exists after unpreserve, stat err=%v", err)
	}
}

func TestSetPreserved_IdempotentOnPreserve(t *testing.T) {
	// Two preserve calls -> one sidecar, latest reason wins.
	r, dir, name := armDisarmOnce(t)
	if err := r.SetPreserved(name, "operator", true); err != nil {
		t.Fatalf("first preserve: %v", err)
	}
	if err := r.SetPreserved(name, "research", true); err != nil {
		t.Fatalf("second preserve: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, name+preserveSuffix))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if string(body) != "research\n" {
		t.Errorf("latest reason should win: got %q, want %q", string(body), "research\n")
	}
}

func TestSetPreserved_IdempotentOnUnpreserve(t *testing.T) {
	// Unpreserve when no sidecar exists is a quiet success, not an error.
	r, _, name := armDisarmOnce(t)
	if err := r.SetPreserved(name, "", false); err != nil {
		t.Errorf("unpreserve with no sidecar: want nil err, got %v", err)
	}
}

func TestSetPreserved_RejectsMissingRecording(t *testing.T) {
	r, _ := newTestRecorder(t)
	err := r.SetPreserved("nope-i-do-not-exist.db", "operator", true)
	if err == nil {
		t.Fatal("missing recording: want error, got nil")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing recording: want fs.ErrNotExist, got %v", err)
	}
}

func TestSetPreserved_RejectsInvalidName(t *testing.T) {
	// Path traversal, separators, and non-.db extensions all rejected.
	r, _ := newTestRecorder(t)
	cases := []string{
		"",
		"foo/bar.db",
		"foo\\bar.db",
		"../etc/passwd",
		"weird..name.db",
		"no-suffix",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			err := r.SetPreserved(name, "operator", true)
			if err == nil {
				t.Fatalf("name %q: want error, got nil", name)
			}
			// fs.ErrNotExist would be surprising here -- the validator
			// should reject before the stat call.
			if errors.Is(err, fs.ErrNotExist) {
				t.Errorf("name %q: got fs.ErrNotExist; should have been rejected by validator", name)
			}
		})
	}
}

func TestRecordings_PreservedFieldReflectsSidecar(t *testing.T) {
	r, _, name := armDisarmOnce(t)
	// Before preserve: Preserved=false.
	recs, err := r.Recordings()
	if err != nil {
		t.Fatalf("Recordings: %v", err)
	}
	if len(recs) != 1 || recs[0].Preserved {
		t.Fatalf("before preserve: got %+v, want one row with Preserved=false", recs)
	}
	// After preserve: Preserved=true.
	if err := r.SetPreserved(name, "operator", true); err != nil {
		t.Fatalf("preserve: %v", err)
	}
	recs, err = r.Recordings()
	if err != nil {
		t.Fatalf("Recordings: %v", err)
	}
	if len(recs) != 1 || !recs[0].Preserved {
		t.Fatalf("after preserve: got %+v, want one row with Preserved=true", recs)
	}
}
