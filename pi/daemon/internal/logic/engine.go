package logic

import (
	"log"
	"sync"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// Engine evaluates all defined logical switches once per Tick. It owns
// the per-switch state and the previous-tick output bitmap. It satisfies
// source.LogicState so the resolver can read prev-tick values during
// L→L evaluation.
//
// The engine is safe for concurrent reads of Logic() while Tick() is
// running; the bitmap swap is atomic at the end of Tick.
type Engine struct {
	model    *model.ZeroTXModel
	resolver *source.Resolver
	clock    Clock

	count    int
	parsed   []parsedDef
	defined  []bool // true if logicalSw[i] has a non-NONE func
	states   []switchState
	unknown  []bool // logged-already flag for unknown function names

	mu     sync.RWMutex
	output []bool
}

// New constructs an Engine. The resolver's Logic field is left untouched;
// the caller must wire engine to resolver after construction:
//
//	eng := logic.New(model, resolver, logic.RealClock{})
//	resolver.Logic = eng
//
// (We do this in two steps because the resolver is constructed first and
// passed in, while the engine implements the LogicState interface that
// the resolver consumes.)
func New(m *model.ZeroTXModel, r *source.Resolver, clk Clock) *Engine {
	if clk == nil {
		clk = RealClock{}
	}
	n := 0
	if m != nil {
		n = len(m.EdgeTX.LogicalSw)
	}
	e := &Engine{
		model:    m,
		resolver: r,
		clock:    clk,
		count:    n,
		parsed:   make([]parsedDef, n),
		defined:  make([]bool, n),
		states:   make([]switchState, n),
		unknown:  make([]bool, n),
		output:   make([]bool, n),
	}
	if m != nil {
		for i := 0; i < n; i++ {
			ls := m.EdgeTX.LogicalSw[i]
			if ls.Func == "" || ls.Func == "FUNC_NONE" {
				continue
			}
			e.defined[i] = true
			e.parsed[i] = parseDef(ls)
		}
	}
	return e
}

// Tick evaluates every defined logical switch and atomically swaps the
// output bitmap. Read-side calls to Logic() during a Tick see the
// previous tick's results until Tick returns.
func (e *Engine) Tick() {
	if e.model == nil || e.count == 0 {
		return
	}
	now := e.clock.Now()
	next := make([]bool, e.count)

	for i := 0; i < e.count; i++ {
		if !e.defined[i] {
			next[i] = false
			continue
		}
		if e.parsed[i].invalid {
			next[i] = false
			continue
		}
		ls := e.model.EdgeTX.LogicalSw[i]
		fn, ok := funcRegistry[ls.Func]
		if !ok {
			if !e.unknown[i] {
				log.Printf("logic: L%d uses unsupported function %q (treating as false)", i+1, ls.Func)
				e.unknown[i] = true
			}
			next[i] = false
			continue
		}
		raw := fn(e.parsed[i], &e.states[i], e.resolver, now)
		next[i] = applyModifiers(raw, ls, &e.states[i], e.resolver, now)
	}

	e.mu.Lock()
	e.output = next
	e.mu.Unlock()
}

// Logic implements source.LogicState. idx is 1-indexed (EdgeTX
// convention: L1 is the first switch). Out-of-range queries return false.
func (e *Engine) Logic(idx int) bool {
	if idx < 1 || idx > e.count {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.output[idx-1]
}

// Snapshot returns a copy of the current output bitmap. Useful for
// logging and tests.
func (e *Engine) Snapshot() []bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]bool, len(e.output))
	copy(out, e.output)
	return out
}
