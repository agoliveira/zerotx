package main

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/audio"
	"github.com/agoliveira/zerotx/pi/daemon/internal/gps"
	"github.com/agoliveira/zerotx/pi/daemon/internal/narrator"
)

// recordingPlayer counts Speak calls and stashes the spoken text.
// Implements audio.Player; other methods are stubs we don't care
// about for this test.
type recordingPlayer struct {
	mu          sync.Mutex
	speakCount  atomic.Int32
	lastText    string
	lastLevel   audio.Level
	threshold   audio.Level
}

func (p *recordingPlayer) Play(kind, name string, level audio.Level)        {}
func (p *recordingPlayer) PlaySequence(kind string, names []string, l audio.Level) {}

func (p *recordingPlayer) Speak(text string, level audio.Level) {
	p.speakCount.Add(1)
	p.mu.Lock()
	p.lastText = text
	p.lastLevel = level
	p.mu.Unlock()
}

func (p *recordingPlayer) Threshold() audio.Level             { return p.threshold }
func (p *recordingPlayer) SetThreshold(l audio.Level)         { p.threshold = l }
func (p *recordingPlayer) Acknowledge(name string)            {}
func (p *recordingPlayer) AcknowledgeAll()                    {}
func (p *recordingPlayer) ActiveAlarms() []audio.ActiveAlarm  { return nil }
func (p *recordingPlayer) Close()                             {}

// pipeRC adapts an io.PipeReader to io.ReadCloser.
type pipeRC struct{ *io.PipeReader }

func (p pipeRC) Close() error { return p.PipeReader.Close() }

// withChecksum builds a complete NMEA sentence with the right XOR
// checksum, mirroring the helper in internal/gps test files.
func withChecksum(body string) string {
	var ck byte
	for i := 0; i < len(body); i++ {
		ck ^= body[i]
	}
	return "$" + body + "*" + hexByte(ck)
}

func hexByte(b byte) string {
	const hex = "0123456789ABCDEF"
	return string([]byte{hex[b>>4], hex[b&0xf]})
}

// TestStationGPSWatcher_NilReader is a no-op when no GPS is configured.
func TestStationGPSWatcher_NilReader(t *testing.T) {
	p := &recordingPlayer{}
	narr := narrator.New(p, "en", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	runStationGPSWatcher(ctx, nil, narr, 5*time.Millisecond)

	if p.speakCount.Load() != 0 {
		t.Errorf("speakCount=%d, expected 0 with nil reader", p.speakCount.Load())
	}
}

// TestStationGPSWatcher_NilNarrator is a no-op (defensive guard).
func TestStationGPSWatcher_NilNarrator(t *testing.T) {
	pr, _ := io.Pipe()
	rdr := gps.New(pipeRC{pr})
	defer rdr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	runStationGPSWatcher(ctx, rdr, nil, 5*time.Millisecond)
	// No assertion beyond "doesn't panic / returns cleanly".
}

// TestStationGPSWatcher_SpeaksOnceOnFirstFix verifies the watcher
// announces exactly once when a 2D-or-better fix arrives, then
// exits even if more fixes follow.
func TestStationGPSWatcher_SpeaksOnceOnFirstFix(t *testing.T) {
	pr, pw := io.Pipe()
	rdr := gps.New(pipeRC{pr})
	if err := rdr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rdr.Close()

	p := &recordingPlayer{}
	narr := narrator.New(p, "en", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the watcher in a goroutine.
	done := make(chan struct{})
	go func() {
		runStationGPSWatcher(ctx, rdr, narr, 5*time.Millisecond)
		close(done)
	}()

	// Push a couple no-fix sentences first.
	noFix := withChecksum("GPGGA,123519,,,,,0,00,99.9,,M,,M,,")
	go func() {
		_, _ = pw.Write([]byte(noFix + "\r\n" + noFix + "\r\n"))
		// Brief pause so the watcher's poll observes the no-fix state.
		time.Sleep(40 * time.Millisecond)
		fix3D := withChecksum("GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,")
		_, _ = pw.Write([]byte(fix3D + "\r\n"))
		// Push more 3D fixes; watcher should have already exited.
		time.Sleep(40 * time.Millisecond)
		_, _ = pw.Write([]byte(fix3D + "\r\n" + fix3D + "\r\n"))
	}()

	// Wait for the watcher to exit (it exits on its own after Speak).
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher did not exit after first fix")
	}

	if got := p.speakCount.Load(); got != 1 {
		t.Errorf("speakCount=%d, want 1", got)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastText != "Station GPS lock acquired." {
		t.Errorf("lastText=%q, want %q", p.lastText, "Station GPS lock acquired.")
	}
	if p.lastLevel != audio.LevelInfo {
		t.Errorf("lastLevel=%v, want LevelInfo", p.lastLevel)
	}
}

// TestStationGPSWatcher_QuietOnNoFix confirms the watcher does NOT
// speak when no fix ever arrives within the context window.
func TestStationGPSWatcher_QuietOnNoFix(t *testing.T) {
	pr, pw := io.Pipe()
	rdr := gps.New(pipeRC{pr})
	if err := rdr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rdr.Close()

	p := &recordingPlayer{}
	narr := narrator.New(p, "en", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go func() {
		noFix := withChecksum("GPGGA,123519,,,,,0,00,99.9,,M,,M,,")
		for i := 0; i < 5; i++ {
			_, _ = pw.Write([]byte(noFix + "\r\n"))
			time.Sleep(15 * time.Millisecond)
		}
	}()

	runStationGPSWatcher(ctx, rdr, narr, 5*time.Millisecond)

	if got := p.speakCount.Load(); got != 0 {
		t.Errorf("speakCount=%d, want 0 (no fix ever arrived)", got)
	}
}

// TestStationGPSWatcher_PortugueseLang confirms the spoken text
// honors the narrator's configured language.
func TestStationGPSWatcher_PortugueseLang(t *testing.T) {
	pr, pw := io.Pipe()
	rdr := gps.New(pipeRC{pr})
	if err := rdr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rdr.Close()

	p := &recordingPlayer{}
	narr := narrator.New(p, "pt", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runStationGPSWatcher(ctx, rdr, narr, 5*time.Millisecond)
		close(done)
	}()

	go func() {
		fix := withChecksum("GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,")
		_, _ = pw.Write([]byte(fix + "\r\n"))
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher did not exit")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastText != "GPS da estação fixado." {
		t.Errorf("lastText=%q, want pt phrase", p.lastText)
	}
}
