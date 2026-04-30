package display

import (
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeTransport is a duplex in-memory transport for testing the
// Driver without a real serial port. The "write" side accumulates
// outgoing lines; the "read" side delivers lines we feed it. Models
// the same interface a serial port would.
type pipeTransport struct {
	mu          sync.Mutex
	written     strings.Builder // lines the daemon wrote
	readBuf     strings.Builder // lines the device "sent"
	readPos     int
	closed      bool
	readBlocked chan struct{} // signals new data available
}

func newPipe() *pipeTransport {
	return &pipeTransport{
		readBlocked: make(chan struct{}, 32),
	}
}

func (p *pipeTransport) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	p.written.Write(b)
	return len(b), nil
}

func (p *pipeTransport) Read(b []byte) (int, error) {
	for {
		p.mu.Lock()
		if p.closed && p.readPos >= p.readBuf.Len() {
			p.mu.Unlock()
			return 0, io.EOF
		}
		buf := p.readBuf.String()
		if p.readPos < len(buf) {
			n := copy(b, buf[p.readPos:])
			p.readPos += n
			p.mu.Unlock()
			return n, nil
		}
		p.mu.Unlock()
		// Block until something arrives or close.
		select {
		case <-p.readBlocked:
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (p *pipeTransport) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	close(p.readBlocked)
	return nil
}

// feedDevice pushes a line onto the read buffer (as if the device sent
// it). Adds the trailing newline for the caller.
func (p *pipeTransport) feedDevice(line string) {
	p.mu.Lock()
	if !p.closed {
		p.readBuf.WriteString(line)
		p.readBuf.WriteByte('\n')
	}
	p.mu.Unlock()
	select {
	case p.readBlocked <- struct{}{}:
	default:
	}
}

// daemonOutput returns what the daemon has written so far.
func (p *pipeTransport) daemonOutput() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.written.String()
}

// === Tests ===

func TestModeIsValid(t *testing.T) {
	cases := []struct {
		m    Mode
		want bool
	}{
		{ModeIdle, true},
		{ModePreflight, true},
		{ModeFlight, true},
		{ModeAlarm, true},
		{ModeRTH, true},
		{ModePostflight, true},
		{Mode("BOGUS"), false},
		{Mode(""), false},
	}
	for _, c := range cases {
		if got := c.m.IsValid(); got != c.want {
			t.Errorf("Mode(%q).IsValid() = %v, want %v", c.m, got, c.want)
		}
	}
}

// === Serializer ===

func TestSerializeStateEmpty(t *testing.T) {
	if got := serializeState(State{}); got != "" {
		t.Errorf("empty state should serialize to empty string, got %q", got)
	}
}

func TestSerializeStateAllFields(t *testing.T) {
	armed := true
	bat := 11.78
	batpct := 73
	alt := 124
	dist := 430
	spd := 22
	link := 87
	sats := 11
	timeSec := 145
	s := State{
		Armed:      &armed,
		BatV:       &bat,
		BatPct:     &batpct,
		AltM:       &alt,
		DistM:      &dist,
		SpdKmh:     &spd,
		LinkPct:    &link,
		Sats:       &sats,
		FlightMode: "ANGLE",
		GpsFix:     "3d",
		TimeSec:    &timeSec,
	}
	got := serializeState(s)
	want := "DISP STATE armed=1 bat=11.78 batpct=73 alt=124 dist=430 spd=22 link=87 sats=11 mode=ANGLE gps=3d time=145"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestSerializeStatePartial(t *testing.T) {
	bat := 11.7
	s := State{BatV: &bat, FlightMode: "ANGLE"}
	got := serializeState(s)
	want := "DISP STATE bat=11.70 mode=ANGLE"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// === mergeState ===

func TestMergeStatePreservesUnsetFields(t *testing.T) {
	bat := 11.7
	dst := State{BatV: &bat, FlightMode: "ANGLE"}
	alt := 100
	mergeState(&dst, State{AltM: &alt})
	if dst.BatV == nil || *dst.BatV != 11.7 {
		t.Errorf("BatV should be preserved, got %v", dst.BatV)
	}
	if dst.FlightMode != "ANGLE" {
		t.Errorf("FlightMode should be preserved, got %q", dst.FlightMode)
	}
	if dst.AltM == nil || *dst.AltM != 100 {
		t.Errorf("AltM should be 100, got %v", dst.AltM)
	}
}

func TestMergeStateOverwrites(t *testing.T) {
	bat1 := 11.7
	dst := State{BatV: &bat1}
	bat2 := 11.5
	mergeState(&dst, State{BatV: &bat2})
	if *dst.BatV != 11.5 {
		t.Errorf("BatV should be 11.5, got %v", *dst.BatV)
	}
}

// === parseEvent ===

func TestParseEventReady(t *testing.T) {
	ev, ok := parseEvent("DISP READY version=0.1.0 panels=2 w=128 h=32")
	if !ok {
		t.Fatal("parse failed")
	}
	if ev.Kind != "READY" {
		t.Errorf("Kind = %q", ev.Kind)
	}
	wantArgs := map[string]string{
		"version": "0.1.0",
		"panels":  "2",
		"w":       "128",
		"h":       "32",
	}
	if !reflect.DeepEqual(ev.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", ev.Args, wantArgs)
	}
}

func TestParseEventHeartbeat(t *testing.T) {
	ev, ok := parseEvent("DISP HEARTBEAT uptime=3247")
	if !ok {
		t.Fatal("parse failed")
	}
	if ev.Kind != "HEARTBEAT" {
		t.Errorf("Kind = %q", ev.Kind)
	}
	if ev.Args["uptime"] != "3247" {
		t.Errorf("uptime = %q", ev.Args["uptime"])
	}
}

func TestParseEventErrorWithQuotedString(t *testing.T) {
	ev, ok := parseEvent(`DISP ERROR "unknown mode FLOOP"`)
	if !ok {
		t.Fatal("parse failed")
	}
	if ev.Kind != "ERROR" {
		t.Errorf("Kind = %q", ev.Kind)
	}
	// First positional arg.
	if v, exists := ev.Args["_0"]; !exists || v != "unknown mode FLOOP" {
		t.Errorf("expected positional arg with quoted string, got args=%v", ev.Args)
	}
}

func TestParseEventBadSource(t *testing.T) {
	if _, ok := parseEvent("WRONG MODE FLIGHT"); ok {
		t.Error("expected parse to reject non-DISP source")
	}
}

func TestParseEventTooShort(t *testing.T) {
	if _, ok := parseEvent("DISP"); ok {
		t.Error("expected parse to reject single-token line")
	}
	if _, ok := parseEvent(""); ok {
		t.Error("expected parse to reject empty line")
	}
}

// === splitFields ===

func TestSplitFieldsBasic(t *testing.T) {
	got := splitFields("DISP MODE FLIGHT")
	want := []string{"DISP", "MODE", "FLIGHT"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSplitFieldsQuotedString(t *testing.T) {
	got := splitFields(`DISP MSG "hello world"`)
	want := []string{"DISP", "MSG", "hello world"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSplitFieldsMixedQuoted(t *testing.T) {
	got := splitFields(`DISP ALARM critical "BATTERY EMPTY"`)
	want := []string{"DISP", "ALARM", "critical", "BATTERY EMPTY"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// === End-to-end with a pipe ===

func TestDriverEndToEnd_ModeAndState(t *testing.T) {
	p := newPipe()
	d := New(p, Config{
		SnapshotRate: 50 * time.Millisecond,
		QueueSize:    16,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the driver in a goroutine.
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx)
	}()

	// Send a mode change and a state update.
	d.SetMode(ModeFlight)
	bat := 11.7
	alt := 124
	d.SetState(State{BatV: &bat, AltM: &alt})

	// Wait for the snapshot ticker to fire at least once.
	time.Sleep(150 * time.Millisecond)

	// Trigger shutdown.
	cancel()
	d.Close()
	<-runDone

	out := p.daemonOutput()

	// Should contain the mode change.
	if !strings.Contains(out, "DISP MODE FLIGHT\n") {
		t.Errorf("expected mode change in output, got:\n%s", out)
	}
	// Should contain at least one state line with our values.
	if !strings.Contains(out, "bat=11.70") {
		t.Errorf("expected bat=11.70 in output, got:\n%s", out)
	}
	if !strings.Contains(out, "alt=124") {
		t.Errorf("expected alt=124 in output, got:\n%s", out)
	}
}

func TestDriverEndToEnd_AlarmAndClear(t *testing.T) {
	p := newPipe()
	d := New(p, Config{
		SnapshotRate: 1 * time.Second, // slow ticker; we don't want noise
		QueueSize:    16,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx)
	}()

	d.FireAlarm(AlarmCritical, "BATTERY EMPTY")
	d.ClearAlarm()
	time.Sleep(50 * time.Millisecond) // let sender drain

	cancel()
	d.Close()
	<-runDone

	out := p.daemonOutput()
	if !strings.Contains(out, `DISP ALARM critical "BATTERY EMPTY"`) {
		t.Errorf("expected alarm fire in output, got:\n%s", out)
	}
	if !strings.Contains(out, "DISP CLEAR-ALARM\n") {
		t.Errorf("expected clear-alarm in output, got:\n%s", out)
	}
}

func TestDriverEndToEnd_ReceivesEvents(t *testing.T) {
	p := newPipe()
	d := New(p, Config{
		SnapshotRate: 1 * time.Second,
		QueueSize:    16,
	})

	var received []Event
	var receivedMu sync.Mutex
	d.SetEventHandler(func(ev Event) {
		receivedMu.Lock()
		received = append(received, ev)
		receivedMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx)
	}()

	// Feed device-side messages.
	p.feedDevice("DISP READY version=0.1.0 panels=2 w=128 h=32")
	p.feedDevice("DISP HEARTBEAT uptime=42")
	p.feedDevice("DISP PONG")

	// Give the receiver time to process.
	time.Sleep(100 * time.Millisecond)

	cancel()
	d.Close()
	<-runDone

	receivedMu.Lock()
	defer receivedMu.Unlock()

	if len(received) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(received), received)
	}
	if received[0].Kind != "READY" {
		t.Errorf("first event Kind = %q, want READY", received[0].Kind)
	}
	if received[1].Kind != "HEARTBEAT" {
		t.Errorf("second event Kind = %q, want HEARTBEAT", received[1].Kind)
	}
	if received[2].Kind != "PONG" {
		t.Errorf("third event Kind = %q, want PONG", received[2].Kind)
	}
}

func TestDriverEndToEnd_SetModeIdempotent(t *testing.T) {
	p := newPipe()
	d := New(p, Config{
		SnapshotRate: 1 * time.Second,
		QueueSize:    16,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx)
	}()

	d.SetMode(ModeFlight)
	d.SetMode(ModeFlight)
	d.SetMode(ModeFlight)
	time.Sleep(50 * time.Millisecond)

	cancel()
	d.Close()
	<-runDone

	out := p.daemonOutput()
	count := strings.Count(out, "DISP MODE FLIGHT\n")
	if count != 1 {
		t.Errorf("expected 1 MODE FLIGHT line, got %d:\n%s", count, out)
	}
}

func TestDriverEndToEnd_BrightnessClamped(t *testing.T) {
	p := newPipe()
	d := New(p, Config{
		SnapshotRate: 1 * time.Second,
		QueueSize:    16,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx)
	}()

	d.SetBrightness(-50)
	d.SetBrightness(150)
	d.SetBrightness(75)
	time.Sleep(50 * time.Millisecond)

	cancel()
	d.Close()
	<-runDone

	out := p.daemonOutput()
	if !strings.Contains(out, "DISP BRIGHTNESS 0\n") {
		t.Errorf("expected clamped 0, got:\n%s", out)
	}
	if !strings.Contains(out, "DISP BRIGHTNESS 100\n") {
		t.Errorf("expected clamped 100, got:\n%s", out)
	}
	if !strings.Contains(out, "DISP BRIGHTNESS 75\n") {
		t.Errorf("expected 75, got:\n%s", out)
	}
}
