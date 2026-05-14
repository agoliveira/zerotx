package selfcheck

import (
	"fmt"
)

// Source is the daemon-internal data plane the comparator queries.
// Each call asks "what is the daemon's view of probe X right now?"
// Returns:
//
//   - status: the daemon's current view of that probe
//   - reason: a short human-readable detail (for the blocker message)
//   - tracked: true if the daemon actually knows about this probe;
//     false means "we have no observer for this probe, can't enforce"
//
// Implementations live in the daemon binary (cmd/zerotxd/selfcheck.go)
// so we keep this package free of devhealth/gps/etc imports and
// trivially unit-testable.
type Source interface {
	Status(probeID string) (status Status, reason string, tracked bool)
}

// Mismatch is one disagreement between baseline and daemon state.
// Used to build both human-readable blocker strings (via String)
// and the structured Preflight surface (future use).
type Mismatch struct {
	ProbeID  string
	Expected Status
	Actual   Status
	Reason   string
}

// String formats a mismatch as a one-line blocker. Example:
//
//	"hardware baseline: rp2040 expected pass, got down (no heartbeat)"
func (m Mismatch) String() string {
	if m.Reason == "" {
		return fmt.Sprintf("hardware baseline: %s expected %s, got %s",
			m.ProbeID, m.Expected, m.Actual)
	}
	return fmt.Sprintf("hardware baseline: %s expected %s, got %s (%s)",
		m.ProbeID, m.Expected, m.Actual, m.Reason)
}

// Compare evaluates a baseline against a Source and returns every
// mismatch. Skip rules:
//
//   - expected_status=skipped: not enforced (operator explicitly
//     skipped this probe at baseline time, can't disagree at runtime)
//   - expected_status=unknown: not enforced (probe was never run)
//   - expected_status=fail: not enforced (baselining a failure would
//     be unusual; we don't enforce "must still be broken")
//   - Source returns tracked=false: not enforced (daemon has no
//     observer; counted in Untracked() for diagnostics)
//
// Only expected_status=pass is actively enforced. A pass-expected
// probe whose actual status is anything other than pass produces a
// Mismatch.
//
// Returns mismatches in baseline order (deterministic for tests).
func Compare(b *Baseline, src Source) []Mismatch {
	if b == nil || src == nil {
		return nil
	}
	var out []Mismatch
	for _, p := range b.Probes {
		if p.ExpectedStatus != StatusPass {
			continue
		}
		actual, reason, tracked := src.Status(p.ID)
		if !tracked {
			continue
		}
		if actual == StatusPass {
			continue
		}
		out = append(out, Mismatch{
			ProbeID:  p.ID,
			Expected: p.ExpectedStatus,
			Actual:   actual,
			Reason:   reason,
		})
	}
	return out
}

// Untracked returns the probe IDs that the baseline declares
// pass-expected but the Source can't observe. Diagnostic info for
// logs; doesn't gate readiness. Helpful for spotting "the baseline
// expects the LED to work but the daemon has no LED observer --
// extend the Source to enforce this in a future release".
func Untracked(b *Baseline, src Source) []string {
	if b == nil || src == nil {
		return nil
	}
	var out []string
	for _, p := range b.Probes {
		if p.ExpectedStatus != StatusPass {
			continue
		}
		_, _, tracked := src.Status(p.ID)
		if !tracked {
			out = append(out, p.ID)
		}
	}
	return out
}
