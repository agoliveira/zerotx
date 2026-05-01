package display

import (
	"fmt"
	"strings"
)

// Thresholds is the per-domain alarm-band configuration the display
// uses to color its bars. A nil sub-pointer means "no thresholds for
// this domain"; the display falls back to neutral rendering for
// any domain whose sub-pointer is nil.
//
// All-nil Thresholds (or a nil *Thresholds passed to SetThresholds)
// clears every domain on the device.
//
// This type is independent of model.Thresholds so the display package
// stays free of model-package dependencies. Callers convert at the
// call site.
type Thresholds struct {
	Battery    *BatteryThresholds
	Altitude   *AltitudeThresholds
	Distance   *DistanceThresholds
	Link       *LinkThresholds
	FlightTime *FlightTimeThresholds
}

// BatteryThresholds carries pack-level voltages. Callers derive these
// from per-cell limits + cell count before passing in (the display
// device works in pack volts, not per-cell).
type BatteryThresholds struct {
	WarnV float64
	CritV float64
	MinV  float64
	FullV float64
}

type AltitudeThresholds struct {
	WarnM int
	CritM int
}

type DistanceThresholds struct {
	WarnM int
	CritM int
}

type LinkThresholds struct {
	RSSIWarnDBM int
	RSSICritDBM int
	LQWarnPct   int
	LQCritPct   int
}

type FlightTimeThresholds struct {
	WarnS int
	CritS int
}

// SetThresholds pushes alarm thresholds to the device. Pass nil to
// clear all thresholds (the display will fall back to neutral bars).
//
// Per the wire protocol, thresholds are one-shot: send once at model
// load, and the device caches the values until the next SetThresholds
// or until the connection resets. There is no periodic resend.
//
// This method emits at most one wire message regardless of how many
// domains are populated. It does not validate warn/crit ordering;
// the caller is responsible for sending self-consistent values
// (the schema validator in package model already enforces this for
// thresholds loaded from a model file).
func (d *Driver) SetThresholds(t *Thresholds) {
	d.enqueue(serializeThresholds(t))
}

// serializeThresholds builds the `DISP THRESHOLDS ...` wire line.
// Returns "DISP THRESHOLDS" (with no fields) when t is nil or has
// no domains set; the device interprets this as "clear all".
func serializeThresholds(t *Thresholds) string {
	var parts []string
	if t != nil {
		if b := t.Battery; b != nil {
			parts = append(parts,
				fmt.Sprintf("bat_warn=%.2f", b.WarnV),
				fmt.Sprintf("bat_crit=%.2f", b.CritV),
				fmt.Sprintf("bat_min=%.2f", b.MinV),
				fmt.Sprintf("bat_full=%.2f", b.FullV),
			)
		}
		if a := t.Altitude; a != nil {
			parts = append(parts,
				fmt.Sprintf("alt_warn=%d", a.WarnM),
				fmt.Sprintf("alt_crit=%d", a.CritM),
			)
		}
		if dist := t.Distance; dist != nil {
			parts = append(parts,
				fmt.Sprintf("dist_warn=%d", dist.WarnM),
				fmt.Sprintf("dist_crit=%d", dist.CritM),
			)
		}
		if l := t.Link; l != nil {
			parts = append(parts,
				fmt.Sprintf("rssi_warn=%d", l.RSSIWarnDBM),
				fmt.Sprintf("rssi_crit=%d", l.RSSICritDBM),
				fmt.Sprintf("lq_warn=%d", l.LQWarnPct),
				fmt.Sprintf("lq_crit=%d", l.LQCritPct),
			)
		}
		if ft := t.FlightTime; ft != nil {
			parts = append(parts,
				fmt.Sprintf("time_warn=%d", ft.WarnS),
				fmt.Sprintf("time_crit=%d", ft.CritS),
			)
		}
	}
	if len(parts) == 0 {
		return "DISP THRESHOLDS"
	}
	return "DISP THRESHOLDS " + strings.Join(parts, " ")
}
