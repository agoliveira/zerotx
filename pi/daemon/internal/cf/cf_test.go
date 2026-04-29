package cf

import (
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

type stubLogic struct{ state map[int]bool }

func (l *stubLogic) Logic(idx int) bool { return l.state[idx] }

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

func TestParseCF_OverrideChannel(t *testing.T) {
	pd := parseCF(model.CustomFunction{
		Func: "OVERRIDE_CHANNEL",
		Def:  "0,-100,1",
	})
	if pd.invalid {
		t.Fatalf("expected valid parse")
	}
	if pd.channel != 0 || pd.value != -1.0 || !pd.enabled {
		t.Errorf("got channel=%d value=%v enabled=%v", pd.channel, pd.value, pd.enabled)
	}
}

func TestParseCF_OverrideChannel_Disabled(t *testing.T) {
	pd := parseCF(model.CustomFunction{Func: "OVERRIDE_CHANNEL", Def: "5,75,0"})
	if pd.enabled {
		t.Errorf("enabled flag 0 should parse as enabled=false")
	}
	if pd.channel != 5 || pd.value != 0.75 {
		t.Errorf("got channel=%d value=%v", pd.channel, pd.value)
	}
}

func TestParseCF_PlayTrack_StripsNulls(t *testing.T) {
	pd := parseCF(model.CustomFunction{
		Func: "PLAY_TRACK",
		Def:  "armed\x00\x00\x00,1,1x",
	})
	if pd.name != "armed" {
		t.Errorf("name with nulls: got %q want %q", pd.name, "armed")
	}
}

// TestProcessor_OverrideChannel: !L3 -> OVERRIDE 0,-100,1
//
// While L3=false, override fires and CH0 should be forced to min.
// While L3=true, override doesn't fire and CH0 keeps its mixer value.
func TestProcessor_OverrideChannel(t *testing.T) {
	m := &model.ZeroTXModel{
		EdgeTX: model.EdgeTXModel{
			CustomFn: map[int]model.CustomFunction{
				0: {Swtch: "!L3", Func: "OVERRIDE_CHANNEL", Def: "0,-100,1"},
			},
		},
	}
	logic := &stubLogic{state: map[int]bool{3: false}}
	r := source.New(m, nil, nil, logic, nil)
	p := New(m, r)

	// L3=false -> !L3=true -> override fires.
	overrides := p.Tick()
	if len(overrides) != 1 {
		t.Fatalf("L3=false: expected 1 override, got %d", len(overrides))
	}
	if overrides[0].Channel != 0 || overrides[0].Value != -1.0 {
		t.Errorf("override mismatch: got %+v", overrides[0])
	}

	// L3 becomes true -> no override.
	logic.state[3] = true
	overrides = p.Tick()
	if len(overrides) != 0 {
		t.Errorf("L3=true: expected 0 overrides, got %d", len(overrides))
	}
}

// TestProcessor_DisabledOverrideSkipped checks the enabled=0 case.
func TestProcessor_DisabledOverrideSkipped(t *testing.T) {
	m := &model.ZeroTXModel{
		EdgeTX: model.EdgeTXModel{
			CustomFn: map[int]model.CustomFunction{
				0: {Swtch: "L1", Func: "OVERRIDE_CHANNEL", Def: "2,100,0"}, // enabled=0
			},
		},
	}
	logic := &stubLogic{state: map[int]bool{1: true}}
	r := source.New(m, nil, nil, logic, nil)
	p := New(m, r)

	overrides := p.Tick()
	if len(overrides) != 0 {
		t.Errorf("enabled=0 override should be skipped, got %d", len(overrides))
	}
}

// TestProcessor_PlayTrackEdgeTriggered confirms PLAY_TRACK fires once on
// rising edge, not while held.
func TestProcessor_PlayTrackEdgeTriggered(t *testing.T) {
	m := &model.ZeroTXModel{
		EdgeTX: model.EdgeTXModel{
			CustomFn: map[int]model.CustomFunction{
				0: {Swtch: "L1", Func: "PLAY_TRACK", Def: "armed\x00\x00\x00,1,1x"},
			},
		},
	}
	logic := &stubLogic{state: map[int]bool{1: false}}
	r := source.New(m, nil, nil, logic, nil)
	p := New(m, r)

	// L1 false: no event.
	p.Tick()
	if len(p.Audio) != 0 {
		t.Errorf("L1 false: expected no audio events")
	}

	// L1 rises -> emit one event.
	logic.state[1] = true
	p.Tick()
	if len(p.Audio) != 1 {
		t.Errorf("L1 rising edge: expected 1 audio event, got %d", len(p.Audio))
	}

	// L1 still true (no edge): no new event.
	p.Tick()
	if len(p.Audio) != 1 {
		t.Errorf("L1 held high: should not re-emit, got %d", len(p.Audio))
	}

	// L1 falls then rises again -> emit again.
	logic.state[1] = false
	p.Tick()
	logic.state[1] = true
	p.Tick()
	if len(p.Audio) != 2 {
		t.Errorf("after re-rise: expected 2 events total, got %d", len(p.Audio))
	}
}

// TestApplyOverrides_DrivesChannel verifies the override mutates the
// channel array correctly.
func TestApplyOverrides_DrivesChannel(t *testing.T) {
	var ch [ipc.Channels]uint16
	for i := range ch {
		ch[i] = ipc.CrsfChMid
	}
	overrides := []Override{
		{Channel: 0, Value: -1.0}, // CRSF min
		{Channel: 5, Value: 1.0},  // CRSF max
		{Channel: 99, Value: 0.5}, // out of range -> skipped
	}
	ApplyOverrides(&ch, overrides)

	if ch[0] != ipc.CrsfChMin {
		t.Errorf("CH0 override -1.0: got %d want %d", ch[0], ipc.CrsfChMin)
	}
	if ch[5] != ipc.CrsfChMax {
		t.Errorf("CH5 override +1.0: got %d want %d", ch[5], ipc.CrsfChMax)
	}
	// Other channels untouched.
	if ch[1] != ipc.CrsfChMid {
		t.Errorf("CH1 not overridden: got %d want %d", ch[1], ipc.CrsfChMid)
	}
}
