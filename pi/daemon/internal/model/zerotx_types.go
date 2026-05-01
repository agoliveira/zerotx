package model

// ZeroTXModel is the top-level container saved per model. It holds the
// imported EdgeTX content under EdgeTX, plus ZeroTX-specific configuration
// (which physical input drives each abstract source, alarm thresholds,
// aircraft taxonomy) under ZeroTX.
//
//	zerotx:
//	  source_bindings:
//	    Thr: { device: "HOTAS X", axis: 2 }
//	    SE:  { device: "GCS", switch: 2, kind: 3pos }
//	  fc_type: inav
//	  airframe: wing
//	  thresholds:
//	    battery:  { cells: 4, cell_warn_v: 3.6, ... }
//	    altitude: { warn_m: 100, crit_m: 200 }
//	    distance: { warn_m: 500, crit_m: 1500 }
//	edgetx:
//	  semver: 2.9.1
//	  header: ...
type ZeroTXModel struct {
	ZeroTX ZeroTXMeta  `yaml:"zerotx"`
	EdgeTX EdgeTXModel `yaml:"edgetx"`
}

// ZeroTXMeta carries everything ZeroTX itself adds to the model file.
// All fields except SourceBindings/Notes are optional; old model files
// authored before thresholds were introduced load and run unchanged.
type ZeroTXMeta struct {
	// SourceBindings maps an abstract EdgeTX source name (e.g. "Thr", "Ail",
	// "SE", "SA", "6POS") to the physical input that supplies its value.
	SourceBindings map[string]Binding `yaml:"source_bindings,omitempty"`

	// Notes is free-form text the user can leave in the file.
	Notes string `yaml:"notes,omitempty"`

	// FCType identifies the flight controller firmware family. Consumers
	// (narrator, HUD) use this to interpret telemetry semantics that vary
	// between INAV and ArduPilot. Empty means "unspecified". Valid values:
	// "inav", "ardupilot", "betaflight".
	FCType string `yaml:"fc_type,omitempty"`

	// Airframe describes the broad airframe class. Used by the narrator
	// for context-appropriate phrasing and by the HUD for default widget
	// selection. Empty means "unspecified". Valid values: "quad", "wing",
	// "plane", "heli".
	Airframe string `yaml:"airframe,omitempty"`

	// Thresholds groups all alarm/warning bands consumed by the display
	// device, narrator, and HUD. Nil means "no thresholds configured" and
	// consumers fall back to neutral display.
	Thresholds *Thresholds `yaml:"thresholds,omitempty"`
}

// Thresholds groups alarm bands by domain. Each sub-section is optional;
// nil means "no thresholds for this domain" and the corresponding
// consumer renders neutral / skips the alarm.
type Thresholds struct {
	Battery    *BatteryThresholds    `yaml:"battery,omitempty"`
	Altitude   *AltitudeThresholds   `yaml:"altitude,omitempty"`
	Distance   *DistanceThresholds   `yaml:"distance,omitempty"`
	Link       *LinkThresholds       `yaml:"link,omitempty"`
	FlightTime *FlightTimeThresholds `yaml:"flight_time,omitempty"`
}

// BatteryThresholds carries per-cell limits. Pack-level voltages are
// derived: PackWarnV = Cells * CellWarnV, etc. Per-cell storage means
// one schema works for any cell count without manual recomputation.
type BatteryThresholds struct {
	Cells     int     `yaml:"cells"`
	CellWarnV float64 `yaml:"cell_warn_v"`
	CellCritV float64 `yaml:"cell_crit_v"`
	CellMinV  float64 `yaml:"cell_min_v"`
	CellFullV float64 `yaml:"cell_full_v"`
}

// AltitudeThresholds defines AGL bands above which warnings/criticals fire.
type AltitudeThresholds struct {
	WarnM int `yaml:"warn_m"`
	CritM int `yaml:"crit_m"`
}

// DistanceThresholds defines distance-from-home bands.
type DistanceThresholds struct {
	WarnM int `yaml:"warn_m"`
	CritM int `yaml:"crit_m"`
}

