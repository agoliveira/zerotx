package logic

import (
	"testing"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// stubPanel / stubLogic / stubJoystick are local test doubles that
// satisfy the source resolver's interfaces.

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

func ptr[T any](v T) *T { return &v }

// makeResolver builds a resolver with the given panel and logic stubs.
func makeResolver(pnl source.PanelState, logic source.LogicState) *source.Resolver {
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Thr": {Device: "X", Axis: ptr(0)},
				"Ail": {Device: "X", Axis: ptr(1)},
				"Ele": {Device: "X", Axis: ptr(2)},
				"Rud": {Device: "X", Axis: ptr(3)},
				"SA":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SB":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SC":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SD":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SE":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SF":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SH":  {Device: "GCS", Switch: ptr(0), Kind: "2pos"},
			},
		},
	}
	js := &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0, 1: 0, 2: 0, 3: 0}}}
	return source.New(m, js, pnl, logic, nil)
}

// --- Value comparison ---

func TestEval_VPOS(t *testing.T) {
	pnl := &stubPanel{}
	r := makeResolver(pnl, nil)
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0.6}}}

	d := parsedDef{op1: "Thr", c1: 0.5}
	if got := evalVPOS(d, &switchState{}, r, time.Now()); !got {
		t.Errorf("Thr=0.6 > 0.5: got false")
	}
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0.4}}}
	if got := evalVPOS(d, &switchState{}, r, time.Now()); got {
		t.Errorf("Thr=0.4 not > 0.5: got true")
	}
}

func TestEval_VNEG(t *testing.T) {
	r := makeResolver(&stubPanel{}, nil)
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {0: -0.99}}}

	d := parsedDef{op1: "Thr", c1: -0.5}
	if got := evalVNEG(d, &switchState{}, r, time.Now()); !got {
		t.Errorf("Thr=-0.99 < -0.5: got false")
	}
}

func TestEval_VEQUAL_AndAlmost(t *testing.T) {
	r := makeResolver(&stubPanel{switches: map[string]int{"SE": 1}}, nil)

	// SE pos 1 (3-pos mid) -> value 0.0
	if got := evalVEQUAL(parsedDef{op1: "SE", c1: 0.0}, &switchState{}, r, time.Now()); !got {
		t.Errorf("VEQUAL SE,0 (SE=mid=0.0): got false")
	}
	// VALMOSTEQUAL with wider tolerance
	r2 := makeResolver(&stubPanel{}, nil)
	r2.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {1: 0.04}}}
	if got := evalVALMOSTEQUAL(parsedDef{op1: "Ail", c1: 0.0}, &switchState{}, r2, time.Now()); !got {
		t.Errorf("VALMOSTEQUAL Ail=0.04 ~= 0: got false")
	}
}

func TestEval_APOS_ANEG(t *testing.T) {
	r := makeResolver(&stubPanel{}, nil)
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {1: -0.7}}}

	if got := evalAPOS(parsedDef{op1: "Ail", c1: 0.5}, &switchState{}, r, time.Now()); !got {
		t.Errorf("APOS |Ail=-0.7| > 0.5: got false")
	}
	if got := evalANEG(parsedDef{op1: "Ail", c1: 0.5}, &switchState{}, r, time.Now()); got {
		t.Errorf("ANEG |Ail=-0.7| < 0.5: got true")
	}
}

// --- Boolean combinators ---

func TestEval_AND_OR_XOR(t *testing.T) {
	pnl := &stubPanel{switches: map[string]int{"SA": 0, "SB": 0}}
	r := makeResolver(pnl, nil)

	d := parsedDef{op1: "SA0", op2: "SB0"}
	if got := evalAND(d, &switchState{}, r, time.Now()); !got {
		t.Errorf("AND SA0,SB0 (both pos 0): got false")
	}
	if got := evalOR(d, &switchState{}, r, time.Now()); !got {
		t.Errorf("OR SA0,SB0 (either): got false")
	}
	if got := evalXOR(d, &switchState{}, r, time.Now()); got {
		t.Errorf("XOR same true: got true")
	}

	pnl.switches["SB"] = 2
	if got := evalAND(d, &switchState{}, r, time.Now()); got {
		t.Errorf("AND SA0(true),SB0(false): got true")
	}
	if got := evalOR(d, &switchState{}, r, time.Now()); !got {
		t.Errorf("OR with one true: got false")
	}
	if got := evalXOR(d, &switchState{}, r, time.Now()); !got {
		t.Errorf("XOR true vs false: got false")
	}
}

