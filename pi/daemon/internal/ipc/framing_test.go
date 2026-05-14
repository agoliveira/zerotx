package ipc

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestCRC16CheckValue(t *testing.T) {
	got := CRC16([]byte("123456789"))
	const want = 0x29B1 // CRC-16/CCITT-FALSE check value
	if got != want {
		t.Fatalf("CRC16: got %#04x want %#04x", got, want)
	}
}

func TestRoundTripHeartbeat(t *testing.T) {
	frame, err := BuildFrame(MsgHeartbeat, 42, []byte{0x42})
	if err != nil {
		t.Fatal(err)
	}
	if frame[len(frame)-1] != 0x00 {
		t.Fatal("frame must end with 0x00 delimiter")
	}
	p := NewParser()
	frames, err := p.Feed(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	got := frames[0]
	if got.Type != MsgHeartbeat || got.Seq != 42 || !bytes.Equal(got.Payload, []byte{0x42}) {
		t.Fatalf("got %+v", got)
	}
}

func TestRoundTripChannelIntent(t *testing.T) {
	channels := make([]uint16, Channels)
	for i := range channels {
		channels[i] = CrsfChMid
	}
	channels[2] = CrsfChMin // throttle
	channels[4] = CrsfChMin // arm

	payload := make([]byte, len(channels)*2)
	for i, v := range channels {
		binary.LittleEndian.PutUint16(payload[i*2:], v)
	}

	frame, err := BuildFrame(MsgChannelIntent, 1, payload)
	if err != nil {
		t.Fatal(err)
	}
	p := NewParser()
	frames, err := p.Feed(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if !bytes.Equal(frames[0].Payload, payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestAllZerosCOBSEdgeCase(t *testing.T) {
	// 32 zero bytes is a worst-case COBS expansion.
	payload := make([]byte, 32)
	frame, err := BuildFrame(MsgChannelIntent, 7, payload)
	if err != nil {
		t.Fatal(err)
	}
	// No 0x00 inside the encoded portion (only the delimiter at the end).
	if bytes.Contains(frame[:len(frame)-1], []byte{0}) {
		t.Fatal("encoded frame contains embedded zero")
	}
	p := NewParser()
	frames, err := p.Feed(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if !bytes.Equal(frames[0].Payload, payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestMatchesFirmwareTestVectors(t *testing.T) {
	// These hex strings are the byte-exact output produced by
	// firmware/crsf/tests/test_ipc.c when fed identical inputs. Keeping this in sync
	// is the contract that proves the C and Go encoders agree.
	cases := []struct {
		name    string
		msgType byte
		seq     byte
		payload []byte
		hexWant string
	}{
		{
			name:    "heartbeat",
			msgType: MsgHeartbeat,
			seq:     42,
			payload: []byte{0x42},
			hexWant: "04032a0104428dff00",
		},
		{
			name:    "intent_centered",
			msgType: MsgChannelIntent,
			seq:     0,
			payload: bytes.Repeat([]byte{0xE0, 0x03}, 16),
			hexWant: "0201022023e003e003e003e003e003e003e003e003e003e003e003e003e003e003e003e003fd4c00",
		},
		{
			name:    "intent_zeros",
			msgType: MsgChannelIntent,
			seq:     7,
			payload: make([]byte, 32),
			hexWant: "040107200101010101010101010101010101010101010101010101010101010101010101032c2b00",
		},
		{
			name:    "log",
			msgType: MsgLog,
			seq:     1,
			payload: []byte("hello, zerotx"),
			hexWant: "0414010d1068656c6c6f2c207a65726f7478eeee00",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := BuildFrame(c.msgType, c.seq, c.payload)
			if err != nil {
				t.Fatal(err)
			}
			want, err := hex.DecodeString(c.hexWant)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("frame bytes mismatch:\ngot:  %x\nwant: %x", got, want)
			}
		})
	}
}

func TestParserHandlesGarbage(t *testing.T) {
	p := NewParser()
	// Random noise terminated by 0x00 should not crash and not produce frames.
	noise := []byte{0xff, 0xaa, 0x55, 0x12, 0x34, 0x00}
	frames, err := p.Feed(noise)
	if err == nil {
		t.Log("parser tolerated garbage as expected (some inputs decode but fail CRC)")
	}
	if len(frames) != 0 {
		t.Fatalf("expected 0 frames from noise, got %d", len(frames))
	}
}

func TestParserStreaming(t *testing.T) {
	// Build two frames, feed in arbitrary chunk boundaries.
	f1, _ := BuildFrame(MsgHeartbeat, 1, []byte{0x11})
	f2, _ := BuildFrame(MsgHeartbeat, 2, []byte{0x22})
	stream := append(f1, f2...)

	p := NewParser()
	var got []Frame
	// Feed one byte at a time.
	for i := 0; i < len(stream); i++ {
		fs, err := p.Feed(stream[i : i+1])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fs...)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(got))
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("seq order wrong: %v %v", got[0].Seq, got[1].Seq)
	}
}

// TestArmConfigPayloadRoundtrip verifies the daemon can serialize an
// ArmConfig and recover it identically. Catches endianness mistakes
// in the uint16 fields and field-order drift between Build and Parse.
// The firmware-side decoder is hand-mirrored from the same payload
// spec; this test guards the daemon side of that mirror.
func TestArmConfigPayloadRoundtrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ArmConfig
	}{
		{"TAER big talon", ArmConfig{ThrIdx: 0, ArmIdx: 4, ThrThreshold: 200, ArmDisarmValue: CrsfChMin}},
		{"AETR synthetic", ArmConfig{ThrIdx: 2, ArmIdx: 4, ThrThreshold: 200, ArmDisarmValue: CrsfChMin}},
		{"extremes", ArmConfig{ThrIdx: 15, ArmIdx: 15, ThrThreshold: CrsfChMax, ArmDisarmValue: CrsfChMax}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := BuildArmConfigPayload(tc.in)
			if len(p) != 6 {
				t.Fatalf("payload len: got %d, want 6", len(p))
			}
			out, err := ParseArmConfigPayload(p)
			if err != nil {
				t.Fatalf("ParseArmConfigPayload: %v", err)
			}
			if out != tc.in {
				t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", out, tc.in)
			}
		})
	}
}

// TestArmConfigPayloadRejectMalformed: parser refuses out-of-range
// channel indices and short payloads rather than silently accepting.
// Mirrors what the firmware-side parser must do.
func TestArmConfigPayloadRejectMalformed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"5 bytes (one short)", make([]byte, 5)},
		{"thrIdx out of range", []byte{16, 4, 200, 0, 172, 0}},
		{"armIdx out of range", []byte{0, 16, 200, 0, 172, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseArmConfigPayload(tc.payload); err == nil {
				t.Errorf("ParseArmConfigPayload(%v): err=nil, want non-nil", tc.payload)
			}
		})
	}
}
