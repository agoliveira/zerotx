// Package joystick provides a thin SDL2-backed reader for game controllers
// and HOTAS-class joysticks. It uses the SDL_Joystick API (raw axes/buttons/
// hats indexed by number) rather than SDL_GameController so devices with
// more axes than a standard gamepad (HOTAS T.16000M, throttle quadrants,
// etc) expose every input directly.
//
// SDL2 is initialised lazily via Init. Devices are opened by index. The
// reader runs an event-pump goroutine and exposes thread-safe accessors for
// the latest state.
package joystick

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// AxisRaw is the SDL2 axis range: signed int16, [-32768, 32767].
const (
	AxisRawMin = -32768
	AxisRawMax = 32767
)

// Reader owns one open SDL joystick.
type Reader struct {
	js   *sdl.Joystick
	idx  int
	name string
	guid string

	mu        sync.RWMutex
	axes      []int16
	buttons   []bool
	hats      []uint8
	stop      chan struct{}
	connected bool
	lostAt    time.Time
}

// Init initialises the SDL2 joystick subsystem. Safe to call multiple times.
func Init() error {
	if err := sdl.InitSubSystem(sdl.INIT_JOYSTICK); err != nil {
		return fmt.Errorf("joystick: init: %w", err)
	}
	sdl.JoystickEventState(sdl.ENABLE)
	return nil
}

// Quit shuts down the SDL2 joystick subsystem.
func Quit() {
	sdl.QuitSubSystem(sdl.INIT_JOYSTICK)
}

// List returns information about every currently-attached joystick.
type DeviceInfo struct {
	Index   int
	Name    string
	NumAxes int
	NumBtns int
	NumHats int
	GUID    string
}

// List enumerates connected devices without opening them.
func List() []DeviceInfo {
	n := sdl.NumJoysticks()
	out := make([]DeviceInfo, 0, n)
	for i := 0; i < n; i++ {
		info := DeviceInfo{
			Index: i,
			Name:  sdl.JoystickNameForIndex(i),
			GUID:  sdl.JoystickGetGUIDString(sdl.JoystickGetDeviceGUID(i)),
		}
		// Need to briefly open to query counts.
		j := sdl.JoystickOpen(i)
		if j != nil {
			info.NumAxes = j.NumAxes()
			info.NumBtns = j.NumButtons()
			info.NumHats = j.NumHats()
			j.Close()
		}
		out = append(out, info)
	}
	return out
}

// GUIDForIndex returns the GUID of the device at the given SDL index
// without opening it. Returns "" if the index is out of range.
func GUIDForIndex(index int) string {
	if index < 0 || index >= sdl.NumJoysticks() {
		return ""
	}
	return sdl.JoystickGetGUIDString(sdl.JoystickGetDeviceGUID(index))
}

// Open opens device by index.
func Open(index int) (*Reader, error) {
	if index < 0 || index >= sdl.NumJoysticks() {
		return nil, fmt.Errorf("joystick: index %d out of range (have %d)", index, sdl.NumJoysticks())
	}
	js := sdl.JoystickOpen(index)
	if js == nil {
		return nil, fmt.Errorf("joystick: SDL_JoystickOpen(%d) failed: %s", index, sdl.GetError())
	}
	r := &Reader{
		js:        js,
		idx:       index,
		name:      js.Name(),
		guid:      sdl.JoystickGetGUIDString(sdl.JoystickGetDeviceGUID(index)),
		axes:      make([]int16, js.NumAxes()),
		buttons:   make([]bool, js.NumButtons()),
		hats:      make([]uint8, js.NumHats()),
		stop:      make(chan struct{}),
		connected: true,
	}
	// Seed initial values.
	for i := range r.axes {
		r.axes[i] = js.Axis(i)
	}
	for i := range r.buttons {
		r.buttons[i] = js.Button(i) != 0
	}
	for i := range r.hats {
		r.hats[i] = js.Hat(i)
	}
	registerReader(r)
	return r, nil
}

