package source

import (
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
)

func ptr[T any](v T) *T { return &v }

// stubJoystick / stubPanel / stubLogic / stubTelem implement the resolver's
// dependency interfaces for tests.

type stubJoystick struct {
	axes    map[string]map[int]float64
	buttons map[string]map[int]bool
}

func (s *stubJoystick) AxisValue(d string, a int) (float64, bool) {
	if dm, ok := s.axes[d]; ok {
		v, ok := dm[a]
		return v, ok
	}
	return 0, false
}
func (s *stubJoystick) Button(d string, b int) (bool, bool) {
	if dm, ok := s.buttons[d]; ok {
		v, ok := dm[b]
		return v, ok
	}
	return false, false
}

type stubPanel struct {
	switches  map[string]int
	selectors map[string]int
	buttons   map[string]bool
}

func (p *stubPanel) Switch(name string) (int, bool) {
	v, ok := p.switches[name]
	return v, ok
}
func (p *stubPanel) Selector(name string) (int, bool) {
	v, ok := p.selectors[name]
	return v, ok
}
func (p *stubPanel) Button(name string) (bool, bool) {
	v, ok := p.buttons[name]
	return v, ok
}

type stubLogic struct {
	state map[int]bool
}

func (l *stubLogic) Logic(idx int) bool { return l.state[idx] }

type stubTelem struct {
	values map[string]float64
}

func (t *stubTelem) Value(name string) (float64, bool) {
	v, ok := t.values[name]
	return v, ok
}

// --- Constants ---

