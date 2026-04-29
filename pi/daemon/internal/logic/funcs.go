package logic

import (
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// switchState holds the per-L# mutable state needed across ticks.
// Allocated once per logical switch; functions read/write the fields
// they care about.
type switchState struct {
	// STICKY: SR latch + previous V1/V2 levels for edge detection.
	stickyLatched bool
	prevV1        bool
	prevV2        bool

	// EDGE: tracks when V1 went high, whether the pulse fired this
	// activation cycle (one-shot), and whether V1 was active in the
	// previous tick.
	edgeV1Active   bool
	edgeRoseAt     time.Time
	edgePulse      bool // true for the single tick the pulse fires
	edgeFiredCycle bool // already fired this V1 cycle, suppress until V1 drops

	// TIMER: phase tracking (on or off) and when the current phase
	// started. timerInit gates first-tick initialization.
	timerInit  bool
	timerOn    bool
	timerStart time.Time

	// DIFFEGREATER / ADIFFEGREATER: previous source value to compute delta.
	hasPrevValue bool
	prevValue    float64

	// Modifier state: delay, duration, duration lockout (the EdgeTX quirk).
	delayActive     bool
	delayStart      time.Time
	durationActive  bool
	durationStart   time.Time
	durationLockout bool
}

// evalFunc is the signature every function evaluator implements. now is
// the engine's clock reading; r is the resolver wired to the engine's
// previous output bitmap.
type evalFunc func(d parsedDef, st *switchState, r *source.Resolver, now time.Time) bool

// funcRegistry maps EdgeTX YAML enum values to evaluators.
var funcRegistry = map[string]evalFunc{
	"FUNC_VEQUAL":        evalVEQUAL,
	"FUNC_VALMOSTEQUAL":  evalVALMOSTEQUAL,
	"FUNC_VPOS":          evalVPOS,
	"FUNC_VNEG":          evalVNEG,
	"FUNC_APOS":          evalAPOS,
	"FUNC_ANEG":          evalANEG,
	"FUNC_AND":           evalAND,
	"FUNC_OR":            evalOR,
	"FUNC_XOR":           evalXOR,
	"FUNC_GREATER":       evalGREATER,
	"FUNC_LESS":          evalLESS,
	"FUNC_EDGE":          evalEDGE,
	"FUNC_STICKY":        evalSTICKY,
	"FUNC_TIMER":         evalTIMER,
	"FUNC_DIFFEGREATER":  evalDIFFEGREATER,
	"FUNC_ADIFFEGREATER": evalADIFFEGREATER,
}

// Tolerance for VEQUAL comparison (value vs constant). With normalized
// [-1, 1] floats, integer-valued sources like switches need a small
// epsilon to handle internal float arithmetic. 0.005 = 0.5% which
// distinguishes adjacent switch positions easily.
const vequalEpsilon = 0.005

// Tolerance for VALMOSTEQUAL. EdgeTX's "almost equal" is a wider band.
const valmostequalEpsilon = 0.05

// --- Value comparison functions ---

func evalVEQUAL(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v, ok := r.ResolveValue(d.op1)
	if !ok {
		return false
	}
	return abs(v-d.c1) < vequalEpsilon
}

func evalVALMOSTEQUAL(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v, ok := r.ResolveValue(d.op1)
	if !ok {
		return false
	}
	return abs(v-d.c1) < valmostequalEpsilon
}

func evalVPOS(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v, ok := r.ResolveValue(d.op1)
	if !ok {
		return false
	}
	return v > d.c1
}

func evalVNEG(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v, ok := r.ResolveValue(d.op1)
	if !ok {
		return false
	}
	return v < d.c1
}

func evalAPOS(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v, ok := r.ResolveValue(d.op1)
	if !ok {
		return false
	}
	return abs(v) > d.c1
}

func evalANEG(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v, ok := r.ResolveValue(d.op1)
	if !ok {
		return false
	}
	return abs(v) < d.c1
}

// --- Boolean combinators ---

func evalAND(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	// FUNC_AND with NONE,NONE operands is a placeholder used to expose
	// just the andsw modifier (see x-tudo L19, L20, L21). Treat empty
	// operands as "true" so the andsw alone can drive output. EdgeTX
	// behaves the same way.
	v1 := operandTruthy(d.op1, r, true)
	v2 := operandTruthy(d.op2, r, true)
	return v1 && v2
}

func evalOR(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v1 := operandTruthy(d.op1, r, false)
	v2 := operandTruthy(d.op2, r, false)
	return v1 || v2
}

func evalXOR(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v1 := operandTruthy(d.op1, r, false)
	v2 := operandTruthy(d.op2, r, false)
	return v1 != v2
}

// operandTruthy resolves an operand as a boolean. NONE/empty operand
// returns the supplied default (true for AND so empty operand is identity,
// false for OR so empty doesn't override).
func operandTruthy(name string, r *source.Resolver, defaultIfEmpty bool) bool {
	if name == "" || name == "NONE" || name == "---" || name == "--" {
		return defaultIfEmpty
	}
	b, ok := r.ResolveBool(name)
	if !ok {
		return false
	}
	return b
}

// --- Source comparison ---

func evalGREATER(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v1, ok1 := r.ResolveValue(d.op1)
	v2, ok2 := r.ResolveValue(d.op2)
	if !ok1 || !ok2 {
		return false
	}
	return v1 > v2
}

func evalLESS(d parsedDef, _ *switchState, r *source.Resolver, _ time.Time) bool {
	v1, ok1 := r.ResolveValue(d.op1)
	v2, ok2 := r.ResolveValue(d.op2)
	if !ok1 || !ok2 {
		return false
	}
	return v1 < v2
}

// --- Edge: held T1..T2 then released ---

// evalEDGE implements FUNC_EDGE.
//
// Behaviors based on T2 spec:
//
//	numeric T2:  fire one tick when V1 deactivates, IF activation duration
//	             is in [T1, T2]. Outside the window: no pulse.
//	"<" (immed): fire one tick the moment elapsed reaches T1, while V1
//	             is still active. Re-arm only when V1 drops.
//	"-" (unbnd): fire one tick when V1 deactivates, IF activation duration
//	             was at least T1. No upper bound.
//
// Once a V1 cycle has produced a pulse (or is past the T2 window without
// firing), no further pulse until V1 drops and rises again.
func evalEDGE(d parsedDef, st *switchState, r *source.Resolver, now time.Time) bool {
	v1 := operandTruthy(d.op1, r, false)

	rising := v1 && !st.edgeV1Active
	falling := !v1 && st.edgeV1Active

	if rising {
		st.edgeRoseAt = now
		st.edgeFiredCycle = false
	}

	pulse := false
	if v1 {
		elapsed := now.Sub(st.edgeRoseAt)
		// "<" mode: fire when T1 reached, regardless of V1 still high.
		if d.edgeT2Spec == edgeT2Immediate && !st.edgeFiredCycle && elapsed >= d.edgeT1 {
			pulse = true
			st.edgeFiredCycle = true
		}
	}

	if falling {
		elapsed := now.Sub(st.edgeRoseAt)
		switch d.edgeT2Spec {
		case edgeT2Numeric:
			if !st.edgeFiredCycle && elapsed >= d.edgeT1 && elapsed <= d.edgeT2 {
				pulse = true
			}
		case edgeT2Unbounded:
			if !st.edgeFiredCycle && elapsed >= d.edgeT1 {
				pulse = true
			}
		case edgeT2Immediate:
			// Already fired (or armed for next cycle). Falling does nothing.
		}
		st.edgeFiredCycle = false // re-arm for next V1 rise
	}

	st.edgeV1Active = v1
	return pulse
}

// --- Sticky: SR latch on rising edges of V1 (set) and V2 (reset) ---

// evalSTICKY implements FUNC_STICKY.
//
// Behavior matches EdgeTX: V1 rising edge sets the latch, V2 rising edge
// resets it. Both V1 and V2 are evaluated as booleans every tick, but
// only their false→true transitions affect the latch. The latch persists
// across V1/V2 returning false. If both edges occur in the same tick,
// reset wins.
//
// AND switch is NOT applied here; modifiers handle it. Latch state is
// independent of AND, matching EdgeTX semantics (a documented quirk that
// users must understand).
func evalSTICKY(d parsedDef, st *switchState, r *source.Resolver, _ time.Time) bool {
	v1 := operandTruthy(d.op1, r, false)
	v2 := operandTruthy(d.op2, r, false)

	v1Edge := v1 && !st.prevV1
	v2Edge := v2 && !st.prevV2

	if v1Edge {
		st.stickyLatched = true
	}
	if v2Edge {
		st.stickyLatched = false // reset wins
	}

	st.prevV1 = v1
	st.prevV2 = v2
	return st.stickyLatched
}

// --- Timer: repeating on/off cycle ---

// evalTIMER implements FUNC_TIMER. Outputs true for op1 deciseconds, then
// false for op2 deciseconds, repeating. Always-running (no enable input).
func evalTIMER(d parsedDef, st *switchState, _ *source.Resolver, now time.Time) bool {
	if !st.timerInit {
		st.timerInit = true
		st.timerStart = now
		st.timerOn = true
		return true
	}

	elapsed := now.Sub(st.timerStart)
	if st.timerOn {
		if elapsed >= d.timerOn {
			st.timerOn = false
			st.timerStart = now
			return false
		}
		return true
	}
	if elapsed >= d.timerOff {
		st.timerOn = true
		st.timerStart = now
		return true
	}
	return false
}

// --- Delta: change between consecutive evaluations ---

// evalDIFFEGREATER fires when (currentValue - previousValue) >= constant.
// Signed: only positive deltas large enough trigger. The previous value
// is updated every tick. First tick after engine start always returns
// false (no baseline).
func evalDIFFEGREATER(d parsedDef, st *switchState, r *source.Resolver, _ time.Time) bool {
	v, ok := r.ResolveValue(d.op1)
	if !ok {
		return false
	}
	if !st.hasPrevValue {
		st.prevValue = v
		st.hasPrevValue = true
		return false
	}
	delta := v - st.prevValue
	st.prevValue = v
	return delta >= d.c1
}

// evalADIFFEGREATER fires when |currentValue - previousValue| >= constant.
func evalADIFFEGREATER(d parsedDef, st *switchState, r *source.Resolver, _ time.Time) bool {
	v, ok := r.ResolveValue(d.op1)
	if !ok {
		return false
	}
	if !st.hasPrevValue {
		st.prevValue = v
		st.hasPrevValue = true
		return false
	}
	delta := abs(v - st.prevValue)
	st.prevValue = v
	return delta >= d.c1
}

// --- helpers ---

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