// OpenByName opens the first device whose name contains the substring (case-
// sensitive). Returns the reader and the matched index.
func OpenByName(substr string) (*Reader, int, error) {
	for _, info := range List() {
		if substr == "" || contains(info.Name, substr) {
			r, err := Open(info.Index)
			return r, info.Index, err
		}
	}
	return nil, -1, fmt.Errorf("joystick: no device matching %q", substr)
}

// Index returns the SDL joystick index this reader was opened with.
func (r *Reader) Index() int { return r.idx }

// Name returns the device name reported by SDL.
func (r *Reader) Name() string { return r.name }

// GUID returns the SDL device GUID captured at Open time. The GUID is
// stable across re-plugs of the same physical device, which lets the
// daemon distinguish "same controller came back" from "different
// controller plugged in."
func (r *Reader) GUID() string { return r.guid }

// NumAxes returns the count of axes for this device.
func (r *Reader) NumAxes() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.axes)
}

// NumButtons returns the count of buttons for this device.
func (r *Reader) NumButtons() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.buttons)
}

// NumHats returns the count of hats for this device.
func (r *Reader) NumHats() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.hats)
}

// Axis returns the raw signed value for axis i, or 0 if i is out of range.
func (r *Reader) Axis(i int) int16 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if i < 0 || i >= len(r.axes) {
		return 0
	}
	return r.axes[i]
}

// Button returns true if button i is pressed, false if released or out of range.
func (r *Reader) Button(i int) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if i < 0 || i >= len(r.buttons) {
		return false
	}
	return r.buttons[i]
}

// Hat returns the raw SDL hat bitmask for hat i (UP=1 RIGHT=2 DOWN=4 LEFT=8).
func (r *Reader) Hat(i int) uint8 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if i < 0 || i >= len(r.hats) {
		return 0
	}
	return r.hats[i]
}

// Connected returns true while the underlying SDL device is still present.
// Flips to false when an SDL_JOYDEVICEREMOVED event fires for this reader's
// instance ID. Reading axes/buttons after disconnection still returns the
// last-known values (the slices are never cleared); callers that care
// about freshness should check Connected first.
func (r *Reader) Connected() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connected
}

// LostAt returns the time the device was reported removed. Zero if the
// device is still connected.
func (r *Reader) LostAt() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lostAt
}

// Snapshot returns a copy of the entire current state.
type Snapshot struct {
	Axes    []int16
	Buttons []bool
	Hats    []uint8
}

// Snapshot returns a copy of the current state.
func (r *Reader) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := Snapshot{
		Axes:    make([]int16, len(r.axes)),
		Buttons: make([]bool, len(r.buttons)),
		Hats:    make([]uint8, len(r.hats)),
	}
	copy(s.Axes, r.axes)
	copy(s.Buttons, r.buttons)
	copy(s.Hats, r.hats)
	return s
}

// Run pumps SDL events into reader state until ctx is cancelled or the
// device disappears. Must be called from the goroutine that initialised SDL
// (or from any goroutine if SDL is configured to allow it).
//
// SDL events are global; if multiple Readers are open, callers are expected
// to run only one event pump (typically by calling Run on one Reader and
// dispatching events to the others, or running a shared pump). For M2.1 we
// open exactly one device so this is fine.
func (r *Reader) Run(ctx context.Context) error {
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.stop:
			return nil
		case <-t.C:
		}
		// SDL event pump must run; otherwise SDL_PollEvent stalls.
		for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
			dispatchEvent(ev)
		}
	}
}

// === Global event pump ===
//
// SDL_PollEvent is a process-global queue. With multiple Readers open at
// once (e.g. a Hotas plus future case gimbals as a USB-HID device), a
// single pump must dispatch to every interested reader. Each reader's
// handler filters events by instance ID. Readers register themselves on
// Open and unregister on Close.

