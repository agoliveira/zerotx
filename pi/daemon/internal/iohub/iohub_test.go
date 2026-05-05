package iohub

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNullClient(t *testing.T) {
	c := New("")
	if _, ok := c.(*NullClient); !ok {
		t.Errorf("empty addr should give NullClient")
	}
	if err := c.Send("anything"); err != nil {
		t.Errorf("NullClient.Send returned error: %v", err)
	}
	c.OnEvent(func(target, payload string) {
		t.Errorf("NullClient should never dispatch events; got %s %s", target, payload)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Run(ctx); err != nil {
		t.Errorf("NullClient.Run returned error: %v", err)
	}
}

func TestLogClient(t *testing.T) {
	c := New("log")
	if _, ok := c.(*LogClient); !ok {
		t.Errorf("addr=log should give LogClient")
	}
	if err := c.Send("test"); err != nil {
		t.Errorf("LogClient.Send returned error: %v", err)
	}
}

func TestSerialClientType(t *testing.T) {
	c := New("/dev/ttyACM0")
	if _, ok := c.(*SerialClient); !ok {
		t.Errorf("path addr should give SerialClient")
	}
}

func TestEventDispatch(t *testing.T) {
	// Use a SerialClient just for its dispatch logic; we feed lines
	// directly via handleLine to avoid needing a real port.
	c := &SerialClient{}
	var got struct {
		sync.Mutex
		events []struct{ target, payload string }
	}
	c.OnEvent(func(target, payload string) {
		got.Lock()
		got.events = append(got.events, struct{ target, payload string }{target, payload})
		got.Unlock()
	})

	c.handleLine("EVENT button.0 down")
	c.handleLine("EVENT boot power-on")
	c.handleLine("EVENT vfd.0 ready")
	c.handleLine("> some response")  // ignored
	c.handleLine("! some error")     // ignored
	c.handleLine("")                 // ignored

	// Allow handlers to finish (synchronous in this implementation).
	time.Sleep(10 * time.Millisecond)

	got.Lock()
	defer got.Unlock()
	if len(got.events) != 3 {
		t.Fatalf("expected 3 events; got %d: %+v", len(got.events), got.events)
	}
	cases := []struct{ target, payload string }{
		{"button.0", "down"},
		{"boot", "power-on"},
		{"vfd.0", "ready"},
	}
	for i, want := range cases {
		if got.events[i] != want {
			t.Errorf("event %d = %+v, want %+v", i, got.events[i], want)
		}
	}
}

func TestEventNoPayload(t *testing.T) {
	c := &SerialClient{}
	var seen struct {
		sync.Mutex
		target, payload string
		count           int
	}
	c.OnEvent(func(target, payload string) {
		seen.Lock()
		seen.target = target
		seen.payload = payload
		seen.count++
		seen.Unlock()
	})
	c.handleLine("EVENT shutdown")
	seen.Lock()
	defer seen.Unlock()
	if seen.count != 1 {
		t.Fatalf("expected 1 event; got %d", seen.count)
	}
	if seen.target != "shutdown" {
		t.Errorf("target = %q, want shutdown", seen.target)
	}
	if seen.payload != "" {
		t.Errorf("payload = %q, want empty", seen.payload)
	}
}

func TestMultipleHandlers(t *testing.T) {
	c := &SerialClient{}
	var counts struct {
		sync.Mutex
		a, b int
	}
	c.OnEvent(func(string, string) {
		counts.Lock()
		counts.a++
		counts.Unlock()
	})
	c.OnEvent(func(string, string) {
		counts.Lock()
		counts.b++
		counts.Unlock()
	})
	c.handleLine("EVENT button.0 down")
	c.handleLine("EVENT button.1 up")
	counts.Lock()
	defer counts.Unlock()
	if counts.a != 2 || counts.b != 2 {
		t.Errorf("expected 2/2 handler invocations; got %d/%d", counts.a, counts.b)
	}
}
