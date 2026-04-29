// Package source resolves EdgeTX source names (I0, Thr, SF2, L3, !L3, 6POS,
// 6P15, RxBatt, -99, MAX, ...) to numeric values in [-1, 1] or to booleans.
//
// The resolver is the single source of truth for what a name means. Both the
// mapper (when evaluating mix sources) and the logic engine (when evaluating
// switch operands) call into this package, so semantics stay consistent.
//
// Inputs the resolver reads from:
//
//	*model.ZeroTXModel  for input names, source bindings, telemetry sensor
//	                    list, gvars
//	JoystickState       axes/buttons by index (used via bindings for
//	                    Thr/Ail/Ele/Rud and any other axis-bound source)
//	PanelState          name-keyed switches/selectors/buttons (GCS panel)
//	LogicState          previous-tick logic switch bitmap
//	Telemetry           live telemetry sensor values; NullTelemetry until
//	                    real plumbing lands
//
// Any of those may be nil; the resolver returns (0, false) for sources that
// would have come from the missing source.
package source

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
)

// JoystickState mirrors mapper.InputState for the parts the resolver needs.
type JoystickState interface {
	AxisValue(device string, axis int) (float64, bool)
	Button(device string, button int) (bool, bool)
}

// PanelState mirrors mapper.PanelState.
type PanelState interface {
	Switch(name string) (pos int, ok bool)
	Selector(name string) (pos int, ok bool)
	Button(name string) (pressed bool, ok bool)
}

// LogicState exposes the previous-tick logic switch bitmap. The logic
// engine satisfies this interface; reads inside one tick get the prior
// tick's state, which sidesteps L→L dependency cycles.
type LogicState interface {
	Logic(idx int) bool // 1-indexed: Logic(3) -> L3
}

// Telemetry abstracts the telemetry data feed. NullTelemetry returns
// (0, false) for every read; real telemetry implementations drop in
// later by satisfying this interface.
type Telemetry interface {
	Value(name string) (float64, bool)
}

// NullTelemetry is the fallback when no telemetry is connected.
type NullTelemetry struct{}

// Value implements Telemetry; always (0, false).
func (NullTelemetry) Value(string) (float64, bool) { return 0, false }

// NullLogic is the fallback when no logic engine has run yet (first tick).
type NullLogic struct{}

// Logic implements LogicState; always false.
func (NullLogic) Logic(int) bool { return false }

// Resolver resolves source names. All fields except Model may be nil.
type Resolver struct {
	Model    *model.ZeroTXModel
	Joystick JoystickState
	Panel    PanelState
	Logic    LogicState
	Telem    Telemetry
}

// New constructs a Resolver. Any nil field falls back to a null
// implementation that returns (0, false) / false for its category.
func New(m *model.ZeroTXModel, js JoystickState, p PanelState, l LogicState, t Telemetry) *Resolver {
	if l == nil {
		l = NullLogic{}
	}
	if t == nil {
		t = NullTelemetry{}
	}
	return &Resolver{
		Model:    m,
		Joystick: js,
		Panel:    p,
		Logic:    l,
		Telem:    t,
	}
}

