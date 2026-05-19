package model

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = "../../testdata/big_talon.yml"

func TestLoadBigTalon(t *testing.T) {
	abs, _ := filepath.Abs(fixture)
	m, err := LoadEdgeTX(abs)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if m.Semver != "2.9.1" {
		t.Errorf("semver: got %q want %q", m.Semver, "2.9.1")
	}
	if m.Header.Name != "Big Talon" {
		t.Errorf("header.name: got %q want %q", m.Header.Name, "Big Talon")
	}

	// Channel layout that we manually verified from the file.
	wantMix := map[int]struct {
		src  string
		name string
	}{
		0:  {src: "I0", name: ""},
		1:  {src: "I1", name: ""},
		2:  {src: "I2", name: ""},
		3:  {src: "I3", name: "Rud"},
		4:  {src: "ls(3)", name: "Arm"},
		5:  {src: "SE", name: "HorAcr"},
		6:  {src: "SA", name: "CruPos"},
		7:  {src: "SG", name: "BeepRT"},
		8:  {src: "SB", name: "AutoLa"},
		9:  {src: "SC", name: "WPGCS"},
		10: {src: "6POS", name: "Tune"},
	}
	for ch, want := range wantMix {
		mix := m.MixForChannel(ch)
		if mix == nil {
			t.Errorf("CH%d: no mix entry", ch)
			continue
		}
		if mix.SrcRaw != want.src {
			t.Errorf("CH%d srcRaw: got %q want %q", ch, mix.SrcRaw, want.src)
		}
		if mix.Name != want.name {
			t.Errorf("CH%d name: got %q want %q", ch, mix.Name, want.name)
		}
	}

	// Input names.
	wantInputs := map[int]string{0: "Thr", 1: "Ail", 2: "Ele", 3: "Rud"}
	for i, want := range wantInputs {
		if got := m.InputName(i); got != want {
			t.Errorf("input %d: got %q want %q", i, got, want)
		}
	}

	// Flight modes.
	if len(m.FlightModeData) != 9 {
		t.Errorf("flight modes: got %d want 9", len(m.FlightModeData))
	}
	if m.FlightModeData[0].Name != "Horizon" {
		t.Errorf("flight mode 0: got %q want Horizon", m.FlightModeData[0].Name)
	}
	if m.FlightModeData[6].Swtch != "SG2" || m.FlightModeData[6].Name != "RTH" {
		t.Errorf("flight mode 6 (RTH): got %+v", m.FlightModeData[6])
	}

	// Logical switches.
	if len(m.LogicalSw) != 4 {
		t.Errorf("logical sw count: got %d want 4", len(m.LogicalSw))
	}
	ls3, ok := m.LogicalSw[3]
	if !ok {
		t.Fatal("L3 missing")
	}
	if ls3.Func != "FUNC_VNEG" || ls3.Andsw != "SF0" || ls3.Def != "I0,-99" {
		t.Errorf("L3 (arm condition): got %+v", ls3)
	}

	// Custom functions: confirm OVERRIDE_CHANNEL safety pattern is present.
	foundOverride := false
	for _, fn := range m.CustomFn {
		if fn.Func == "OVERRIDE_CHANNEL" && fn.Swtch == "!L3" && fn.Def == "0,-100,1" {
			foundOverride = true
			break
		}
	}
	if !foundOverride {
		t.Error("expected !L3 -> OVERRIDE_CHANNEL 0,-100,1 (throttle low when disarmed)")
	}

	// Module: CRSF on slot 1.
	mod1, ok := m.ModuleData[1]
	if !ok {
		t.Fatal("moduleData[1] missing")
	}
	if mod1.Type != "TYPE_CROSSFIRE" || mod1.ChannelsCount != 16 {
		t.Errorf("module: got %+v", mod1)
	}

	// Telemetry sensors: 24 entries.
	if len(m.TelemetrySensors) != 24 {
		t.Errorf("telemetry sensor count: got %d want 24", len(m.TelemetrySensors))
	}

	// Spot check a known sensor (RxBt = sensor 17, unit 1 (volts), prec 1).
	rxbt, ok := m.TelemetrySensors[17]
	if !ok {
		t.Fatal("telemetrySensors[17] (RxBt) missing")
	}
	if rxbt.Label != "RxBt" || rxbt.Unit != 1 || rxbt.Prec != 1 {
		t.Errorf("RxBt: got %+v", rxbt)
	}

	// Tier 2: extras must contain at least screenData and varioData.
	if _, ok := m.Extras["screenData"]; !ok {
		t.Error("extras missing screenData")
	}
	if _, ok := m.Extras["varioData"]; !ok {
		t.Error("extras missing varioData")
	}
}

