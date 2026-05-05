package vfd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
