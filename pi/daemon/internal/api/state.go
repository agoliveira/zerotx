// Package api exposes daemon state via HTTP (one-shot reads) and
// WebSocket (continuous push) on localhost. The API is read-only; clients
// observe state but cannot mutate it. Future write paths (e.g. load
// model, edit panel state remotely) will need explicit M4+ design work
// around auth, validation, and safety.
//
// Routes:
//
//	GET  /api/v1/health           daemon liveness + link state
//	GET  /api/v1/model            current model summary
//	GET  /api/v1/state            full state snapshot
//	WS   /api/v1/stream           continuous state push at 10Hz
//
// All routes bind to 127.0.0.1 by default. The daemon is not intended
// to expose itself on the network without explicit operator action.
package api

import (
	"context"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/panel"
)

// State is the JSON payload pushed to /stream and returned by /state.
// Fields use lowercase JSON keys; missing/inactive sources have nil or
// zero-value fields so the client can render gracefully.
type State struct {
	Timestamp string            `json:"ts"`
	Channels  []uint16          `json:"channels"`
	Logic     map[string]bool   `json:"logic,omitempty"`
	Panel     panel.Snapshot    `json:"panel"`
	Joystick  *JoystickSnapshot `json:"joystick,omitempty"`
	Link      LinkSnapshot      `json:"link"`
	Telemetry interface{}       `json:"telemetry,omitempty"`
	Audio     *AudioInfo        `json:"audio,omitempty"`
	Arm       interface{}       `json:"arm,omitempty"`
}

// AudioInfo summarises the audio subsystem's current state for API
// consumers. Threshold is the minimum level that plays; ActiveAlarms
// is the list of currently-scheduled repeating alarms.
type AudioInfo struct {
	Threshold    string      `json:"threshold"`
	ActiveAlarms interface{} `json:"activeAlarms"`
}

// Recording is a saved-recording summary for the GUI's Recordings tab.
// Mirrors recorder.Recording without an import cycle.
type Recording struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// JoystickSnapshot exposes normalized axis values and button states. nil
// when no joystick is connected.
type JoystickSnapshot struct {
	Name    string    `json:"name"`
	Axes    []float64 `json:"axes"` // [-1.0, 1.0]
	Buttons []bool    `json:"buttons"`
}

// LinkSnapshot describes the RP2040 USB-CDC link state.
type LinkSnapshot struct {
	State         string `json:"state"` // "active", "stale", "down"
	Port          string `json:"port,omitempty"`
	LastHeartbeat string `json:"lastHeartbeat,omitempty"` // RFC3339 or empty
}

// ModelSummary is returned by /model. Lightweight — just enough to label
// what's loaded.
type ModelSummary struct {
	Name      string `json:"name"`
	Mixes     int    `json:"mixes"`
	Logic     int    `json:"logic"`
	CustomFn  int    `json:"customFn"`
	Sensors   int    `json:"sensors"`
	Available bool   `json:"available"` // false if no model loaded
}

// ModelDetails is returned by /model/details. Heavy payload describing
// the parsed EdgeTX model in full. Fetched once on connect by the GUI.
type ModelDetails struct {
	Available     bool                `json:"available"`
	Name          string              `json:"name"`
	Bitmap        string              `json:"bitmap"`    // YAML-declared filename, e.g. "bigtalon"
	HasBitmap     bool                `json:"hasBitmap"` // server has a file ready to serve
	Airframe      string              `json:"airframe,omitempty"`
	Thresholds    *ThresholdDetails   `json:"thresholds,omitempty"`
	Mixes         []MixDetail         `json:"mixes"`
	LogicSwitches []LogicSwitchDetail `json:"logicSwitches"`
	CustomFns     []CustomFnDetail    `json:"customFns"`
	Sensors       []SensorDetail      `json:"sensors"`
}