var (
	registryMu sync.Mutex
	registry   []*Reader

	// onDeviceAdded fires when SDL reports JOYDEVICEADDED. The daemon
	// uses this to handle hot-plug: when a previously-disconnected
	// joystick returns (matched by GUID), the daemon transparently
	// reattaches and resumes emission.
	onDeviceAddedMu sync.Mutex
	onDeviceAdded   func(deviceIndex int)
)

// SetOnDeviceAdded installs a callback that fires every time SDL
// reports a new joystick has appeared. The callback receives the
// SDL device index (suitable for passing to Open). Pass nil to clear.
func SetOnDeviceAdded(cb func(deviceIndex int)) {
	onDeviceAddedMu.Lock()
	onDeviceAdded = cb
	onDeviceAddedMu.Unlock()
}

func registerReader(r *Reader) {
	registryMu.Lock()
	registry = append(registry, r)
	registryMu.Unlock()
}

func unregisterReader(r *Reader) {
	registryMu.Lock()
	for i, x := range registry {
		if x == r {
			registry = append(registry[:i], registry[i+1:]...)
			break
		}
	}
	registryMu.Unlock()
}

func dispatchEvent(ev sdl.Event) {
	// Hot-plug add events: notify the daemon, which decides whether to
	// reattach (GUID-match against a disconnected reader) or ignore.
	if e, ok := ev.(*sdl.JoyDeviceAddedEvent); ok {
		onDeviceAddedMu.Lock()
		cb := onDeviceAdded
		onDeviceAddedMu.Unlock()
		if cb != nil {
			cb(int(e.Which))
		}
	}

	registryMu.Lock()
	rs := append([]*Reader(nil), registry...)
	registryMu.Unlock()
	for _, r := range rs {
		r.handleEvent(ev)
	}
}

// PumpEvents runs the SDL event loop until ctx is cancelled. Use this
// when there might be zero or more readers; each Open/Close registers
// and unregisters with the dispatcher automatically. This is preferred
// over calling Reader.Run when the daemon supports runtime joystick
// swap.
func PumpEvents(ctx context.Context) {
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
			dispatchEvent(ev)
		}
	}
}

// Close releases the SDL joystick handle.
func (r *Reader) Close() {
	if r.js != nil {
		select {
		case <-r.stop:
			// already stopped
		default:
			close(r.stop)
		}
		unregisterReader(r)
		r.js.Close()
		r.js = nil
	}
}

func (r *Reader) handleEvent(ev sdl.Event) {
	// All Joy* events carry a Which field naming the instance ID of the
	// source device. We only mutate state for events from our own device.
	var myInstanceID sdl.JoystickID = -1
	if r.js != nil {
		myInstanceID = r.js.InstanceID()
	}
	switch e := ev.(type) {
	case *sdl.JoyAxisEvent:
		if e.Which != myInstanceID {
			return
		}
		r.mu.Lock()
		if int(e.Axis) < len(r.axes) {
			r.axes[e.Axis] = e.Value
		}
		r.mu.Unlock()
	case *sdl.JoyButtonEvent:
		if e.Which != myInstanceID {
			return
		}
		r.mu.Lock()
		if int(e.Button) < len(r.buttons) {
			r.buttons[e.Button] = e.State == sdl.PRESSED
		}
		r.mu.Unlock()
	case *sdl.JoyHatEvent:
		if e.Which != myInstanceID {
			return
		}
		r.mu.Lock()
		if int(e.Hat) < len(r.hats) {
			r.hats[e.Hat] = e.Value
		}
		r.mu.Unlock()
	case *sdl.JoyDeviceRemovedEvent:
		if e.Which == myInstanceID {
			r.mu.Lock()
			r.connected = false
			r.lostAt = time.Now()
			r.mu.Unlock()
			log.Printf("joystick: device %d (%q) disconnected", e.Which, r.name)
		}
	}
}

func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
