package lcd

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
)

func TestNew_DispatchesByAddr(t *testing.T) {
	if _, ok := New("").(*NullDriver); !ok {
		t.Errorf("empty addr should give NullDriver")
	}
	if _, ok := New("log").(*LogDriver); !ok {
		t.Errorf("\"log\" addr should give LogDriver")
	}
	if _, ok := New("/dev/ttyACM2").(*HubDriver); !ok {
		t.Errorf("path addr should give HubDriver")
	}
}

func TestNullDriver_NoOp(t *testing.T) {
	d := &NullDriver{}
	if err := d.WriteLine(0, "x"); err != nil {
		t.Errorf("WriteLine: %v", err)
	}
	if err := d.Clear(); err != nil {
		t.Errorf("Clear: %v", err)
	}
	if err := d.Backlight(true); err != nil {
		t.Errorf("Backlight: %v", err)
	}
	if err := d.Cursor(CursorBlink); err != nil {
		t.Errorf("Cursor: %v", err)
	}
	if err := d.SetGeom(20, 2); err != nil {
		t.Errorf("SetGeom: %v", err)
	}
	if err := d.SetAddr(0x27); err != nil {
		t.Errorf("SetAddr: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// captureLogger records what LogDriver emits.
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

func TestLogDriver_DedupesIdenticalWriteLine(t *testing.T) {
	cap := &captureLogger{}
	d := &LogDriver{logf: cap.logf}
	if err := d.WriteLine(0, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteLine(0, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteLine(1, "world"); err != nil {
		t.Fatal(err)
	}
	got := cap.snapshot()
	if len(got) != 2 {
		t.Errorf("expected 2 lines (one deduped), got %d: %v", len(got), got)
	}
}

func TestLogDriver_AllCommandsLog(t *testing.T) {
	cap := &captureLogger{}
	d := &LogDriver{logf: cap.logf}
	_ = d.WriteLine(0, "row0")
	_ = d.Clear()
	_ = d.Backlight(true)
	_ = d.Backlight(false)
	_ = d.Cursor(CursorOn)
	_ = d.SetGeom(20, 4)
	_ = d.SetAddr(0x27)
	got := cap.snapshot()
	want := []string{
		`[lcd] line 0 "row0"`,
		`[lcd] clear`,
		`[lcd] backlight 1`,
		`[lcd] backlight 0`,
		`[lcd] cursor on`,
		`[lcd] geom 20 4`,
		`[lcd] addr 0x27`,
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

// fakeHub satisfies iohub.Client and records the lines that were
// sent so tests can assert the on-the-wire format.
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
func (h *fakeHub) OnEvent(_ iohub.EventHandler)  {}
func (h *fakeHub) Run(_ context.Context) error   { return nil }
func (h *fakeHub) Close() error                  { return nil }

func (h *fakeHub) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.sent))
	copy(out, h.sent)
	return out
}

func TestHubDriver_Wireshape(t *testing.T) {
	h := &fakeHub{}
	d := &HubDriver{hub: h}

	_ = d.WriteLine(0, "hello")
	_ = d.WriteLine(1, "world")
	_ = d.Clear()
	_ = d.Backlight(true)
	_ = d.Backlight(false)
	_ = d.Cursor(CursorBlink)
	_ = d.SetGeom(20, 4)
	_ = d.SetAddr(0x3F)

	got := h.snapshot()
	want := []string{
		"SET lcd.0 line 0 hello",
		"SET lcd.0 line 1 world",
		"SET lcd.0 clear",
		"SET lcd.0 backlight 1",
		"SET lcd.0 backlight 0",
		"SET lcd.0 cursor blink",
		"SET lcd.0 geom 20 4",
		"SET lcd.0 addr 0x3F",
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

func TestHubDriver_RangeChecks(t *testing.T) {
	h := &fakeHub{}
	d := &HubDriver{hub: h}

	if err := d.SetGeom(0, 0); err == nil {
		t.Errorf("SetGeom(0,0) should error")
	}
	if err := d.SetGeom(64, 4); err == nil {
		t.Errorf("SetGeom(64,4) should error")
	}
	if err := d.SetAddr(0x07); err == nil {
		t.Errorf("SetAddr(0x07) should error (below 0x08)")
	}
	if err := d.SetAddr(0x80); err == nil {
		t.Errorf("SetAddr(0x80) should error (above 0x77)")
	}
	if err := d.Cursor("invalid"); err == nil {
		t.Errorf("Cursor(\"invalid\") should error")
	}
	// Valid SetAddr should succeed.
	if err := d.SetAddr(0x27); err != nil {
		t.Errorf("SetAddr(0x27): %v", err)
	}
}

func TestHubDriver_NewlineInWriteLineSanitized(t *testing.T) {
	h := &fakeHub{}
	d := &HubDriver{hub: h}
	if err := d.WriteLine(0, "a\nb\rc"); err != nil {
		t.Fatal(err)
	}
	got := h.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 send, got %d", len(got))
	}
	if got[0] != "SET lcd.0 line 0 a b c" {
		t.Errorf("newlines not sanitized: %q", got[0])
	}
}
