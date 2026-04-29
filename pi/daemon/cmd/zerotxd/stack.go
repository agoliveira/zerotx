package main

import (
	"fmt"
	"sync/atomic"

	"github.com/agoliveira/zerotx/pi/daemon/internal/cf"
	"github.com/agoliveira/zerotx/pi/daemon/internal/logic"
	"github.com/agoliveira/zerotx/pi/daemon/internal/mapper"
	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/panel"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// Stack is the per-model bundle of resolver, engine, CF processor and
// mapper. They share state through pointers, so they must be constructed
// together and replaced together. The tick loop reads the active Stack
// via atomic.Pointer for lock-free swapping.
//
// IDLE = no Stack stored. The tick loop sees nil and skips emission.
// READY = Stack stored. Tick loop ticks the engine and emits CRSF.
type Stack struct {
	Model    *model.ZeroTXModel
	Resolver *source.Resolver
	Engine   *logic.Engine
	CFP      *cf.Processor
	Mapper   *mapper.Mapper
}

// BuildStack assembles a complete Stack for the given model. The resolver
// is wired to the joystick state (may be nil for "no joystick selected")
// and panel. The engine is then wired back into the resolver so logic
// switches can reference each other.
func BuildStack(m *model.ZeroTXModel, jsState source.JoystickState, pnl panel.Panel) (*Stack, error) {
	if m == nil {
		return nil, fmt.Errorf("BuildStack: model is nil")
	}
	resolver := source.New(m, jsState, pnl, nil, nil)
	engine := logic.New(m, resolver, logic.RealClock{})
	resolver.Logic = engine
	cfp := cf.New(m, resolver)
	mp := mapper.New(m, resolver)
	mp.SetEngine(engine)
	mp.SetCFProcessor(cfp)
	return &Stack{
		Model:    m,
		Resolver: resolver,
		Engine:   engine,
		CFP:      cfp,
		Mapper:   mp,
	}, nil
}

// stackHolder wraps an atomic.Pointer[Stack] for clarity. nil means IDLE.
type stackHolder struct {
	p atomic.Pointer[Stack]
}

func (h *stackHolder) Load() *Stack       { return h.p.Load() }
func (h *stackHolder) Store(s *Stack)     { h.p.Store(s) }
func (h *stackHolder) IsReady() bool      { return h.p.Load() != nil }
