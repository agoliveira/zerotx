package mapper

import (
	"testing"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/cf"
	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/logic"
	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// stubJoystick / stubPanel: test doubles for the resolver.
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

func ptr[T any](v T) *T { return &v }

func TestSafeDefaults(t *testing.T) {
	ch := SafeDefaults()
	if ch[0] != ipc.CrsfChMid {
		t.Errorf("CH0 default: got %d want %d", ch[0], ipc.CrsfChMid)
	}
	if ch[2] != ipc.CrsfChMin {
		t.Errorf("CH2 (throttle slot): got %d want %d", ch[2], ipc.CrsfChMin)
	}
	if ch[4] != ipc.CrsfChMin {
		t.Errorf("CH4 (arm slot): got %d want %d", ch[4], ipc.CrsfChMin)
	}
}

func TestResolve_NoModel(t *testing.T) {
	mp := New(nil, nil)
	ch := mp.Resolve()
	defaults := SafeDefaults()
	if ch != defaults {
		t.Errorf("nil model should return SafeDefaults")
	}
}

// TestResolve_StickAxis verifies an analog stick mapped through the
// resolver populates the channel correctly.
func TestResolve_StickAxis(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Thr": {Device: "X", Axis: ptr(0)},
			},
		},
		EdgeTX: model.EdgeTXModel{
			InputNames: map[int]model.InputName{0: {Val: "Thr"}},
			MixData:    []model.Mix{{DestCh: 0, SrcRaw: "I0", Weight: 100}},
		},
	}
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {0: 1.0}}}
	r := source.New(m, js, nil, nil, nil)
	mp := New(m, r)

	ch := mp.Resolve()
	if ch[0] != ipc.CrsfChMax {
		t.Errorf("Thr=1.0 -> CH0: got %d want %d", ch[0], ipc.CrsfChMax)
	}

	js.axes["X"][0] = -1.0
	ch = mp.Resolve()
	if ch[0] != ipc.CrsfChMin {
		t.Errorf("Thr=-1.0 -> CH0: got %d want %d", ch[0], ipc.CrsfChMin)
	}
}

// TestResolve_PanelSwitch confirms GCS-bound switches still work.
func TestResolve_PanelSwitch(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"SE": {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
			},
		},
		EdgeTX: model.EdgeTXModel{
			MixData: []model.Mix{{DestCh: 5, SrcRaw: "SE", Weight: 100}},
		},
	}
	pnl := &stubPanel{switches: map[string]int{"SE": 2}}
	r := source.New(m, nil, pnl, nil, nil)
	mp := New(m, r)

	ch := mp.Resolve()
	if ch[5] != ipc.CrsfChMax {
		t.Errorf("SE=down -> CH5: got %d want %d", ch[5], ipc.CrsfChMax)
	}
}

// TestResolve_OverrideChannel_BasicSafety: with a CF that forces CH0 to
// min when L1 is true, throttle stick at +1.0 still produces CH0=min
// because the CF override beats the mix.
func TestResolve_OverrideChannel_BasicSafety(t *testing.T) {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Thr": {Device: "X", Axis: ptr(0)},
			},
		},
		EdgeTX: model.EdgeTXModel{
			InputNames: map[int]model.InputName{0: {Val: "Thr"}},
			MixData:    []model.Mix{{DestCh: 0, SrcRaw: "I0", Weight: 100}},
			LogicalSw: map[int]model.LogicalSwitch{
				0: {Func: "FUNC_VPOS", Def: "Thr,99"}, // L1 true while Thr > 0.99
			},
			CustomFn: map[int]model.CustomFunction{
				0: {Swtch: "L1", Func: "OVERRIDE_CHANNEL", Def: "0,-100,1"},
			},
		},
	}
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {0: 1.0}}}
	r := source.New(m, js, nil, nil, nil)
	eng := logic.New(m, r, logic.NewFakeClock())
	r.Logic = eng
	cfp := cf.New(m, r)
	mp := New(m, r)
	mp.SetEngine(eng)
	mp.SetCFProcessor(cfp)

	// Tick once to settle. With Thr=1.0, L1 should evaluate true on the
	// first tick (mapper ticks engine, engine sees Thr from resolver).
	mp.Resolve()
	// Second tick: CF reads L1 (was true on prev tick) and overrides CH0.
	ch := mp.Resolve()
	if ch[0] != ipc.CrsfChMin {
		t.Errorf("override should force CH0 to min: got %d want %d", ch[0], ipc.CrsfChMin)
	}
}

