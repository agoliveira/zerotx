package model

import "fmt"

// validFCTypes lists the flight controller firmware families that consumers
// know how to interpret telemetry from.
var validFCTypes = map[string]bool{
	"":           true, // unspecified is allowed
	"inav":       true,
	"ardupilot":  true,
	"betaflight": true,
}

// validAirframes lists the airframe classes the narrator/HUD recognize.
var validAirframes = map[string]bool{
	"":      true, // unspecified is allowed
	"quad":  true,
	"wing":  true,
	"plane": true,
	"heli":  true,
}

// Validate checks that the metadata is internally consistent. Returns
// the first problem found, or nil if everything looks OK. Callers
// (typically the daemon at model load time) should treat any error
// as a hard failure: a misconfigured threshold is worse than no
// threshold at all because it produces wrong-but-confident alarms.
//
// Optional fields (anything pointer-typed or string-empty) that are
// absent are accepted. When a section IS provided, all its required
// fields must be set and pass sanity checks.
func (m *ZeroTXMeta) Validate() error {
	if !validFCTypes[m.FCType] {
		return fmt.Errorf("zerotx.fc_type: invalid value %q (want one of: inav, ardupilot, betaflight, or empty)", m.FCType)
	}
	if !validAirframes[m.Airframe] {
		return fmt.Errorf("zerotx.airframe: invalid value %q (want one of: quad, wing, plane, heli, or empty)", m.Airframe)
	}

	if m.Thresholds == nil {
		return nil
	}
	t := m.Thresholds

	if t.Battery != nil {
		if err := validateBattery(t.Battery); err != nil {
			return err
		}
	}
	if t.Altitude != nil {
		if err := validateAltitude(t.Altitude); err != nil {
			return err
		}
	}
	if t.Distance != nil {
		if err := validateDistance(t.Distance); err != nil {
			return err
		}
	}
	if t.Link != nil {
		if err := validateLink(t.Link); err != nil {
			return err
		}
	}
	if t.FlightTime != nil {
		if err := validateFlightTime(t.FlightTime); err != nil {
			return err
		}
	}
	return nil
}

func validateBattery(b *BatteryThresholds) error {
	if b.Cells < 1 || b.Cells > 16 {
		return fmt.Errorf("zerotx.thresholds.battery.cells: %d outside reasonable range [1, 16]", b.Cells)
	}
	if b.CellMinV <= 0 || b.CellCritV <= 0 || b.CellWarnV <= 0 || b.CellFullV <= 0 {
		return fmt.Errorf("zerotx.thresholds.battery: all four cell voltages must be > 0 and explicitly set (got min=%.2f crit=%.2f warn=%.2f full=%.2f)",
			b.CellMinV, b.CellCritV, b.CellWarnV, b.CellFullV)
	}
	if !(b.CellMinV <= b.CellCritV && b.CellCritV <= b.CellWarnV && b.CellWarnV <= b.CellFullV) {
		return fmt.Errorf("zerotx.thresholds.battery: voltages must satisfy min <= crit <= warn <= full (got min=%.2f crit=%.2f warn=%.2f full=%.2f)",
			b.CellMinV, b.CellCritV, b.CellWarnV, b.CellFullV)
	}
	if b.CellFullV > 4.35 {
		return fmt.Errorf("zerotx.thresholds.battery.cell_full_v: %.2f exceeds reasonable maximum 4.35V (LiHV); check cell chemistry", b.CellFullV)
	}
	return nil
}

func validateAltitude(a *AltitudeThresholds) error {
	if a.WarnM < 0 || a.CritM < 0 {
		return fmt.Errorf("zerotx.thresholds.altitude: warn_m and crit_m must be >= 0 (got warn=%d crit=%d)", a.WarnM, a.CritM)
	}
	if a.WarnM >= a.CritM {
		return fmt.Errorf("zerotx.thresholds.altitude: warn_m must be < crit_m (got warn=%d crit=%d)", a.WarnM, a.CritM)
	}
	return nil
}

func validateDistance(d *DistanceThresholds) error {
	if d.WarnM < 0 || d.CritM < 0 {
		return fmt.Errorf("zerotx.thresholds.distance: warn_m and crit_m must be >= 0 (got warn=%d crit=%d)", d.WarnM, d.CritM)
	}
	if d.WarnM >= d.CritM {
		return fmt.Errorf("zerotx.thresholds.distance: warn_m must be < crit_m (got warn=%d crit=%d)", d.WarnM, d.CritM)
	}
	return nil
}

func validateLink(l *LinkThresholds) error {
	// RSSI is in dBm: less negative = stronger. Warn fires first (less negative
	// number is the higher value), crit fires later (more negative).
	if l.RSSIWarnDBM <= l.RSSICritDBM {
		return fmt.Errorf("zerotx.thresholds.link: rssi_warn_dbm must be > rssi_crit_dbm (less negative = stronger; got warn=%d crit=%d)",
			l.RSSIWarnDBM, l.RSSICritDBM)
	}
	if l.LQWarnPct < 0 || l.LQWarnPct > 100 || l.LQCritPct < 0 || l.LQCritPct > 100 {
		return fmt.Errorf("zerotx.thresholds.link: lq_warn_pct and lq_crit_pct must be in [0, 100] (got warn=%d crit=%d)",
			l.LQWarnPct, l.LQCritPct)
	}
	if l.LQWarnPct <= l.LQCritPct {
		return fmt.Errorf("zerotx.thresholds.link: lq_warn_pct must be > lq_crit_pct (higher LQ is better; got warn=%d crit=%d)",
			l.LQWarnPct, l.LQCritPct)
	}
	return nil
}

func validateFlightTime(f *FlightTimeThresholds) error {
	if f.WarnS <= 0 || f.CritS <= 0 {
		return fmt.Errorf("zerotx.thresholds.flight_time: warn_s and crit_s must be > 0 (got warn=%d crit=%d)", f.WarnS, f.CritS)
	}
	if f.WarnS >= f.CritS {
		return fmt.Errorf("zerotx.thresholds.flight_time: warn_s must be < crit_s (got warn=%d crit=%d)", f.WarnS, f.CritS)
	}
	return nil
}
