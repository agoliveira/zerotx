package model

// ZeroTXModel is the top-level container saved per model. It holds the
// imported EdgeTX content under EdgeTX, plus ZeroTX-specific configuration
// (which physical input drives each abstract source) under ZeroTX.
//
//	zerotx:
//	  source_bindings:
//	    Thr: { device: "HOTAS X", axis: 2 }
//	    SE:  { device: "GCS", switch: 2, kind: 3pos }
//	edgetx:
//	  semver: 2.9.1
//	  header: ...
type ZeroTXModel struct {
	ZeroTX ZeroTXMeta  `yaml:"zerotx"`
	EdgeTX EdgeTXModel `yaml:"edgetx"`
}

// ZeroTXMeta carries everything ZeroTX itself adds to the model file. Kept
// minimal in M2; expand as needed.
type ZeroTXMeta struct {
	// SourceBindings maps an abstract EdgeTX source name (e.g. "Thr", "Ail",
	// "SE", "SA", "6POS") to the physical input that supplies its value.
	SourceBindings map[string]Binding `yaml:"source_bindings,omitempty"`

	// Notes is free-form text the user can leave in the file.
	Notes string `yaml:"notes,omitempty"`
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
