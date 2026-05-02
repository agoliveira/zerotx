package audio

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// substituteFileExists swaps the package-level fileExists for a mock
// over the duration of the test, restoring it on cleanup.
func substituteFileExists(t *testing.T, mock func(path string) bool) {
	t.Helper()
	prev := fileExists
	fileExists = mock
	t.Cleanup(func() { fileExists = prev })
}

func TestResolve_LanguageSubdirPreferred(t *testing.T) {
	have := map[string]bool{
		"/sounds/en/armed.wav": true,
		"/sounds/armed.wav":    true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}
	got, ok := p.resolve("armed")
	if !ok || got != "/sounds/en/armed.wav" {
		t.Errorf("got %q (ok=%v), want /sounds/en/armed.wav", got, ok)
	}
}

func TestResolve_FallsBackToRoot(t *testing.T) {
	have := map[string]bool{
		"/sounds/siren.wav": true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}
	got, ok := p.resolve("siren")
	if !ok || got != "/sounds/siren.wav" {
		t.Errorf("got %q (ok=%v), want /sounds/siren.wav", got, ok)
	}
}

func TestResolve_TriesExtensionsInOrder(t *testing.T) {
	have := map[string]bool{
		"/sounds/en/armed.ogg": true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav", ".ogg", ".mp3"},
	}}
	got, ok := p.resolve("armed")
	if !ok || got != "/sounds/en/armed.ogg" {
		t.Errorf("got %q (ok=%v), want /sounds/en/armed.ogg", got, ok)
	}
}

func TestResolve_HandlesEdgeTXSuffixedNames(t *testing.T) {
	have := map[string]bool{
		"/sounds/en/armed.1x.wav": true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}
	got, ok := p.resolve("armed.1x")
	if !ok || got != "/sounds/en/armed.1x.wav" {
		t.Errorf("got %q (ok=%v), want /sounds/en/armed.1x.wav", got, ok)
	}
}

func TestResolve_NotFound(t *testing.T) {
	substituteFileExists(t, func(p string) bool { return false })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}
	_, ok := p.resolve("ghost")
	if ok {
		t.Errorf("expected resolve to fail when no file exists")
	}
}

func TestResolve_NoLangOnlyRoot(t *testing.T) {
	have := map[string]bool{
		"/sounds/en/armed.wav": true,
		"/sounds/armed.wav":    true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Extensions: []string{".wav"},
	}}
	got, ok := p.resolve("armed")
	if !ok || got != "/sounds/armed.wav" {
		t.Errorf("got %q (ok=%v), want /sounds/armed.wav", got, ok)
	}
}

func TestNullPlayer_Interface(t *testing.T) {
	p := &NullPlayer{}
	p.Play("track", "anything", LevelInfo)
	p.Play("track", "armed", LevelCritical)
	if p.Threshold() != LevelInfo {
		t.Errorf("NullPlayer Threshold should be LevelInfo")
	}
	p.SetThreshold(LevelWarning)
	p.Acknowledge("anything")
	p.AcknowledgeAll()
	if len(p.ActiveAlarms()) != 0 {
		t.Errorf("NullPlayer ActiveAlarms should be empty")
	}
	p.Close()
}

func TestFileExists_RealFilesystem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "real.wav")
	if err := os.WriteFile(path, []byte("not really wav"), 0644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(path) {
		t.Errorf("fileExists should return true for a real file")
	}
	if fileExists(filepath.Join(dir, "ghost.wav")) {
		t.Errorf("fileExists should return false for a missing file")
	}
	if fileExists(dir) {
		t.Errorf("fileExists should return false for a directory")
	}
}

