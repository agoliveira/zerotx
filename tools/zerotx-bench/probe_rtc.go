package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// rtcProbe verifies the DS3231 RTC module on I2C bus 1 (addr 0x68)
// and surfaces drift between the RTC and system time.
//
// Probing: runs `i2cdetect -y 1` and looks for the 0x68 row/col.
// Doesn't open the I2C device directly because that conflicts
// with the kernel's RTC driver (dtoverlay=i2c-rtc,ds3231 binds the
// chip to /dev/rtc0). Instead we read time via `hwclock`, which
// goes through the kernel RTC subsystem -- the same path used by
// the rest of the system.
//
// Test actions:
//   - "Read RTC time": shows RTC time, system time, and drift.
//   - "Sync RTC from system" / "Sync system from RTC": deferred to
//     a future commit -- writes are irreversible for the current
//     session and need their own confirmation UX.
type rtcProbe struct{}

const (
	rtcI2CBus     = 1
	rtcI2CAddress = "68"
	rtcDevice     = "/dev/rtc0"
)

func (rtcProbe) ID() string        { return "rtc-ds3231" }
func (rtcProbe) Name() string      { return "DS3231 RTC" }
func (rtcProbe) Category() string  { return "I2C" }
func (rtcProbe) WiringRef() string { return "ds3231-rtc-module-4-wires" }

func (rtcProbe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{}}
	r.Details["i2c bus"] = strconv.Itoa(rtcI2CBus)
	r.Details["i2c address"] = "0x" + rtcI2CAddress
	r.Details["rtc device"] = rtcDevice

	// Confirm the chip answers on the bus. i2cdetect prints a grid;
	// the address row has hex digits where chips ack, "--" where
	// they don't.
	out, err := runCmd(ctx, 2*time.Second, "i2cdetect", "-y", strconv.Itoa(rtcI2CBus))
	if err != nil {
		r.Status = StatusFail
		r.Error = "i2cdetect failed: " + err.Error()
		r.Notes = "is i2c-tools installed? `sudo apt install i2c-tools`"
		return r
	}
	if !strings.Contains(out, " "+rtcI2CAddress+" ") && !strings.Contains(out, "UU") {
		r.Status = StatusFail
		r.Notes = "no chip at 0x" + rtcI2CAddress + " on bus " + strconv.Itoa(rtcI2CBus) +
			" -- check wiring (SDA=pin 3, SCL=pin 5) and 3V3 power"
		return r
	}
	// i2cdetect either shows "68" (no driver bound) or "UU" (driver
	// has claimed it). Both mean a chip is there; the "UU" case is
	// the expected production state with dtoverlay=i2c-rtc,ds3231.
	if strings.Contains(out, "UU") {
		r.Details["kernel driver"] = "bound (UU on i2cdetect)"
	} else {
		r.Details["kernel driver"] = "not bound (raw 68 -- dtoverlay may be missing)"
	}

	// Read RTC time via hwclock and compare to system time.
	rtcTime, sysTime, drift, hwErr := readRTCAndDrift(ctx)
	if hwErr != nil {
		// Chip is there but we can't read it. Probably means
		// /dev/rtc0 doesn't exist (no dtoverlay) -- not fatal for
		// the probe, just diagnostic.
		r.Status = StatusPass
		r.Notes = "i2c chip present but hwclock read failed: " + hwErr.Error() +
			". Check that `dtoverlay=i2c-rtc,ds3231` is in /boot/config.txt and the Pi has been rebooted."
		return r
	}
	r.Details["rtc time"] = rtcTime.Format("2006-01-02 15:04:05 MST")
	r.Details["system time"] = sysTime.Format("2006-01-02 15:04:05 MST")
	r.Details["drift"] = fmt.Sprintf("%+.1fs (system − rtc)", drift.Seconds())
	r.Status = StatusPass
	if abs(drift) > 10*time.Second {
		r.Notes = "drift exceeds 10s -- consider `sudo hwclock -w` to sync RTC from system, or `sudo hwclock -s` to sync system from RTC (assuming the RTC has the correct time)"
	}
	return r
}

func (rtcProbe) Tests() []TestAction {
	return []TestAction{
		{
			ID:          "read-time",
			Label:       "Read RTC time",
			Description: "Reads /dev/rtc0 via hwclock and reports drift against system clock.",
			Run: func(ctx context.Context) (string, error) {
				rtcTime, sysTime, drift, err := readRTCAndDrift(ctx)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf(
					"RTC:    %s\nSystem: %s\nDrift:  %+.1fs",
					rtcTime.Format(time.RFC3339),
					sysTime.Format(time.RFC3339),
					drift.Seconds(),
				), nil
			},
		},
	}
}

// readRTCAndDrift reads the current RTC time via hwclock and returns
// (rtcTime, sysTime, sysTime-rtcTime). sysTime is sampled as close
// to the hwclock invocation as possible to keep the drift number
// meaningful.
func readRTCAndDrift(ctx context.Context) (time.Time, time.Time, time.Duration, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "hwclock", "-r", "--rtc="+rtcDevice).Output()
	sysTime := time.Now()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return time.Time{}, sysTime, 0, fmt.Errorf("hwclock: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return time.Time{}, sysTime, 0, err
	}
	// hwclock prints e.g. "2026-05-14 11:23:46.123456-03:00" -- the
	// fractional seconds and tz offset vary by version. Parse
	// flexibly.
	rtcTime, perr := parseHwclockOutput(strings.TrimSpace(string(out)))
	if perr != nil {
		return time.Time{}, sysTime, 0, perr
	}
	drift := sysTime.Sub(rtcTime)
	return rtcTime, sysTime, drift, nil
}

// parseHwclockOutput accepts the formats hwclock has used across
// util-linux versions:
//
//	"2026-05-14 11:23:46.123456-03:00"
//	"2026-05-14 11:23:46.123456 -03:00"
//	"Thu 14 May 2026 11:23:46 -03"
//
// The first two are util-linux 2.34+; the third is older. We try
// each in turn.
func parseHwclockOutput(s string) (time.Time, error) {
	// Strip any leading day-of-week if present.
	if i := strings.Index(s, " "); i > 0 && len(s[:i]) <= 3 {
		s = strings.TrimSpace(s[i+1:])
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999 -07:00",
		"02 Jan 2006 03:04:05 PM -07",
		"2 Jan 2006 15:04:05 -07",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse hwclock output: %q", s)
}

// runCmd runs a command with timeout, returns stdout as string.
// Used by probes that shell out to system tools (i2cdetect, hwclock,
// gpioget, etc.). Helper here so each probe doesn't duplicate the
// timeout pattern.
func runCmd(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s timed out after %s", name, timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return stdout.String(), err
		}
		return stdout.String(), fmt.Errorf("%s: %s", name, msg)
	}
	return stdout.String(), nil
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
