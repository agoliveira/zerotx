package main

import (
	"fmt"
	"sync/atomic"

	"github.com/agoliveira/zerotx/pi/daemon/internal/audio"
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
//
// Each Stack owns a goroutine that drains its CF processor's Audio
// channel and forwards events to the daemon's audio Player. The drain
// goroutine stops when the Stack is replaced (Stop is called on the
// outgoing Stack).
type Stack struct {
	Model    *model.ZeroTXModel
	Resolver *source.Resolver
	Engine   *logic.Engine
	CFP      *cf.Processor
	Mapper   *mapper.Mapper

	stopAudio chan struct{}
	audioDone chan struct{}
}

// BuildStack assembles a complete Stack for the given model. The resolver
// is wired to the joystick state (may be nil for "no joystick selected")
// and panel. The engine is then wired back into the resolver so logic
// switches can reference each other.
//
// If player is non-nil, BuildStack starts a goroutine that drains the
// CF processor's Audio channel and forwards events to the player. The
// drain runs until Stop is called on the returned Stack.
func BuildStack(m *model.ZeroTXModel, jsState source.JoystickState, pnl panel.Panel, player audio.Player) (*Stack, error) {
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

	s := &Stack{
		Model:    m,
		Resolver: resolver,
		Engine:   engine,
		CFP:      cfp,
		Mapper:   mp,
	}
	if player != nil {
		s.stopAudio = make(chan struct{})
		s.audioDone = make(chan struct{})
		go s.drainAudio(player)
	}
	return s, nil
}

// drainAudio forwards events from the CF processor's Audio channel to
// the player until Stop is called. The level is computed from the track
// name via audio.DefaultLevelFor; future per-CF priority overrides
// would plumb through here.
func (s *Stack) drainAudio(player audio.Player) {
	defer close(s.audioDone)
	for {
		select {
		case <-s.stopAudio:
			return
		case ev, ok := <-s.CFP.Audio:
			if !ok {
				return
			}
			player.Play(ev.Kind, ev.Name, audio.DefaultLevelFor(ev.Name))
		}
	}
}

// Stop signals the audio drain goroutine to exit and waits for it.
// Safe to call multiple times. Safe to call on a Stack built without
// a player.
func (s *Stack) Stop() {
	if s.stopAudio == nil {
		return
	}
	select {
	case <-s.stopAudio:
		// already closed
	default:
		close(s.stopAudio)
	}
	<-s.audioDone
}

// stackHolder wraps an atomic.Pointer[Stack] for clarity. nil means IDLE.
//
// Replacing the active stack stops the previous one's audio drain so
// goroutines don't leak across model swaps.
type stackHolder struct {
	p atomic.Pointer[Stack]
}

func (h *stackHolder) Load() *Stack { return h.p.Load() }

// Store atomically replaces the active stack and stops the previous
// one's audio drain (if any). Returns the previous stack so callers
// can perform any additional teardown.
func (h *stackHolder) Store(s *Stack) *Stack {
	prev := h.p.Swap(s)
	if prev != nil {
		prev.Stop()
	}
	return prev
}

func (h *stackHolder) IsReady() bool { return h.p.Load() != nil }
