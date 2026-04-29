package ipc

import (
	"testing"
)

func TestHelloPayload_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		proto   uint8
		version string
	}{
		{"empty version", 1, ""},
		{"normal version", 1, "zerotxd 0.12.0-handshake"},
		{"firmware version", 1, "zerotx-fw m1.3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf := buildHelloPayload(c.proto, c.version)
			if len(buf) < 4 {
				t.Fatalf("payload too short: %d", len(buf))
			}
			gotProto, gotVer := parseHelloPayload(buf)
			if gotProto != c.proto {
				t.Errorf("proto: got %d, want %d", gotProto, c.proto)
			}
			if gotVer != c.version {
				t.Errorf("version: got %q, want %q", gotVer, c.version)
			}
		})
	}
}

func TestHelloPayload_TruncatesLongVersion(t *testing.T) {
	long := make([]byte, MaxPayload+100)
	for i := range long {
		long[i] = 'x'
	}
	buf := buildHelloPayload(1, string(long))
	if len(buf) > MaxPayload {
		t.Errorf("payload should be capped at MaxPayload (%d), got %d", MaxPayload, len(buf))
	}
	if buf[0] != 1 {
		t.Errorf("proto byte: got %d, want 1", buf[0])
	}
}

func TestParseHelloPayload_ShortPayload(t *testing.T) {
	// Anything shorter than the 4-byte header is treated as malformed
	// and reported as proto=0, version="". The handshake then sees
	// "remote proto 0 != local proto 1" and stays gated.
	cases := [][]byte{
		nil,
		{},
		{0x01},
		{0x01, 0x00, 0x00},
	}
	for _, p := range cases {
		proto, ver := parseHelloPayload(p)
		if proto != 0 || ver != "" {
			t.Errorf("short payload %v: got proto=%d ver=%q, want 0,\"\"", p, proto, ver)
		}
	}
}

func TestRecordHandshakeResult_Match(t *testing.T) {
	l := &Link{LocalVersion: "zerotxd test"}
	l.recordHandshakeResult(ProtoVersion, "zerotx-fw m1.3")
	ok, legacy, ver := l.HandshakeComplete()
	if !ok {
		t.Errorf("expected hsOK=true after matching proto")
	}
	if legacy {
		t.Errorf("expected hsLegacy=false")
	}
	if ver != "zerotx-fw m1.3" {
		t.Errorf("expected remote version recorded, got %q", ver)
	}
}

func TestRecordHandshakeResult_Mismatch(t *testing.T) {
	l := &Link{LocalVersion: "zerotxd test"}
	// Wrong proto: gate stays closed, ok stays false, legacy stays false.
	// Channel intent will be silently dropped; FC failsafe takes over.
	l.recordHandshakeResult(99, "zerotx-fw vintage")
	ok, legacy, ver := l.HandshakeComplete()
	if ok {
		t.Errorf("expected hsOK=false on proto mismatch")
	}
	if legacy {
		t.Errorf("expected hsLegacy=false on proto mismatch")
	}
	if ver != "zerotx-fw vintage" {
		t.Errorf("expected remote version recorded for diagnostics, got %q", ver)
	}
}

func TestRecordHandshakeResult_StragglerIgnored(t *testing.T) {
	// Once handshake is OK, a later mismatch ack must not flip state.
	l := &Link{LocalVersion: "zerotxd test"}
	l.recordHandshakeResult(ProtoVersion, "zerotx-fw m1.3")
	l.recordHandshakeResult(99, "another firmware")
	ok, _, ver := l.HandshakeComplete()
	if !ok {
		t.Errorf("expected hsOK to remain true after straggler")
	}
	if ver != "zerotx-fw m1.3" {
		t.Errorf("expected first ack's version preserved, got %q", ver)
	}
}

func TestSendChannelIntent_GatedBeforeHandshake(t *testing.T) {
	// SendChannelIntent must not return error when gated; it silently
	// drops. The error path is reserved for actual write failures.
	l := &Link{}
	var ch [Channels]uint16
	for i := range ch {
		ch[i] = CrsfChMid
	}
	if err := l.SendChannelIntent(ch); err != nil {
		t.Errorf("expected nil error when gated, got %v", err)
	}
}
