// Daemon-side wiring for the hardware-baseline self-check. The
// selfcheck package itself is pure (YAML reading + comparison); the
// glue that knows how to ask devhealth/gps/etc lives here.
//
// Lifecycle:
//
//  1. main() calls loadAndRunSelfCheck once at startup if the
//     baseline flag is set (default /etc/zerotx/hardware-baseline.yaml).
//     The function loads + parses asynchronously after a short
//     settle delay so devhealth has time to populate (typically
//     ~3 seconds covers the boot heartbeat exchange).
//
//  2. Compare() runs against a daemonSource that bridges probe IDs
//     to the daemon's existing observers (devhealth, gps.Reader).
//     Mismatches are stored in a hardwareBaselineHolder.
//
//  3. The Preflight provider in main.go appends the holder's current
//     mismatches to out.Blockers. Each mismatch becomes one blocker
//     line on /status.
//
// Probes the daemon doesn't currently track (RTC, heartbeat LED,
// joystick presence, audio, ELRS-as-distinct) are reported as
// untracked in startup logs; not enforced. Future commits can
// extend daemonSource with the missing observers without changes
// to the selfcheck package.
package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/devhealth"
	"github.com/agoliveira/zerotx/pi/daemon/internal/gps"
	"github.com/agoliveira/zerotx/pi/daemon/internal/selfcheck"
)

// hardwareBaselineHolder is the shared store for self-check
// mismatches. The Preflight provider reads it on each request; the
// background goroutine writes once at startup.
type hardwareBaselineHolder struct {
	mu           sync.RWMutex
	mismatches   []selfcheck.Mismatch
	baselinePath string
}

func newHardwareBaselineHolder(path string) *hardwareBaselineHolder {
	return &hardwareBaselineHolder{baselinePath: path}
}

// Blockers returns mismatch strings in the format expected by
// Preflight.Blockers. Returns nil if self-check is disabled, the
// baseline file is absent, or no mismatches were found.
func (h *hardwareBaselineHolder) Blockers() []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.mismatches) == 0 {
		return nil
	}
	out := make([]string, len(h.mismatches))
	for i, m := range h.mismatches {
		out[i] = m.String()
	}
	return out
}

// setMismatches stores the comparator result.
func (h *hardwareBaselineHolder) setMismatches(ms []selfcheck.Mismatch) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mismatches = ms
}

// daemonSource is the selfcheck.Source implementation that maps
// probe IDs to the daemon's existing observers. Owned by main()
// and built after devhealth/gps are constructed.
//
// Probe IDs come from the bench tool's probe.ID() values. The
// mapping table lives here as a switch rather than a generic map
// so the relationship between bench probe IDs and daemon data
// sources is explicit and grep-able.
type daemonSource struct {
	devs *devhealth.Registry
	gps  *gps.Reader
}

func newDaemonSource(devs *devhealth.Registry, gpsRdr *gps.Reader) *daemonSource {
	return &daemonSource{devs: devs, gps: gpsRdr}
}

// Status implements selfcheck.Source. Returns tracked=false for
// probes without a corresponding daemon observer; the selfcheck
// comparator skips those rather than treating them as mismatches.
func (s *daemonSource) Status(probeID string) (selfcheck.Status, string, bool) {
	switch probeID {
	case "rp2040", "mega", "esp32-display":
		// Direct devhealth name match.
		return s.devhealthStatus(probeID)
	case "hdmi":
		// Bench tool calls it "hdmi"; devhealth calls the same
		// thing "hdmi-displays". Bridge here.
		return s.devhealthStatus("hdmi-displays")
	case "gps-ublox":
		return s.gpsStatus()
	default:
		// Untracked: RTC, heartbeat LED, joystick, audio, ELRS.
		// The bench tool can probe these; the daemon has no
		// observer for them today.
		return selfcheck.StatusUnknown, "", false
	}
}

func (s *daemonSource) devhealthStatus(name string) (selfcheck.Status, string, bool) {
	if s.devs == nil {
		return selfcheck.StatusUnknown, "", false
	}
	sn, ok := s.devs.Snapshot(name)
	if !ok {
		// Not registered in devhealth -> not tracked, comparator skips.
		return selfcheck.StatusUnknown, "", false
	}
	switch sn.Status {
	case devhealth.StatusUp:
		return selfcheck.StatusPass, "", true
	case devhealth.StatusDown:
		reason := "no heartbeat"
		if sn.FirstError != "" {
			reason = sn.FirstError
		}
		return selfcheck.StatusFail, reason, true
	default:
		return selfcheck.StatusUnknown, "no status yet", true
	}
}

// gpsStatus interprets the GPS reader's view as a self-check status.
// Pass = reader has a valid fix. Fail = reader is connected but not
// fixed within tolerance. Mirrors the bench tool's GPS probe semantic
// where "chattering but no fix" is fail.
func (s *daemonSource) gpsStatus() (selfcheck.Status, string, bool) {
	if s.gps == nil {
		return selfcheck.StatusUnknown, "", false
	}
	state := s.gps.Get()
	if state.Fix == gps.FixNone {
		return selfcheck.StatusFail, "no fix", true
	}
	return selfcheck.StatusPass, "", true
}

// loadAndRunSelfCheck loads the baseline file (if present), waits
// for the settle delay, runs Compare(), and stores results in the
// holder. Logs progress at each step. Spawned as a goroutine; never
// returns an error to main() because self-check shouldn't be able
// to crash the daemon.
//
// settleDelay gives devhealth time to receive at least one
// heartbeat from each registered device before we ask its status.
// 3 seconds is well over the 200ms heartbeat interval; tunable via
// the flag if a particular install needs longer.
func loadAndRunSelfCheck(ctx context.Context, h *hardwareBaselineHolder, src selfcheck.Source, settleDelay time.Duration) {
	if h == nil || h.baselinePath == "" {
		return
	}
	b, err := selfcheck.Load(h.baselinePath)
	if err != nil {
		log.Printf("selfcheck: load %s failed (continuing without self-check): %v", h.baselinePath, err)
		return
	}
	if b == nil {
		log.Printf("selfcheck: no baseline at %s; self-check disabled", h.baselinePath)
		return
	}
	log.Printf("selfcheck: loaded baseline from %s (host=%s, %d probes), settling %s before comparison",
		h.baselinePath, b.Host, len(b.Probes), settleDelay)

	select {
	case <-time.After(settleDelay):
	case <-ctx.Done():
		return
	}

	mismatches := selfcheck.Compare(b, src)
	untracked := selfcheck.Untracked(b, src)

	if len(untracked) > 0 {
		log.Printf("selfcheck: %d pass-expected probes have no daemon observer (not enforced): %v",
			len(untracked), untracked)
	}
	if len(mismatches) == 0 {
		log.Printf("selfcheck: baseline matches current state (0 blockers)")
	} else {
		log.Printf("selfcheck: %d mismatch(es) -- listed as blockers in Preflight:", len(mismatches))
		for _, m := range mismatches {
			log.Printf("  - %s", m.String())
		}
	}
	h.setMismatches(mismatches)
}