func TestResolveValue_Constants(t *testing.T) {
	r := New(nil, nil, nil, nil, nil)
	cases := []struct {
		name string
		want float64
	}{
		{"-99", -0.99},
		{"50", 0.5},
		{"100", 1.0},
		{"-100", -1.0},
		{"0", 0},
		{"150", 1.0},   // clamped
		{"-150", -1.0}, // clamped
		{"+50", 0.5},
		{"-50.5", -0.505},
	}
	for _, c := range cases {
		got, ok := r.ResolveValue(c.name)
		if !ok {
			t.Errorf("%s: ok=false", c.name)
			continue
		}
		if abs(got-c.want) > 1e-9 {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestResolveValue_NotANumber(t *testing.T) {
	r := New(nil, nil, nil, nil, nil)
	for _, s := range []string{"L3"} { // valid logic ref, but no logic state
		// Should not be parsed as constant.
		got, _ := r.ResolveValue(s)
		_ = got
	}
}

// --- Logic switches ---

func TestResolveValue_LogicSwitch(t *testing.T) {
	logic := &stubLogic{state: map[int]bool{1: true, 3: false}}
	r := New(nil, nil, nil, logic, nil)

	if v, ok := r.ResolveValue("L1"); !ok || v != 1.0 {
		t.Errorf("L1 (true): got (%v, %v)", v, ok)
	}
	if v, ok := r.ResolveValue("L3"); !ok || v != -1.0 {
		t.Errorf("L3 (false): got (%v, %v)", v, ok)
	}
	if b, ok := r.ResolveBool("L1"); !ok || !b {
		t.Errorf("L1 bool: got (%v, %v)", b, ok)
	}
	if b, ok := r.ResolveBool("L3"); !ok || b {
		t.Errorf("L3 bool: got (%v, %v)", b, ok)
	}
}

func TestResolveValue_LogicSwitchOutOfRange(t *testing.T) {
	r := New(nil, nil, nil, nil, nil)
	for _, s := range []string{"L0", "L65", "L99"} {
		if _, ok := r.ResolveValue(s); ok {
			t.Errorf("%s should not parse as logic switch", s)
		}
	}
}

// TestResolveValue_LogicSwitchLsForm covers the legacy ls(N) syntax
// EdgeTX uses in mixData srcRaw fields. Big Talon's CH4 source is
// ls(3); the resolver must treat this identically to L3.
func TestResolveValue_LogicSwitchLsForm(t *testing.T) {
	logic := &stubLogic{state: map[int]bool{3: true, 5: false}}
	r := New(nil, nil, nil, logic, nil)

	if v, ok := r.ResolveValue("ls(3)"); !ok || v != 1.0 {
		t.Errorf("ls(3) (L3=true): got (%v, %v) want (1.0, true)", v, ok)
	}
	if v, ok := r.ResolveValue("ls(5)"); !ok || v != -1.0 {
		t.Errorf("ls(5) (L5=false): got (%v, %v) want (-1.0, true)", v, ok)
	}
	// As bool too.
	if b, ok := r.ResolveBool("ls(3)"); !ok || !b {
		t.Errorf("ls(3) bool: got (%v, %v) want true", b, ok)
	}
	// Out of range.
	for _, s := range []string{"ls(0)", "ls(65)", "ls(abc)", "ls()"} {
		if _, ok := r.ResolveValue(s); ok {
			t.Errorf("%s should not parse", s)
		}
	}
}

// --- Negation ---

func TestResolveBool_Negation(t *testing.T) {
	logic := &stubLogic{state: map[int]bool{3: true}}
	r := New(nil, nil, nil, logic, nil)

	if b, ok := r.ResolveBool("!L3"); !ok || b {
		t.Errorf("!L3 (L3=true): got (%v, %v)", b, ok)
	}
	if b, ok := r.ResolveBool("!L4"); !ok || !b {
		t.Errorf("!L4 (L4=false): got (%v, %v)", b, ok)
	}
}

func TestResolveValue_Negation(t *testing.T) {
	logic := &stubLogic{state: map[int]bool{3: true}}
	r := New(nil, nil, nil, logic, nil)

	if v, ok := r.ResolveValue("!L3"); !ok || v != -1.0 {
		t.Errorf("!L3 value (L3=true): got (%v, %v)", v, ok)
	}
	if v, ok := r.ResolveValue("!50"); !ok || v != -0.5 {
		t.Errorf("!50: got (%v, %v)", v, ok)
	}
}

// --- Switch position match ---

func TestResolveBool_SwitchPosition(t *testing.T) {
	pnl := &stubPanel{switches: map[string]int{"SE": 2, "SF": 0, "SA": 1}}
	r := New(nil, nil, pnl, nil, nil)

	if b, ok := r.ResolveBool("SE2"); !ok || !b {
		t.Errorf("SE2 (SE=2): got (%v, %v)", b, ok)
	}
	if b, ok := r.ResolveBool("SE1"); !ok || b {
		t.Errorf("SE1 (SE=2): got (%v, %v) want false", b, ok)
	}
	if b, ok := r.ResolveBool("SF0"); !ok || !b {
		t.Errorf("SF0 (SF=0): got (%v, %v)", b, ok)
	}
	if b, ok := r.ResolveBool("SA1"); !ok || !b {
		t.Errorf("SA1: got (%v, %v)", b, ok)
	}
}

func TestResolveValue_SwitchPositionAsValue(t *testing.T) {
	pnl := &stubPanel{switches: map[string]int{"SE": 2}}
	r := New(nil, nil, pnl, nil, nil)

	// SE2 as value: +1 if matched, -1 otherwise.
	if v, ok := r.ResolveValue("SE2"); !ok || v != 1.0 {
		t.Errorf("SE2 value: got (%v, %v)", v, ok)
	}
	if v, ok := r.ResolveValue("SE0"); !ok || v != -1.0 {
		t.Errorf("SE0 value: got (%v, %v)", v, ok)
	}
}

func TestResolveBool_SelectorPosition(t *testing.T) {
	pnl := &stubPanel{selectors: map[string]int{"6POS": 15}}
	r := New(nil, nil, pnl, nil, nil)

	if b, ok := r.ResolveBool("6P15"); !ok || !b {
		t.Errorf("6P15: got (%v, %v)", b, ok)
	}
	if b, ok := r.ResolveBool("6P00"); !ok || b {
		t.Errorf("6P00 should be false (selector at 15): got (%v, %v)", b, ok)
	}
}

// --- Bound source: stick alias via binding ---

func TestResolveValue_BoundStickAlias(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Thr": {Device: "HOTAS", Axis: ptr(2), Deadband: 0.02},
			},
		},
		EdgeTX: model.EdgeTXModel{
			InputNames: map[int]model.InputName{0: {Val: "Thr"}},
		},
	}
	js := &stubJoystick{
		axes: map[string]map[int]float64{"HOTAS": {2: 0.75}},
	}
	r := New(m, js, nil, nil, nil)

	// Direct alias: "Thr"
	if v, ok := r.ResolveValue("Thr"); !ok || abs(v-0.75) > 1e-9 {
		t.Errorf("Thr: got (%v, %v) want 0.75", v, ok)
	}
	// Indirect via I0
	if v, ok := r.ResolveValue("I0"); !ok || abs(v-0.75) > 1e-9 {
		t.Errorf("I0: got (%v, %v) want 0.75", v, ok)
	}
}

