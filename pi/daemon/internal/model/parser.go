package model

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadEdgeTX reads an EdgeTX-format YAML file (e.g. as exported by Companion
// or copied from the radio's SD card) and returns a typed model.
func LoadEdgeTX(path string) (*EdgeTXModel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("model: open %s: %w", path, err)
	}
	defer f.Close()
	return DecodeEdgeTX(f)
}

// DecodeEdgeTX reads from r.
func DecodeEdgeTX(r io.Reader) (*EdgeTXModel, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(false) // unknown fields land in Extras
	var m EdgeTXModel
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("model: decode edgetx: %w", err)
	}
	return &m, nil
}

// LoadZeroTX reads a ZeroTX wrapper file (zerotx + edgetx sections).
func LoadZeroTX(path string) (*ZeroTXModel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("model: open %s: %w", path, err)
	}
	defer f.Close()
	return DecodeZeroTX(f)
}

// DecodeZeroTX reads from r. Returns an error if the file fails to
// parse OR if the resulting zerotx metadata fails Validate(). Failing
// validation at load time means malformed config never reaches the
// running daemon: a bogus airframe or out-of-range arm_channel is a
// hard load failure, not a silent runtime surprise.
func DecodeZeroTX(r io.Reader) (*ZeroTXModel, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(false)
	var m ZeroTXModel
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("model: decode zerotx: %w", err)
	}
	if err := m.ZeroTX.Validate(); err != nil {
		return nil, fmt.Errorf("model: validate zerotx: %w", err)
	}
	return &m, nil
}

// ImportFromEdgeTX wraps an existing EdgeTX model in an empty ZeroTX outer
// (no source_bindings). The user fills source_bindings via GUI or by editing
// the saved file. The EdgeTX content is preserved verbatim.
func ImportFromEdgeTX(e *EdgeTXModel) *ZeroTXModel {
	return &ZeroTXModel{
		EdgeTX: *e,
	}
}

// Save writes the model out as YAML.
func Save(path string, m *ZeroTXModel) error {
	data, err := Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Marshal serializes a ZeroTX model to YAML bytes.
func Marshal(m *ZeroTXModel) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("model: marshal: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MixForChannel returns the (first) mix entry whose destCh matches the given
// 0-indexed channel, or nil if none exists.
func (m *EdgeTXModel) MixForChannel(ch int) *Mix {
	for i := range m.MixData {
		if m.MixData[i].DestCh == ch {
			return &m.MixData[i]
		}
	}
	return nil
}

// InputName returns the logical input name (e.g. "Thr") for index i, or "" if
// the index isn't defined.
func (m *EdgeTXModel) InputName(i int) string {
	if n, ok := m.InputNames[i]; ok {
		return n.Val
	}
	return ""
}

// ThrottleChannel returns the 0-indexed destCh that this model's
// throttle stick feeds into, or -1 if no throttle source is mixed to
// any channel. Used by the GCS arming state machine to gate confirm
// on "throttle low" without hardcoding a channel order.
//
// Models can express throttle in two ways: a direct source name in
// the mix (SrcRaw == "Thr") or a reference to a named input via
// SrcRaw == "I<n>" where input n is named "Thr" in the inputNames
// table. Both are accepted. Among multiple matches the highest
// absolute weight wins; ties break on the lowest DestCh for
// determinism.
//
// For TAER models (typical EdgeTX layout) the result is 0 (wire CH1).
// For AETR models it's 2 (wire CH3). For aircraft without a
// throttle source (gliders without a brake mix, etc.), it's -1.
func (m *EdgeTXModel) ThrottleChannel() int {
	// Resolve the input index whose val is "Thr". Map iteration order
	// is non-deterministic but we break on first hit; if a model has
	// two inputs named "Thr" (malformed) we just pick one and move on.
	thrInputIdx := -1
	for idx, in := range m.InputNames {
		if in.Val == "Thr" {
			thrInputIdx = idx
			break
		}
	}

	bestCh := -1
	bestAbsWeight := -1
	indexedSrc := ""
	if thrInputIdx >= 0 {
		indexedSrc = fmt.Sprintf("I%d", thrInputIdx)
	}
	for _, mix := range m.MixData {
		match := mix.SrcRaw == "Thr" || (indexedSrc != "" && mix.SrcRaw == indexedSrc)
		if !match {
			continue
		}
		w := mix.Weight
		if w < 0 {
			w = -w
		}
		// Strict-greater wins; tie goes to the lower DestCh.
		if w > bestAbsWeight {
			bestCh = mix.DestCh
			bestAbsWeight = w
		} else if w == bestAbsWeight && mix.DestCh < bestCh {
			bestCh = mix.DestCh
		}
	}
	return bestCh
}
