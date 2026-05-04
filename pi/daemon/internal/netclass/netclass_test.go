package netclass

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNew_NoFile_DefaultsOffline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netclass.json")
	h, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if h.Current() != Offline {
		t.Errorf("first run should default Offline; got %v", h.Current())
	}
}

func TestNew_LoadsExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netclass.json")
	if err := os.WriteFile(path, []byte(`{"class":"home"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if h.Current() != Home {
		t.Errorf("expected Home from file; got %v", h.Current())
	}
}

func TestNew_RejectsInvalidClass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netclass.json")
	if err := os.WriteFile(path, []byte(`{"class":"bogus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path); err == nil {
		t.Errorf("expected error on invalid class")
	}
}

func TestNew_RejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netclass.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path); err == nil {
		t.Errorf("expected parse error")
	}
}

func TestNew_NoPath_NoPersistence(t *testing.T) {
	h, err := New("")
	if err != nil {
		t.Fatalf("New(\"\"): %v", err)
	}
	if h.Current() != Offline {
		t.Errorf("default class should be Offline; got %v", h.Current())
	}
	// Set should succeed without writing anywhere.
	if err := h.Set(Home); err != nil {
		t.Errorf("Set on no-path holder: %v", err)
	}
	if h.Current() != Home {
		t.Errorf("Set didn't take effect")
	}
}

func TestSet_PersistsToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netclass.json")
	h, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Set(Free); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Reload from file.
	h2, err := New(path)
	if err != nil {
		t.Fatalf("New (reload): %v", err)
	}
	if h2.Current() != Free {
		t.Errorf("reload class = %v, want Free", h2.Current())
	}
}

func TestSet_RejectsInvalid(t *testing.T) {
	h, _ := New("")
	if err := h.Set(Class("nonsense")); err == nil {
		t.Errorf("expected ErrInvalidClass")
	}
}

func TestSet_NoOpOnSameClass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netclass.json")
	h, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	// Set to Home, capture timestamp.
	if err := h.Set(Home); err != nil {
		t.Fatal(err)
	}
	first := h.Snapshot().UpdatedAt
	// Set to Home again - should not bump timestamp.
	if err := h.Set(Home); err != nil {
		t.Fatal(err)
	}
	if !h.Snapshot().UpdatedAt.Equal(first) {
		t.Errorf("redundant Set should not update timestamp")
	}
}

func TestPersist_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netclass.json")
	h, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Set(Field); err != nil {
		t.Fatal(err)
	}
	// .tmp must not exist after a successful Set.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Errorf(".tmp file should be gone after successful Set")
	}
}

func TestSnapshot_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netclass.json")
	h, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Set(Home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal persisted snapshot: %v", err)
	}
	if snap.Class != Home {
		t.Errorf("persisted class = %v, want Home", snap.Class)
	}
	if snap.UpdatedAt.IsZero() {
		t.Errorf("persisted snapshot should have non-zero UpdatedAt")
	}
}

func TestClassPolicies(t *testing.T) {
	cases := []struct {
		c                  Class
		homeLAN, bgInet, fgInet bool
	}{
		{Home, true, true, true},
		{Free, false, true, true},
		{Field, false, false, true},
		{Offline, false, false, false},
	}
	for _, tc := range cases {
		if got := tc.c.AllowsHomeLAN(); got != tc.homeLAN {
			t.Errorf("%v.AllowsHomeLAN = %v, want %v", tc.c, got, tc.homeLAN)
		}
		if got := tc.c.AllowsBackgroundInternet(); got != tc.bgInet {
			t.Errorf("%v.AllowsBackgroundInternet = %v, want %v", tc.c, got, tc.bgInet)
		}
		if got := tc.c.AllowsForegroundInternet(); got != tc.fgInet {
			t.Errorf("%v.AllowsForegroundInternet = %v, want %v", tc.c, got, tc.fgInet)
		}
	}
}

func TestValid(t *testing.T) {
	for _, c := range AllClasses() {
		if !Valid(c) {
			t.Errorf("AllClasses contains invalid class %v", c)
		}
	}
	if Valid(Class("")) {
		t.Errorf("empty class should be invalid")
	}
	if Valid(Class("HOME")) {
		t.Errorf("class is case-sensitive; HOME should be invalid")
	}
}
