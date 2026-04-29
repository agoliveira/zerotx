package logic

import (
	"testing"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// loadModelFromFile loads an EdgeTX YAML and wraps it in a ZeroTXModel.
func loadModelFromFile(t *testing.T, path string) *model.ZeroTXModel {
	t.Helper()
	em, err := model.LoadEdgeTX(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Thr": {Device: "X", Axis: ptr(0), Deadband: 0.02},
				"Ail": {Device: "X", Axis: ptr(1), Deadband: 0.02},
				"Ele": {Device: "X", Axis: ptr(2), Deadband: 0.02},
				"Rud": {Device: "X", Axis: ptr(3), Deadband: 0.02},
				"SA":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SB":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SC":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SD":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SE":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SF":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SG":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SH":  {Device: "GCS", Switch: ptr(0), Kind: "2pos"},
			},
		},
		EdgeTX: *em,
	}
}

// TestEngine_XTudoAllSwitchesEvaluate confirms every L# in x-tudo runs
// without error or panic given a clean input snapshot. Doesn't assert
// specific outputs; just exercises every code path once.
func TestEngine_XTudoAllSwitchesEvaluate(t *testing.T) {
	m := loadModelFromFile(t, "../../testdata/x_tudo.yml")
	pnl := &stubPanel{
		switches: map[string]int{
			"SA": 0, "SB": 0, "SC": 0, "SD": 0, "SE": 0, "SF": 0, "SG": 0, "SH": 0,
		},
	}
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0, 1: 0, 2: 0, 3: 0}}}

	r := source.New(m, js, pnl, nil, nil)
	clk := NewFakeClock()
	eng := New(m, r, clk)
	r.Logic = eng

	// 50 ticks at 20ms each = 1 second of evaluation.
	for i := 0; i < 50; i++ {
		eng.Tick()
		clk.Advance(20 * time.Millisecond)
	}

	// Snapshot is the right shape.
	snap := eng.Snapshot()
	if len(snap) != 22 {
		t.Errorf("snapshot length: got %d want 22", len(snap))
	}
}

