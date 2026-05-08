package vfd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
	"github.com/agoliveira/zerotx/pi/daemon/internal/logbuf"
)

func TestPadOrTruncate(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "                    "},        // 20 spaces
		{"short", "short               "},   // padded
		{"exactly twenty chars", "exactly twenty chars"},
		{"this is way too long for the row", "this is way too long"},
	}
	for _, c := range cases {
		if got := padOrTruncate(c.in); got != c.want {
			t.Errorf("padOrTruncate(%q) = %q (len=%d), want %q",
				c.in, got, len(got), c.want)
		}
	}
}

func TestStripTimestamp(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			"2026/05/02 15:50:25.481 crsftee: listening on 127.0.0.1",
			"crsftee: listening on 127.0.0.1",
		},
		{
			"2026/05/02 15:50:25 ipc: handshake OK",
			"ipc: handshake OK",
		},
		{
			"no timestamp here",
			"no timestamp here",
		},
		{
			"",
			"",
		},
	}
	for _, c := range cases {
		if got := stripTimestamp(c.in); got != c.want {
			t.Errorf("stripTimestamp(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatLine(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			"2026/05/02 15:50:25.481 crsftee: listening on 127.0.0.1:5761",
			"crsftee: listening o",
		},
		{
			"2026/05/02 15:51:43.414 fc-ready: mode=\"ANGL\" ready=true",
			"fc-ready: mode=\"ANGL",
		},
		{
			"short msg",
			"short msg",
		},
		{
			"   ",
			"",
		},
		{
			"",
			"",
		},
		// Self-output from LogDriver should be filtered to break
		// the feedback loop (LogDriver -> log -> logbuf -> firehose).
		{
			"[vfd] |crsftee: listening|",
			"",
		},
		{
			"2026/05/02 15:50:25.481 [vfd] |something|",
			"",
		},
	}
	for _, c := range cases {
		if got := FormatLine(c.in); got != c.want {
			t.Errorf("FormatLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNullDriver_NoOp(t *testing.T) {
	d := &NullDriver{}
	if err := d.WriteLines("a", "b"); err != nil {
		t.Errorf("WriteLines: %v", err)
	}
	if err := d.Clear(); err != nil {
		t.Errorf("Clear: %v", err)
	}
	if err := d.Brightness(2); err != nil {
		t.Errorf("Brightness: %v", err)
	}
	if err := d.Event("tick", "1"); err != nil {
		t.Errorf("Event: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestLogDriver_EventFormat(t *testing.T) {
	cap := &captureLogger{}
	d := &LogDriver{logf: cap.logf}

	if err := d.Event("tick"); err != nil {
		t.Fatal(err)
	}
	if err := d.Event("arm", "1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Event("mode", "ANGL"); err != nil {
		t.Fatal(err)
	}

	got := cap.snapshot()
	want := []string{
		"[vfd] E tick",
		"[vfd] E arm 1",
		"[vfd] E mode ANGL",
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

// captureLogger lets us assert what LogDriver emitted.
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

func TestLogDriver_DedupesIdenticalWrites(t *testing.T) {
	cap := &captureLogger{}
	d := &LogDriver{logf: cap.logf}

	if err := d.WriteLines("first", "second"); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteLines("first", "second"); err != nil {
		t.Fatal(err)
	}
	got := cap.snapshot()
	// Two log lines from first call; second is deduped.
	if len(got) != 2 {
		t.Errorf("expected 2 log lines, got %d: %v", len(got), got)
	}
}

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

func TestFirehose_ScrollsLogEntries(t *testing.T) {
	buf := logbuf.New(100)
	cap := &captureLogger{}
	driver := &LogDriver{logf: cap.logf}
	fh := NewFirehose(driver, buf)

	// Write two log lines BEFORE starting the firehose so they
	// land in the buffer with timestamps the firehose will pick
	// up on its first poll.
	buf.Write([]byte("first event\n"))
	time.Sleep(2 * time.Millisecond) // ensure distinct timestamps
	buf.Write([]byte("second event\n"))

	// The firehose tracks 'since' starting at NewFirehose call
	// time, so writes before Run won't appear. Wait a tick after
	// Run starts and write more.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = fh.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	buf.Write([]byte("third event\n"))
	time.Sleep(time.Second/PollHz + 50*time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	lines := cap.snapshot()
	// Each WriteLines emits 2 lines. Expect at least 1 entry to
	// have been delivered.
	if len(lines) < 2 {
		t.Errorf("expected scrolled output, got %d lines: %v", len(lines), lines)
	}
	// The third event should have been formatted into the bottom
	// row at some point.
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "third event") {
		t.Errorf("expected 'third event' in output, got:\n%s", joined)
	}
}

func TestFirehose_NilDriverIsSafe(t *testing.T) {
	fh := NewFirehose(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fh.Run(ctx); err != nil {
		t.Errorf("Run with nil driver: %v", err)
	}
}

// fakeHub satisfies iohub.Client and records sent lines.
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

func TestHubDriver_TargetsByInstance(t *testing.T) {
	h0 := &fakeHub{}
	h1 := &fakeHub{}
	d0 := NewInstanceWithHub(h0, 0)
	d1 := NewInstanceWithHub(h1, 1)

	if err := d0.WriteLines("hello", "world"); err != nil {
		t.Fatal(err)
	}
	if err := d1.WriteLines("foo", "bar"); err != nil {
		t.Fatal(err)
	}
	if err := d0.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := d1.Brightness(2); err != nil {
		t.Fatal(err)
	}
	if err := d0.Event("tick"); err != nil {
		t.Fatal(err)
	}
	if err := d1.Event("arm", "1"); err != nil {
		t.Fatal(err)
	}

	got0 := h0.snapshot()
	want0 := []string{
		"SET vfd.0 line 0 hello               ",
		"SET vfd.0 line 1 world               ",
		"SET vfd.0 clear",
		"SET vfd.0 tick",
	}
	if len(got0) != len(want0) {
		t.Fatalf("d0: got %d sends, want %d: %v", len(got0), len(want0), got0)
	}
	for i, w := range want0 {
		if got0[i] != w {
			t.Errorf("d0 send %d: got %q, want %q", i, got0[i], w)
		}
	}

	got1 := h1.snapshot()
	want1 := []string{
		"SET vfd.1 line 0 foo                 ",
		"SET vfd.1 line 1 bar                 ",
		"SET vfd.1 brightness 2",
		"SET vfd.1 arm 1",
	}
	if len(got1) != len(want1) {
		t.Fatalf("d1: got %d sends, want %d: %v", len(got1), len(want1), got1)
	}
	for i, w := range want1 {
		if got1[i] != w {
			t.Errorf("d1 send %d: got %q, want %q", i, got1[i], w)
		}
	}
}

func TestNewWithHub_DefaultsToInstance0(t *testing.T) {
	h := &fakeHub{}
	d := NewWithHub(h)
	if err := d.Clear(); err != nil {
		t.Fatal(err)
	}
	got := h.snapshot()
	if len(got) != 1 || got[0] != "SET vfd.0 clear" {
		t.Errorf("NewWithHub should target vfd.0; got %v", got)
	}
}