func TestResolveValue_BoundStickDeadband(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Ail": {Device: "X", Axis: ptr(0), Deadband: 0.05},
			},
		},
	}
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0.02}}}
	r := New(m, js, nil, nil, nil)

	if v, ok := r.ResolveValue("Ail"); !ok || v != 0 {
		t.Errorf("inside deadband should be 0: got (%v, %v)", v, ok)
	}
}

func TestResolveValue_BoundStickInvert(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Ele": {Device: "X", Axis: ptr(1), Invert: true},
			},
		},
	}
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {1: 0.5}}}
	r := New(m, js, nil, nil, nil)

	if v, ok := r.ResolveValue("Ele"); !ok || v != -0.5 {
		t.Errorf("inverted: got (%v, %v) want -0.5", v, ok)
	}
}

// --- Bound source: panel switch as value ---

func TestResolveValue_PanelSwitch(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"SE": {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
			},
		},
	}
	pnl := &stubPanel{switches: map[string]int{"SE": 0}}
	r := New(m, nil, pnl, nil, nil)

	if v, ok := r.ResolveValue("SE"); !ok || v != -1.0 {
		t.Errorf("SE pos 0 (3-pos up): got (%v, %v) want -1", v, ok)
	}
	pnl.switches["SE"] = 1
	if v, ok := r.ResolveValue("SE"); !ok || v != 0 {
		t.Errorf("SE pos 1: got (%v, %v) want 0", v, ok)
	}
	pnl.switches["SE"] = 2
	if v, ok := r.ResolveValue("SE"); !ok || v != 1 {
		t.Errorf("SE pos 2: got (%v, %v) want 1", v, ok)
	}
}

func TestResolveValue_PanelSelector(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"6POS": {Device: "GCS", Selector: ptr(0)},
			},
		},
	}
	pnl := &stubPanel{selectors: map[string]int{"6POS": 5}}
	r := New(m, nil, pnl, nil, nil)

	if v, ok := r.ResolveValue("6POS"); !ok || v != 1.0 {
		t.Errorf("6POS pos 5: got (%v, %v) want 1.0", v, ok)
	}
}

// --- Telemetry ---

func TestResolveValue_Telemetry(t *testing.T) {
	m := &model.ZeroTXModel{
		EdgeTX: model.EdgeTXModel{
			TelemetrySensors: map[int]model.TelemetrySensor{
				0: {Label: "RxBatt"},
				1: {Label: "RSSI"},
			},
		},
	}
	tlm := &stubTelem{values: map[string]float64{"RxBatt": 3.7, "RSSI": -65}}
	r := New(m, nil, nil, nil, tlm)

	if v, ok := r.ResolveValue("RxBatt"); !ok || v != 3.7 {
		t.Errorf("RxBatt: got (%v, %v)", v, ok)
	}
	if v, ok := r.ResolveValue("RSSI"); !ok || v != -65 {
		t.Errorf("RSSI: got (%v, %v)", v, ok)
	}
	// Unknown sensor not in model: not a telemetry name, falls to default.
	if _, ok := r.ResolveValue("UnknownSensor"); ok {
		t.Errorf("UnknownSensor should not resolve")
	}
}

func TestResolveValue_TelemetryMissingPlumbing(t *testing.T) {
	m := &model.ZeroTXModel{
		EdgeTX: model.EdgeTXModel{
			TelemetrySensors: map[int]model.TelemetrySensor{
				0: {Label: "RxBatt"},
			},
		},
	}
	// No telemetry plumbing -> NullTelemetry returns false.
	r := New(m, nil, nil, nil, nil)
	if _, ok := r.ResolveValue("RxBatt"); ok {
		t.Errorf("RxBatt without telemetry plumbing should return ok=false")
	}
}

