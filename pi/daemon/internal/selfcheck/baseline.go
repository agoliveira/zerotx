// Package selfcheck reads the hardware baseline produced by the
// zerotx-bench diagnostic tool and compares it against the daemon's
// own view of each device at startup. Mismatches surface as
// additional Preflight.Blockers entries so the operator sees them
// on /status alongside "no model loaded" and "device down: rp2040".
//
// The baseline file is what the bench tool exports when the
// operator presses "Export baseline". Format is hand-rolled YAML
// (see tools/zerotx-bench/baseline.go for the writer). This
// package parses it with gopkg.in/yaml.v3 -- the structure is
// shallow enough that the round-trip works without custom marshal
// hooks.
//
// Self-check is opt-in by file presence: if /etc/zerotx/hardware-
// baseline.yaml exists, it's enforced. If absent, the daemon
// behaves as it always has. The bench tool's "save baseline"
// button writes to the cwd by default; deployment is the
// operator's choice to copy the file to /etc/zerotx/ once the
// hardware is the way they want it.
package selfcheck

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Status mirrors the bench tool's Status enum. Values must match
// the strings the bench tool emits, since the same YAML is the
// wire format between them.
type Status string

const (
	StatusUnknown Status = "unknown"
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
)

// Baseline is the in-memory shape of hardware-baseline.yaml.
type Baseline struct {
	Generated time.Time     `yaml:"generated"`
	Host      string        `yaml:"host"`
	Probes    []ProbeExpect `yaml:"probes"`
}

// ProbeExpect is one probe's expected state at baseline time. The
// daemon will refuse to declare itself ready if the current state
// disagrees with this -- with the exception of expected_status
// values that don't make sense to enforce (unknown, fail).
type ProbeExpect struct {
	ID             string            `yaml:"id"`
	Name           string            `yaml:"name"`
	Category       string            `yaml:"category"`
	ExpectedStatus Status            `yaml:"expected_status"`
	Notes          string            `yaml:"notes,omitempty"`
	Details        map[string]string `yaml:"details,omitempty"`
}

// Load reads and parses a baseline file. Returns (nil, nil) if the
// path is empty (caller opted self-check off) OR the file doesn't
// exist (no baseline saved yet). Returns an error only on
// readable-but-malformed YAML; that case shouldn't crash the
// daemon, callers should log and proceed with self-check disabled.
func Load(path string) (*Baseline, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("selfcheck: read %s: %w", path, err)
	}
	var b Baseline
	if err := yaml.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("selfcheck: parse %s: %w", path, err)
	}
	return &b, nil
}
