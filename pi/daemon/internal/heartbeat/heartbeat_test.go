package heartbeat

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDriver records every SetValue call. Safe for concurrent use.
type fakeDriver struct {
	mu       sync.Mutex
	values   []int
	closeErr error
	closed   atomic.Bool
}

func (f *fakeDriver) SetValue(v int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values = append(f.values, v)
	return nil
}

func (f *fakeDriver) Close() error {
	f.closed.Store(true)
	return f.closeErr
}

func (f *fakeDriver) snapshot() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.values))
	copy(out, f.values)
	return out
}

// fakeClock advances under test control. Safe for concurrent reads.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestNew_DefaultsApplied verifies zero-valued config gets sensible
// defaults filled in.
func TestNew_DefaultsApplied(t *testing.T) {
	h := New(NewNull(), Config{})
	if h.cfg.ToggleInterval != 500*time.Millisecond {
		t.Errorf("default ToggleInterval = %v, want 500ms", h.cfg.ToggleInterval)
	}
	if h.cfg.Freshness != 1500*time.Millisecond {
		t.Errorf("default Freshness = %v, want 1.5s", h.cfg.Freshness)
	}
	if h.cfg.Now == nil {
		t.Error("default Now is nil")
	}
	_ = h.Close()
}

// TestRun_TogglesWhileFresh confirms the LED toggles on each tick
// when Tick() is called inside the freshness window.
func TestRun_TogglesWhileFresh(t *testing.T) {
	clk := newFakeClock(time.Unix(1000, 0))
	drv := &fakeDriver{}
	h := New(drv, Config{
		ToggleInterval: 10 * time.Millisecond,
		Freshness:      1 * time.Second,
		Now:            clk.Now,
	})
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drive a few real-time toggle cycles. We let the goroutine's
	// own ticker fire (10ms toggle), and we call Tick() through a
	// goroutine to keep the heartbeat fresh.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				// Match real time on the fake clock so freshness
				// stays satisfied.
				clk.advance(5 * time.Millisecond)
				h.Tick()
			}
		}
	}()

	time.Sleep(60 * time.Millisecond) // ~6 toggle ticks
	close(done)
	_ = h.Close()

	vals := drv.snapshot()
	if len(vals) < 3 {
		t.Fatalf("expected several toggles, got %d: %v", len(vals), vals)
	}
	// Verify alternation between consecutive toggles. Freshness was
	// always satisfied, so each tick should have flipped state.
	// Drop the trailing forced-low from Close.
	body := vals[:len(vals)-1]
	for i := 1; i < len(body); i++ {
		if body[i] == body[i-1] {
			t.Errorf("vals[%d]=%d same as vals[%d]=%d, expected toggle", i, body[i], i-1, body[i-1])
		}
	}
}

// TestRun_GoesLowWhenStale confirms the LED is forced low when no
// Tick arrives within the freshness window.
func TestRun_GoesLowWhenStale(t *testing.T) {
	clk := newFakeClock(time.Unix(1000, 0))
	drv := &fakeDriver{}
	h := New(drv, Config{
		ToggleInterval: 10 * time.Millisecond,
		Freshness:      30 * time.Millisecond,
		Now:            clk.Now,
	})
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Advance well past the freshness window before any toggle wake
	// can fire. This makes the first goroutine wake unambiguously
	// stale, and every wake after it stays stale because we never
	// call Tick().
	clk.advance(500 * time.Millisecond)

	// Real-time wait for several toggle wakes (6+ at 10ms interval).
	time.Sleep(80 * time.Millisecond)
	_ = h.Close()

	vals := drv.snapshot()
	if len(vals) == 0 {
		t.Fatal("expected at least one SetValue, got none")
	}
	for i, v := range vals {
		if v != 0 {
			t.Errorf("vals[%d] = %d, want 0 (stale should force low)", i, v)
		}
	}
}

