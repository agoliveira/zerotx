package joystick

import (
	"strings"
)

// AsInputState wraps a Reader so it can be used wherever a mapper InputState
// is expected. Device matching is by case-insensitive substring against the
// Reader's name; this lets a binding say "HOTAS" and match a device whose
// SDL name is "Thrustmaster T.16000M".
//
// When multiple devices arrive in M2.2+, this wrapper expands to a registry.
type ReaderAdapter struct {
	Reader *Reader
}

// AxisValue returns the normalized [-1.0, +1.0] axis value.
func (a *ReaderAdapter) AxisValue(device string, axis int) (float64, bool) {
	if !a.matches(device) {
		return 0, false
	}
	if axis < 0 || axis >= a.Reader.NumAxes() {
		return 0, false
	}
	raw := a.Reader.Axis(axis)
	// SDL axis range is [-32768, 32767]. Normalize, clamping +32768 to +1.0
	// (the +32768 endpoint isn't representable as int16 but the formula
	// is symmetric enough for our purposes).
	v := float64(raw) / 32767.0
	if v > 1.0 {
		v = 1.0
	}
	if v < -1.0 {
		v = -1.0
	}
	return v, true
}

// Button returns the pressed state of button index.
func (a *ReaderAdapter) Button(device string, button int) (bool, bool) {
	if !a.matches(device) {
		return false, false
	}
	if button < 0 || button >= a.Reader.NumButtons() {
		return false, false
	}
	return a.Reader.Button(button), true
}

// Switch is not directly supported on a USB joystick (no notion of multi-pos
// switch). Always returns false; the GCS panel binding uses the RP2040
// instead. M2.1 leaves this stubbed.
func (a *ReaderAdapter) Switch(device string, sw int) (int, bool) {
	return 0, false
}

// Selector likewise stubbed in M2.1 (the 6POS lives on the RP2040 panel).
func (a *ReaderAdapter) Selector(device string, sel int) (int, bool) {
	return 0, false
}

func (a *ReaderAdapter) matches(device string) bool {
	if device == "" {
		return true
	}
	return strings.Contains(
		strings.ToLower(a.Reader.Name()),
		strings.ToLower(device),
	)
}