// --- Source compare ---

func TestEval_GREATER_LESS(t *testing.T) {
	r := makeResolver(&stubPanel{}, nil)
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {1: 0.6, 2: 0.3}}}

	d := parsedDef{op1: "Ail", op2: "Ele"}
	if got := evalGREATER(d, &switchState{}, r, time.Now()); !got {
		t.Errorf("GREATER 0.6 > 0.3: got false")
	}
	if got := evalLESS(d, &switchState{}, r, time.Now()); got {
		t.Errorf("LESS 0.6 < 0.3: got true")
	}
}

// --- STICKY ---

func TestEval_STICKY_SetReset(t *testing.T) {
	logic := &stubLogic{state: map[int]bool{}}
	r := makeResolver(&stubPanel{}, logic)
	st := &switchState{}
	d := parsedDef{op1: "L1", op2: "L2"}

	// Initially L1=false, L2=false. No edges. Latch starts false.
	if got := evalSTICKY(d, st, r, time.Now()); got {
		t.Errorf("initial: latch should be false")
	}

	// L1 rising edge -> set.
	logic.state[1] = true
	if got := evalSTICKY(d, st, r, time.Now()); !got {
		t.Errorf("after L1 rise: latch should be set")
	}

	// L1 still high -> latch stays set, no new edge.
	if got := evalSTICKY(d, st, r, time.Now()); !got {
		t.Errorf("L1 held: latch should remain set")
	}

	// L1 drops to false -> latch persists (level-insensitive).
	logic.state[1] = false
	if got := evalSTICKY(d, st, r, time.Now()); !got {
		t.Errorf("L1 fell, no V2 edge: latch should persist")
	}

	// L2 rising edge -> reset.
	logic.state[2] = true
	if got := evalSTICKY(d, st, r, time.Now()); got {
		t.Errorf("L2 rose: latch should be reset")
	}

	// Both rising in same tick: reset wins.
	logic.state[1] = false
	logic.state[2] = false
	evalSTICKY(d, st, r, time.Now()) // settle prev state
	logic.state[1] = true
	logic.state[2] = true
	if got := evalSTICKY(d, st, r, time.Now()); got {
		t.Errorf("simultaneous V1+V2 rise: reset should win, latch should be false")
	}
}

// --- EDGE ---

func TestEval_EDGE_NumericT2(t *testing.T) {
	pnl := &stubPanel{switches: map[string]int{"SH": 0}}
	r := makeResolver(pnl, nil)
	clk := NewFakeClock()
	st := &switchState{}
	// SH2, T1=0.5s, T2=2.0s
	d := parsedDef{op1: "SH2", edgeT1: 500 * time.Millisecond, edgeT2: 2 * time.Second, edgeT2Spec: edgeT2Numeric}

	// V1 (SH2) false: no pulse.
	if got := evalEDGE(d, st, r, clk.Now()); got {
		t.Errorf("V1 inactive: got pulse")
	}

	// V1 rises.
	pnl.switches["SH"] = 2
	clk.Advance(20 * time.Millisecond)
	evalEDGE(d, st, r, clk.Now()) // tick after rise

	// Hold V1 for 1.0s, then drop. Pulse should fire (within 0.5..2.0s).
	clk.Advance(1 * time.Second)
	evalEDGE(d, st, r, clk.Now()) // still high
	pnl.switches["SH"] = 0
	clk.Advance(20 * time.Millisecond)
	if got := evalEDGE(d, st, r, clk.Now()); !got {
		t.Errorf("EDGE with held=1.0s in window 0.5..2.0s: expected pulse")
	}
	// Next tick: pulse cleared.
	clk.Advance(20 * time.Millisecond)
	if got := evalEDGE(d, st, r, clk.Now()); got {
		t.Errorf("pulse should be one-shot")
	}
}

func TestEval_EDGE_TooShort_NoPulse(t *testing.T) {
	pnl := &stubPanel{switches: map[string]int{"SH": 0}}
	r := makeResolver(pnl, nil)
	clk := NewFakeClock()
	st := &switchState{}
	d := parsedDef{op1: "SH2", edgeT1: 500 * time.Millisecond, edgeT2: 2 * time.Second, edgeT2Spec: edgeT2Numeric}

	// V1 rises.
	pnl.switches["SH"] = 2
	clk.Advance(20 * time.Millisecond)
	evalEDGE(d, st, r, clk.Now())
	// Held only 100ms, drops.
	clk.Advance(100 * time.Millisecond)
	pnl.switches["SH"] = 0
	if got := evalEDGE(d, st, r, clk.Now()); got {
		t.Errorf("held too short (<T1): got pulse")
	}
}

