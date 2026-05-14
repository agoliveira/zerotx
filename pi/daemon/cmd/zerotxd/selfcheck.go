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
	"github.com/agoliveira/zerotx/pi/daemon/internal/heartbeat"
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
// and built after devhealth/gps/heartbeat/joystick are constructed.
//
// Probe IDs come from the bench tool's probe.ID() values. The
// mapping table lives here as a switch rather than a generic map
// so the relationship between bench probe IDs and daemon data
// sources is explicit and grep-able.
type daemonSource struct {
	devs     *devhealth.Registry
	gps      *gps.Reader
	hb       *heartbeat.Heartbeat
	jsHolder *joystickHolder
	rtcName  string // empty if /sys/class/rtc/rtc0/name was unreadable
}

func newDaemonSource(devs *devhealth.Registry, gpsRdr *gps.Reader, hb *heartbeat.Heartbeat, jsHolder *joystickHolder, rtcName string) *daemonSource {
	return &daemonSource{
		devs:     devs,
		gps:      gpsRdr,
		hb:       hb,
		jsHolder: jsHolder,
		rtcName:  rtcName,
	}
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
	case "rtc-ds3231":
		return s.rtcStatus()
	case "led-heartbeat":
		return s.ledHeartbeatStatus()
	case "joystick":
		return s.joystickStatus()
	default:
		// Untracked: audio, ELRS (both land in commit F2).
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

// rtcStatus reports whether the kernel detected a hardware RTC.
// The check happens once at daemon startup (via os.ReadFile on
// /sys/class/rtc/rtc0/name); we just cache the result. Pass when
// the kernel sees an RTC; fail when /sys/class/rtc/rtc0/name was
// unreadable at startup (no dtoverlay, chip not wired, etc).
//
// This is necessarily weaker than the bench tool's probe, which
// directly i2cdetects 0x68. The daemon trusts the kernel's
// detection result; if the kernel said "no rtc0" then the chip
// either isn't there or the dtoverlay wasn't loaded -- the bench
// distinction between the two doesn't matter for runtime gating.
func (s *daemonSource) rtcStatus() (selfcheck.Status, string, bool) {
	if s.rtcName == "" {
		return selfcheck.StatusFail, "kernel reports no rtc0", true
	}
	return selfcheck.StatusPass, "", true
}

// ledHeartbeatStatus reports whether the heartbeat LED driver
// successfully bound to a real GPIO line. Pass when the daemon
// is wrapping the real driver from heartbeat.NewReal; fail when
// it fell back to NewNull (gpio chip absent, -heartbeat-gpio < 0,
// NewReal returned error, etc).
//
// This tests the daemon's own decision about whether it can
// drive the LED. It doesn't verify the LED itself is wired or
// lit -- that requires an external observer (a phototransistor or
// the operator's eyes). The bench tool's probe is stronger here
// because it can re-open the chip and confirm it's not [used],
// then drive blinks; the daemon can only report what it believes
// at startup.
func (s *daemonSource) ledHeartbeatStatus() (selfcheck.Status, string, bool) {
	if s.hb == nil {
		return selfcheck.StatusUnknown, "", false
	}
	if s.hb.IsActive() {
		return selfcheck.StatusPass, "", true
	}
	return selfcheck.StatusFail, "null driver (gpio chip unavailable or -heartbeat-gpio disabled)", true
}

// joystickStatus reports whether any joystick is currently open
// AND connected. Pass requires both: a Reader exists in the holder
// AND it currently sees the device (SDL hot-plug Connected() flag).
// Fail when no reader, or reader exists but device is disconnected
// (USB cable yanked, etc).
//
// The bench tool's probe enumerates /dev/input/js* without
// committing to one; the daemon picks one via GUID match and is
// pickier. A successful daemon-side pass here implies the bench-
// tool probe would also pass (the device is plugged in and the
// daemon could open it).
func (s *daemonSource) joystickStatus() (selfcheck.Status, string, bool) {
	if s.jsHolder == nil {
		return selfcheck.StatusUnknown, "", false
	}
	r := s.jsHolder.Reader()
	if r == nil {
		return selfcheck.StatusFail, "no joystick open", true
	}
	if !r.Connected() {
		return selfcheck.StatusFail, "joystick disconnected (was: " + r.Name() + ")", true
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
