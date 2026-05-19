package main

import (
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
)

// armConfigFromModel resolves both channel indices the RP2040
// firmware-level safety net needs: ThrIdx from the EdgeTX mix table,
// ArmIdx from the operator-set ZeroTX meta. These tests pin the
// boundary cases: nil model (boot defaults), default arm channel
// when arm_channel is absent, explicit arm_channel honored, throttle
// resolved from a TAER mix, and an out-of-range ArmChannel ignored
// (defensive belt-and-braces below the load-time validator).

func TestArmConfigFromModel_NilModel(t *testing.T) {
	c := armConfigFromModel(nil)
	if c.ThrIdx != 0 {
		t.Errorf("nil model ThrIdx: got %d, want 0", c.ThrIdx)
	}
	if c.ArmIdx != 4 {
		t.Errorf("nil model ArmIdx: got %d, want 4", c.ArmIdx)
	}
	if c.ThrThreshold != 200 {
		t.Errorf("nil model ThrThreshold: got %d, want 200", c.ThrThreshold)
	}
	if c.ArmDisarmValue != ipc.CrsfChMin {
		t.Errorf("nil model ArmDisarmValue: got %d, want %d", c.ArmDisarmValue, ipc.CrsfChMin)
	}
}

func TestArmConfigFromModel_DefaultArmChannel(t *testing.T) {
	// Model present but arm_channel not set. Should fall back to
	// the compile-time default of channel 4. Throttle is also unset
	// (no mix data) so it stays at the TAER-default zero.
	m := &model.ZeroTXModel{}
	c := armConfigFromModel(m)
	if c.ArmIdx != 4 {
		t.Errorf("ArmIdx with nil ZeroTX.ArmChannel: got %d, want 4", c.ArmIdx)
	}
	if c.ThrIdx != 0 {
		t.Errorf("ThrIdx with empty mix data: got %d, want 0", c.ThrIdx)
	}
}

func TestArmConfigFromModel_ArmChannelHonored(t *testing.T) {
	// Operator has bound arm to channel 5 (typical AETR layout).
	// Daemon must hand that index through to the firmware.
	armCh := 5
	m := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{ArmChannel: &armCh},
	}
	c := armConfigFromModel(m)
	if c.ArmIdx != 5 {
		t.Errorf("ArmIdx with arm_channel=5: got %d, want 5", c.ArmIdx)
	}
}

func TestArmConfigFromModel_ThrottleFromMix(t *testing.T) {
	// AETR mix: throttle is on channel 2 (CH3 on the wire), arm
	// stays at the default. Pins the resolver's interaction with
	// the EdgeTX mix table independent of the arm channel branch.
	m := &model.ZeroTXModel{
		EdgeTX: model.EdgeTXModel{
			InputNames: map[int]model.InputName{
				0: {Val: "Thr"},
			},
			MixData: []model.Mix{
				{DestCh: 2, SrcRaw: "I0", Weight: 100},
			},
		},
	}
	c := armConfigFromModel(m)
	if c.ThrIdx != 2 {
		t.Errorf("AETR ThrIdx: got %d, want 2", c.ThrIdx)
	}
	if c.ArmIdx != 4 {
		t.Errorf("ArmIdx (default branch): got %d, want 4", c.ArmIdx)
	}
}

func TestArmConfigFromModel_OutOfRangeArmChannelIgnored(t *testing.T) {
	// The load-time validator (DecodeZeroTX) rejects out-of-range
	// arm_channel values, so production never reaches this branch.
	// But a programmer constructing a ZeroTXModel in code could
	// skip Decode entirely. The resolver's bounds check must catch
	// that and fall back to the default rather than corrupt the
	// firmware-side ArmConfig.
	for _, bad := range []int{-1, 16, 99} {
		v := bad
		m := &model.ZeroTXModel{
			ZeroTX: model.ZeroTXMeta{ArmChannel: &v},
		}
		c := armConfigFromModel(m)
		if c.ArmIdx != 4 {
			t.Errorf("arm_channel=%d: ArmIdx got %d, want fallback 4", bad, c.ArmIdx)
		}
	}
}
