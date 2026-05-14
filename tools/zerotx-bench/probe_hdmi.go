package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// hdmiProbe checks HDMI connector status by reading the kernel's
// DRM sysfs tree at /sys/class/drm/. Each HDMI-A connector has:
//
//	status   - "connected" or "disconnected"
//	enabled  - "enabled" if a framebuffer is bound
//	modes    - newline-separated mode list (first = preferred)
//
// Pass requires at least one connected HDMI. The bench uses two
// (HUD LCD on one port, map LCD on the other); the probe lists
// both as separate details rather than treating them as one unit
// so the operator can see which port is which.
//
// No test actions: HDMI is passive from the daemon side; the
// kiosk programs (Chrome) own the rendering and the bench tool
// has no business poking the framebuffer.
type hdmiProbe struct{}

const drmSysfs = "/sys/class/drm"

func (hdmiProbe) ID() string        { return "hdmi" }
func (hdmiProbe) Name() string      { return "HDMI displays" }
func (hdmiProbe) Category() string  { return "DRM" }
func (hdmiProbe) WiringRef() string { return "" }

func (hdmiProbe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{}}

	connectors, err := listHDMIConnectors()
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		r.Notes = "could not read " + drmSysfs + " -- is the kernel DRM driver enabled? On Pi the modern driver is vc4-kms-v3d"
		return r
	}
	if len(connectors) == 0 {
		r.Status = StatusFail
		r.Notes = "no HDMI-A connectors found in DRM sysfs -- system may be using legacy fbdev or no GPU driver is loaded"
		return r
	}

	connectedCount := 0
	for _, c := range connectors {
		key := c.name
		val := c.status
		if c.status == "connected" {
			connectedCount++
			if c.preferredMode != "" {
				val = fmt.Sprintf("connected, mode %s", c.preferredMode)
			}
			if c.enabled {
				val += " [framebuffer bound]"
			}
		}
		r.Details[key] = val
	}
	r.Details["connected count"] = fmt.Sprintf("%d of %d", connectedCount, len(connectors))

	if connectedCount == 0 {
		r.Status = StatusFail
		r.Notes = "DRM connectors enumerated but none report connected -- check HDMI cables and that the LCDs are powered"
		return r
	}
	r.Status = StatusPass
	if connectedCount == 1 {
		r.Notes = "only one HDMI connected; the ground station's kiosk layout uses two (HUD + map)"
	}
	return r
}

func (hdmiProbe) Tests() []TestAction { return nil }

// hdmiConnector is one HDMI-A connector parsed from sysfs.
type hdmiConnector struct {
	name          string // e.g. "card0-HDMI-A-1"
	status        string // "connected" or "disconnected"
	enabled       bool   // framebuffer is bound
	preferredMode string // first line of modes file, if connected
}

// listHDMIConnectors enumerates DRM HDMI-A connectors. Sorted by
// name for deterministic output.
func listHDMIConnectors() ([]hdmiConnector, error) {
	entries, err := os.ReadDir(drmSysfs)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", drmSysfs, err)
	}
	var out []hdmiConnector
	for _, e := range entries {
		name := e.Name()
		// Match cardN-HDMI-A-M pattern. Ignore DP, VGA, eDP, etc.
		if !strings.Contains(name, "-HDMI-A-") {
			continue
		}
		conn := hdmiConnector{name: name}
		conn.status = readSysAttr(filepath.Join(drmSysfs, name, "status"))
		conn.enabled = readSysAttr(filepath.Join(drmSysfs, name, "enabled")) == "enabled"
		if conn.status == "connected" {
			modes := readSysAttr(filepath.Join(drmSysfs, name, "modes"))
			if modes != "" {
				// First non-empty line is the preferred mode.
				for _, line := range strings.Split(modes, "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						conn.preferredMode = line
						break
					}
				}
			}
		}
		out = append(out, conn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}