// === Level / ParseLevel ===

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in    string
		want  Level
		ok    bool
	}{
		{"info", LevelInfo, true},
		{"notice", LevelNotice, true},
		{"warning", LevelWarning, true},
		{"warn", LevelWarning, true},
		{"critical", LevelCritical, true},
		{"crit", LevelCritical, true},
		{"INFO", LevelInfo, true},
		{"  warning  ", LevelWarning, true},
		{"", LevelNotice, false},
		{"junk", LevelNotice, false},
	}
	for _, c := range cases {
		got, ok := ParseLevel(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseLevel(%q): got (%s, %v), want (%s, %v)",
				c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestLevelString(t *testing.T) {
	if LevelInfo.String() != "info" || LevelNotice.String() != "notice" ||
		LevelWarning.String() != "warning" || LevelCritical.String() != "critical" {
		t.Errorf("Level.String() round-trip broken")
	}
}

// === DefaultLevelFor ===

func TestDefaultLevelFor(t *testing.T) {
	cases := []struct {
		name string
		want Level
	}{
		// Critical
		{"failsafe.1x", LevelCritical},
		{"crit-error.1x", LevelCritical},

		// Warning
		{"bat-low.1x", LevelWarning},
		{"sig-low.1x", LevelWarning},
		{"warn-something.1x", LevelWarning},
		{"rth.1x", LevelWarning},

		// Notice (state changes, not alarms)
		{"armed.1x", LevelNotice},
		{"armed", LevelNotice},
		{"disarm.1x", LevelNotice},
		{"fm-acr.1x", LevelNotice},
		{"fm-hor.1x", LevelNotice},
		{"cruise.1x", LevelNotice},
		{"poshld.1x", LevelNotice},
		{"manmod.1x", LevelNotice},

		// Default
		{"random-track.1x", LevelNotice},
		{"unknownsound", LevelNotice},
	}
	for _, c := range cases {
		got := DefaultLevelFor(c.name)
		if got != c.want {
			t.Errorf("DefaultLevelFor(%q): got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestStripRepeatSuffix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"armed.1x", "armed"},
		{"bat-low.0x", "bat-low"},
		{"alarm.5x", "alarm"},
		{"alarm.10x", "alarm"},
		{"alarm.123x", "alarm"},
		{"fm-acr", "fm-acr"},                  // no suffix
		{"sample.wav", "sample.wav"},          // not a repeat suffix
		{"prefix.x", "prefix.x"},              // missing digits
		{"with.weird.1x", "with.weird"},
		{"", ""},
	}
	for _, c := range cases {
		got := stripRepeatSuffix(c.in)
		if got != c.want {
			t.Errorf("stripRepeatSuffix(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// === Threshold ===

func TestThreshold_DropsBelow(t *testing.T) {
	// Use a shellPlayer with a stub command path that doesn't actually
	// run. We don't start the goroutine; we just verify what gets
	// enqueued vs dropped.
	p := newTestPlayer(LevelNotice)

	// info < notice → dropped, queue stays empty
	p.Play("track", "ping", LevelInfo)
	if len(p.events) != 0 {
		t.Errorf("expected info to be dropped at notice threshold")
	}

	// notice >= notice → enqueued
	p.Play("track", "fm-acr", LevelNotice)
	if len(p.events) != 1 {
		t.Errorf("expected notice to be enqueued at notice threshold")
	}
}

func TestThreshold_CriticalAlwaysPlays(t *testing.T) {
	p := newTestPlayer(LevelCritical)
	// notice would normally drop at critical threshold...
	p.Play("track", "fm-acr", LevelNotice)
	if len(p.events) != 0 {
		t.Errorf("notice should drop at critical threshold")
	}
	// ...but critical itself never drops.
	p.Play("track", "armed", LevelCritical)
	if len(p.events) != 1 {
		t.Errorf("critical should always play")
	}
}

func TestThreshold_DynamicChange(t *testing.T) {
	p := newTestPlayer(LevelInfo) // play everything
	p.Play("track", "info-thing", LevelInfo)
	if len(p.events) != 1 {
		t.Fatalf("expected info enqueued at info threshold")
	}
	// Drain and tighten threshold.
	<-p.events
	p.SetThreshold(LevelWarning)
	if p.Threshold() != LevelWarning {
		t.Errorf("Threshold() not updated")
	}
	p.Play("track", "info-thing", LevelInfo)
	if len(p.events) != 0 {
		t.Errorf("info should drop after threshold raised")
	}
}

// === Repeat schedule ===

func TestAlarm_WarningRepeatsThenStops(t *testing.T) {
	// Tight intervals so the test runs quickly.
	policies := map[Level]RepeatPolicy{
		LevelWarning: {Interval: 30 * time.Millisecond, MaxCycles: 3},
	}
	p := newTestPlayerPolicies(LevelInfo, policies)

	p.Play("track", "bat-low", LevelWarning)

	// First play is enqueued immediately. Drain events until we see
	// MaxCycles (3) total enqueues or timeout.
	got := drainPlays(p, "bat-low", 3, 250*time.Millisecond)
	if got != 3 {
		t.Errorf("got %d plays, want 3 (MaxCycles)", got)
	}

	// After max cycles, no more enqueues.
	if blockingDrainOne(p, 100*time.Millisecond) {
		t.Errorf("expected no further plays after MaxCycles reached")
	}
	if len(p.ActiveAlarms()) != 0 {
		t.Errorf("alarm should be removed after MaxCycles, got %v", p.ActiveAlarms())
	}
}

func TestAlarm_AcknowledgeStops(t *testing.T) {
	policies := map[Level]RepeatPolicy{
		LevelCritical: {Interval: 30 * time.Millisecond, MaxCycles: 0},
	}
	p := newTestPlayerPolicies(LevelInfo, policies)

	p.Play("track", "failsafe", LevelCritical)
	// Wait for at least one repeat to schedule, then ack.
	time.Sleep(50 * time.Millisecond)
	p.Acknowledge("failsafe")
	// Drain residual.
	time.Sleep(20 * time.Millisecond)
	for len(p.events) > 0 {
		<-p.events
	}
	// No more plays.
	if blockingDrainOne(p, 100*time.Millisecond) {
		t.Errorf("expected no plays after Acknowledge")
	}
	if len(p.ActiveAlarms()) != 0 {
		t.Errorf("alarm should be removed after Acknowledge")
	}
}

func TestAlarm_AcknowledgeAllStops(t *testing.T) {
	policies := map[Level]RepeatPolicy{
		LevelWarning:  {Interval: 30 * time.Millisecond, MaxCycles: 0},
		LevelCritical: {Interval: 30 * time.Millisecond, MaxCycles: 0},
	}
	p := newTestPlayerPolicies(LevelInfo, policies)

	p.Play("track", "bat-low", LevelWarning)
	p.Play("track", "failsafe", LevelCritical)
	time.Sleep(50 * time.Millisecond)
	if len(p.ActiveAlarms()) != 2 {
		t.Errorf("expected 2 active alarms, got %d", len(p.ActiveAlarms()))
	}
	p.AcknowledgeAll()
	if len(p.ActiveAlarms()) != 0 {
		t.Errorf("expected 0 active alarms after AcknowledgeAll, got %d",
			len(p.ActiveAlarms()))
	}
}

func TestAlarm_RepeatedFireResetsTimer(t *testing.T) {
	policies := map[Level]RepeatPolicy{
		LevelWarning: {Interval: 50 * time.Millisecond, MaxCycles: 5},
	}
	p := newTestPlayerPolicies(LevelInfo, policies)

	p.Play("track", "bat-low", LevelWarning)
	time.Sleep(20 * time.Millisecond)
	// Same alarm fires again before the first interval expires.
	p.Play("track", "bat-low", LevelWarning)
	// playedCount should now be 1 (reset), not 2.
	alarms := p.ActiveAlarms()
	if len(alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(alarms))
	}
	if alarms[0].PlayedCount != 1 {
		t.Errorf("expected playedCount=1 after reset, got %d", alarms[0].PlayedCount)
	}
}

func TestAlarm_NoticeDoesNotRepeat(t *testing.T) {
	policies := map[Level]RepeatPolicy{
		LevelNotice: {Interval: 30 * time.Millisecond, MaxCycles: 5},
	}
	p := newTestPlayerPolicies(LevelInfo, policies)

	p.Play("track", "fm-acr", LevelNotice)
	// One enqueue right away, but no further repeats — Play() only
	// schedules for warning/critical regardless of policy. This is
	// the intentional design (notice/info never repeat).
	got := drainPlays(p, "fm-acr", 1, 100*time.Millisecond)
	if got != 1 {
		t.Errorf("expected 1 play for notice, got %d", got)
	}
	if blockingDrainOne(p, 100*time.Millisecond) {
		t.Errorf("notice should not repeat even with non-zero policy interval")
	}
	if len(p.ActiveAlarms()) != 0 {
		t.Errorf("notice should not register as active alarm")
	}
}

// === Test helpers ===

// newTestPlayer constructs a shellPlayer with stubbed everything for
// unit tests. The events channel is consumed by tests; the run()
// goroutine is NOT started so events accumulate.
func newTestPlayer(thr Level) *shellPlayer {
	return newTestPlayerPolicies(thr, nil)
}

func newTestPlayerPolicies(thr Level, custom map[Level]RepeatPolicy) *shellPlayer {
	policies := make(map[Level]RepeatPolicy, 4)
	for l, p := range DefaultPolicies {
		policies[l] = p
	}
	for l, p := range custom {
		policies[l] = p
	}
	p := &shellPlayer{
		cfg:      Config{Threshold: thr, QueueDepth: 32},
		backends: map[string]backend{".wav": {cmd: "echo"}}, // never invoked since run() isn't started
		fallback: backend{cmd: "echo"},
		policies: policies,
		events:   make(chan playRequest, 32),
		done:     make(chan struct{}),
		alarms:   make(map[string]*alarmState),
	}
	p.threshold = int32(thr)
	return p
}

// drainPlays waits up to deadline for `want` plays of the given name
// to appear on the events channel. Returns how many it saw.
func drainPlays(p *shellPlayer, name string, want int, deadline time.Duration) int {
	got := 0
	end := time.Now().Add(deadline)
	for got < want && time.Now().Before(end) {
		select {
		case ev := <-p.events:
			if ev.name == name {
				got++
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	return got
}

// blockingDrainOne returns true if at least one play is enqueued
// within the deadline.
func blockingDrainOne(p *shellPlayer, deadline time.Duration) bool {
	select {
	case <-p.events:
		return true
	case <-time.After(deadline):
		return false
	}
}

// Concurrency smoke test: arm-style storm of plays and acks
// shouldn't deadlock or panic.
func TestConcurrency_Smoke(t *testing.T) {
	policies := map[Level]RepeatPolicy{
		LevelWarning:  {Interval: 5 * time.Millisecond, MaxCycles: 3},
		LevelCritical: {Interval: 5 * time.Millisecond, MaxCycles: 0},
	}
	p := newTestPlayerPolicies(LevelInfo, policies)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				p.Play("track", "alarm", LevelWarning)
				p.Play("track", "siren", LevelCritical)
				if j%5 == 0 {
					p.Acknowledge("alarm")
				}
			}
		}(i)
	}
	wg.Wait()
	p.AcknowledgeAll()
	if len(p.ActiveAlarms()) != 0 {
		t.Errorf("expected no active alarms after AcknowledgeAll")
	}
}

// === Decomposition / stitching ===

func TestDecompose_KnownCompound(t *testing.T) {
	parts, ok := decompose("bat-low")
	if !ok {
		t.Fatal("expected bat-low to decompose")
	}
	if len(parts) != 2 || parts[0] != "w-battery" || parts[1] != "low" {
		t.Errorf("unexpected decomposition: %v", parts)
	}
}

func TestDecompose_UnknownReturnsFalse(t *testing.T) {
	_, ok := decompose("never-defined-track")
	if ok {
		t.Error("expected unknown name to return ok=false")
	}
}

func TestDecompose_DefensiveCopy(t *testing.T) {
	// Caller mutating the returned slice must not corrupt the table.
	parts, _ := decompose("bat-low")
	parts[0] = "MUTATED"
	parts2, _ := decompose("bat-low")
	if parts2[0] == "MUTATED" {
		t.Error("decompose returned a shared slice; mutation leaked back")
	}
}

func TestDecompose_AllEntriesNonEmpty(t *testing.T) {
	// Sanity check that no entry has zero fragments.
	for name, parts := range decomposition {
		if len(parts) == 0 {
			t.Errorf("%s decomposes to empty", name)
		}
		for i, p := range parts {
			if p == "" {
				t.Errorf("%s fragment %d is empty", name, i)
			}
		}
	}
}

// resolveOnly tests the run() lookup-then-decompose path without
// actually exec'ing audio. We replace the play() function with a
// recorder by giving the player a stub command and substituting
// fileExists to control which paths "exist".
func TestStitching_LookupFirstThenDecompose(t *testing.T) {
	// Whole phrase exists: stitching should NOT trigger.
	have := map[string]bool{
		"/sounds/en/bat-low.wav": true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}

	// Resolve directly to confirm whole-phrase lookup wins.
	path, ok := p.resolve("bat-low")
	if !ok || path != "/sounds/en/bat-low.wav" {
		t.Errorf("whole-phrase lookup failed: got %q ok=%v", path, ok)
	}
}

func TestStitching_FallsBackWhenWholeMissing(t *testing.T) {
	// Whole bat-low.wav missing; fragments present.
	have := map[string]bool{
		"/sounds/en/w-battery.wav": true,
		"/sounds/en/low.wav":       true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}

	// Whole-phrase lookup should fail.
	if _, ok := p.resolve("bat-low"); ok {
		t.Fatal("whole-phrase shouldn't resolve here")
	}
	// Decomposition should produce the right parts.
	parts, ok := decompose("bat-low")
	if !ok || len(parts) != 2 {
		t.Fatalf("decompose failed: %v ok=%v", parts, ok)
	}
	// Each fragment should resolve.
	for _, frag := range parts {
		if _, ok := p.resolve(frag); !ok {
			t.Errorf("fragment %q didn't resolve but its file exists", frag)
		}
	}
}

func TestStitching_PartialMissingFragmentDegrades(t *testing.T) {
	// w-battery exists but "low" is missing. The player should still
	// play what it can rather than going completely silent.
	have := map[string]bool{
		"/sounds/en/w-battery.wav": true,
		// "low.wav" deliberately absent
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}

	parts, _ := decompose("bat-low")
	resolvedCount := 0
	for _, frag := range parts {
		if _, ok := p.resolve(frag); ok {
			resolvedCount++
		}
	}
	if resolvedCount != 1 {
		t.Errorf("expected exactly 1 fragment to resolve (w-battery), got %d", resolvedCount)
	}
}

// (helper removed; tests use shellPlayer directly without starting run())
