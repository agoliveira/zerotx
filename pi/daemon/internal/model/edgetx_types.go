// Package model parses EdgeTX model YAML files and wraps them in the
// ZeroTX-specific outer structure.
//
// The parser uses a two-tier strategy. Fields in the EdgeTX schema that ZeroTX
// either uses now or expects to use soon are typed as concrete Go fields.
// Everything else falls into Extras maps (yaml.Node) so the data round-trips
// cleanly without being acted on. Promote a field from Extras to a typed
// member when ZeroTX actually starts using it.
package model

import "gopkg.in/yaml.v3"

// EdgeTXModel mirrors the schema produced by EdgeTX Companion / radio.
//
// EdgeTX uses YAML maps with integer keys for indexed collections (e.g.
// flightModeData[0] .. [8]). This is preserved as map[int]X rather than
// flattened to a slice because the keys are sometimes sparse (limitData
// in big_talon.yml only has 0,1,2,5).
type EdgeTXModel struct {
	Semver           string                  `yaml:"semver"`
	Header           Header                  `yaml:"header"`
	InputNames       map[int]InputName       `yaml:"inputNames"`
	ExpoData         []Expo                  `yaml:"expoData"`
	MixData          []Mix                   `yaml:"mixData"`
	LimitData        map[int]Limit           `yaml:"limitData"`
	FlightModeData   map[int]FlightMode      `yaml:"flightModeData"`
	LogicalSw        map[int]LogicalSwitch   `yaml:"logicalSw"`
	CustomFn         map[int]CustomFunction  `yaml:"customFn"`
	ModuleData       map[int]Module          `yaml:"moduleData"`
	TelemetrySensors map[int]TelemetrySensor `yaml:"telemetrySensors"`
	ThrTraceSrc      string                  `yaml:"thrTraceSrc"`

	// Extras catches unrecognized top-level keys (radio UX prefs, screen
	// layouts, vario config, USB joystick mode flags, etc). They are preserved
	// verbatim so saving the model back to disk is non-destructive.
	Extras map[string]yaml.Node `yaml:",inline"`
}

// Header carries the model identity. modelId nesting is preserved as Extras.
type Header struct {
	Name   string               `yaml:"name"`
	Bitmap string               `yaml:"bitmap,omitempty"`
	Labels string               `yaml:"labels,omitempty"`
	Extras map[string]yaml.Node `yaml:",inline"`
}

// InputName is the wrapper around an InputNames entry: { val: "Thr" }.
type InputName struct {
	Val string `yaml:"val"`
}

// Curve is a small {type, value} pair attached to expo and mix entries.
type Curve struct {
	Type  int `yaml:"type"`
	Value int `yaml:"value"`
}

// Expo is one entry in expoData. EdgeTX expo defines how a stick translates
// to a logical input. ZeroTX honors weight/offset; expo curves themselves are
// out of scope per the project brief (FC handles them).
type Expo struct {
	SrcRaw      string `yaml:"srcRaw"`
	Scale       int    `yaml:"scale"`
	Mode        int    `yaml:"mode"`
	Chn         int    `yaml:"chn"`
	Swtch       string `yaml:"swtch"`
	FlightModes string `yaml:"flightModes"`
	Weight      int    `yaml:"weight"`
	Offset      int    `yaml:"offset"`
	Curve       Curve  `yaml:"curve"`
	TrimSource  int    `yaml:"trimSource"`
	Name        string `yaml:"name"`
}

// Mix is one entry in mixData. destCh is 0-indexed in EdgeTX storage.
type Mix struct {
	DestCh      int    `yaml:"destCh"`
	SrcRaw      string `yaml:"srcRaw"`
	Weight      int    `yaml:"weight"`
	Offset      int    `yaml:"offset"`
	Swtch       string `yaml:"swtch"`
	Curve       Curve  `yaml:"curve"`
	DelayUp     int    `yaml:"delayUp"`
	DelayDown   int    `yaml:"delayDown"`
	SpeedUp     int    `yaml:"speedUp"`
	SpeedDown   int    `yaml:"speedDown"`
	CarryTrim   int    `yaml:"carryTrim"`
	Mltpx       string `yaml:"mltpx"` // ADD | MULTIPLY | REPLACE
	MixWarn     int    `yaml:"mixWarn"`
	FlightModes string `yaml:"flightModes"` // 9-char mask, 1 = mode disabled
	Name        string `yaml:"name"`
}

// Limit is a per-channel output limit. EdgeTX min/max are signed percentages.
// If max < min the channel is implicitly inverted (revert can also flip it).
type Limit struct {
	Min        int    `yaml:"min"`
	Max        int    `yaml:"max"`
	Revert     int    `yaml:"revert"`
	Offset     int    `yaml:"offset"`
	PpmCenter  int    `yaml:"ppmCenter"`
	Symetrical int    `yaml:"symetrical"`
	Name       string `yaml:"name"`
	Curve      int    `yaml:"curve"`
}

// FlightMode names a flight mode and the switch state that selects it.
// FlightMode 0 is the default (no swtch).
type FlightMode struct {
	Name    string `yaml:"name"`
	Swtch   string `yaml:"swtch,omitempty"`
	FadeIn  int    `yaml:"fadeIn"`
	FadeOut int    `yaml:"fadeOut"`
}

// LogicalSwitch holds the raw definition. Evaluation lands in M3.
//
// Func examples: FUNC_VNEG (value < N), FUNC_EDGE (rising edge with window),
// FUNC_STICKY (latch). Def encodes the operands as a comma-separated string
// in the form EdgeTX uses internally.
type LogicalSwitch struct {
	Func     string `yaml:"func"`
	Def      string `yaml:"def"`
	Delay    int    `yaml:"delay"`
	Duration int    `yaml:"duration"`
	Andsw    string `yaml:"andsw"`
}

// CustomFunction is one PLAY_TRACK / PLAY_SOUND / OVERRIDE_CHANNEL etc.
type CustomFunction struct {
	Swtch string `yaml:"swtch"`
	Func  string `yaml:"func"`
	Def   string `yaml:"def"`
}

// Module describes one RF module configuration.
type Module struct {
	Type          string               `yaml:"type"`
	ChannelsStart int                  `yaml:"channelsStart"`
	ChannelsCount int                  `yaml:"channelsCount"`
	FailsafeMode  string               `yaml:"failsafeMode"`
	Mod           map[string]yaml.Node `yaml:"mod,omitempty"`
}

// TelemetrySensorID is the nested {id: N} or {instance: N} sub-object.
type TelemetrySensorID struct {
	ID       int `yaml:"id,omitempty"`
	Instance int `yaml:"instance,omitempty"`
}

// TelemetrySensor describes one sensor. Unit codes and prec/precision are
// EdgeTX-internal enums; ZeroTX preserves them as integers for now.
type TelemetrySensor struct {
	Type         string               `yaml:"type"`
	ID1          TelemetrySensorID    `yaml:"id1"`
	SubID        int                  `yaml:"subId"`
	ID2          TelemetrySensorID    `yaml:"id2"`
	Label        string               `yaml:"label"`
	Unit         int                  `yaml:"unit"`
	Prec         int                  `yaml:"prec"`
	AutoOffset   int                  `yaml:"autoOffset"`
	Filter       int                  `yaml:"filter"`
	Logs         int                  `yaml:"logs"`
	Persistent   int                  `yaml:"persistent"`
	OnlyPositive int                  `yaml:"onlyPositive"`
	Cfg          map[string]yaml.Node `yaml:"cfg,omitempty"`
}