// TestResolve_BigTalonArmChainEndToEnd is the safety regression test.
//
// Setup: load Big Talon model, simulate stick + GCS panel inputs over
// multiple ticks. Verify:
//
//  1. Throttle stick at +1.0 with !L3 (not armed) -> CH0 forced to min
//     by the OVERRIDE_CHANNEL CF.
//
//  2. After arming via the SH-tap-with-SF-down sequence, L3 is true and
//     CH0 follows the throttle stick (no override).
//
//  3. Disarming via SF-up triggers L4 and resets L3, so CH0 is force-min
//     again even with stick high.
//
// This is the master integration test. If this passes, the arm chain
// works end-to-end without ever flying.
func TestResolve_BigTalonArmChainEndToEnd(t *testing.T) {
	em, err := model.LoadEdgeTX("../../testdata/big_talon.yml")
	if err != nil {
		t.Fatalf("load big_talon: %v", err)
	}
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Thr": {Device: "X", Axis: ptr(0), Deadband: 0.02},
				"Ail": {Device: "X", Axis: ptr(1), Deadband: 0.02},
				"Ele": {Device: "X", Axis: ptr(2), Deadband: 0.02},
				"Rud": {Device: "X", Axis: ptr(3), Deadband: 0.02},
				"SA":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SB":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SC":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SE":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SF":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SG":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SH":  {Device: "GCS", Switch: ptr(0), Kind: "2pos"},
			},
		},
		EdgeTX: *em,
	}

	js := &stubJoystick{axes: map[string]map[int]float64{
		"X": {0: -1.0, 1: 0, 2: 0, 3: 0}, // throttle at idle initially
	}}
	pnl := &stubPanel{switches: map[string]int{
		"SA": 0, "SB": 0, "SC": 0, "SE": 0, "SF": 0, "SG": 0, "SH": 0,
	}}
	clk := logic.NewFakeClock()
	r := source.New(m, js, pnl, nil, nil)
	eng := logic.New(m, r, clk)
	r.Logic = eng
	cfp := cf.New(m, r)
	mp := New(m, r)
	mp.SetEngine(eng)
	mp.SetCFProcessor(cfp)

	tick := func() [ipc.Channels]uint16 {
		ch := mp.Resolve()
		clk.Advance(20 * time.Millisecond)
		return ch
	}

	// Phase 1: settle. Throttle idle, SF up, SH released. L3 should be
	// false (not armed). CH0 driven by override to min (override fires
	// while !L3 = true).
	for i := 0; i < 5; i++ {
		tick()
	}
	if eng.Logic(3) {
		t.Fatalf("phase 1: L3 should be false (not armed)")
	}

	// Phase 2: try to arm. Push throttle to +1.0 (max). Without arming,
	// CH0 must STILL be min (override forces it).
	js.axes["X"][0] = 1.0
	for i := 0; i < 3; i++ {
		tick()
	}
	ch := tick()
	if ch[0] != ipc.CrsfChMin {
		t.Errorf("phase 2 (stick high, not armed): CH0 must be safe-min, got %d want %d",
			ch[0], ipc.CrsfChMin)
	}

	// Phase 3: arm sequence. Throttle back to idle, SF down (precondition),
	// SH press-and-release within 1 second.
	js.axes["X"][0] = -1.0
	pnl.switches["SF"] = 2
	for i := 0; i < 5; i++ {
		tick()
	}
	if !eng.Logic(1) {
		t.Errorf("phase 3: L1 (Thr<-99 AND SF2) should be true")
	}

	// SH momentary press (release within 1s for EDGE T2=10).
	pnl.switches["SH"] = 2
	tick()
	tick()
	pnl.switches["SH"] = 0
	tick()
	tick()
	for i := 0; i < 5; i++ {
		tick()
	}
	if !eng.Logic(3) {
		t.Errorf("phase 3: arm latch L3 should be set after SH press with SF2")
	}

	// Phase 4: armed and stick high. CH0 should now follow the stick
	// (no override, because L3=true means !L3=false, override doesn't fire).
	js.axes["X"][0] = 1.0
	ch = tick()
	if ch[0] != ipc.CrsfChMax {
		t.Errorf("phase 4 (armed, stick max): CH0 should follow stick to max, got %d want %d",
			ch[0], ipc.CrsfChMax)
	}

	// Phase 5: disarm via SF up. L4 triggers (Thr low + SF up), L3 resets.
	js.axes["X"][0] = -1.0
	pnl.switches["SF"] = 0
	for i := 0; i < 10; i++ {
		tick()
	}
	if eng.Logic(3) {
		t.Errorf("phase 5: L3 should be reset after SF returned to up")
	}

	// Phase 6: disarmed, stick high again. Override engages, CH0 = min.
	js.axes["X"][0] = 1.0
	ch = tick()
	if ch[0] != ipc.CrsfChMin {
		t.Errorf("phase 6 (disarmed, stick high): CH0 must be safe-min again, got %d want %d",
			ch[0], ipc.CrsfChMin)
	}
}