// ResolveValue returns the source's value in [-1, 1].
//
// Recognized name forms (in order tried):
//
//	"!" prefix          negation (recurses)
//	numeric             percent constant; "-99" -> -0.99
//	"I" + digits        input by index, looked up via inputNames
//	stick/input alias   Thr / Ail / Ele / Rud (resolved via binding)
//	logic switch        L1..L64 -> +1 if true, -1 if false
//	switch position     SF2, SA1, 6P15 -> +1 if matched, -1 otherwise
//	bare switch         SA, SB, ..., SH -> -1/0/+1 per position
//	bare selector       6POS -> -1..+1 across N positions
//	flight mode         FM0..FM8 -> +1 if active, -1 otherwise
//	MAX                 always +1
//	trim                TrmH/V/A/E/R/T -> 0, ok=false (deferred)
//	gvar                GV1..GV9 -> 0, ok=false (deferred)
//	telemetry           via Telem; NullTelemetry returns (0, false)
//
// Returns (0, false) if the name is unrecognized or its source is missing.
func (r *Resolver) ResolveValue(name string) (float64, bool) {
	if name == "" || name == "NONE" || name == "---" || name == "--" {
		return 0, false
	}

	// Negation prefix.
	if strings.HasPrefix(name, "!") {
		v, ok := r.ResolveValue(name[1:])
		if !ok {
			return 0, false
		}
		return -v, true
	}

	// MAX = always +1 (EdgeTX "always on" source).
	if name == "MAX" {
		return 1.0, true
	}

	// Numeric constant: percentage, scaled to [-1, 1] (clamped).
	if v, ok := parsePercent(name); ok {
		return v, true
	}

	// Input by index: I0..IN.
	if strings.HasPrefix(name, "I") && len(name) > 1 && isAllDigits(name[1:]) {
		if r.Model == nil {
			return 0, false
		}
		idx, _ := strconv.Atoi(name[1:])
		alias := r.Model.EdgeTX.InputName(idx)
		if alias == "" {
			return 0, false
		}
		return r.resolveBoundSource(alias)
	}

	// Logic switch as value: L1..L64.
	if idx, ok := parseLogicRef(name); ok {
		if r.Logic.Logic(idx) {
			return 1.0, true
		}
		return -1.0, true
	}

	// Trims, pots, sliders: deferred sources, but they're hardware-specific
	// names that can collide with the switch-position pattern (e.g. "SL1"
	// would otherwise parse as switch SL pos 1). Check these first.
	if isTrimName(name) || isPotName(name) || isSliderName(name) || isGVarName(name) {
		return 0, false
	}

	// Switch position match (SF2, SA1, 6P15) used as value: +1 if in
	// position, -1 otherwise. Common in EdgeTX as a sticky operand.
	if sw, pos, ok := parseSwitchPosition(name); ok {
		if r.matchesSwitchPosition(sw, pos) {
			return 1.0, true
		}
		return -1.0, true
	}

	// Flight mode reference (FM0..FM8) as value.
	if idx, ok := parseFlightModeRef(name); ok {
		if r.flightModeActive(idx) {
			return 1.0, true
		}
		return -1.0, true
	}

	// Bare switch / selector / stick alias -> via source binding.
	v, ok := r.resolveBoundSource(name)
	if ok {
		return v, true
	}

	// Telemetry: name must appear in the model's telemetrySensors list to
	// avoid silently swallowing typos.
	if r.isTelemetryName(name) {
		return r.Telem.Value(name)
	}

	return 0, false
}

// ResolveBool evaluates the source as a boolean for AND/OR/XOR/andsw and
// for the V1/V2 of switch logic functions like STICKY.
//
// EdgeTX rules:
//   - Position match (SF2, 6P15): true if in that position
//   - Logic switch (L3): the latch state
//   - Flight mode (FM2): true if mode active
//   - "!X": negation of X
//   - MAX: true
//   - Other sources: ResolveValue() >= 0  (numeric "active" if at-or-above center)
func (r *Resolver) ResolveBool(name string) (bool, bool) {
	if name == "" || name == "NONE" || name == "---" || name == "--" {
		return false, false
	}

	if strings.HasPrefix(name, "!") {
		b, ok := r.ResolveBool(name[1:])
		if !ok {
			return false, false
		}
		return !b, true
	}

	if name == "MAX" {
		return true, true
	}

	// Hardware names that would collide with switch-position pattern.
	if isTrimName(name) || isPotName(name) || isSliderName(name) || isGVarName(name) {
		return false, false
	}

	// Logic switch.
	if idx, ok := parseLogicRef(name); ok {
		return r.Logic.Logic(idx), true
	}

	// Switch position match.
	if sw, pos, ok := parseSwitchPosition(name); ok {
		return r.matchesSwitchPosition(sw, pos), true
	}

	// Flight mode.
	if idx, ok := parseFlightModeRef(name); ok {
		return r.flightModeActive(idx), true
	}

	// Otherwise: numeric active = value >= 0.
	v, ok := r.ResolveValue(name)
	if !ok {
		return false, false
	}
	return v >= 0, true
}