func TestEval_EDGE_Immediate(t *testing.T) {
	pnl := &stubPanel{switches: map[string]int{"SH": 0}}
	r := makeResolver(pnl, nil)
	clk := NewFakeClock()
	st := &switchState{}
	// T2="<" mode: fire when T1 reached, V1 still high.
	d := parsedDef{op1: "SH2", edgeT1: 500 * time.Millisecond, edgeT2Spec: edgeT2Immediate}

	pnl.switches["SH"] = 2
	clk.Advance(20 * time.Millisecond)
	evalEDGE(d, st, r, clk.Now())
	// Still 100ms after rise; no pulse yet.
	clk.Advance(100 * time.Millisecond)
	if got := evalEDGE(d, st, r, clk.Now()); got {
		t.Errorf("before T1: got pulse")
	}
	// 600ms after rise; should pulse now.
	clk.Advance(500 * time.Millisecond)
	if got := evalEDGE(d, st, r, clk.Now()); !got {
		t.Errorf("at T1, V1 still high: expected pulse")
	}
	// Next tick: not pulsing again.
	clk.Advance(20 * time.Millisecond)
	if got := evalEDGE(d, st, r, clk.Now()); got {
		t.Errorf("after first pulse: should not re-pulse")
	}
}

func TestEval_EDGE_Unbounded(t *testing.T) {
	pnl := &stubPanel{switches: map[string]int{"SH": 0}}
	r := makeResolver(pnl, nil)
	clk := NewFakeClock()
	st := &switchState{}
	// T2="-" mode: no upper bound.
	d := parsedDef{op1: "SH2", edgeT1: 500 * time.Millisecond, edgeT2Spec: edgeT2Unbounded}

	pnl.switches["SH"] = 2
	clk.Advance(20 * time.Millisecond)
	evalEDGE(d, st, r, clk.Now())

	// Hold for 5 seconds (well past anything).
	clk.Advance(5 * time.Second)
	pnl.switches["SH"] = 0
	if got := evalEDGE(d, st, r, clk.Now()); !got {
		t.Errorf("unbounded T2, held 5s, then dropped: expected pulse")
	}
}

// --- TIMER ---

func TestEval_TIMER(t *testing.T) {
	r := makeResolver(&stubPanel{}, nil)
	clk := NewFakeClock()
	st := &switchState{}
	// 0.5s on, 1.0s off
	d := parsedDef{timerOn: 500 * time.Millisecond, timerOff: 1 * time.Second}

	// First tick: initialize, output true.
	if got := evalTIMER(d, st, r, clk.Now()); !got {
		t.Errorf("timer init: should start true (on phase)")
	}
	// 100ms in: still on.
	clk.Advance(100 * time.Millisecond)
	if got := evalTIMER(d, st, r, clk.Now()); !got {
		t.Errorf("timer 100ms in: should still be on")
	}
	// 600ms total: on phase done, transition to off.
	clk.Advance(500 * time.Millisecond)
	if got := evalTIMER(d, st, r, clk.Now()); got {
		t.Errorf("timer 600ms in: should be off")
	}
	// Wait through the 1s off phase.
	clk.Advance(1100 * time.Millisecond)
	if got := evalTIMER(d, st, r, clk.Now()); !got {
		t.Errorf("after off phase: should be on again")
	}
}

// --- DIFFEGREATER / ADIFFEGREATER ---

func TestEval_DIFFEGREATER(t *testing.T) {
	r := makeResolver(&stubPanel{}, nil)
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0.0}}}
	st := &switchState{}
	d := parsedDef{op1: "Thr", c1: 0.30} // 30% threshold

	// First tick: baseline, no pulse.
	if got := evalDIFFEGREATER(d, st, r, time.Now()); got {
		t.Errorf("first tick: expected no pulse (baseline)")
	}
	// Small change.
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0.10}}}
	if got := evalDIFFEGREATER(d, st, r, time.Now()); got {
		t.Errorf("delta 0.10 < 0.30: expected no pulse")
	}
	// Big positive jump.
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {0: 0.50}}}
	if got := evalDIFFEGREATER(d, st, r, time.Now()); !got {
		t.Errorf("delta +0.40 >= 0.30: expected pulse")
	}
	// Big negative jump (signed: not >= threshold).
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {0: -0.5}}}
	if got := evalDIFFEGREATER(d, st, r, time.Now()); got {
		t.Errorf("signed delta -1.0 not >= 0.30: should not pulse")
	}
}

