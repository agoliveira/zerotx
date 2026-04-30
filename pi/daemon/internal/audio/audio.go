// Package audio plays audio samples in response to model events
// (PLAY_TRACK / PLAY_SOUND custom functions). The package owns the
// flight-time playback path; sample generation (whether by hand,
// EdgeTX bank, TTS, or anything else) is out of scope.
//
// Each event has a Level (info / notice / warning / critical). The
// Player has a Threshold; events below threshold are dropped (logged
// at debug level). Critical events ignore the threshold and always
// play, unless the whole audio system is disabled (-no-audio).
//
// Warning and critical events also schedule periodic re-plays — the
// "alarm" pattern. Operators can acknowledge (silence) one or all
// active alarms via Acknowledge / AcknowledgeAll. The post-arm GUI
// surfaces active alarms with dismiss buttons; disarming auto-acks.
//
// Player implementations MUST not block: Play queues, RepeatPolicy
// timers run on their own goroutine, the daemon's tick loop is
// never on the hot path of audio.
package audio

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level is the priority of an audio event. Higher levels survive
// more aggressive thresholds.
type Level int

const (
	LevelInfo Level = iota
	LevelNotice
	LevelWarning
	LevelCritical
)

func (l Level) String() string {
	switch l {
	case LevelInfo:
		return "info"
	case LevelNotice:
		return "notice"
	case LevelWarning:
		return "warning"
	case LevelCritical:
		return "critical"
	default:
		return fmt.Sprintf("level(%d)", int(l))
	}
}

// ParseLevel returns the Level for a string like "info" / "notice" /
// "warning" / "critical". Empty or unknown returns LevelNotice (the
// safe middle ground) and ok=false. Casing is normalised.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return LevelInfo, true
	case "notice":
		return LevelNotice, true
	case "warning", "warn":
		return LevelWarning, true
	case "critical", "crit":
		return LevelCritical, true
	}
	return LevelNotice, false
}

// RepeatPolicy controls how repeating alarms behave for warning and
// critical levels. Info and notice never repeat regardless of policy.
type RepeatPolicy struct {
	// Interval between re-plays. Zero disables repetition for this
	// level (events play exactly once).
	Interval time.Duration
	// MaxCycles caps the total number of plays (including the first).
	// Zero means unlimited.
	MaxCycles int
}

// Default policies match the agreed-upon balance: critical alarms
// repeat every 5s indefinitely until acknowledged; warning alarms
// repeat every 30s up to 3 total plays; notice and info never repeat.
var DefaultPolicies = map[Level]RepeatPolicy{
	LevelInfo:     {Interval: 0, MaxCycles: 1},
	LevelNotice:   {Interval: 0, MaxCycles: 1},
	LevelWarning:  {Interval: 30 * time.Second, MaxCycles: 3},
	LevelCritical: {Interval: 5 * time.Second, MaxCycles: 0},
}

// Player accepts events and plays the corresponding audio. Implementations
// MUST not block; they queue or drop and return. Drops are logged.
type Player interface {
	// Play accepts an event. Kind is "track" or "sound" (treated the
	// same way; the distinction is preserved for future use). Name is
	// the track stem (e.g. "armed.1x") without extension. Level is the
	// priority; events below the configured threshold are dropped.
	// Returns immediately.
	Play(kind, name string, level Level)

	// PlaySequence accepts an ordered list of track names and plays
	// them as a single stitched utterance with the standard
	// inter-fragment gap between each. Used by the narrator to emit
	// runtime-composed announcements (post-flight summary, narrative
	// alarms, etc.) where the sequence isn't known at dictionary time.
	// Threshold gating is applied once, to the whole sequence.
	// Returns immediately. No-op if names is empty.
	PlaySequence(kind string, names []string, level Level)

	// Threshold returns the current minimum level. Events with a
	// strictly lower level are dropped.
	Threshold() Level

	// SetThreshold updates the minimum level. Safe to call from any
	// goroutine.
	SetThreshold(l Level)

	// Acknowledge cancels the active repeat schedule for the given
	// alarm name. No-op if no repeat is active for that name.
	Acknowledge(name string)

	// AcknowledgeAll cancels all active repeat schedules. Used on
	// disarm and via the GUI's "Acknowledge all" button.
	AcknowledgeAll()

	// ActiveAlarms returns the names of alarms currently scheduled to
	// repeat. Read-only snapshot; safe to call from any goroutine.
	ActiveAlarms() []ActiveAlarm

	// Close stops the player and releases any resources. Pending
	// events and active alarms are discarded.
	Close()
}

