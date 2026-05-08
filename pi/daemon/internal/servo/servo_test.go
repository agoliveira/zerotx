package servo

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
)

func TestNew_DispatchesByAddr(t *testing.T) {
	if _, ok := New("").(*NullController); !ok {
		t.Errorf("empty addr should give NullController")
	}
	if _, ok := New("log").(*LogController); !ok {
		t.Errorf("\"log\" addr should give LogController")
	}
	if _, ok := New("/dev/ttyACM2").(*HubController); !ok {
		t.Errorf("path addr should give HubController")
	}
}

func TestNullController_NoOp(t *testing.T) {
	c := &NullController{}
	if err := c.Angle(0, 90); err != nil {
		t.Errorf("Angle: %v", err)
	}
	if err := c.Microseconds(0, 1500); err != nil {
		t.Errorf("Microseconds: %v", err)
	}
	if err := c.Detach(0); err != nil {
		t.Errorf("Detach: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestController_InstanceValidation(t *testing.T) {
	c := &NullController{}
	if err := c.Angle(Count, 90); err == nil {
		t.Errorf("Angle on instance=Count should error")
	}
	if err := c.Microseconds(Count+5, 1500); err == nil {
		t.Errorf("Microseconds on out-of-range instance should error")
	}
	if err := c.Detach(Count); err == nil {
		t.Errorf("Detach on instance=Count should error")
	}
}

func TestController_RangeChecks(t *testing.T) {
	c := &LogController{logf: func(string, ...interface{}) {}}

	if err := c.Angle(0, -1); err == nil {
		t.Errorf("Angle(-1) should error")
	}
	if err := c.Angle(0, 181); err == nil {
		t.Errorf("Angle(181) should error")
	}
	if err := c.Microseconds(0, 499); err == nil {
		t.Errorf("Microseconds(499) should error")
	}
	if err := c.Microseconds(0, 2501); err == nil {
		t.Errorf("Microseconds(2501) should error")
	}

	// Boundary values should succeed.
	if err := c.Angle(0, 0); err != nil {
		t.Errorf("Angle(0): %v", err)
	}
	if err := c.Angle(0, 180); err != nil {
		t.Errorf("Angle(180): %v", err)
	}
	if err := c.Microseconds(0, 500); err != nil {
		t.Errorf("Microseconds(500): %v", err)
	}
	if err := c.Microseconds(0, 2500); err != nil {
		t.Errorf("Microseconds(2500): %v", err)
	}
}

// captureLogger records what LogController emits.
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLogger) logf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *captureLogger) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

func TestLogController_AllCommandsLog(t *testing.T) {
	cap := &captureLogger{}
	c := &LogController{logf: cap.logf}
	_ = c.Angle(0, 90)
	_ = c.Microseconds(1, 1500)
	_ = c.Detach(2)
	got := cap.snapshot()
	want := []string{
		"[servo.0] angle 90",
		"[servo.1] us 1500",
		"[servo.2] detach",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d log lines, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d: got %q, want %q", i, got[i], w)
		}
	}
}

// fakeHub satisfies iohub.Client.
type fakeHub struct {
	mu   sync.Mutex
	sent []string
}

func (h *fakeHub) Send(line string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, line)
	return nil
}
func (h *fakeHub) OnEvent(_ iohub.EventHandler) {}
func (h *fakeHub) Run(_ context.Context) error  { return nil }
func (h *fakeHub) Close() error                 { return nil }
func (h *fakeHub) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.sent))
	copy(out, h.sent)
	return out
}

func TestHubController_Wireshape(t *testing.T) {
	h := &fakeHub{}
	c := &HubController{hub: h}

	_ = c.Angle(0, 90)
	_ = c.Angle(3, 0)
	_ = c.Microseconds(1, 1500)
	_ = c.Microseconds(2, 2500)
	_ = c.Detach(3)

	got := h.snapshot()
	want := []string{
		"SET servo.0 angle 90",
		"SET servo.3 angle 0",
		"SET servo.1 us 1500",
		"SET servo.2 us 2500",
		"SET servo.3 detach",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sends, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("send %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestHubController_OutOfRangeNoSend(t *testing.T) {
	h := &fakeHub{}
	c := &HubController{hub: h}
	_ = c.Angle(0, 200)
	_ = c.Microseconds(0, 100)
	_ = c.Angle(Count, 90)
	if got := h.snapshot(); len(got) != 0 {
		t.Errorf("expected no sends on validation failure, got: %v", got)
	}
}