func TestEval_ADIFFEGREATER(t *testing.T) {
	r := makeResolver(&stubPanel{}, nil)
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {1: 0.0}}}
	st := &switchState{}
	d := parsedDef{op1: "Ail", c1: 0.20}

	evalADIFFEGREATER(d, st, r, time.Now()) // baseline
	// Big negative jump triggers absolute version.
	r.Joystick = &stubJoystick{axes: map[string]map[int]float64{"X": {1: -0.5}}}
	if got := evalADIFFEGREATER(d, st, r, time.Now()); !got {
		t.Errorf("abs delta 0.5 >= 0.2: expected pulse")
	}
}

// --- Modifiers ---

func TestModifier_Andsw_Gating(t *testing.T) {
	pnl := &stubPanel{switches: map[string]int{"SC": 0}}
	r := makeResolver(pnl, nil)

	// raw=true gated by SC1 which is currently false.
	if got := applyAndsw(true, "SC1", r); got {
		t.Errorf("andsw SC1 false: expected gating to false")
	}
	pnl.switches["SC"] = 1
	if got := applyAndsw(true, "SC1", r); !got {
		t.Errorf("andsw SC1 true: expected pass-through")
	}
}

func TestModifier_Delay(t *testing.T) {
	clk := NewFakeClock()
	st := &switchState{}
	delay := 1 * time.Second

	// Off: stays off.
	if got := applyDelay(false, st, delay, clk.Now()); got {
		t.Errorf("delay off: expected false")
	}
	// Rising edge: held false until delay elapses.
	if got := applyDelay(true, st, delay, clk.Now()); got {
		t.Errorf("rising edge before delay: expected false")
	}
	clk.Advance(500 * time.Millisecond)
	if got := applyDelay(true, st, delay, clk.Now()); got {
		t.Errorf("500ms in: expected false")
	}
	clk.Advance(600 * time.Millisecond)
	if got := applyDelay(true, st, delay, clk.Now()); !got {
		t.Errorf("after delay: expected true")
	}
	// Falling: immediate.
	if got := applyDelay(false, st, delay, clk.Now()); got {
		t.Errorf("falling: expected immediate false")
	}
}

func TestModifier_Duration(t *testing.T) {
	clk := NewFakeClock()
	st := &switchState{}
	dur := 1 * time.Second

	// Rising: starts duration window.
	if got := applyDuration(true, st, dur, clk.Now()); !got {
		t.Errorf("duration start: expected true")
	}
	clk.Advance(500 * time.Millisecond)
	if got := applyDuration(true, st, dur, clk.Now()); !got {
		t.Errorf("mid duration: expected true")
	}
	// Past duration: forced false.
	clk.Advance(600 * time.Millisecond)
	if got := applyDuration(true, st, dur, clk.Now()); got {
		t.Errorf("past duration with raw still true: expected false (lockout)")
	}
	// Lockout: input still true, output stays false.
	clk.Advance(20 * time.Millisecond)
	if got := applyDuration(true, st, dur, clk.Now()); got {
		t.Errorf("locked out: expected false while input true")
	}
	// Input drops, lockout cleared.
	if got := applyDuration(false, st, dur, clk.Now()); got {
		t.Errorf("input dropped during lockout: expected false")
	}
	// New rising edge re-triggers normally.
	clk.Advance(20 * time.Millisecond)
	if got := applyDuration(true, st, dur, clk.Now()); !got {
		t.Errorf("new rising edge after lockout: expected true")
	}
}

func TestModifier_Duration_NaturalDeactivation(t *testing.T) {
	// If raw goes false during the duration window, no lockout.
	clk := NewFakeClock()
	st := &switchState{}
	dur := 2 * time.Second

	applyDuration(true, st, dur, clk.Now())
	clk.Advance(500 * time.Millisecond)
	if got := applyDuration(false, st, dur, clk.Now()); got {
		t.Errorf("raw fell mid-window: expected immediate false")
	}
	if st.durationLockout {
		t.Errorf("natural deactivation should not engage lockout")
	}
	// New rising edge works immediately.
	clk.Advance(20 * time.Millisecond)
	if got := applyDuration(true, st, dur, clk.Now()); !got {
		t.Errorf("new rising edge after natural deactivation: expected true")
	}
}
