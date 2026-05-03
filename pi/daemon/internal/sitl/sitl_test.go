package sitl

import (
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
)

// unpackChannels reverses buildRCChannelsFrame's bit-packing. Used
// only here to verify round-trips; the real decoder lives in
// internal/telemetry where CRSF RC_CHANNELS frames are parsed.
func unpackChannels(payload []byte) [ipc.Channels]uint16 {
	var out [ipc.Channels]uint16
	var bitpos uint
	for i := 0; i < ipc.Channels; i++ {
		shift := bitpos & 7
		idx := bitpos >> 3
		var v uint32
		v = uint32(payload[idx]) >> shift
		v |= uint32(payload[idx+1]) << (8 - shift)
		if shift > 5 {
			v |= uint32(payload[idx+2]) << (16 - shift)
		}
		out[i] = uint16(v & 0x7FF)
		bitpos += 11
	}
	return out
}

func TestBuildRCChannelsFrame_Shape(t *testing.T) {
	var ch [ipc.Channels]uint16
	for i := range ch {
		ch[i] = ipc.CrsfChMid
	}
	f := buildRCChannelsFrame(ch)

	// Shape: addr + length + type + 22 + crc = 26
	if len(f) != 26 {
		t.Fatalf("frame len = %d, want 26", len(f))
	}
	if f[0] != 0xC8 {
		t.Errorf("addr = 0x%02x, want 0xC8", f[0])
	}
	if f[1] != 24 {
		t.Errorf("length = %d, want 24", f[1])
	}
	if f[2] != 0x16 {
		t.Errorf("type = 0x%02x, want 0x16", f[2])
	}
	// CRC validates.
	gotCRC := f[len(f)-1]
	wantCRC := crsfCRC(f[2 : len(f)-1])
	if gotCRC != wantCRC {
		t.Errorf("crc = 0x%02x, want 0x%02x", gotCRC, wantCRC)
	}
}

func TestBuildRCChannelsFrame_RoundTrip(t *testing.T) {
	// Distinct values per channel so misalignment is obvious.
	var in [ipc.Channels]uint16
	for i := range in {
		in[i] = uint16(172 + i*100) // 172..1672, all valid 11-bit
	}
	f := buildRCChannelsFrame(in)
	got := unpackChannels(f[3 : 3+rcChannelsPayloadSz])

	for i := range in {
		if got[i] != in[i] {
			t.Errorf("channel %d: got %d, want %d", i, got[i], in[i])
		}
	}
}

func TestBuildRCChannelsFrame_BoundaryValues(t *testing.T) {
	// CRSF channels are 11-bit so values go up to 2047. Confirm
	// extremes round-trip cleanly.
	var in [ipc.Channels]uint16
	in[0] = 0
	in[1] = 0x7FF // max 11-bit
	in[15] = 1234
	for i := 2; i < 15; i++ {
		in[i] = uint16(i)
	}
	f := buildRCChannelsFrame(in)
	got := unpackChannels(f[3 : 3+rcChannelsPayloadSz])
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("channel %d: got %d, want %d", i, got[i], in[i])
		}
	}
}

func TestCRSFCRC_KnownVector(t *testing.T) {
	// Empty input -> 0.
	if got := crsfCRC([]byte{}); got != 0 {
		t.Errorf("crsfCRC(empty) = 0x%02x, want 0", got)
	}
	// Sanity: same input twice = same output.
	in := []byte{0x16, 0x01, 0x02, 0x03}
	a := crsfCRC(in)
	b := crsfCRC(in)
	if a != b {
		t.Errorf("crsfCRC not deterministic: %02x vs %02x", a, b)
	}
}

func TestDrainFrames_DispatchesValid(t *testing.T) {
	c := &Conn{}
	var got [][]byte
	c.OnTelemetry = func(p []byte) { got = append(got, append([]byte(nil), p...)) }

	// Build two adjacent CRSF telemetry frames: addr=0xC8, type=0x08
	// (BATTERY_SENSOR), payload 8 bytes.
	build := func(payload []byte) []byte {
		inner := append([]byte{0x08}, payload...)
		out := []byte{0xC8, byte(len(inner) + 1)}
		out = append(out, inner...)
		out = append(out, crsfCRC(inner))
		return out
	}
	frame1 := build([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	frame2 := build([]byte{9, 10, 11, 12, 13, 14, 15, 16})

	buf := append([]byte(nil), frame1...)
	buf = append(buf, frame2...)
	rest := c.drainFrames(buf)

	if len(rest) != 0 {
		t.Errorf("expected no leftover bytes, got %d", len(rest))
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 dispatches, got %d", len(got))
	}
	// Stripped form: [addr][type][payload].
	if got[0][0] != 0xC8 || got[0][1] != 0x08 {
		t.Errorf("frame1 stripped header: %02x %02x", got[0][0], got[0][1])
	}
	if len(got[0]) != 1+1+8 {
		t.Errorf("frame1 stripped len = %d, want %d", len(got[0]), 1+1+8)
	}
}

func TestDrainFrames_DropsBadCRC(t *testing.T) {
	c := &Conn{}
	var calls int
	c.OnTelemetry = func([]byte) { calls++ }

	frame := []byte{0xC8, 0x04, 0x08, 0xAA, 0xBB, 0x00 /*bad crc*/}
	rest := c.drainFrames(frame)
	if calls != 0 {
		t.Errorf("expected no dispatch on bad CRC, got %d", calls)
	}
	if len(rest) >= len(frame) {
		t.Errorf("drainFrames must consume on bad CRC, len before/after %d/%d", len(frame), len(rest))
	}
}

func TestDrainFrames_ResyncsOnGarbage(t *testing.T) {
	c := &Conn{}
	var got int
	c.OnTelemetry = func([]byte) { got++ }

	// Three garbage bytes, then one valid frame.
	build := func() []byte {
		inner := []byte{0x08, 1, 2, 3, 4}
		out := []byte{0xC8, byte(len(inner) + 1)}
		out = append(out, inner...)
		out = append(out, crsfCRC(inner))
		return out
	}
	buf := []byte{0x00, 0xFF, 0x42}
	buf = append(buf, build()...)
	rest := c.drainFrames(buf)

	if got != 1 {
		t.Errorf("expected 1 dispatch after resync, got %d", got)
	}
	if len(rest) != 0 {
		t.Errorf("leftover bytes after resync: %d", len(rest))
	}
}

func TestDrainFrames_HoldsIncomplete(t *testing.T) {
	c := &Conn{}
	var got int
	c.OnTelemetry = func([]byte) { got++ }

	// First half of a frame: header says length=4 but only 3 bytes
	// after the header.
	buf := []byte{0xC8, 0x04, 0x08, 0xAA}
	rest := c.drainFrames(buf)
	if got != 0 {
		t.Errorf("expected 0 dispatch on incomplete, got %d", got)
	}
	if len(rest) != len(buf) {
		t.Errorf("incomplete frame should be retained, got len %d", len(rest))
	}
}
