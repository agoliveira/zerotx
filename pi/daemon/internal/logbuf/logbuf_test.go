package logbuf

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuffer_BasicWriteRead(t *testing.T) {
	b := New(10)
	b.Write([]byte("hello\n"))
	b.Write([]byte("world\n"))
	snap := b.Snapshot(time.Time{})
	if len(snap) != 2 {
		t.Fatalf("got %d entries, want 2", len(snap))
	}
	if snap[0].Msg != "hello" || snap[1].Msg != "world" {
		t.Errorf("entries: %+v", snap)
	}
}

func TestBuffer_StripsTrailingNewline(t *testing.T) {
	b := New(5)
	b.Write([]byte("line\n"))
	snap := b.Snapshot(time.Time{})
	if snap[0].Msg != "line" {
		t.Errorf("expected stripped newline, got %q", snap[0].Msg)
	}
}

func TestBuffer_RingWrap(t *testing.T) {
	b := New(3)
	for i := 0; i < 5; i++ {
		b.Write([]byte(strings.Repeat("x", i+1) + "\n"))
	}
	// Capacity 3, wrote 5 entries. Should retain the last 3 (xxx, xxxx, xxxxx).
	snap := b.Snapshot(time.Time{})
	if len(snap) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(snap))
	}
	if snap[0].Msg != "xxx" || snap[1].Msg != "xxxx" || snap[2].Msg != "xxxxx" {
		t.Errorf("ring order broken: %+v", snap)
	}
}

func TestBuffer_SinceFilter(t *testing.T) {
	b := New(10)
	b.Write([]byte("a\n"))
	mid := time.Now()
	time.Sleep(2 * time.Millisecond)
	b.Write([]byte("b\n"))
	b.Write([]byte("c\n"))

	snap := b.Snapshot(mid)
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries after mid, got %d: %+v", len(snap), snap)
	}
	if snap[0].Msg != "b" || snap[1].Msg != "c" {
		t.Errorf("filtered wrong: %+v", snap)
	}
}

func TestBuffer_ConcurrentWrites(t *testing.T) {
	b := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				b.Write([]byte("from goroutine\n"))
			}
		}(i)
	}
	wg.Wait()
	snap := b.Snapshot(time.Time{})
	if len(snap) != 500 {
		t.Errorf("expected 500 entries, got %d", len(snap))
	}
}
