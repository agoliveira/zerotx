package logic

import (
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// applyModifiers wraps the raw function output with the andsw / delay /
// duration modifiers in EdgeTX's documented order:
//
//	function -> andsw gate -> delay (activation only) -> duration (max-on)
//
// AND switch:
//
//	Resolves a switch expression every tick. When false, the OUTPUT is
//	suppressed (forced false). The switch's internal state (sticky latch,
//	edge timer, etc) is unaffected — that's handled in the function
//	evaluator and is independent of AND. Matches EdgeTX behavior.
//
// Delay:
//
//	Activation-only. When the gated signal goes from false to true, hold
//	output false until `delay` has elapsed. Deactivation is immediate.
//
// Duration:
//
//	Max-on. When the delayed signal goes true, output stays true for at
//	most `duration`. After expiry, output forces false. EdgeTX has a
//	known lockout quirk: if the underlying condition is still true at
//	duration expiry, no re-trigger happens until that condition first
//	transitions to false. We replicate the quirk for compatibility.
func applyModifiers(raw bool, ls model.LogicalSwitch, st *switchState, r *source.Resolver, now time.Time) bool {
	gated := applyAndsw(raw, ls.Andsw, r)
	delayed := applyDelay(gated, st, dsToDuration(ls.Delay), now)
	final := applyDuration(delayed, st, dsToDuration(ls.Duration), now)
	return final
}

// applyAndsw gates the result with the andsw modifier. NONE/empty andsw
// means no gating.
func applyAndsw(in bool, andsw string, r *source.Resolver) bool {
	if andsw == "" || andsw == "NONE" || andsw == "---" || andsw == "--" {
		return in
	}
	gate, ok := r.ResolveBool(andsw)
	if !ok {
		// Unresolvable andsw: default false (safe / explicit).
		return false
	}
	return in && gate
}

// applyDelay holds the input false for the first `delay` after a rising
// edge. Falling edges propagate immediately.
func applyDelay(in bool, st *switchState, delay time.Duration, now time.Time) bool {
	if delay == 0 {
		st.delayActive = false
		return in
	}
	if !in {
		st.delayActive = false
		return false
	}
	// in is true.
	if !st.delayActive {
		st.delayActive = true
		st.delayStart = now
	}
	if now.Sub(st.delayStart) >= delay {
		return true
	}
	return false
}

// applyDuration limits how long the output stays true. After the timer
// expires, the EdgeTX lockout quirk requires the underlying condition
// to drop before the switch can re-trigger.
//
// Behavior is max-on: input drops within the window propagate immediately
// (deactivation is not held). Only timer-expiry-while-still-true engages
// the lockout.
func applyDuration(in bool, st *switchState, duration time.Duration, now time.Time) bool {
	if duration == 0 {
		st.durationActive = false
		st.durationLockout = false
		return in
	}

	// Lockout: ignore input until it goes false.
	if st.durationLockout {
		if !in {
			st.durationLockout = false
		}
		return false
	}

	// Currently within an active duration window.
	if st.durationActive {
		if now.Sub(st.durationStart) >= duration {
			st.durationActive = false
			if in {
				// Input still true at expiry: lockout until it drops.
				st.durationLockout = true
			}
			return false
		}
		if !in {
			// Natural deactivation within window: no lockout.
			st.durationActive = false
			return false
		}
		return true
	}

	// Not active: rising edge starts a new duration window.
	if in {
		st.durationActive = true
		st.durationStart = now
		return true
	}
	return false
}