// ActiveAlarm is a snapshot of one currently-scheduled repeating
// alarm. Returned by Player.ActiveAlarms for GUI display.
type ActiveAlarm struct {
	Name        string    `json:"name"`
	Level       string    `json:"level"`        // "warning" / "critical"
	StartedAt   time.Time `json:"startedAt"`
	NextPlayAt  time.Time `json:"nextPlayAt"`
	PlayedCount int       `json:"playedCount"`
	MaxCycles   int       `json:"maxCycles"`
}

// Config controls the file-based player.
type Config struct {
	// SoundsDir is the root of the audio bank. Files are looked up at
	// <SoundsDir>/<Lang>/<name>.<ext> with a fallback to
	// <SoundsDir>/<name>.<ext> for language-neutral sounds.
	SoundsDir string

	// Lang is the language subdirectory ("en", "pt", etc.). Empty
	// means language-neutral lookups only. The operator's personal
	// global preference; not changed at runtime.
	Lang string

	// Extensions is the search order of file extensions. Defaults to
	// [".wav", ".ogg", ".mp3"] when zero-length.
	Extensions []string

	// QueueDepth is the number of events that can be buffered before
	// new events are dropped. Defaults to 16. Drops are logged.
	QueueDepth int

	// Threshold is the minimum level that plays. Events strictly
	// below this drop. Critical always plays regardless.
	Threshold Level

	// Policies overrides DefaultPolicies for any subset of levels.
	// A nil map uses defaults entirely; a partial map merges with
	// defaults (only specified levels override).
	Policies map[Level]RepeatPolicy
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
	// Merge custom policies with defaults.
	policies := make(map[Level]RepeatPolicy, 4)
	for l, p := range DefaultPolicies {
		policies[l] = p
	}
	for l, p := range cfg.Policies {
		policies[l] = p
	}

	cmd, args := detectBackend()
	if cmd == "" {
		log.Printf("audio: no playback backend found (tried paplay, aplay); audio events will be silent")
		return &NullPlayer{}
	}
	log.Printf("audio: backend=%s sounds-dir=%s lang=%s threshold=%s",
		cmd, cfg.SoundsDir, cfg.Lang, cfg.Threshold)
	p := &shellPlayer{
		cfg:      cfg,
		cmd:      cmd,
		args:     args,
		policies: policies,
		events:   make(chan playRequest, cfg.QueueDepth),
		done:     make(chan struct{}),
		alarms:   make(map[string]*alarmState),
	}
	atomic.StoreInt32(&p.threshold, int32(cfg.Threshold))
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

// === NullPlayer ===

// NullPlayer drops everything. Used when no audio backend is available
// (CI, headless servers, missing utilities) or when -no-audio is set.
type NullPlayer struct{}

func (n *NullPlayer) Play(string, string, Level)              {}
func (n *NullPlayer) PlaySequence(string, []string, Level)    {}
func (n *NullPlayer) Threshold() Level              { return LevelInfo }
func (n *NullPlayer) SetThreshold(Level)            {}
func (n *NullPlayer) Acknowledge(string)            {}
func (n *NullPlayer) AcknowledgeAll()               {}
func (n *NullPlayer) ActiveAlarms() []ActiveAlarm   { return nil }
func (n *NullPlayer) Close()                        {}

// === shellPlayer: file-based playback with priority and repeats ===

type shellPlayer struct {
	cfg      Config
	cmd      string
	args     []string
	policies map[Level]RepeatPolicy

	events chan playRequest
	done   chan struct{}

	threshold int32 // atomic Level

	mu      sync.Mutex
	closed  bool
	missing map[string]bool         // tracks names we've already warned about
	alarms  map[string]*alarmState  // active repeating alarms keyed by name
}

// playRequest is what flows through the events channel. Stripped of
// any scheduling concerns; the worker just resolves and plays.
//
// If sequence is non-empty, the worker plays each name in order with
// the standard inter-fragment gap. Otherwise the worker resolves
// `name` (with whole-phrase-then-decompose semantics).
type playRequest struct {
	kind     string
	name     string
	sequence []string
}

// alarmState tracks one currently-scheduled repeating alarm. It owns
// a goroutine that fires re-plays on the timer until Acknowledge()
// closes its done channel or MaxCycles is reached.
type alarmState struct {
	name        string
	level       Level
	policy      RepeatPolicy
	startedAt   time.Time
	playedCount int       // protected by shellPlayer.mu
	nextPlayAt  time.Time // protected by shellPlayer.mu
	done        chan struct{}
}

// PlaySequence implements Player. Builds a single playRequest that
// carries a pre-resolved sequence; the worker plays each fragment
// with the standard inter-fragment gap. No alarm scheduling for
// sequences (they're narrative, not repeating alarms).
func (p *shellPlayer) PlaySequence(kind string, names []string, level Level) {
	if len(names) == 0 {
		return
	}
	if p.isClosed() {
		return
	}
	thr := Level(atomic.LoadInt32(&p.threshold))
	if level < thr && level != LevelCritical {
		log.Printf("audio: dropped sequence first=%s level=%s (threshold=%s)", names[0], level, thr)
		return
	}
	// Defensive copy so caller can't mutate the slice mid-flight.
	seq := make([]string, len(names))
	copy(seq, names)
	p.enqueue(playRequest{kind: kind, sequence: seq})
}

func (p *shellPlayer) Play(kind, name string, level Level) {
	if name == "" {
		return
	}
	if p.isClosed() {
		return
	}
	thr := Level(atomic.LoadInt32(&p.threshold))
	// Critical always plays. Anything else needs to meet threshold.
	if level < thr && level != LevelCritical {
		log.Printf("audio: dropped track=%s level=%s (threshold=%s)", name, level, thr)
		return
	}

	// Enqueue the immediate playback request. Always do this whether
	// or not we also schedule repeats; the first play is part of the
	// schedule's count.
	p.enqueue(playRequest{kind: kind, name: name})

	// Schedule repeats for warning and critical levels. The same name
	// already active resets its timer (debounce — operator hasn't
	// addressed the underlying condition, so we keep alarming, but
	// from "now" rather than the original start).
	if level == LevelWarning || level == LevelCritical {
		policy := p.policies[level]
		if policy.Interval > 0 {
			p.scheduleAlarm(kind, name, level, policy)
		}
	}
}

func (p *shellPlayer) enqueue(r playRequest) {
	select {
	case p.events <- r:
	default:
		log.Printf("audio: queue full, dropped %s/%s", r.kind, r.name)
	}
}

// scheduleAlarm starts (or resets) a repeat schedule for the given
// alarm. The first play has already been enqueued by Play().
func (p *shellPlayer) scheduleAlarm(kind, name string, level Level, policy RepeatPolicy) {
	now := time.Now()
	p.mu.Lock()
	if existing, ok := p.alarms[name]; ok {
		// Reset existing alarm: cancel its goroutine, start fresh.
		// Played count restarts (this is a new occurrence of the
		// triggering condition).
		close(existing.done)
		delete(p.alarms, name)
	}
	a := &alarmState{
		name:        name,
		level:       level,
		policy:      policy,
		startedAt:   now,
		playedCount: 1, // first play already enqueued
		nextPlayAt:  now.Add(policy.Interval),
		done:        make(chan struct{}),
	}
	p.alarms[name] = a
	p.mu.Unlock()
	go p.runAlarm(kind, a)
}

// runAlarm is the per-alarm goroutine. It fires re-plays on the
// configured interval until ack'd or MaxCycles is reached.
func (p *shellPlayer) runAlarm(kind string, a *alarmState) {
	for {
		select {
		case <-a.done:
			return
		case <-time.After(time.Until(a.nextPlayAt)):
		}
		// Check for cap and closed state.
		p.mu.Lock()
		if p.closed {
			delete(p.alarms, a.name)
			p.mu.Unlock()
			return
		}
		// Has someone removed us via Acknowledge?
		if _, stillActive := p.alarms[a.name]; !stillActive {
			p.mu.Unlock()
			return
		}
		// MaxCycles=0 means unlimited.
		if a.policy.MaxCycles > 0 && a.playedCount >= a.policy.MaxCycles {
			delete(p.alarms, a.name)
			p.mu.Unlock()
			return
		}
		a.playedCount++
		a.nextPlayAt = time.Now().Add(a.policy.Interval)
		p.mu.Unlock()

		p.enqueue(playRequest{kind: kind, name: a.name})
	}
}

func (p *shellPlayer) Acknowledge(name string) {
	p.mu.Lock()
	a, ok := p.alarms[name]
	if ok {
		close(a.done)
		delete(p.alarms, name)
	}
	p.mu.Unlock()
	if ok {
		log.Printf("audio: alarm acknowledged: %s", name)
	}
}

func (p *shellPlayer) AcknowledgeAll() {
	p.mu.Lock()
	count := len(p.alarms)
	for _, a := range p.alarms {
		close(a.done)
	}
	p.alarms = make(map[string]*alarmState)
	p.mu.Unlock()
	if count > 0 {
		log.Printf("audio: %d alarm(s) acknowledged (all)", count)
	}
}

func (p *shellPlayer) ActiveAlarms() []ActiveAlarm {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ActiveAlarm, 0, len(p.alarms))
	for _, a := range p.alarms {
		out = append(out, ActiveAlarm{
			Name:        a.name,
			Level:       a.level.String(),
			StartedAt:   a.startedAt,
			NextPlayAt:  a.nextPlayAt,
			PlayedCount: a.playedCount,
			MaxCycles:   a.policy.MaxCycles,
		})
	}
	return out
}

