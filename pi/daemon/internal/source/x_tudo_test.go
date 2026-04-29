package source

import (
	"strings"
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
)

// loadXTudo parses the x-tudo synthetic test model. Located in
// pi/daemon/testdata; tests run from internal/source so we walk up.
func loadXTudo(t *testing.T) *model.EdgeTXModel {
	t.Helper()
	m, err := model.LoadEdgeTX("../../testdata/x_tudo.yml")
	if err != nil {
		t.Fatalf("load x-tudo: %v", err)
	}
	return m
}

// TestXTudo_ParsesCleanly confirms the parser accepts the synthetic model.
// Hardcoded counts catch regressions against this reference fixture.
func TestXTudo_ParsesCleanly(t *testing.T) {
	m := loadXTudo(t)
	if len(m.LogicalSw) != 22 {
		t.Errorf("logical switches: got %d, want 22", len(m.LogicalSw))
	}
	if len(m.CustomFn) != 7 {
		t.Errorf("custom functions: got %d, want 7", len(m.CustomFn))
	}
	if len(m.TelemetrySensors) != 7 {
		t.Errorf("telemetry sensors: got %d, want 7", len(m.TelemetrySensors))
	}
}

// TestXTudo_ResolveAllLogicSwitchOperands walks every L# in x-tudo and
// confirms each operand in its def field resolves through the resolver
// without panicking. Doesn't assert specific values; the goal is name
// coverage. Logic switch evaluation lands in phase 2-c.
func TestXTudo_ResolveAllLogicSwitchOperands(t *testing.T) {
	m := loadXTudo(t)

	z := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"Thr": {Device: "HOTAS", Axis: ptr(2), Deadband: 0.02},
				"Ail": {Device: "HOTAS", Axis: ptr(0), Deadband: 0.02},
				"Ele": {Device: "HOTAS", Axis: ptr(1), Deadband: 0.02},
				"Rud": {Device: "HOTAS", Axis: ptr(3), Deadband: 0.02},
				"SA":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SB":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SC":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SD":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SE":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SF":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SG":  {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SH":  {Device: "GCS", Switch: ptr(0), Kind: "2pos"},
			},
		},
		EdgeTX: *m,
	}

	js := &stubJoystick{
		axes: map[string]map[int]float64{"HOTAS": {0: 0, 1: 0, 2: 0, 3: 0}},
	}
	pnl := &stubPanel{
		switches: map[string]int{
			"SA": 0, "SB": 0, "SC": 0, "SD": 0, "SE": 0, "SF": 0, "SG": 0, "SH": 0,
		},
	}
	logic := &stubLogic{state: map[int]bool{1: false, 2: false, 16: false}}
	r := New(z, js, pnl, logic, nil)

	// Walk each logical switch and try to resolve its def operands.
	for i := 0; i < len(m.LogicalSw); i++ {
		ls := m.LogicalSw[i]
		operands := strings.Split(ls.Def, ",")
		for _, op := range operands {
			op = strings.TrimSpace(op)
			if op == "" || op == "NONE" {
				continue
			}
			// EDGE T2 special markers: not source names.
			if op == "<" || op == "-" {
				continue
			}
			// Some operands are time numbers (TIMER's 5,10) which parse as
			// constants. Some are switch position matches (SA0, L1, SE2).
			// All should resolve as either bool or value without crashing.
			_, _ = r.ResolveValue(op)
			_, _ = r.ResolveBool(op)
		}
		// And the andsw modifier:
		if ls.Andsw != "" && ls.Andsw != "NONE" {
			_, _ = r.ResolveBool(ls.Andsw)
		}
	}
}

// TestXTudo_ResolveCustomFunctionSwitches confirms every CF's "swtch" field
// resolves cleanly. CFs use boolean trigger semantics.
func TestXTudo_ResolveCustomFunctionSwitches(t *testing.T) {
	m := loadXTudo(t)

	z := &model.ZeroTXModel{
		ZeroTX: model.ZeroTXMeta{
			SourceBindings: map[string]model.Binding{
				"SC": {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SE": {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
				"SF": {Device: "GCS", Switch: ptr(0), Kind: "3pos"},
			},
		},
		EdgeTX: *m,
	}
	pnl := &stubPanel{switches: map[string]int{"SC": 0, "SE": 0, "SF": 0}}
	logic := &stubLogic{state: map[int]bool{1: false, 7: false, 16: false}}
	r := New(z, nil, pnl, logic, nil)

	for i := 0; i < len(m.CustomFn); i++ {
		cf := m.CustomFn[i]
		if cf.Swtch == "" || cf.Swtch == "NONE" {
			continue
		}
		// Both ResolveBool (the natural CF use) and ResolveValue must
		// not panic for any switch expression in real models.
		_, _ = r.ResolveBool(cf.Swtch)
		_, _ = r.ResolveValue(cf.Swtch)
	}
}

// TestXTudo_PotsAndSliders exercises the deferred-source paths added for
// x-tudo's hardware-specific names.
func TestXTudo_PotsAndSliders(t *testing.T) {
	r := New(nil, nil, nil, nil, nil)
	for _, name := range []string{"P1", "P2", "P3", "SL1", "SL2"} {
		if _, ok := r.ResolveValue(name); ok {
			t.Errorf("%s should be deferred (ok=false), got ok=true", name)
		}
	}
}

// TestXTudo_TelemetryLabelTruncation confirms the 4-char telemetry label
// quirk: x-tudo has "RxBa" (truncated from RxBatt) which is what the model
// stores. The resolver finds it under that label.
func TestXTudo_TelemetryLabelTruncation(t *testing.T) {
	m := loadXTudo(t)
	z := &model.ZeroTXModel{EdgeTX: *m}
	tlm := &stubTelem{values: map[string]float64{"RxBa": 4.2, "RSSI": -55}}
	r := New(z, nil, nil, nil, tlm)

	if v, ok := r.ResolveValue("RxBa"); !ok || v != 4.2 {
		t.Errorf("RxBa: got (%v, %v), want (4.2, true)", v, ok)
	}
	// "RxBatt" is NOT in the model, so resolver returns ok=false.
	if _, ok := r.ResolveValue("RxBatt"); ok {
		t.Errorf("RxBatt (not in model) should not resolve")
	}
}
