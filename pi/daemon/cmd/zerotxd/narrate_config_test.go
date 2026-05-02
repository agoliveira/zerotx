package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNarrateConfigYAMLRoundTrip(t *testing.T) {
	in := narrateConfig{
		Interval: 90 * time.Second,
		Fields:   []narrateField{fieldBattery, fieldDistance, fieldAltitude},
	}
	yamlForm := in.toYAML()
	if yamlForm.Interval != "1m30s" {
		t.Errorf("interval: got %q", yamlForm.Interval)
	}
	out, err := narrateConfigFromYAML(yamlForm)
	if err != nil {
		t.Fatalf("from yaml: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("got %#v want %#v", out, in)
	}
}

func TestNarrateConfigFromYAML_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   narrateConfigYAML
	}{
		{"bad-duration", narrateConfigYAML{Interval: "bogus"}},
		{"negative", narrateConfigYAML{Interval: "-5s"}},
		{"unknown-field", narrateConfigYAML{Interval: "60s", Fields: []string{"nope"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := narrateConfigFromYAML(c.in); err == nil {
				t.Errorf("expected error for %#v", c.in)
			}
		})
	}
}

func TestNarrateConfigFromYAML_CanonicalOrder(t *testing.T) {
	in := narrateConfigYAML{
		Interval: "60s",
		Fields:   []string{"altitude", "battery", "distance"},
	}
	got, err := narrateConfigFromYAML(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []narrateField{fieldBattery, fieldDistance, fieldAltitude}
	if !reflect.DeepEqual(got.Fields, want) {
		t.Errorf("got %v want %v", got.Fields, want)
	}
}

func TestNarrateConfigStore_LoadSetNotify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "narrate.yml")
	store := newNarrateConfigStore(path, narrateConfig{
		Interval: 60 * time.Second,
		Fields:   []narrateField{fieldBattery},
	})
	got := store.Load()
	if got.Interval != 60*time.Second {
		t.Errorf("initial interval: %v", got.Interval)
	}

	store.Set(narrateConfig{
		Interval: 30 * time.Second,
		Fields:   []narrateField{fieldDistance, fieldAltitude},
	})

	select {
	case <-store.Notify():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("did not receive notify")
	}

	got = store.Load()
	if got.Interval != 30*time.Second {
		t.Errorf("post-set interval: %v", got.Interval)
	}
	if len(got.Fields) != 2 {
		t.Errorf("post-set fields: %v", got.Fields)
	}
}

func TestNarrateConfigStore_Save(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "narrate.yml")
	store := newNarrateConfigStore(path, narrateConfig{
		Interval: 45 * time.Second,
		Fields:   []narrateField{fieldBattery, fieldLink},
	})
	saved, err := store.Save()
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved != path {
		t.Errorf("path: got %q want %q", saved, path)
	}

	// Reload via loadNarrateConfigFile; should match.
	loaded, err := loadNarrateConfigFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.Interval != 45*time.Second {
		t.Errorf("interval: %v", loaded.Interval)
	}
}

func TestLoadNarrateConfigFile_Missing(t *testing.T) {
	got, err := loadNarrateConfigFile("/nonexistent/zerotx/narrate.yml")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got.Interval != 0 || len(got.Fields) != 0 {
		t.Errorf("expected zero config, got %#v", got)
	}
}

func TestInitialNarrateConfig_FallsBackToCLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "narrate.yml")
	got := initialNarrateConfig(path, "", "battery,distance", "", 75*time.Second)
	if got.Interval != 75*time.Second {
		t.Errorf("interval: %v", got.Interval)
	}
	if len(got.Fields) != 2 {
		t.Errorf("fields: %v", got.Fields)
	}
}

func TestInitialNarrateConfig_PrefersFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "narrate.yml")
	yamlSrc := []byte("interval: 30s\nfields:\n  - link\n  - mode\n")
	if err := os.WriteFile(path, yamlSrc, 0o644); err != nil {
		t.Fatal(err)
	}
	got := initialNarrateConfig(path, "", "battery", "compact", 60*time.Second)
	if got.Interval != 30*time.Second {
		t.Errorf("file-interval: %v", got.Interval)
	}
	want := []narrateField{fieldLink, fieldMode}
	if !reflect.DeepEqual(got.Fields, want) {
		t.Errorf("file-fields: %v want %v", got.Fields, want)
	}
}