// TestRun_RecoversFromStale confirms that ticks resuming after a
// stale period restart the toggle behavior.
func TestRun_RecoversFromStale(t *testing.T) {
	clk := newFakeClock(time.Unix(1000, 0))
	drv := &fakeDriver{}
	h := New(drv, Config{
		ToggleInterval: 10 * time.Millisecond,
		Freshness:      30 * time.Millisecond,
		Now:            clk.Now,
	})
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Phase 1: stale. Pre-advance past freshness, then wait so
	// several toggle wakes fire and all see stale.
	clk.advance(500 * time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	staleCount := len(drv.snapshot())

	// Phase 2: fresh. Tick on every toggle interval. We advance the
	// fake clock by a small amount and call Tick() before each
	// real-time wait so each goroutine wake sees age below freshness.
	for i := 0; i < 8; i++ {
		clk.advance(5 * time.Millisecond)
		h.Tick()
		time.Sleep(15 * time.Millisecond)
	}
	_ = h.Close()

	vals := drv.snapshot()
	if len(vals) <= staleCount {
		t.Fatalf("expected new toggles after recovery; stale=%d total=%d", staleCount, len(vals))
	}
	// Look at the post-recovery slice. Drop the trailing forced-low
	// from Close. There should be at least one transition to high.
	post := vals[staleCount:]
	if len(post) > 0 {
		post = post[:len(post)-1]
	}
	hasHigh := false
	for _, v := range post {
		if v == 1 {
			hasHigh = true
			break
		}
	}
	if !hasHigh {
		t.Errorf("post-recovery values had no 1: %v", post)
	}
}

// TestClose_Idempotent confirms Close can be called twice.
func TestClose_Idempotent(t *testing.T) {
	drv := &fakeDriver{}
	h := New(drv, Config{ToggleInterval: 1 * time.Millisecond})
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if !drv.closed.Load() {
		t.Error("driver not closed")
	}
}

// TestClose_ForcesLow confirms Close drives the line to 0 even if
// the last toggle left it high.
func TestClose_ForcesLow(t *testing.T) {
	clk := newFakeClock(time.Unix(1000, 0))
	drv := &fakeDriver{}
	h := New(drv, Config{
		ToggleInterval: 5 * time.Millisecond,
		Freshness:      1 * time.Second,
		Now:            clk.Now,
	})
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drive it briefly fresh so it toggles at least once.
	for i := 0; i < 3; i++ {
		clk.advance(5 * time.Millisecond)
		h.Tick()
		time.Sleep(8 * time.Millisecond)
	}
	_ = h.Close()

	vals := drv.snapshot()
	if len(vals) == 0 {
		t.Fatal("no SetValue calls")
	}
	if vals[len(vals)-1] != 0 {
		t.Errorf("last SetValue = %d, want 0 (Close should force low)", vals[len(vals)-1])
	}
}

// TestStart_AfterCloseFails confirms Start on a closed Heartbeat is
// rejected.
func TestStart_AfterCloseFails(t *testing.T) {
	h := New(NewNull(), Config{})
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.Start(); err == nil {
		t.Error("Start after Close returned nil error")
	}
}

// TestNullDriver confirms the null driver swallows everything.
func TestNullDriver(t *testing.T) {
	d := NewNull()
	if err := d.SetValue(1); err != nil {
		t.Errorf("SetValue: %v", err)
	}
	if err := d.SetValue(0); err != nil {
		t.Errorf("SetValue: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestIsActive_NullDriver(t *testing.T) {
	h := New(NewNull(), Config{})
	if h.IsActive() {
		t.Error("IsActive() = true for null driver, want false")
	}
}

func TestIsActive_RealDriverFake(t *testing.T) {
	// A non-null Driver implementation is treated as active. We use
	// fakeDriver as a stand-in since NewReal opens a real gpiochip.
	h := New(&fakeDriver{}, Config{})
	if !h.IsActive() {
		t.Error("IsActive() = false for non-null driver, want true")
	}
}

func TestIsActive_NilReceiver(t *testing.T) {
	var h *Heartbeat
	if h.IsActive() {
		t.Error("IsActive() on nil receiver = true, want false")
	}
}