// resolveBoundSource reads a source via its model.Binding (joystick axis,
// panel switch, etc). The binding's Kind dictates the value mapping.
func (r *Resolver) resolveBoundSource(name string) (float64, bool) {
	if r.Model == nil {
		return 0, false
	}
	bindings := r.Model.ZeroTX.SourceBindings
	bind, ok := bindings[name]
	if !ok {
		// Convention fallback: bare panel switch / selector by name.
		// SA..SH and 6POS without a binding still resolve via the panel
		// using a default 3-pos / 6-pos mapping. Returns false if no panel.
		return r.resolveByConvention(name)
	}

	switch {
	case bind.Axis != nil:
		if r.Joystick == nil {
			return 0, false
		}
		v, ok := r.Joystick.AxisValue(bind.Device, *bind.Axis)
		if !ok {
			return 0, false
		}
		if bind.Deadband > 0 && abs(v) < bind.Deadband {
			v = 0
		}
		if bind.Invert {
			v = -v
		}
		return v, true

	case bind.Button != nil:
		if isPanelDevice(bind.Device) {
			if r.Panel == nil {
				return 0, false
			}
			pressed, ok := r.Panel.Button(name)
			if !ok {
				return 0, false
			}
			if pressed {
				return 1.0, true
			}
			return -1.0, true
		}
		if r.Joystick == nil {
			return 0, false
		}
		pressed, ok := r.Joystick.Button(bind.Device, *bind.Button)
		if !ok {
			return 0, false
		}
		if pressed {
			return 1.0, true
		}
		return -1.0, true

	case bind.Switch != nil:
		if isPanelDevice(bind.Device) {
			if r.Panel == nil {
				return 0, false
			}
			pos, ok := r.Panel.Switch(name)
			if !ok {
				return 0, false
			}
			return switchPosToValue(pos, bind.Kind), true
		}
		// Joystick-based switches: not realistic in our setup but
		// handle for completeness.
		return 0, false

	case bind.Selector != nil:
		if isPanelDevice(bind.Device) {
			if r.Panel == nil {
				return 0, false
			}
			pos, ok := r.Panel.Selector(name)
			if !ok {
				return 0, false
			}
			return selectorPosToValue(pos), true
		}
		return 0, false
	}

	return 0, false
}

// resolveByConvention handles bare switch / selector names that don't have
// an explicit binding. Used when a logic switch operand references a panel
// source that wasn't declared as a binding (rare but legal).
func (r *Resolver) resolveByConvention(name string) (float64, bool) {
	if r.Panel == nil {
		return 0, false
	}
	upper := strings.ToUpper(name)
	switch {
	case len(upper) == 2 && upper[0] == 'S' && upper[1] >= 'A' && upper[1] <= 'Z':
		// Bare panel switch.
		pos, ok := r.Panel.Switch(name)
		if !ok {
			return 0, false
		}
		// Default to 3-pos mapping; if the user meant 2-pos, they should
		// have provided a binding with the right kind.
		return switchPosToValue(pos, "3pos"), true
	case strings.HasPrefix(upper, "6P"):
		pos, ok := r.Panel.Selector(name)
		if !ok {
			return 0, false
		}
		return selectorPosToValue(pos), true
	}
	return 0, false
}

// matchesSwitchPosition checks whether the named switch is currently in
// the given position. Used for SF2-style operands.
func (r *Resolver) matchesSwitchPosition(switchName string, pos int) bool {
	if r.Panel == nil {
		return false
	}
	upper := strings.ToUpper(switchName)
	if strings.HasPrefix(upper, "6P") {
		current, ok := r.Panel.Selector(switchName)
		if !ok {
			return false
		}
		return current == pos
	}
	current, ok := r.Panel.Switch(switchName)
	if !ok {
		return false
	}
	return current == pos
}

// flightModeActive returns whether flight mode idx is the currently
// active mode. EdgeTX flight modes are evaluated by their switch
// expressions; for now we don't compute flight modes (a future addition),
// so this always returns false. The logic engine and CFs that reference
// FMx will see "false" until flight-mode tracking lands.
func (r *Resolver) flightModeActive(idx int) bool {
	_ = idx
	return false
}

// isTelemetryName checks whether a name appears in the model's
// telemetrySensors section. This avoids silently treating typos as
// telemetry calls.
func (r *Resolver) isTelemetryName(name string) bool {
	if r.Model == nil {
		return false
	}
	for _, s := range r.Model.EdgeTX.TelemetrySensors {
		if s.Label == name {
			return true
		}
	}
	return false
}

// --- name parsers ---

