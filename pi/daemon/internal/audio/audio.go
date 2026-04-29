// Package audio plays audio samples in response to model events
// (PLAY_TRACK / PLAY_SOUND custom functions). The package owns the
// flight-time playback path; sample generation (whether by hand,
// EdgeTX bank, TTS, or anything else) is out of scope.
//
// Player is the playback abstraction. The default backend shells out
// to the first available system command (paplay, aplay) and runs all
// playback in a worker goroutine so the daemon's tick loop never
// blocks. If no playback backend is available, NullPlayer takes over
// and the daemon stays useful but silent.
package audio

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Player accepts events and plays the corresponding audio. Implementations
// MUST not block; they queue or drop and return. Drops are logged.
type Player interface {
	// Play accepts a track event. Kind is "track" or "sound" (currently
	// treated identically; sound patterns are rendered the same way as
	// tracks, just looking up a different name). Name is the track stem
	// (e.g. "armed") with no extension. Returns immediately.
	Play(kind, name string)

	// Close stops the player and releases any resources. Pending events
	// in the queue are discarded.
	Close()
}

// Config controls the file-based player.
type Config struct {
	// SoundsDir is the root of the audio bank. Files are looked up at
	// <SoundsDir>/<Lang>/<name>.<ext> with a fallback to
	// <SoundsDir>/<name>.<ext> for language-neutral sounds.
	SoundsDir string

	// Lang is the language subdirectory ("en", "pt", etc.). Empty means
	// language-neutral lookups only.
	Lang string

	// Extensions is the search order of file extensions. Defaults to
	// [".wav", ".ogg", ".mp3"] when zero-length.
	Extensions []string

	// QueueDepth is the number of events that can be buffered before
	// new events are dropped. Defaults to 16. Drops are logged.
	QueueDepth int
}

// New picks an available playback backend (paplay, then aplay) and
// returns a running Player. Returns a NullPlayer with a one-time log
// warning if no backend is found. Never returns an error.
func New(cfg Config) Player {
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = 16
	}
	if len(cfg.Extensions) == 0 {
		cfg.Extensions = []string{".wav", ".ogg", ".mp3"}
	}
	cmd, args := detectBackend()
	if cmd == "" {
		log.Printf("audio: no playback backend found (tried paplay, aplay); audio events will be silent")
		return &NullPlayer{}
	}
	log.Printf("audio: backend=%s sounds-dir=%s lang=%s", cmd, cfg.SoundsDir, cfg.Lang)
	p := &shellPlayer{
		cfg:    cfg,
		cmd:    cmd,
		args:   args,
		events: make(chan event, cfg.QueueDepth),
		done:   make(chan struct{}),
	}
	go p.run()
	return p
}

// detectBackend returns the first available playback command and any
// fixed arguments to pass before the filename. Empty cmd means none found.
func detectBackend() (string, []string) {
	candidates := []struct {
		cmd  string
		args []string
	}{
		{"paplay", nil},          // PulseAudio / PipeWire (most desktop systems)
		{"aplay", []string{"-q"}}, // ALSA fallback (-q to quiet startup banner)
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c.cmd); err == nil {
			return c.cmd, c.args
		}
	}
	return "", nil
}

// NullPlayer drops everything. Used when no audio backend is available
// (CI, headless servers, missing utilities).
type NullPlayer struct{}

func (n *NullPlayer) Play(string, string) {}
func (n *NullPlayer) Close()              {}

// shellPlayer queues events, resolves filenames, and shells out to a
// playback command (paplay/aplay) one at a time. Concurrent playback is
// not supported: a new event while one is playing waits in the queue,
// and if the queue is full the event is dropped.
type shellPlayer struct {
	cfg  Config
	cmd  string
	args []string

	events chan event
	done   chan struct{}

	mu      sync.Mutex
	closed  bool
	missing map[string]bool // tracks names we've already warned about
}

type event struct {
	kind string
	name string
}

func (p *shellPlayer) Play(kind, name string) {
	if name == "" {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	select {
	case p.events <- event{kind: kind, name: name}:
	default:
		log.Printf("audio: queue full, dropped %s/%s", kind, name)
	}
}

func (p *shellPlayer) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.events)
	p.mu.Unlock()
	<-p.done
}

func (p *shellPlayer) run() {
	defer close(p.done)
	for ev := range p.events {
		path, ok := p.resolve(ev.name)
		if !ok {
			p.warnMissing(ev.name)
			continue
		}
		p.play(path)
	}
}

// resolve searches for a sample file. EdgeTX puts language-specific
// audio at /SOUNDS/<lang>/<name>.wav; some samples (e.g. system-wide
// sirens) live at /SOUNDS/<name>.wav. We try both.
//
// EdgeTX's ".1x" / ".0x" suffixes on names are NOT extensions; they're
// part of the stem and the file is e.g. "armed.1x.wav".
func (p *shellPlayer) resolve(name string) (string, bool) {
	var candidates []string
	if p.cfg.Lang != "" {
		for _, ext := range p.cfg.Extensions {
			candidates = append(candidates, filepath.Join(p.cfg.SoundsDir, p.cfg.Lang, name+ext))
		}
	}
	for _, ext := range p.cfg.Extensions {
		candidates = append(candidates, filepath.Join(p.cfg.SoundsDir, name+ext))
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c, true
		}
	}
	return "", false
}

func (p *shellPlayer) play(path string) {
	// Bound playback to a sane maximum. A wedged audio process must not
	// halt the queue forever.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := append([]string{}, p.args...)
	args = append(args, path)
	cmd := exec.CommandContext(ctx, p.cmd, args...)
	if err := cmd.Run(); err != nil {
		log.Printf("audio: %s %s: %v", p.cmd, path, err)
	}
}

func (p *shellPlayer) warnMissing(name string) {
	p.mu.Lock()
	if p.missing == nil {
		p.missing = map[string]bool{}
	}
	if p.missing[name] {
		p.mu.Unlock()
		return
	}
	p.missing[name] = true
	p.mu.Unlock()
	log.Printf("audio: sample not found for %q in %s (lang=%q); event will be silent",
		name, p.cfg.SoundsDir, p.cfg.Lang)
}

// fileExists reports whether path is an existing regular file. Package
// variable so tests can substitute it.
var fileExists = func(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}
