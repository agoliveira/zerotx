package main

// narrate_config.go: persistent, live-updatable configuration for
// the periodic in-flight narrator.
//
// Storage: a small YAML file at $XDG_CONFIG_HOME/zerotx/narrate.yml
// (default $HOME/.config/zerotx/narrate.yml). The file holds two
// fields: interval (Go duration string, e.g. "60s") and fields (a
// list of narrateField names).
//
// Loading: at daemon startup, if the file exists it overrides CLI
// flag defaults. If it doesn't, CLI flags become the initial config
// (and remain volatile until a POST creates the file).
//
// Updating: the API server's POST handler validates new config,
// writes it to disk, then atomically swaps the in-memory pointer
// and pings the change channel so the periodic narrator wakes up
// and applies the new interval immediately.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/api"
	"gopkg.in/yaml.v3"
)

// narrateConfig is the operator-facing configuration for periodic
// narration. Both fields can be modified at runtime via the API.
type narrateConfig struct {
	Interval time.Duration
	Fields   []narrateField
}

// narrateConfigYAML is the on-disk shape. We keep it separate from
// the in-memory struct so the YAML serialisation is simple (interval
// as a string, fields as plain strings) without imposing on the
// runtime type.
type narrateConfigYAML struct {
	Interval string   `yaml:"interval"`
	Fields   []string `yaml:"fields"`
}

func (c narrateConfig) toYAML() narrateConfigYAML {
	out := narrateConfigYAML{
		Interval: c.Interval.String(),
		Fields:   make([]string, len(c.Fields)),
	}
	for i, f := range c.Fields {
		out.Fields[i] = string(f)
	}
	return out
}

func narrateConfigFromYAML(in narrateConfigYAML) (narrateConfig, error) {
	out := narrateConfig{}
	if in.Interval != "" {
		d, err := time.ParseDuration(in.Interval)
		if err != nil {
			return out, fmt.Errorf("invalid interval %q: %w", in.Interval, err)
		}
		if d <= 0 {
			return out, fmt.Errorf("interval must be positive, got %s", d)
		}
		out.Interval = d
	}
	for _, raw := range in.Fields {
		name := narrateField(strings.TrimSpace(strings.ToLower(raw)))
		valid := false
		for _, f := range allNarrateFields {
			if f == name {
				valid = true
				break
			}
		}
		if !valid {
			return out, fmt.Errorf("unknown field %q (valid: %s)", name,
				strings.Join(narrateFieldNames(), ", "))
		}
		out.Fields = append(out.Fields, name)
	}
	// Canonical ordering on save/load so the JSON round-trip is
	// stable for the GUI.
	out.Fields = sortFieldsCanonical(out.Fields)
	return out, nil
}

func sortFieldsCanonical(in []narrateField) []narrateField {
	have := map[narrateField]bool{}
	for _, f := range in {
		have[f] = true
	}
	out := make([]narrateField, 0, len(in))
	for _, f := range allNarrateFields {
		if have[f] {
			out = append(out, f)
		}
	}
	return out
}

// narrateConfigStore holds the live config and notifies subscribers
// when it changes. The pointer is updated atomically; notify is a
// buffered channel ("at least one wakeup pending" semantics).
type narrateConfigStore struct {
	p      atomic.Pointer[narrateConfig]
	notify chan struct{}
	path   string
	mu     sync.Mutex // serializes Save() to the same file
}

func newNarrateConfigStore(path string, initial narrateConfig) *narrateConfigStore {
	s := &narrateConfigStore{
		notify: make(chan struct{}, 1),
		path:   path,
	}
	c := initial
	s.p.Store(&c)
	return s
}

// Load returns a copy of the current config. Safe for concurrent use.
func (s *narrateConfigStore) Load() narrateConfig {
	if p := s.p.Load(); p != nil {
		return *p
	}
	return narrateConfig{}
}

// Set replaces the current config and pings the notify channel.
// Does not persist to disk; call Save separately if persistence
// is required.
func (s *narrateConfigStore) Set(c narrateConfig) {
	cc := c
	s.p.Store(&cc)
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Notify returns the channel that fires when Set is called. The
// channel is buffered (capacity 1) so missed wakeups coalesce.
func (s *narrateConfigStore) Notify() <-chan struct{} {
	return s.notify
}

// Save writes the current config to the configured YAML file path,
// creating parent directories if needed. Returns the path written.
// Atomic: writes to <path>.tmp then renames.
func (s *narrateConfigStore) Save() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return "", fmt.Errorf("no path configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	cfg := s.Load()
	data, err := yaml.Marshal(cfg.toYAML())
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	return s.path, nil
}

// loadNarrateConfigFile reads the YAML file and returns a parsed
// config. Returns (zero, nil) if the file doesn't exist (caller
// falls back to CLI defaults). Other I/O / parse errors are
// returned and should be logged.
func loadNarrateConfigFile(path string) (narrateConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return narrateConfig{}, nil
		}
		return narrateConfig{}, err
	}
	var raw narrateConfigYAML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return narrateConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return narrateConfigFromYAML(raw)
}

// defaultNarrateConfigPath returns the conventional persistence
// location respecting XDG_CONFIG_HOME.
func defaultNarrateConfigPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "zerotx", "narrate.yml")
	}
	return os.ExpandEnv("$HOME/.config/zerotx/narrate.yml")
}

// initialNarrateConfig assembles the startup config: prefers the
// persisted YAML file when present, otherwise falls back to the
// CLI flags. Returns the final config plus the path used (which
// is always returned, even if the file didn't exist, so future
// saves go to the canonical place).
func initialNarrateConfig(path, cliInterval, cliContent, cliPreset string, cliIntervalDur time.Duration) narrateConfig {
	if path == "" {
		path = defaultNarrateConfigPath()
	}
	cfg, err := loadNarrateConfigFile(path)
	if err != nil {
		log.Printf("narrate: failed to load %s (%v); falling back to CLI flags", path, err)
		cfg = narrateConfig{}
	}
	if cfg.Interval == 0 {
		cfg.Interval = cliIntervalDur
	}
	if len(cfg.Fields) == 0 {
		cfg.Fields = resolveNarrateContent(cliContent, cliPreset)
	}
	return cfg
}

// narrateConfigToAPI converts the internal config to the API wire
// shape. Used by the GET handler.
func narrateConfigToAPI(c narrateConfig) api.NarrateConfig {
	out := api.NarrateConfig{
		Interval: c.Interval.String(),
		Fields:   make([]string, len(c.Fields)),
	}
	for i, f := range c.Fields {
		out.Fields[i] = string(f)
	}
	return out
}

// narrateConfigFromAPI parses an API wire-shape config back into
// the internal type. Validates interval and field names; returns
// a friendly error message on failure (the API surfaces this
// directly to the operator).
func narrateConfigFromAPI(in api.NarrateConfig) (narrateConfig, error) {
	return narrateConfigFromYAML(narrateConfigYAML{
		Interval: in.Interval,
		Fields:   in.Fields,
	})
}
