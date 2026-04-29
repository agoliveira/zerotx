package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/joystick"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// joystickHolder owns the currently-active joystick reader. The resolver
// holds a JoystickState adapter built from this holder once at Stack-build
// time; swapping which device is active is done by replacing the inner
// reader without touching the resolver or the active model stack.
type joystickHolder struct {
	mu     sync.RWMutex
	reader *joystick.Reader

	// flightArmed mirrors the daemon's "armed for flight" status. While
	// true, joystick swap is rejected unless the caller asks for it
	// explicitly with the emergency flag. This is the "no mid-flight
	// controller swap" guarantee.
	flightArmed bool
}

func newJoystickHolder() *joystickHolder {
	return &joystickHolder{}
}

// Set installs a reader without flight-armed checks. Used at startup
// when the daemon opens whatever -joystick-name was passed on the
// command line. Pass nil to clear.
func (h *joystickHolder) Set(r *joystick.Reader) {
	h.mu.Lock()
	h.reader = r
	h.mu.Unlock()
}

// Reader returns the current reader, or nil if none is active.
func (h *joystickHolder) Reader() *joystick.Reader {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.reader
}

// Connected returns true if a reader is installed and its device is
// still present.
func (h *joystickHolder) Connected() bool {
	h.mu.RLock()
	r := h.reader
	h.mu.RUnlock()
	if r == nil {
		return false
	}
	return r.Connected()
}

// LostAt returns the time the active joystick was disconnected, or zero
// if it's still connected (or no joystick is installed).
func (h *joystickHolder) LostAt() time.Time {
	h.mu.RLock()
	r := h.reader
	h.mu.RUnlock()
	if r == nil {
		return time.Time{}
	}
	return r.LostAt()
}

// SetFlightArmed updates the armed state. While armed, Swap rejects
// non-emergency requests.
func (h *joystickHolder) SetFlightArmed(armed bool) {
	h.mu.Lock()
	h.flightArmed = armed
	h.mu.Unlock()
}

// FlightArmed returns the current armed state.
func (h *joystickHolder) FlightArmed() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.flightArmed
}

// errFlightArmedSwap is returned by Swap when a swap is rejected because
// the daemon is in armed-for-flight state and the caller didn't pass
// emergency=true.
var errFlightArmedSwap = fmt.Errorf("cannot swap joystick while armed for flight (pass emergency=true to override)")

// Swap atomically replaces the active reader with `next`. Returns the
// previous reader so callers can close it. If the daemon is armed for
// flight, the swap is rejected unless emergency=true.
func (h *joystickHolder) Swap(next *joystick.Reader, emergency bool) (prev *joystick.Reader, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.flightArmed && !emergency {
		return nil, errFlightArmedSwap
	}
	prev = h.reader
	h.reader = next
	return prev, nil
}

// JoystickState returns an adapter that satisfies source.JoystickState
// by consulting whichever reader is currently installed in the holder.
// Capture once at Stack-build time; swaps within the holder are
// reflected transparently.
func (h *joystickHolder) JoystickState() source.JoystickState {
	return &holderAdapter{holder: h}
}

// holderAdapter satisfies source.JoystickState by consulting the holder.
type holderAdapter struct {
	holder *joystickHolder
}

// AxisValue returns the normalized [-1.0, +1.0] value for the named
// device's axis, or (0, false) when no reader is installed, the device
// has been disconnected, or the name doesn't match.
func (a *holderAdapter) AxisValue(device string, axis int) (float64, bool) {
	a.holder.mu.RLock()
	r := a.holder.reader
	a.holder.mu.RUnlock()
	if r == nil {
		return 0, false
	}
	if !readerMatches(r, device) {
		return 0, false
	}
	if axis < 0 || axis >= r.NumAxes() {
		return 0, false
	}
	raw := r.Axis(axis)
	v := float64(raw) / 32767.0
	if v > 1.0 {
		v = 1.0
	}
	if v < -1.0 {
		v = -1.0
	}
	return v, true
}

// Button returns the pressed state for the named device's button.
func (a *holderAdapter) Button(device string, button int) (bool, bool) {
	a.holder.mu.RLock()
	r := a.holder.reader
	a.holder.mu.RUnlock()
	if r == nil {
		return false, false
	}
	if !readerMatches(r, device) {
		return false, false
	}
	if button < 0 || button >= r.NumButtons() {
		return false, false
	}
	return r.Button(button), true
}

func readerMatches(r *joystick.Reader, device string) bool {
	if device == "" {
		return true
	}
	return strings.Contains(
		strings.ToLower(r.Name()),
		strings.ToLower(device),
	)
}