// TestEngine_XTudoSpecificBehaviors exercises a few specific x-tudo
// switches with deterministic inputs to catch regressions.
func TestEngine_XTudoSpecificBehaviors(t *testing.T) {
	m := loadModelFromFile(t, "../../testdata/x_tudo.yml")
	pnl := &stubPanel{
		switches: map[string]int{
			"SA": 0, "SB": 0, "SC": 0, "SD": 0, "SE": 0, "SF": 0, "SG": 0, "SH": 0,
		},
	}
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0, 1: 0, 2: 0, 3: 0}}}
	r := source.New(m, js, pnl, nil, nil)
	clk := NewFakeClock()
	eng := New(m, r, clk)
	r.Logic = eng

	// Initial tick.
	eng.Tick()
	clk.Advance(20 * time.Millisecond)

	// L1: VPOS Thr,50 -> Thr > 0.5 -> false initially.
	if eng.Logic(1) {
		t.Errorf("L1 (VPOS Thr,50) with Thr=0: expected false")
	}

	// Push throttle high.
	js.axes["X"][0] = 0.7
	eng.Tick()
	clk.Advance(20 * time.Millisecond)
	if !eng.Logic(1) {
		t.Errorf("L1 with Thr=0.7: expected true")
	}

	// L2: VNEG Thr,-50 -> Thr < -0.5.
	js.axes["X"][0] = -0.8
	eng.Tick()
	clk.Advance(20 * time.Millisecond)
	if !eng.Logic(2) {
		t.Errorf("L2 (VNEG Thr,-50) with Thr=-0.8: expected true")
	}

	// L7: AND SA0,SB0 -> both at pos 0 = true.
	pnl.switches["SA"] = 0
	pnl.switches["SB"] = 0
	eng.Tick()
	clk.Advance(20 * time.Millisecond)
	if !eng.Logic(7) {
		t.Errorf("L7 (AND SA0,SB0) with both pos 0: expected true")
	}
	pnl.switches["SA"] = 2
	eng.Tick()
	clk.Advance(20 * time.Millisecond)
	if eng.Logic(7) {
		t.Errorf("L7 with SA=2: expected false")
	}

	// L13: STICKY L1,L2 - latches via L1 rising, resets via L2 rising.
	// Set up: throttle middle so L1 false, L2 false.
	js.axes["X"][0] = 0
	for i := 0; i < 5; i++ {
		eng.Tick()
		clk.Advance(20 * time.Millisecond)
	}
	if eng.Logic(13) {
		t.Errorf("L13 sticky: expected false initially")
	}
	// Push Thr high -> L1 rises next tick -> sticky sets the tick after.
	js.axes["X"][0] = 0.8
	eng.Tick()
	clk.Advance(20 * time.Millisecond)
	// Now L1 is true; sticky reads previous-tick L1 (was false), sees rising edge against its OWN prev — wait, sticky's prev is its own internal state, not the engine's prev.
	eng.Tick()
	clk.Advance(20 * time.Millisecond)
	if !eng.Logic(13) {
		t.Errorf("L13 sticky after L1 rose: expected latched true")
	}
	// Drop Thr; latch persists.
	js.axes["X"][0] = 0
	for i := 0; i < 5; i++ {
		eng.Tick()
		clk.Advance(20 * time.Millisecond)
	}
	if !eng.Logic(13) {
		t.Errorf("L13 sticky after Thr dropped: latch should persist")
	}
	// Trigger L2 (Thr negative).
	js.axes["X"][0] = -0.8
	for i := 0; i < 5; i++ {
		eng.Tick()
		clk.Advance(20 * time.Millisecond)
	}
	if eng.Logic(13) {
		t.Errorf("L13 sticky after L2 rose: expected reset")
	}
}

// TestEngine_BigTalonArmChain replays the Big Talon arm/disarm sequence
// against the actual model file. This is the critical safety test: it
// verifies that the SH-tap-with-SF-down arm sequence works AND that the
// !L3 disarm condition correctly tracks the arm latch.
//
// Big Talon arm chain:
//
//	L1 = VNEG I0,-99 andsw=SF2     true when throttle low AND SF down
//	L2 = EDGE SH2,0,10 andsw=L1    SH momentary press (released within 1s),
//	                               only if L1 active
//	L3 = STICKY L2,L4              the arm latch
//	L4 = VNEG I0,-99 andsw=SF0     disarm: throttle low AND SF up
//
// Custom function: !L3 -> OVERRIDE_CHANNEL 0,-100,1 (force CH0 min when
// not armed). Tested in phase 2-d via the mapper integration test.
func TestEngine_BigTalonArmChain(t *testing.T) {
	m := loadModelFromFile(t, "../../testdata/big_talon.yml")

	pnl := &stubPanel{
		switches: map[string]int{
			"SA": 0, "SB": 0, "SC": 0, "SE": 0, "SF": 0, "SG": 0, "SH": 0,
		},
	}
	// Throttle at idle (-1.0): satisfies L1's "throttle very low" precondition.
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {0: -1.0, 1: 0, 2: 0, 3: 0}}}
	r := source.New(m, js, pnl, nil, nil)
	clk := NewFakeClock()
	eng := New(m, r, clk)
	r.Logic = eng

	tick := func() {
		eng.Tick()
		clk.Advance(20 * time.Millisecond)
	}

	// Initial state: SF up (pos 0), throttle idle, SH released.
	// Expected: L1 false (SF not in pos 2), L3 false (not armed).
	for i := 0; i < 5; i++ {
		tick()
	}
	if eng.Logic(3) {
		t.Errorf("initial: L3 should be false (not armed)")
	}

	// Move SF to position 2 (down): pre-arm condition active.
	pnl.switches["SF"] = 2
	for i := 0; i < 5; i++ {
		tick()
	}
	if !eng.Logic(1) {
		t.Errorf("after SF=2: L1 (VNEG Thr below -99 AND SF2) should be true")
	}
	if eng.Logic(3) {
		t.Errorf("L1 alone shouldn't arm: L3 should still be false")
	}

	// SH momentary press and release within 1 second.
	pnl.switches["SH"] = 2
	tick()
	tick() // hold for two ticks (40ms)
	pnl.switches["SH"] = 0
	tick()
	// L2 should pulse this tick. L3 sees L2 rising -> latches.
	tick()

	// Allow time for the multi-tick chain to propagate.
	for i := 0; i < 5; i++ {
		tick()
	}
	if !eng.Logic(3) {
		t.Errorf("after SH momentary press with SF2: L3 (arm latch) should be true")
	}

	// SF returns to up (pos 0). SH is released. Throttle still idle.
	// L4 should fire (throttle low + SF up). L3 sees L4 rise, resets.
	pnl.switches["SF"] = 0
	for i := 0; i < 10; i++ {
		tick()
	}
	if eng.Logic(3) {
		t.Errorf("after SF=0 (disarm precondition): L3 should be reset")
	}
}