// ThresholdDetails mirrors model.Thresholds with the same per-domain
// shape. Pack-level battery voltages are pre-computed from per-cell
// limits + cell count for GUI display convenience.
type ThresholdDetails struct {
	Battery    *BatteryThresholdDetails    `json:"battery,omitempty"`
	Altitude   *AltitudeThresholdDetails   `json:"altitude,omitempty"`
	Distance   *DistanceThresholdDetails   `json:"distance,omitempty"`
	Link       *LinkThresholdDetails       `json:"link,omitempty"`
	FlightTime *FlightTimeThresholdDetails `json:"flightTime,omitempty"`
}

type BatteryThresholdDetails struct {
	Cells     int     `json:"cells"`
	CellWarnV float64 `json:"cellWarnV"`
	CellCritV float64 `json:"cellCritV"`
	CellMinV  float64 `json:"cellMinV"`
	CellFullV float64 `json:"cellFullV"`
	// Pre-computed pack-level voltages (cells * cell_*) for display.
	PackWarnV float64 `json:"packWarnV"`
	PackCritV float64 `json:"packCritV"`
	PackMinV  float64 `json:"packMinV"`
	PackFullV float64 `json:"packFullV"`
}

type AltitudeThresholdDetails struct {
	WarnM int `json:"warnM"`
	CritM int `json:"critM"`
}

type DistanceThresholdDetails struct {
	WarnM int `json:"warnM"`
	CritM int `json:"critM"`
}

type LinkThresholdDetails struct {
	RSSIWarnDBM int `json:"rssiWarnDbm"`
	RSSICritDBM int `json:"rssiCritDbm"`
	LQWarnPct   int `json:"lqWarnPct"`
	LQCritPct   int `json:"lqCritPct"`
}

type FlightTimeThresholdDetails struct {
	WarnS int `json:"warnS"`
	CritS int `json:"critS"`
}

// MixDetail mirrors one entry in EdgeTX's mixData. Multiple mixes can
// target the same channel; ordering is preserved from the YAML.
type MixDetail struct {
	Index     int    `json:"index"`
	Ch        int    `json:"ch"` // 1-based for human display
	Name      string `json:"name"`
	Source    string `json:"source"`
	Weight    int    `json:"weight"`
	Offset    int    `json:"offset"`
	Switch    string `json:"switch"`
	Mltpx     string `json:"mltpx"` // ADD / MULTIPLY / REPLACE
	DelayUp   int    `json:"delayUp"`
	DelayDown int    `json:"delayDown"`
}

// LogicSwitchDetail describes one logical switch (L1, L2, ...).
type LogicSwitchDetail struct {
	Name     string  `json:"name"`
	Func     string  `json:"func"`
	Def      string  `json:"def"` // raw, e.g. "I0,-99"
	Andsw    string  `json:"andsw"`
	Delay    float64 `json:"delay"`    // seconds
	Duration float64 `json:"duration"` // seconds
	Active   bool    `json:"active"`   // current state from logic engine
}

// CustomFnDetail describes one custom function (CF1, CF2, ...).
type CustomFnDetail struct {
	ID     int    `json:"id"`
	Switch string `json:"switch"`
	Func   string `json:"func"` // OVERRIDE_CHANNEL, PLAY_TRACK, etc.
	Def    string `json:"def"`  // raw, with null bytes stripped
}

// SensorDetail describes a telemetry sensor.
type SensorDetail struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Unit  string `json:"unit"` // resolved from EdgeTX unit code
}

// HealthResponse is returned by /health.
type HealthResponse struct {
	Version      string `json:"version"`
	Uptime       string `json:"uptime"`
	LinkState    string `json:"linkState"`
	ModelLoaded  bool   `json:"modelLoaded"`
	JoystickOpen bool   `json:"joystickOpen"`
}

// LogEntry is a single captured log line.
type LogEntry struct {
	Time string `json:"ts"` // RFC3339Nano
	Msg  string `json:"msg"`
}

// LogsResponse is returned by GET /api/v1/logs.
type LogsResponse struct {
	Entries []LogEntry `json:"entries"`
}

// === Pre-flight ===