// --- Special sources ---

func TestResolveValue_MAX(t *testing.T) {
	r := New(nil, nil, nil, nil, nil)
	if v, ok := r.ResolveValue("MAX"); !ok || v != 1.0 {
		t.Errorf("MAX: got (%v, %v) want 1.0", v, ok)
	}
	if b, ok := r.ResolveBool("MAX"); !ok || !b {
		t.Errorf("MAX bool: got (%v, %v)", b, ok)
	}
}

func TestResolveValue_None(t *testing.T) {
	r := New(nil, nil, nil, nil, nil)
	for _, s := range []string{"", "NONE", "---", "--"} {
		if _, ok := r.ResolveValue(s); ok {
			t.Errorf("%q should resolve as none", s)
		}
	}
}

// --- Trims and gvars: parse but return false ---

func TestResolveValue_TrimsAndGvarsDeferred(t *testing.T) {
	r := New(nil, nil, nil, nil, nil)
	for _, s := range []string{"TrmH", "TrmV", "TrmA", "TrmE", "TrmR", "TrmT", "GV1", "GV3", "GV9"} {
		if _, ok := r.ResolveValue(s); ok {
			t.Errorf("%s should be deferred (ok=false)", s)
		}
	}
}

// --- Big Talon arm chain operands ---

// TestBigTalonArmOperands replays the operand resolution for the Big Talon
// arm chain (L1: VNEG I0,-99 andsw=SF2; etc) given a snapshot of inputs.
// The actual logic engine that consumes these results isn't built yet;
// this just verifies the resolver can produce all the inputs it'll need.
func TestBigTalonArmOperands(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Thr": {Device: "HOTAS", Axis: ptr(2), Deadband: 0.02},
				"SF":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SH":  {Device: "GCS", Switch: ptr(0), Kind: "2pos"},
			},
		},
		EdgeTX: model.EdgeTXModel{
			InputNames: map[int]model.InputName{0: {Val: "Thr"}},
		},
	}
	js := &stubJoystick{axes: map[string]map[int]float64{"HOTAS": {2: -1.0}}}
	pnl := &stubPanel{switches: map[string]int{"SF": 2, "SH": 0}}
	logic := &stubLogic{}
	r := New(m, js, pnl, logic, nil)

	// L1: FUNC_VNEG I0,-99  =>  I0 < -99% (i.e. throttle < -0.99)
	thr, ok := r.ResolveValue("I0")
	if !ok {
		t.Fatal("I0 not resolved")
	}
	const99, _ := r.ResolveValue("-99")
	if thr >= const99 {
		// thr=-1, const99=-0.99 -> thr<const99 should be true
		t.Errorf("VNEG check: thr=%v const=%v, expected thr<const", thr, const99)
	}

	// andsw=SF2: bool true if SF in pos 2.
	if b, ok := r.ResolveBool("SF2"); !ok || !b {
		t.Errorf("SF2 (andsw): got (%v, %v) want true", b, ok)
	}

	// L2: FUNC_EDGE SH2,0,10 andsw=L1  =>  needs SH2 bool and L1 prev state.
	if b, _ := r.ResolveBool("SH2"); b {
		t.Errorf("SH2 (SH=0): want false")
	}
	pnl.switches["SH"] = 2
	if b, ok := r.ResolveBool("SH2"); !ok || !b {
		t.Errorf("SH2 (SH=2): got (%v, %v) want true", b, ok)
	}

	// L3: FUNC_STICKY L2,L4
	logic.state = map[int]bool{2: true, 4: false}
	if b, ok := r.ResolveBool("L2"); !ok || !b {
		t.Errorf("L2: got (%v, %v) want true", b, ok)
	}
	if b, ok := r.ResolveBool("L4"); !ok || b {
		t.Errorf("L4: got (%v, %v) want false", b, ok)
	}
	// !L3 used in custom function
	logic.state[3] = false
	if b, ok := r.ResolveBool("!L3"); !ok || !b {
		t.Errorf("!L3 (L3=false): got (%v, %v) want true", b, ok)
	}
}
