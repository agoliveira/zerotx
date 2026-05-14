package selfcheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeSource is a deterministic Source for tests. Returns whatever
// the test sets up; tracked is false for any unknown ID.
type fakeSource map[string]struct {
	Status Status
	Reason string
}

func (f fakeSource) Status(id string) (Status, string, bool) {
	v, ok := f[id]
	if !ok {
		return StatusUnknown, "", false
	}
	return v.Status, v.Reason, true
}

func TestLoad_EmptyPath(t *testing.T) {
	b, err := Load("")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if b != nil {
		t.Fatalf("expected nil baseline, got %+v", b)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	b, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("unexpected err for missing file: %v", err)
	}
	if b != nil {
		t.Fatalf("expected nil baseline for missing file, got %+v", b)
	}
}

func TestLoad_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.yaml")
	content := `generated: 2026-05-14T22:30:00-03:00
host: cartman
probes:
  - id: rp2040
    name: RP2040 CRSF generator
    category: MCU
    expected_status: pass
    details:
      device: usb-Raspberry_Pi_Pico_E66...
  - id: mega
    name: Mega 2560 IO board
    category: MCU
    expected_status: pass
  - id: gps-ublox
    name: u-blox GPS
    category: UART
    expected_status: skipped
    notes: tested separately
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if b == nil {
		t.Fatalf("expected baseline, got nil")
	}
	if b.Host != "cartman" {
		t.Errorf("host: got %q want cartman", b.Host)
	}
	if len(b.Probes) != 3 {
		t.Fatalf("expected 3 probes, got %d", len(b.Probes))
	}
	if b.Probes[0].ID != "rp2040" || b.Probes[0].ExpectedStatus != StatusPass {
		t.Errorf("probe[0]: %+v", b.Probes[0])
	}
	if b.Probes[2].ExpectedStatus != StatusSkipped {
		t.Errorf("probe[2] expected skipped, got %v", b.Probes[2].ExpectedStatus)
	}
}

func TestLoad_Malformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("generated: not-a-date\nprobes: [not-a-list-item"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := Load(path)
	if err == nil {
		t.Errorf("expected error on malformed yaml; got baseline=%+v", b)
	}
}

func TestCompare_NilInputs(t *testing.T) {
	if got := Compare(nil, fakeSource{}); got != nil {
		t.Errorf("Compare(nil, _) = %v, want nil", got)
	}
	if got := Compare(&Baseline{}, nil); got != nil {
		t.Errorf("Compare(_, nil) = %v, want nil", got)
	}
}

func TestCompare_AllMatch(t *testing.T) {
	b := &Baseline{
		Probes: []ProbeExpect{
			{ID: "rp2040", ExpectedStatus: StatusPass},
			{ID: "mega", ExpectedStatus: StatusPass},
		},
	}
	src := fakeSource{
		"rp2040": {Status: StatusPass},
		"mega":   {Status: StatusPass},
	}
	if got := Compare(b, src); len(got) != 0 {
		t.Errorf("expected no mismatches, got %v", got)
	}
}

func TestCompare_OneMismatch(t *testing.T) {
	b := &Baseline{
		Probes: []ProbeExpect{
			{ID: "rp2040", ExpectedStatus: StatusPass},
			{ID: "mega", ExpectedStatus: StatusPass},
		},
	}
	src := fakeSource{
		"rp2040": {Status: StatusPass},
		"mega":   {Status: StatusFail, Reason: "down"},
	}
	got := Compare(b, src)
	want := []Mismatch{
		{ProbeID: "mega", Expected: StatusPass, Actual: StatusFail, Reason: "down"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCompare_SkipsNonPassExpected(t *testing.T) {
	// expected_status=skipped, unknown, fail should not be enforced.
	b := &Baseline{
		Probes: []ProbeExpect{
			{ID: "a", ExpectedStatus: StatusSkipped},
			{ID: "b", ExpectedStatus: StatusUnknown},
			{ID: "c", ExpectedStatus: StatusFail},
		},
	}
	// Source reports them all as fail; no mismatches should fire.
	src := fakeSource{
		"a": {Status: StatusFail},
		"b": {Status: StatusFail},
		"c": {Status: StatusPass}, // ironic: was baselined as fail, now passes
	}
	if got := Compare(b, src); len(got) != 0 {
		t.Errorf("expected no mismatches for non-pass expectations, got %v", got)
	}
}

func TestCompare_UntrackedProbesIgnored(t *testing.T) {
	b := &Baseline{
		Probes: []ProbeExpect{
			{ID: "rp2040", ExpectedStatus: StatusPass}, // tracked
			{ID: "rtc-ds3231", ExpectedStatus: StatusPass}, // not tracked
		},
	}
	src := fakeSource{
		"rp2040": {Status: StatusPass},
		// no rtc-ds3231 entry -> tracked=false
	}
	if got := Compare(b, src); len(got) != 0 {
		t.Errorf("expected untracked probes to be ignored, got %v", got)
	}
	untracked := Untracked(b, src)
	if len(untracked) != 1 || untracked[0] != "rtc-ds3231" {
		t.Errorf("Untracked() = %v, want [rtc-ds3231]", untracked)
	}
}

func TestMismatch_String(t *testing.T) {
	m := Mismatch{ProbeID: "rp2040", Expected: StatusPass, Actual: StatusFail, Reason: "no heartbeat"}
	got := m.String()
	want := "hardware baseline: rp2040 expected pass, got fail (no heartbeat)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	mNoReason := Mismatch{ProbeID: "mega", Expected: StatusPass, Actual: StatusFail}
	got = mNoReason.String()
	want = "hardware baseline: mega expected pass, got fail"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