// Preflight is returned by GET /api/v1/preflight. It's the aggregate
// readiness snapshot the GUI consumes to render the pre-flight checklist.
type Preflight struct {
	State         string             `json:"state"`    // "idle" or "ready"
	Ready         bool               `json:"ready"`    // all blockers cleared
	Blockers      []string           `json:"blockers"` // human-readable reasons not ready
	GroundStation PreflightGS        `json:"groundStation"`
	Joystick      PreflightJoystickG `json:"joystick"`
	Model         PreflightModelG    `json:"model"`
}

// PreflightGS holds always-available daemon facts.
type PreflightGS struct {
	LinkPort  string `json:"linkPort"`
	LinkState string `json:"linkState"`
}

// PreflightJoystickG holds joystick selection state. Selected is nil
// when no joystick is open.
type PreflightJoystickG struct {
	Selected *PreflightJoystick `json:"selected"`
}

// PreflightJoystick describes a selected joystick.
type PreflightJoystick struct {
	Name      string `json:"name"`
	Axes      int    `json:"axes"`
	Buttons   int    `json:"buttons"`
	Connected bool   `json:"connected"`
}

// PreflightModelG holds model selection state. Loaded is nil when the
// daemon is in IDLE.
type PreflightModelG struct {
	Loaded *PreflightModel `json:"loaded"`
}

// PreflightModel describes a loaded model.
type PreflightModel struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Mixes    int    `json:"mixes"`
	Logic    int    `json:"logic"`
	CustomFn int    `json:"customFn"`
	Sensors  int    `json:"sensors"`
}

// LoadModelRequest is the JSON body for POST /api/v1/model/load.
type LoadModelRequest struct {
	Path string `json:"path"`
}

// === Joystick / model directory ===

// JoystickDevice is one entry in the GET /api/v1/joysticks response.
type JoystickDevice struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Axes    int    `json:"axes"`
	Buttons int    `json:"buttons"`
	Hats    int    `json:"hats"`
	GUID    string `json:"guid"`
}

// SelectJoystickRequest is the body for POST /api/v1/joystick/select.
// Index identifies the SDL device. Emergency must be true to swap while
// the daemon is in armed-for-flight state; the GUI exposes this only
// behind an explicit confirm dialog.
type SelectJoystickRequest struct {
	Index     int  `json:"index"`
	Emergency bool `json:"emergency"`
}

// ModelFile is one entry in the GET /api/v1/models response.
type ModelFile struct {
	Path string `json:"path"` // absolute or as given via dir param
	File string `json:"file"` // basename
	Name string `json:"name"` // parsed model name (best-effort; "" if parse failed)
}