func (p *shellPlayer) Threshold() Level {
	return Level(atomic.LoadInt32(&p.threshold))
}

func (p *shellPlayer) SetThreshold(l Level) {
	old := atomic.SwapInt32(&p.threshold, int32(l))
	if Level(old) != l {
		log.Printf("audio: threshold changed: %s -> %s", Level(old), l)
	}
}

func (p *shellPlayer) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	for _, a := range p.alarms {
		close(a.done)
	}
	p.alarms = nil
	close(p.events)
	p.mu.Unlock()
	<-p.done
}

func (p *shellPlayer) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *shellPlayer) run() {
	defer close(p.done)
	for ev := range p.events {
		// Sequence request: play each fragment in order, skipping
		// whole-phrase lookup and decomposition (the caller has
		// already resolved the list).
		if len(ev.sequence) > 0 {
			p.playSequence(ev.sequence)
			continue
		}

		// First try whole-phrase lookup. Cheapest path; one stat call
		// per extension and we play the matching file.
		if path, ok := p.resolve(ev.name); ok {
			p.play(path)
			continue
		}

		// Whole-phrase lookup failed. Try decomposing the track name
		// into building blocks. Stitching plays each block in sequence
		// with a short inter-fragment gap so it sounds like natural
		// speech rather than a robotic concat.
		//
		// Decomposition is hand-curated (see decompositions.go) — only
		// names with a known mapping decompose. Algorithmic splitting
		// on '-' was tempting but produced wrong results when a single
		// concept happens to be hyphenated (e.g. "fm-acr" is one
		// concept, not two; "bat-low" really is "bat" + "low").
		if parts, ok := decompose(ev.name); ok {
			p.playSequence(parts)
			continue
		}

		// Nothing matched. Log once per missing name and move on.
		p.warnMissing(ev.name)
	}
}