// parsePercent parses a numeric constant in EdgeTX's percent convention.
// "-99" -> -0.99. "100" -> 1.00. "-150" -> -1.0 (clamped). Decimals OK.
func parsePercent(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	// Quick reject: must look like a number.
	c0 := s[0]
	if c0 != '-' && c0 != '+' && (c0 < '0' || c0 > '9') {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	v /= 100.0
	if v > 1.0 {
		v = 1.0
	}
	if v < -1.0 {
		v = -1.0
	}
	return v, true
}

// parseLogicRef parses logic switch references in two forms:
//
//	"L1".."L64"     used in logic-switch operand defs and CF swtch fields
//	"ls(1)".."ls(64)"  legacy form used in mixData srcRaw fields
//
// Both return the 1-indexed switch number.
func parseLogicRef(s string) (int, bool) {
	// "ls(N)" form.
	if strings.HasPrefix(s, "ls(") && strings.HasSuffix(s, ")") {
		inner := s[3 : len(s)-1]
		if !isAllDigits(inner) {
			return 0, false
		}
		n, err := strconv.Atoi(inner)
		if err != nil || n < 1 || n > 64 {
			return 0, false
		}
		return n, true
	}
	// "LN" form.
	if len(s) < 2 || s[0] != 'L' {
		return 0, false
	}
	if !isAllDigits(s[1:]) {
		return 0, false
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil || n < 1 || n > 64 {
		return 0, false
	}
	return n, true
}

// parseSwitchPosition parses "SF2", "SA1", "6P15", "SH↑" (Unicode position
// symbols) into a (switchName, position) tuple. Returns ok=false if not
// a switch-position match expression.
//
// Position digit comes after the switch name. For 6POS positions, the
// format is "6P00" .. "6P15" (two digits).
func parseSwitchPosition(s string) (string, int, bool) {
	upper := strings.ToUpper(s)

	// 6PNN: selector position match.
	if strings.HasPrefix(upper, "6P") && len(upper) >= 4 {
		rest := upper[2:]
		if isAllDigits(rest) {
			n, err := strconv.Atoi(rest)
			if err == nil && n >= 0 && n <= 15 {
				return "6POS", n, true
			}
		}
	}

	// SXN: 3-pos / 2-pos switch position match.
	if len(upper) >= 3 && upper[0] == 'S' && upper[1] >= 'A' && upper[1] <= 'Z' {
		rest := upper[2:]
		if isAllDigits(rest) {
			n, err := strconv.Atoi(rest)
			if err == nil && n >= 0 && n <= 15 {
				return upper[:2], n, true
			}
		}
	}

	return "", 0, false
}

// parseFlightModeRef parses "FM0".."FM8".
func parseFlightModeRef(s string) (int, bool) {
	if len(s) < 3 || s[0] != 'F' || s[1] != 'M' {
		return 0, false
	}
	if !isAllDigits(s[2:]) {
		return 0, false
	}
	n, err := strconv.Atoi(s[2:])
	if err != nil || n < 0 || n > 8 {
		return 0, false
	}
	return n, true
}

func isTrimName(s string) bool {
	if !strings.HasPrefix(s, "Trm") {
		return false
	}
	if len(s) != 4 {
		return false
	}
	switch s[3] {
	case 'H', 'V', 'A', 'E', 'R', 'T':
		return true
	}
	return false
}

// isPotName matches "P1".."P9" — radio pots (TX16S etc).
func isPotName(s string) bool {
	if len(s) < 2 || s[0] != 'P' {
		return false
	}
	return isAllDigits(s[1:])
}

// isSliderName matches "SL1".."SL9".
func isSliderName(s string) bool {
	if len(s) < 3 || s[0] != 'S' || s[1] != 'L' {
		return false
	}
	return isAllDigits(s[2:])
}

func isGVarName(s string) bool {
	if len(s) < 3 || s[0] != 'G' || s[1] != 'V' {
		return false
	}
	return isAllDigits(s[2:])
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// --- value helpers (kept consistent with mapper) ---

func switchPosToValue(pos int, kind string) float64 {
	switch kind {
	case "3pos":
		switch pos {
		case 0:
			return -1
		case 1:
			return 0
		default:
			return 1
		}
	case "2pos", "momentary", "toggle", "":
		if pos > 0 {
			return 1
		}
		return -1
	}
	return 0
}

func selectorPosToValue(pos int) float64 {
	// 6POS: 0..5 mapped to -1..+1.
	return -1.0 + (float64(pos)*2.0)/5.0
}

func isPanelDevice(s string) bool {
	return strings.EqualFold(s, "GCS")
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// errUnknown is returned when a source name isn't recognized at all.
// Callers normally don't see this directly; ResolveValue/ResolveBool
// just return ok=false. Kept here for any future debug paths.
var errUnknown = fmt.Errorf("unknown source")
