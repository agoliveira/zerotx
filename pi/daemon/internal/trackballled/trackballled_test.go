package trackballled

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/arm"
	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
	"github.com/agoliveira/zerotx/pi/daemon/internal/wxalert"
)

// fakeHub captures Send calls for assertion.
type fakeHub struct {
	mu    sync.Mutex
	lines []string
}

func (h *fakeHub) Send(line string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, line)
	return nil
}
func (h *fakeHub) OnEvent(_ iohub.EventHandler) {}
func (h *fakeHub) Run(_ context.Context) error  { return nil }
func (h *fakeHub) Close() error                 { return nil }

// fakeAlerts returns a fixed set on each Snapshot call.
type fakeAlerts struct {
	mu     sync.Mutex
	alerts []wxalert.Alert
}

func (f *fakeAlerts) Snapshot() []wxalert.Alert {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]wxalert.Alert, len(f.alerts))
	copy(out, f.alerts)
	return out
}
func (f *fakeAlerts) set(alerts []wxalert.Alert) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = alerts
}

func TestComputeState_Disarmed_NoAlerts_GreenSolid(t *testing.T) {
	hub := &fakeHub{}
	m := arm.New()
	alerts := &fakeAlerts{}
	d := New(hub, m, alerts)
	if got := d.computeState(); got != "green-solid" {
		t.Errorf("disarmed+no-alerts = %q, want green-solid", got)
	}
}

func TestComputeState_WarningAlert_RedSolid(t *testing.T) {
	hub := &fakeHub{}
	m := arm.New()
	alerts := &fakeAlerts{
		alerts: []wxalert.Alert{
			{Name: "wind", Severity: wxalert.SeverityWarning},
		},
	}
	d := New(hub, m, alerts)
	if got := d.computeState(); got != "red-solid" {
		t.Errorf("warning alert = %q, want red-solid", got)
	}
}

func TestComputeState_CriticalAlert_RedBlink_TrumpsWarning(t *testing.T) {
	hub := &fakeHub{}
	m := arm.New()
	alerts := &fakeAlerts{
		alerts: []wxalert.Alert{
			{Name: "shear", Severity: wxalert.SeverityWarning},
			{Name: "tornado", Severity: wxalert.SeverityCritical},
		},
	}
	d := New(hub, m, alerts)
	if got := d.computeState(); got != "red-blink" {
		t.Errorf("warning+critical = %q, want red-blink", got)
	}
}

func TestComputeState_NoticeAlert_NotElevated(t *testing.T) {
	hub := &fakeHub{}
	m := arm.New()
	alerts := &fakeAlerts{
		alerts: []wxalert.Alert{
			{Name: "info", Severity: wxalert.SeverityNotice},
		},
	}
	d := New(hub, m, alerts)
	// Notice doesn't trigger red; back to green-solid (disarmed).
	if got := d.computeState(); got != "green-solid" {
		t.Errorf("notice alert = %q, want green-solid", got)
	}
}

func TestComputeState_NilArm_TreatedAsDisarmed(t *testing.T) {
	hub := &fakeHub{}
	d := New(hub, nil, &fakeAlerts{})
	if got := d.computeState(); got != "green-solid" {
		t.Errorf("nil arm = %q, want green-solid", got)
	}
}

func TestComputeState_NilAlerts_OK(t *testing.T) {
	hub := &fakeHub{}
	m := arm.New()
	d := New(hub, m, nil)
	if got := d.computeState(); got != "green-solid" {
		t.Errorf("nil alerts = %q, want green-solid", got)
	}
}

func TestTick_OnlySendsOnChange(t *testing.T) {
	hub := &fakeHub{}
	m := arm.New()
	alerts := &fakeAlerts{}
	d := New(hub, m, alerts)
	// First tick should send.
	d.tick()
	// Second tick with no change should NOT send.
	d.tick()
	// Third tick after state change should send.
	alerts.set([]wxalert.Alert{{Severity: wxalert.SeverityWarning}})
	d.tick()

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.lines) != 2 {
		t.Fatalf("expected 2 sends; got %d: %v", len(hub.lines), hub.lines)
	}
	if hub.lines[0] != "SET led.trackball green-solid" {
		t.Errorf("first send = %q", hub.lines[0])
	}
	if hub.lines[1] != "SET led.trackball red-solid" {
		t.Errorf("second send = %q", hub.lines[1])
	}
}

func TestRun_SendsOffAtStart(t *testing.T) {
	hub := &fakeHub{}
	m := arm.New()
	d := New(hub, m, &fakeAlerts{})
	d.SetPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	// Give it a moment to issue the startup off and at least one tick.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.lines) < 2 {
		t.Fatalf("expected at least 2 sends (off + initial tick); got %d: %v",
			len(hub.lines), hub.lines)
	}
	if hub.lines[0] != "SET led.trackball off" {
		t.Errorf("first send = %q, want SET led.trackball off", hub.lines[0])
	}
	// Last send should also be off (cleanup on shutdown).
	last := hub.lines[len(hub.lines)-1]
	if last != "SET led.trackball off" {
		t.Errorf("last send = %q, want SET led.trackball off", last)
	}
}
