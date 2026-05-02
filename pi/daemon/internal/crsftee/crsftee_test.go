package crsftee

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestCRSFCRC_KnownValues(t *testing.T) {
	// CRSF CRC8 (poly 0xD5, init 0x00). Values verified by hand
	// computation; serves as a regression guard against accidental
	// polynomial changes. crc(empty) is 0 by construction.
	cases := []struct {
		name string
		in   []byte
		want byte
	}{
		{"empty", []byte{}, 0x00},
		{"single-zero", []byte{0x00}, 0x00},
		{"single-FF", []byte{0xFF}, 0xF9},
		{"single-14", []byte{0x14}, 0xAC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := crsfCRC(c.in); got != c.want {
				t.Errorf("got 0x%02X want 0x%02X", got, c.want)
			}
		})
	}
}

func TestBuildFrame(t *testing.T) {
	// Stripped: addr=0xC8, type=0x14, payload=[0x01,0x02,0x03]
	stripped := []byte{0xC8, 0x14, 0x01, 0x02, 0x03}
	got := buildFrame(stripped)

	// Expected on-wire: [0xC8, length, 0x14, 0x01, 0x02, 0x03, crc]
	// length = type(1) + payload(3) + crc(1) = 5
	if len(got) != 7 {
		t.Fatalf("frame length: got %d want 7", len(got))
	}
	if got[0] != 0xC8 {
		t.Errorf("addr byte: got 0x%02X want 0xC8", got[0])
	}
	if got[1] != 5 {
		t.Errorf("length byte: got %d want 5", got[1])
	}
	if !bytes.Equal(got[2:6], []byte{0x14, 0x01, 0x02, 0x03}) {
		t.Errorf("type+payload mismatch: %x", got[2:6])
	}
	wantCRC := crsfCRC([]byte{0x14, 0x01, 0x02, 0x03})
	if got[6] != wantCRC {
		t.Errorf("crc: got 0x%02X want 0x%02X", got[6], wantCRC)
	}
}

func TestBuildFrame_TooShort(t *testing.T) {
	if got := buildFrame(nil); got != nil {
		t.Errorf("nil input: got %x", got)
	}
	if got := buildFrame([]byte{0xC8}); got != nil {
		t.Errorf("1-byte input: got %x", got)
	}
}

func TestTee_Disabled(t *testing.T) {
	tee := New("", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Run on disabled tee returns nil immediately.
	if err := tee.Run(ctx); err != nil {
		t.Errorf("disabled Run: %v", err)
	}
	// Forward on disabled tee is a no-op (no clients).
	tee.Forward([]byte{0xC8, 0x14, 0x00})
}

func TestTee_FanOut(t *testing.T) {
	// Use port 0 to let the kernel pick a free port.
	tee := New("127.0.0.1:0", func(string, ...interface{}) {})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	tee.addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- tee.Run(ctx) }()

	// Wait briefly for listener to come up.
	var conn1, conn2 net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			conn1 = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn1 == nil {
		t.Fatal("could not dial tee")
	}
	defer conn1.Close()

	conn2, err = net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()

	// Allow handlers to register both clients.
	time.Sleep(100 * time.Millisecond)

	stripped := []byte{0xC8, 0x14, 0x42, 0x43, 0x44}
	wantFrame := buildFrame(stripped)
	tee.Forward(stripped)

	// Read from both clients with a deadline.
	for i, c := range []net.Conn{conn1, conn2} {
		c.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, len(wantFrame))
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Errorf("client %d read: %v", i, err)
			continue
		}
		if !bytes.Equal(buf, wantFrame) {
			t.Errorf("client %d frame mismatch: got %x want %x", i, buf, wantFrame)
		}
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Error("Run did not return after cancel")
	}
}

func TestTee_NoClientsNoOp(t *testing.T) {
	tee := New("127.0.0.1:0", func(string, ...interface{}) {})
	// Forward without running listener: should not panic, just no-op.
	tee.Forward([]byte{0xC8, 0x14, 0x00})
}
