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

// DecodeZeroTX reads from r.
func DecodeZeroTX(r io.Reader) (*ZeroTXModel, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(false)
	var m ZeroTXModel
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("model: decode zerotx: %w", err)
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
