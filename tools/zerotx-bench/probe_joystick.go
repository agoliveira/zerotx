package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// joystickProbe enumerates Linux jsdev nodes (/dev/input/js*),
// surfaces device names via sysfs, and captures a few seconds of
// activity in the test action so the operator can confirm the
// stick is actually responsive (and not just enumerated).
//
// Probes don't try to identify "the" joystick: any number of
// js* devices can be plugged in. The probe lists all of them and
// passes if at least one is present.
//
// Approach: jsdev API (legacy joystick interface) gives a clean
// 8-byte event stream that's easy to parse from Go without cgo
// or ioctl. The newer evdev API is more capable but heavier; for
// a presence + activity check, jsdev suffices.
type joystickProbe struct{}

const (
	jsDevDir = "/dev/input"
	// jsCaptureWindow caps the test action's read duration.
	// 3 seconds gives the operator time to wiggle a stick and
	// hit a button; longer would feel laggy.
	jsCaptureWindow = 3 * time.Second
)

func (joystickProbe) ID() string        { return "joystick" }
func (joystickProbe) Name() string      { return "USB joystick" }
func (joystickProbe) Category() string  { return "USB" }
func (joystickProbe) WiringRef() string { return "" }

func (joystickProbe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{}}

	devices, err := listJoysticks()
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		return r
	}
	if len(devices) == 0 {
		r.Status = StatusFail
		r.Notes = "no /dev/input/js* devices found -- plug in a USB joystick or check the hub"
		return r
	}

	r.Details["count"] = fmt.Sprintf("%d", len(devices))
	for i, d := range devices {
		key := d.path
		val := d.name
		if d.uniq != "" {
			val = fmt.Sprintf("%s (uniq=%s)", d.name, d.uniq)
		}
		r.Details[key] = val
		_ = i
	}
	r.Status = StatusPass
	if len(devices) > 1 {
		r.Notes = fmt.Sprintf("%d joysticks found -- the daemon's joystick selector picks one by GUID; use `Capture activity` to identify which is which", len(devices))
	}
	return r
}

func (joystickProbe) Tests() []TestAction {
	return []TestAction{
		{
			ID:          "capture",
			Label:       "Capture 3 seconds of activity",
			Description: "Reads jsdev events from every js* device and reports which axes moved and which buttons fired.",
			Run: func(ctx context.Context) (string, error) {
				devices, err := listJoysticks()
				if err != nil {
					return "", err
				}
				if len(devices) == 0 {
					return "", fmt.Errorf("no /dev/input/js* devices")
				}
				var sb strings.Builder
				for _, d := range devices {
					fmt.Fprintf(&sb, "=== %s (%s) ===\n", d.path, d.name)
					summary, err := captureJoystick(ctx, d.path, jsCaptureWindow)
					if err != nil {
						fmt.Fprintf(&sb, "  ERROR: %v\n", err)
						continue
					}
					sb.WriteString(summary)
					sb.WriteString("\n")
				}
				return sb.String(), nil
			},
		},
	}
}

// joystickInfo is one device's identifying fields.
type joystickInfo struct {
	path string // /dev/input/jsN
	name string // from sysfs
	uniq string // optional, from sysfs (often empty for cheap sticks)
}

// listJoysticks enumerates /dev/input/js* in numeric order and
// pairs each with its sysfs name + uniq. Returns empty slice
// (not error) if nothing is plugged in -- that's a valid system
// state for the bench tool.
func listJoysticks() ([]joystickInfo, error) {
	entries, err := os.ReadDir(jsDevDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", jsDevDir, err)
	}
	var devices []joystickInfo
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "js") {
			continue
		}
		path := filepath.Join(jsDevDir, name)
		info := joystickInfo{
			path: path,
			name: readSysAttr("/sys/class/input/" + name + "/device/name"),
			uniq: readSysAttr("/sys/class/input/" + name + "/device/uniq"),
		}
		if info.name == "" {
			info.name = "(unknown -- sysfs name unavailable)"
		}
		devices = append(devices, info)
	}
	// Sort by path so js0, js1, js2 come out in numeric order.
	sort.Slice(devices, func(i, j int) bool { return devices[i].path < devices[j].path })
	return devices, nil
}

func readSysAttr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// jsEvent is the on-the-wire 8-byte jsdev event structure. Layout
// from <linux/joystick.h>:
//
//	struct js_event {
//	    __u32 time;     // event timestamp ms
//	    __s16 value;    // axis value or button state
//	    __u8  type;     // 0x01=button, 0x02=axis, |0x80=init (sync)
//	    __u8  number;   // axis or button index
//	};
type jsEvent struct {
	Time   uint32
	Value  int16
	Type   uint8
	Number uint8
}

const (
	jsEventButton = 0x01
	jsEventAxis   = 0x02
	jsEventInit   = 0x80 // OR'd into Type on the synthetic startup events
)

// captureJoystick opens a jsdev, reads events for the given window,
// and produces a human-readable summary. Filters out the synthetic
// init events sent at open (Type & jsEventInit) so the summary
// reflects real operator activity, not the initial state dump.
func captureJoystick(ctx context.Context, path string, window time.Duration) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Best-effort read deadline; some kernels don't honor it on
	// jsdev. The context goroutine below is the backstop.
	_ = f.SetReadDeadline(time.Now().Add(window))
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(window + 100*time.Millisecond):
		}
		_ = f.Close()
	}()

	axisMin := map[uint8]int16{}
	axisMax := map[uint8]int16{}
	buttonPresses := map[uint8]int{}
	totalEvents := 0
	initEvents := 0

	buf := make([]byte, 8)
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		n, err := io.ReadFull(f, buf)
		if err != nil || n != 8 {
			break
		}
		ev := jsEvent{
			Time:   binary.LittleEndian.Uint32(buf[0:4]),
			Value:  int16(binary.LittleEndian.Uint16(buf[4:6])),
			Type:   buf[6],
			Number: buf[7],
		}
		isInit := ev.Type&jsEventInit != 0
		realType := ev.Type &^ jsEventInit
		if isInit {
			initEvents++
			continue
		}
		totalEvents++
		switch realType {
		case jsEventAxis:
			if cur, ok := axisMin[ev.Number]; !ok || ev.Value < cur {
				axisMin[ev.Number] = ev.Value
			}
			if cur, ok := axisMax[ev.Number]; !ok || ev.Value > cur {
				axisMax[ev.Number] = ev.Value
			}
		case jsEventButton:
			if ev.Value != 0 {
				buttonPresses[ev.Number]++
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "  init events: %d (axis/button counts implied)\n", initEvents)
	fmt.Fprintf(&sb, "  real events: %d in %s\n", totalEvents, window)
	if totalEvents == 0 {
		fmt.Fprintf(&sb, "  no operator activity detected -- wiggle a stick or press a button\n")
		return sb.String(), nil
	}
	if len(axisMin) > 0 {
		fmt.Fprintf(&sb, "  axes moved:\n")
		axes := sortKeys(axisMin)
		for _, k := range axes {
			fmt.Fprintf(&sb, "    axis %2d: %6d .. %6d\n", k, axisMin[k], axisMax[k])
		}
	}
	if len(buttonPresses) > 0 {
		fmt.Fprintf(&sb, "  buttons pressed:\n")
		btns := sortKeys(buttonPresses)
		for _, k := range btns {
			fmt.Fprintf(&sb, "    button %2d: %d press(es)\n", k, buttonPresses[k])
		}
	}
	return sb.String(), nil
}

func sortKeys[V any](m map[uint8]V) []uint8 {
	out := make([]uint8, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