func TestRoundTripMarshalsBack(t *testing.T) {
	abs, _ := filepath.Abs(fixture)
	m, err := LoadEdgeTX(abs)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := ImportFromEdgeTX(m)
	wrapped.ZeroTX.SourceBindings = map[string]Binding{
		"Thr": {Device: "HOTAS X", Axis: ptr(2)},
		"Ail": {Device: "HOTAS X", Axis: ptr(0)},
	}
	out, err := Marshal(wrapped)
	if err != nil {
		t.Fatal(err)
	}

	// The output must contain both sections.
	if !bytes.Contains(out, []byte("zerotx:")) {
		t.Error("output missing zerotx section")
	}
	if !bytes.Contains(out, []byte("edgetx:")) {
		t.Error("output missing edgetx section")
	}
	if !bytes.Contains(out, []byte("Big Talon")) {
		t.Error("output missing model name")
	}

	// Re-decode and verify it survived.
	round, err := DecodeZeroTX(strings.NewReader(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	if round.EdgeTX.Header.Name != "Big Talon" {
		t.Errorf("round-trip name: got %q", round.EdgeTX.Header.Name)
	}
	if len(round.EdgeTX.MixData) != len(m.MixData) {
		t.Errorf("round-trip mixData count: got %d want %d",
			len(round.EdgeTX.MixData), len(m.MixData))
	}
	if round.ZeroTX.SourceBindings["Thr"].Device != "HOTAS X" {
		t.Errorf("round-trip binding: got %+v", round.ZeroTX.SourceBindings["Thr"])
	}
}

// TestThrottleChannel_TAERFromBigTalon verifies the helper returns
// CH1 (index 0) for the real Big Talon fixture, which is TAER:
// inputNames 0:Thr maps via mix srcRaw "I0" to destCh 0.
func TestThrottleChannel_TAERFromBigTalon(t *testing.T) {
	abs, _ := filepath.Abs(fixture)
	m, err := LoadEdgeTX(abs)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := m.ThrottleChannel(); got != 0 {
		t.Errorf("Big Talon (TAER) ThrottleChannel: got %d, want 0", got)
	}
}

// TestThrottleChannel_AETRSynthetic builds an AETR-style model
// in-memory and confirms the helper returns CH3 (index 2).
func TestThrottleChannel_AETRSynthetic(t *testing.T) {
	m := &EdgeTXModel{
		InputNames: map[int]InputName{
			0: {Val: "Ail"},
			1: {Val: "Ele"},
			2: {Val: "Thr"},
			3: {Val: "Rud"},
		},
		MixData: []Mix{
			{DestCh: 0, SrcRaw: "I0", Weight: 100},
			{DestCh: 1, SrcRaw: "I1", Weight: 100},
			{DestCh: 2, SrcRaw: "I2", Weight: 100},
			{DestCh: 3, SrcRaw: "I3", Weight: 100},
		},
	}
	if got := m.ThrottleChannel(); got != 2 {
		t.Errorf("AETR ThrottleChannel: got %d, want 2", got)
	}
}

// TestThrottleChannel_DirectSrcName accepts srcRaw == "Thr" without
// the input-index indirection. Some hand-edited models or alternative
// editors write the source name directly into the mix.
func TestThrottleChannel_DirectSrcName(t *testing.T) {
	m := &EdgeTXModel{
		// No inputNames table at all; mix references "Thr" directly.
		MixData: []Mix{
			{DestCh: 5, SrcRaw: "Thr", Weight: 100},
			{DestCh: 0, SrcRaw: "Ail", Weight: 100},
		},
	}
	if got := m.ThrottleChannel(); got != 5 {
		t.Errorf("direct-srcRaw ThrottleChannel: got %d, want 5", got)
	}
}

// TestThrottleChannel_NoThrottleSource returns -1 when no mix
// references the throttle stick at all (e.g. a glider with no brake
// mix on a powered channel).
func TestThrottleChannel_NoThrottleSource(t *testing.T) {
	m := &EdgeTXModel{
		InputNames: map[int]InputName{
			0: {Val: "Ail"},
			1: {Val: "Ele"},
			2: {Val: "Rud"},
		},
		MixData: []Mix{
			{DestCh: 0, SrcRaw: "I0", Weight: 100},
			{DestCh: 1, SrcRaw: "I1", Weight: 100},
			{DestCh: 2, SrcRaw: "I2", Weight: 100},
		},
	}
	if got := m.ThrottleChannel(); got != -1 {
		t.Errorf("no-throttle ThrottleChannel: got %d, want -1", got)
	}
}

// TestThrottleChannel_HighestWeightWins covers the case where the
// throttle source is mixed onto multiple channels (e.g. a curve
// modifier on a secondary channel). The primary mix has the highest
// absolute weight; that is the one we return.
func TestThrottleChannel_HighestWeightWins(t *testing.T) {
	m := &EdgeTXModel{
		InputNames: map[int]InputName{
			0: {Val: "Thr"},
		},
		MixData: []Mix{
			// Secondary mix on CH5 with weight 30.
			{DestCh: 5, SrcRaw: "I0", Weight: 30},
			// Primary mix on CH0 with weight 100.
			{DestCh: 0, SrcRaw: "I0", Weight: 100},
		},
	}
	if got := m.ThrottleChannel(); got != 0 {
		t.Errorf("multi-mix ThrottleChannel: got %d, want 0", got)
	}
}

// TestThrottleChannel_TieBreakLowestDestCh confirms that when two
// throttle mixes have equal weight, the lower destCh wins. Keeps the
// result deterministic regardless of MixData iteration order.
func TestThrottleChannel_TieBreakLowestDestCh(t *testing.T) {
	m := &EdgeTXModel{
		InputNames: map[int]InputName{
			0: {Val: "Thr"},
		},
		MixData: []Mix{
			{DestCh: 7, SrcRaw: "I0", Weight: 100},
			{DestCh: 3, SrcRaw: "I0", Weight: 100},
		},
	}
	if got := m.ThrottleChannel(); got != 3 {
		t.Errorf("tie-break ThrottleChannel: got %d, want 3", got)
	}
}

// TestThrottleChannel_NegativeWeightUsedByAbs confirms that an
// inverted throttle mix (weight < 0) is still considered: it's the
// magnitude that ranks, not the sign. Inverted-throttle setups
// (some helicopters, some experimental airframes) can land here.
func TestThrottleChannel_NegativeWeightUsedByAbs(t *testing.T) {
	m := &EdgeTXModel{
		InputNames: map[int]InputName{
			0: {Val: "Thr"},
		},
		MixData: []Mix{
			{DestCh: 4, SrcRaw: "I0", Weight: -100},
		},
	}
	if got := m.ThrottleChannel(); got != 4 {
		t.Errorf("negative-weight ThrottleChannel: got %d, want 4", got)
	}
}

func ptr[T any](v T) *T { return &v }

// DecodeZeroTX wires the meta Validate() into the load path so
// malformed config never reaches the running daemon. These tests
// pin the happy path and the two validation failure modes that
// matter today (out-of-range arm_channel, unknown airframe).

func TestDecodeZeroTX_AcceptsValidMinimum(t *testing.T) {
	// Smallest legitimate file: an empty zerotx section and an
	// edgetx semver. Validate has nothing to complain about.
	const y = `
zerotx: {}
edgetx:
  semver: 2.9.1
`
	if _, err := DecodeZeroTX(bytes.NewReader([]byte(y))); err != nil {
		t.Fatalf("minimum valid file failed to decode: %v", err)
	}
}

func TestDecodeZeroTX_AcceptsArmChannelInRange(t *testing.T) {
	const y = `
zerotx:
  arm_channel: 4
edgetx:
  semver: 2.9.1
`
	m, err := DecodeZeroTX(bytes.NewReader([]byte(y)))
	if err != nil {
		t.Fatalf("arm_channel=4 should decode, got: %v", err)
	}
	if m.ZeroTX.ArmChannel == nil || *m.ZeroTX.ArmChannel != 4 {
		t.Errorf("arm_channel: got %v, want 4", m.ZeroTX.ArmChannel)
	}
}

func TestDecodeZeroTX_RejectsArmChannelOutOfRange(t *testing.T) {
	const y = `
zerotx:
  arm_channel: 99
edgetx:
  semver: 2.9.1
`
	_, err := DecodeZeroTX(bytes.NewReader([]byte(y)))
	if err == nil {
		t.Fatal("arm_channel=99 should fail validate, got nil")
	}
	if !strings.Contains(err.Error(), "arm_channel") {
		t.Errorf("error %q should mention arm_channel", err.Error())
	}
}

func TestDecodeZeroTX_RejectsBadAirframe(t *testing.T) {
	// Sanity check that the validator hook works for fields other
	// than the one this commit adds.
	const y = `
zerotx:
  airframe: submarine
edgetx:
  semver: 2.9.1
`
	_, err := DecodeZeroTX(bytes.NewReader([]byte(y)))
	if err == nil {
		t.Fatal("airframe=submarine should fail validate, got nil")
	}
	if !strings.Contains(err.Error(), "airframe") {
		t.Errorf("error %q should mention airframe", err.Error())
	}
}