// playSequence resolves and plays a series of building-block names in
// order, with an inter-fragment gap between each. Used for stitched
// announcements when whole-phrase lookup fails. Skips fragments that
// can't be resolved (logging each once) so a partial stitch is better
// than total silence.
func (p *shellPlayer) playSequence(names []string) {
	for i, n := range names {
		path, ok := p.resolve(n)
		if !ok {
			p.warnMissing(n)
			continue
		}
		p.play(path)
		// Inter-fragment gap. 80ms approximates natural between-word
		// timing without sounding robotic. Skipped after the last
		// fragment so the queue moves on cleanly.
		if i < len(names)-1 {
			time.Sleep(stitchGap)
		}
	}
}

// stitchGap is the pause inserted between stitched fragments.
const stitchGap = 80 * time.Millisecond

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

// === Default level mapping ===

// DefaultLevelFor maps a track stem (with or without the EdgeTX
// .1x/.0x suffix) to a default priority level. Returns LevelNotice
// for anything not matched — the safe middle ground per the agreed
// design.
//
// Patterns are checked in order; first match wins.
//
//	armed.*         → critical
//	disarm.*        → critical
//	*failsafe*      → critical
//	*crit*          → critical
//	*low* (battery, signal) → warning
//	*warn*          → warning
//	*rth*           → warning   (RTH is significant)
//	*fm-*           → notice    (flight mode change)
//	*cruise*        → notice
//	*poshld*        → notice
//	manmod.*        → notice
//	(everything else) → notice
func DefaultLevelFor(name string) Level {
	stem := stripRepeatSuffix(strings.ToLower(name))
	switch {
	case strings.HasPrefix(stem, "armed"):
		return LevelCritical
	case strings.HasPrefix(stem, "disarm"):
		return LevelCritical
	case strings.Contains(stem, "failsafe"):
		return LevelCritical
	case strings.Contains(stem, "crit"):
		return LevelCritical

	case strings.Contains(stem, "low"):
		return LevelWarning
	case strings.Contains(stem, "warn"):
		return LevelWarning
	case strings.Contains(stem, "rth"):
		return LevelWarning

	case strings.Contains(stem, "fm-"):
		return LevelNotice
	case strings.Contains(stem, "cruise"):
		return LevelNotice
	case strings.Contains(stem, "poshld"):
		return LevelNotice
	case strings.HasPrefix(stem, "manmod"):
		return LevelNotice
	}
	return LevelNotice
}

// stripRepeatSuffix removes ".1x", ".0x", or ".<digit>x" from the end
// of a name. EdgeTX uses these to indicate repeat behaviour; ZeroTX
// applies its own per-level repeat policy instead.
func stripRepeatSuffix(name string) string {
	if idx := strings.LastIndex(name, "."); idx > 0 && idx < len(name)-2 {
		tail := name[idx+1:]
		if strings.HasSuffix(tail, "x") && allDigits(tail[:len(tail)-1]) {
			return name[:idx]
		}
	}
	return name
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