// LinkThresholds defines RF link health bands. RSSI is in dBm (more
// negative = weaker), LQ is link quality percentage (higher = better).
// Warn fires before crit: RSSIWarnDBM > RSSICritDBM (e.g. -90 > -100),
// LQWarnPct > LQCritPct.
type LinkThresholds struct {
	RSSIWarnDBM int `yaml:"rssi_warn_dbm"`
	RSSICritDBM int `yaml:"rssi_crit_dbm"`
	LQWarnPct   int `yaml:"lq_warn_pct"`
	LQCritPct   int `yaml:"lq_crit_pct"`
}

// FlightTimeThresholds defines elapsed-mission-time bands.
type FlightTimeThresholds struct {
	WarnS int `yaml:"warn_s"`
	CritS int `yaml:"crit_s"`
}

// Binding describes one physical input. Exactly one of Axis/Button/Switch/
// Selector is set in a typical entry. Kind classifies discrete inputs:
// "2pos", "3pos", "momentary", "toggle", or "" for analog axes.
type Binding struct {
	Device   string  `yaml:"device"`
	Axis     *int    `yaml:"axis,omitempty"`
	Button   *int    `yaml:"button,omitempty"`
	Switch   *int    `yaml:"switch,omitempty"`
	Selector *int    `yaml:"selector,omitempty"`
	Kind     string  `yaml:"kind,omitempty"`
	Invert   bool    `yaml:"invert,omitempty"`
	Deadband float64 `yaml:"deadband,omitempty"`
}

// === Convenience accessors ===
//
// These helpers let callers avoid nil-checking the threshold pointer
// chain at every site. They return zero values when thresholds aren't
// configured; callers should pair them with HasXxxThresholds() when
// the distinction matters.

// HasBatteryThresholds reports whether battery thresholds are configured.
func (m *ZeroTXMeta) HasBatteryThresholds() bool {
	return m.Thresholds != nil && m.Thresholds.Battery != nil
}

// HasAltThresholds reports whether altitude thresholds are configured.
func (m *ZeroTXMeta) HasAltThresholds() bool {
	return m.Thresholds != nil && m.Thresholds.Altitude != nil
}

// HasDistThresholds reports whether distance thresholds are configured.
func (m *ZeroTXMeta) HasDistThresholds() bool {
	return m.Thresholds != nil && m.Thresholds.Distance != nil
}

// HasLinkThresholds reports whether link thresholds are configured.
func (m *ZeroTXMeta) HasLinkThresholds() bool {
	return m.Thresholds != nil && m.Thresholds.Link != nil
}

// HasFlightTimeThresholds reports whether flight time thresholds are configured.
func (m *ZeroTXMeta) HasFlightTimeThresholds() bool {
	return m.Thresholds != nil && m.Thresholds.FlightTime != nil
}

// PackWarnV returns the pack-level battery warning voltage. Returns 0
// if battery thresholds aren't configured; pair with HasBatteryThresholds.
func (m *ZeroTXMeta) PackWarnV() float64 {
	if !m.HasBatteryThresholds() {
		return 0
	}
	b := m.Thresholds.Battery
	return float64(b.Cells) * b.CellWarnV
}

// PackCritV returns the pack-level battery critical voltage.
func (m *ZeroTXMeta) PackCritV() float64 {
	if !m.HasBatteryThresholds() {
		return 0
	}
	b := m.Thresholds.Battery
	return float64(b.Cells) * b.CellCritV
}

// PackMinV returns the pack-level battery minimum (damage zone) voltage.
func (m *ZeroTXMeta) PackMinV() float64 {
	if !m.HasBatteryThresholds() {
		return 0
	}
	b := m.Thresholds.Battery
	return float64(b.Cells) * b.CellMinV
}

// PackFullV returns the pack-level battery fully-charged voltage,
// useful as the 100% reference for percentage gauges.
func (m *ZeroTXMeta) PackFullV() float64 {
	if !m.HasBatteryThresholds() {
		return 0
	}
	b := m.Thresholds.Battery
	return float64(b.Cells) * b.CellFullV
}
