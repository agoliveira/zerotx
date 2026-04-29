// Package mapper resolves a ZeroTX model's mix table to a CRSF channel
// array, ticking the logic engine and applying custom-function overrides
// in the process.
//
// Mapper.Resolve() is the single per-tick entrypoint:
//
//  1. Tick the logic engine (computes new logical-switch outputs).
//  2. For each entry in EdgeTX.MixData: resolve the source name via
//     source.Resolver, scale by the mix weight, place into the channel
//     array. Multiple mix entries for the same destCh accumulate (ADD
//     mode); REPL/MULTIPLY are not yet implemented and behave as ADD.
//  3. Tick the CF processor and apply OVERRIDE_CHANNEL actions to the
//     final channel array.
//
// Source-name resolution lives entirely in source.Resolver — the mapper
// no longer parses I0/Thr/SE/L3 etc. It just asks the resolver.
package mapper

import (
	"github.com/agoliveira/zerotx/pi/daemon/internal/cf"
	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/logic"
	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// Mapper drives the per-tick channel-resolution pipeline.
type Mapper struct {
	model    *model.ZeroTXModel
	resolver *source.Resolver
	engine   *logic.Engine // optional
	cfp      *cf.Processor // optional
}

// New constructs a Mapper. m and r may both be nil; in that case Resolve
// returns SafeDefaults every call.
func New(m *model.ZeroTXModel, r *source.Resolver) *Mapper {
	return &Mapper{model: m, resolver: r}
}

// SetEngine attaches a logic engine. The engine is ticked at the start
// of each Resolve call.
func (mp *Mapper) SetEngine(e *logic.Engine) { mp.engine = e }

// SetCFProcessor attaches a CF processor whose Tick output is applied
// after mix evaluation.
func (mp *Mapper) SetCFProcessor(p *cf.Processor) { mp.cfp = p }

// SafeDefaults returns the default channel array for when no model is
// loaded or a binding can't be resolved. Sticks centered, throttle and
// arm channels at min — fail-safe values.
func SafeDefaults() [ipc.Channels]uint16 {
	var ch [ipc.Channels]uint16
	for i := range ch {
		ch[i] = ipc.CrsfChMid
	}
	ch[2] = ipc.CrsfChMin // throttle slot (legacy mid-range default)
	ch[4] = ipc.CrsfChMin // arm channel default-low
	return ch
}

// Resolve produces the channel array for this tick.
func (mp *Mapper) Resolve() [ipc.Channels]uint16 {
	ch := SafeDefaults()
	if mp.model == nil || mp.resolver == nil {
		return ch
	}

	// Tick the logic engine first so logic-switch references in mixes
	// see fresh state. (The engine internally double-buffers, so reads
	// during this Resolve see the previous tick — see logic.Engine docs.)
	if mp.engine != nil {
		mp.engine.Tick()
	}

	// Walk the mix table.
	for _, mix := range mp.model.EdgeTX.MixData {
		if mix.DestCh < 0 || mix.DestCh >= ipc.Channels {
			continue
		}
		val, ok := mp.resolver.ResolveValue(mix.SrcRaw)
		if !ok {
			// Unresolved source: leave channel at safe default.
			continue
		}
		val = val * float64(mix.Weight) / 100.0
		if val > 1.0 {
			val = 1.0
		}
		if val < -1.0 {
			val = -1.0
		}
		ch[mix.DestCh] = normToCRSF(val)
	}

	// Apply custom-function overrides (the safety override path).
	if mp.cfp != nil {
		overrides := mp.cfp.Tick()
		cf.ApplyOverrides(&ch, overrides)
	}

	return ch
}

func normToCRSF(v float64) uint16 {
	if v > 1 {
		v = 1
	}
	if v < -1 {
		v = -1
	}
	span := float64(ipc.CrsfChMax) - float64(ipc.CrsfChMin)
	return uint16(float64(ipc.CrsfChMin) + (v+1.0)*0.5*span + 0.5)
}