// TestEngine_LogicChainPrevTickLatency verifies the documented one-tick
// latency on L→L references. L13: STICKY L1,L2. Changes to L1 are
// observed by L13's sticky on the SUBSEQUENT tick, not the same tick.
func TestEngine_LogicChainPrevTickLatency(t *testing.T) {
	m := loadModelFromFile(t, "../../testdata/x_tudo.yml")
	pnl := &stubPanel{}
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0, 1: 0, 2: 0, 3: 0}}}
	r := source.New(m, js, pnl, nil, nil)
	clk := NewFakeClock()
	eng := New(m, r, clk)
	r.Logic = eng

	// Settle.
	for i := 0; i < 3; i++ {
		eng.Tick()
		clk.Advance(20 * time.Millisecond)
	}

	// At this tick: L1 will become true.
	js.axes["X"][0] = 0.8
	eng.Tick()
	clk.Advance(20 * time.Millisecond)

	// L1 just became true. L13 (STICKY L1,L2) reads L1's PREVIOUS state
	// from prior tick (was false). So sticky's prev_v1=false, current_v1=false
	// (because resolver returned prev). No edge yet.
	if eng.Logic(13) {
		t.Errorf("L13 should not yet see L1's transition (1-tick latency)")
	}

	// Next tick: L13 reads L1=true (prev tick), sticky sees rising edge.
	eng.Tick()
	clk.Advance(20 * time.Millisecond)
	if !eng.Logic(13) {
		t.Errorf("L13 should see L1's transition by now (1-tick later)")
	}
}

// TestEngine_NoModel tolerates a nil model.
func TestEngine_NoModel(t *testing.T) {
	r := source.New(nil, nil, nil, nil, nil)
	eng := New(nil, r, NewFakeClock())
	eng.Tick() // should not panic
	if eng.Logic(1) {
		t.Errorf("nil model: Logic should return false")
	}
}

// TestEngine_UnknownFunctionLogged confirms an unsupported function
// doesn't crash the engine; output stays false.
func TestEngine_UnknownFunctionLogged(t *testing.T) {
	m := &model.ZeroTXModel{
		EdgeTX: model.EdgeTXModel{
			LogicalSw: map[int]model.LogicalSwitch{
				0: {Func: "FUNC_FUTURISTIC_SOMETHING", Def: "x,y"},
			},
		},
	}
	r := source.New(m, nil, nil, nil, nil)
	eng := New(m, r, NewFakeClock())
	r.Logic = eng

	eng.Tick()
	if eng.Logic(1) {
		t.Errorf("unknown function: expected false output")
	}
}