// Providers is the set of sources the API server pulls from. All fields
// must be non-nil; pass no-op implementations for unavailable sources.
type Providers struct {
	Channels       func() [ipc.Channels]uint16
	Logic          func() map[string]bool
	Panel          func() panel.Snapshot
	Joystick       func() *JoystickSnapshot // returns nil if not connected
	Link           func() LinkSnapshot
	Model          func() ModelSummary
	ModelDetails   func() ModelDetails
	ModelImagePath func() string
	Logs           func(since time.Time) []LogEntry
	Preflight      func() Preflight
	LoadModel      func(path string) error
	UnloadModel    func()

	// Joystick selection.
	Joysticks       func() []JoystickDevice
	SelectJoystick  func(index int, emergency bool) error
	ReleaseJoystick func() error

	// Models directory listing.
	ListModels func(dir string) ([]ModelFile, error)

	// Telemetry returns the current FC telemetry snapshot (typed in
	// the daemon's internal/telemetry package). Returns nil when no
	// telemetry has ever been received. The api package treats the
	// value as opaque JSON to avoid an import cycle.
	Telemetry func() interface{}

	// Audio returns the current threshold and any active repeating
	// alarms. ActiveAlarms is a thin shape that survives the import
	// cycle by holding the audio package's ActiveAlarm directly via
	// interface{}; the GUI consumes it as JSON.
	Audio             func() AudioInfo
	SetAudioThreshold func(level string) error
	Acknowledge       func(name string)
	AcknowledgeAll    func()

	// Speak runs the given text through the TTS engine and plays
	// the result. Used by the debug endpoint POST /api/v1/debug/speak.
	// May be nil if TTS isn't configured; the handler returns 503.
	Speak func(text string, level string)

	// FlightEvents returns the events logged for the current armed
	// session (or last session, before rotation). Used by the debug
	// endpoint GET /api/v1/debug/flight-events. Returned as opaque
	// JSON to avoid an import cycle on the recorder package.
	FlightEvents func() (interface{}, error)

	// NarrateConfig returns the current periodic-narration
	// configuration (interval + fields). Used by GET /api/v1/narrate.
	NarrateConfig func() NarrateConfig
	// NarrateConfigSet validates and applies a new periodic-narration
	// configuration, persisting to disk on success. Used by
	// POST /api/v1/narrate. Returns an error on validation failure.
	NarrateConfigSet func(NarrateConfig) error

	// Recordings returns the saved flight recordings on disk
	// (newest first). Empty when recording is disabled.
	Recordings func() ([]Recording, error)

	// Summarize opens a saved recording (by file basename) and
	// returns aggregate stats. Returns an error if the file is
	// missing or unreadable; nil pointers within the summary mean
	// "no data of that kind in this recording".
	Summarize func(name string) (interface{}, error)

	// Arm reports the current arm state machine state and inputs.
	// Returned as opaque interface{} (JSON-encoded by the api
	// package) to avoid importing the arm package.
	Arm func() interface{}
	// ArmConfirm fires the operator's confirm action (e.g. from a
	// keyboard combo handled by the GUI). Returns no error: the
	// state machine accepts confirms silently if not in
	// ARMING_REQUESTED state. The actual outcome arrives via
	// arm-state events, which the GUI also subscribes to.
	ArmConfirm func()

	// ArmChecklist updates the operator-checklist gate on the arm
	// state machine. Defaults to false at boot; arming is denied
	// until a consumer (typically the GUI) says the checklist is
	// satisfied or the operator has disabled the checklist policy.
	ArmChecklist func(ok bool)

	// WeatherCurrent returns the cached weather for the observer's
	// current location, plus the lat/lon used for the lookup and a
	// label describing where the coordinates came from ("gps", "home",
	// "site"). ok=false means either no resolved coordinates or no
	// cached data yet. The weather value is opaque interface{} so
	// the api package doesn't import internal/weather.
	WeatherCurrent func() (data interface{}, latDeg, lonDeg float64, source string, ok bool)

	// WeatherFetch returns the weather for the explicitly-given
	// coordinates, fetching fresh if the cache is missing or stale.
	// Errors propagate to the API as 503. May be nil; the handler
	// returns 503 without details.
	WeatherFetch func(ctx context.Context, latDeg, lonDeg float64) (interface{}, error)

	Version string
	Uptime  func() time.Duration
}

// ArmChecklistRequest is the body for POST /api/v1/arm/checklist.
type ArmChecklistRequest struct {
	Ok bool `json:"ok"`
}

// snapshot assembles the current State from the providers.
func (p *Providers) snapshot() State {
	ch := p.Channels()
	channels := make([]uint16, len(ch))
	for i, v := range ch {
		channels[i] = v
	}
	out := State{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Channels:  channels,
		Logic:     p.Logic(),
		Panel:     p.Panel(),
		Joystick:  p.Joystick(),
		Link:      p.Link(),
	}
	if p.Telemetry != nil {
		out.Telemetry = p.Telemetry()
	}
	if p.Audio != nil {
		ai := p.Audio()
		out.Audio = &ai
	}
	if p.Arm != nil {
		out.Arm = p.Arm()
	}
	return out
}

// NarrateConfig is the wire shape for periodic narration settings.
// Interval is a Go duration string ("60s", "2m"). Fields is a list
// of canonical field names (see daemon-side narrateField).
type NarrateConfig struct {
	Interval string   `json:"interval"`
	Fields   []string `json:"fields"`
}
