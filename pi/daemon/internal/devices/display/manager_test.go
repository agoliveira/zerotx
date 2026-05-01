package display

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManager_OpenFailureRetries(t *testing.T) {
	var attempts int32
	cfg := ManagerConfig{
		Open: func() (Transport, error) {
			atomic.AddInt32(&attempts, 1)
			return nil, errors.New("nope")
		},
		RetryDelay: 20 * time.Millisecond,
	}
	m := NewManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = m.Run(ctx)
	got := atomic.LoadInt32(&attempts)
	if got < 2 {
		t.Errorf("expected at least 2 open attempts, got %d", got)
	}
}

func TestManager_NoDriverDropsCalls(t *testing.T) {
	// With no Open ever succeeding, calls should be no-ops, not panics.
	cfg := ManagerConfig{
		Open: func() (Transport, error) {
			return nil, errors.New("offline")
		},
		RetryDelay: 1 * time.Hour, // never retry within test
	}
	m := NewManager(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = m.Run(ctx) }()
	defer cancel()
	time.Sleep(20 * time.Millisecond)
	// All of these should be silent no-ops.
	m.SetMode(ModeFlight)
	m.SetState(State{BatV: Float64Ptr(11.7)})
	m.SetThresholds(&Thresholds{Battery: &BatteryThresholds{WarnV: 14.4, CritV: 13.6, MinV: 12.8, FullV: 16.8}})
	m.SetBrightness(50)
	m.FireAlarm(AlarmCritical, "test")
	m.ClearAlarm()
	m.ShowMessage("hi")
	m.Ping()
	// No panic = test passes.
}

func TestManager_ReplaysStateOnReconnect(t *testing.T) {
	// Track the pipe used for each connection so we can inspect what
	// was written after replay.
	var (
		mu    sync.Mutex
		pipes []*pipeTransport
		opens int32
	)
	cfg := ManagerConfig{
		Open: func() (Transport, error) {
			n := atomic.AddInt32(&opens, 1)
			if n == 1 {
				// First open succeeds but we'll close it externally
				// to force a reconnect.
				p := newPipe()
				mu.Lock()
				pipes = append(pipes, p)
				mu.Unlock()
				return p, nil
			}
			// Second open also succeeds.
			p := newPipe()
			mu.Lock()
			pipes = append(pipes, p)
			mu.Unlock()
			return p, nil
		},
		DriverConfig: Config{SnapshotRate: 1 * time.Second, QueueSize: 16},
		RetryDelay:   10 * time.Millisecond,
	}
	m := NewManager(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Run(ctx) }()

	// Wait for first connection.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&opens) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&opens) < 1 {
		t.Fatal("first connection never happened")
	}

	// Set caller-facing state.
	m.SetMode(ModeFlight)
	m.SetThresholds(&Thresholds{
		Battery: &BatteryThresholds{WarnV: 14.4, CritV: 13.6, MinV: 12.8, FullV: 16.8},
	})
	m.SetBrightness(75)
	m.SetState(State{BatV: Float64Ptr(11.7), AltM: IntPtr(124)})
	time.Sleep(50 * time.Millisecond)

	// Force disconnect by closing the first pipe.
	mu.Lock()
	pipes[0].Close()
	mu.Unlock()

	// Wait for second connection.
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&opens) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&opens) < 2 {
		t.Fatal("reconnection never happened")
	}
	time.Sleep(100 * time.Millisecond)

	// Inspect the second pipe: it should have received the replay.
	mu.Lock()
	out := pipes[1].daemonOutput()
	mu.Unlock()

	wantSubs := []string{
		"DISP MODE FLIGHT",
		"DISP BRIGHTNESS 75",
		"DISP THRESHOLDS bat_warn=14.40",
	}
	for _, s := range wantSubs {
		if !strings.Contains(out, s) {
			t.Errorf("expected %q in replay output:\n%s", s, out)
		}
	}
}

func TestManager_BrightnessClamped(t *testing.T) {
	cfg := ManagerConfig{
		Open: func() (Transport, error) {
			return nil, errors.New("never")
		},
		RetryDelay: 1 * time.Hour,
	}
	m := NewManager(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Run(ctx) }()
	time.Sleep(10 * time.Millisecond)

	m.SetBrightness(-50)
	m.mu.RLock()
	if m.bright != 0 {
		t.Errorf("expected clamped 0, got %d", m.bright)
	}
	m.mu.RUnlock()

	m.SetBrightness(150)
	m.mu.RLock()
	if m.bright != 100 {
		t.Errorf("expected clamped 100, got %d", m.bright)
	}
	m.mu.RUnlock()

	m.SetBrightness(50)
	m.mu.RLock()
	if m.bright != 50 {
		t.Errorf("expected 50, got %d", m.bright)
	}
	m.mu.RUnlock()
}
