package logic

import (
	"strconv"
	"strings"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
)

// edgeT2Spec discriminates EDGE's T2 special markers.
type edgeT2Spec int

const (
	edgeT2Numeric   edgeT2Spec = iota // 0..N: max active duration
	edgeT2Immediate                   // YAML "<": fire as T1 reached, don't wait for V1 to drop
	edgeT2Unbounded                   // YAML "-": no upper bound, fire on V1 drop
)

// parsedDef holds the decoded def fields. Different functions use
// different fields; the parser populates only what's relevant for each
// function type. Unset fields hold their zero value.
type parsedDef struct {
	op1 string  // first operand (source name) or empty
	op2 string  // second operand (source name) or empty
	c1  float64 // first constant in [-1, 1] (already percent-scaled)
	c2  float64 // second constant (rare; e.g. VALMOSTEQUAL tolerance)

	// EDGE
	edgeT1     time.Duration
	edgeT2     time.Duration
	edgeT2Spec edgeT2Spec

	// TIMER
	timerOn  time.Duration
	timerOff time.Duration

	// Set when the def couldn't be parsed in the form expected for the
	// function. Engine logs once and treats output as false.
	invalid bool
}

// parseDef decodes an EdgeTX logicalSw entry's def string. The function
// name dictates which fields are populated.
//
// Special characters in EDGE's T2 (single-char, not the manual's
// double-char display form):
//
//	"<"   fire immediately when T1 met (no wait for V1 to drop)
//	"-"   no upper bound on V1 active duration
func parseDef(ls model.LogicalSwitch) parsedDef {
	var d parsedDef
	parts := splitDef(ls.Def)

	switch ls.Func {
	case "FUNC_VEQUAL", "FUNC_VALMOSTEQUAL", "FUNC_VPOS", "FUNC_VNEG",
		"FUNC_APOS", "FUNC_ANEG", "FUNC_DIFFEGREATER", "FUNC_ADIFFEGREATER":
		// source, constant
		if len(parts) < 2 {
			d.invalid = true
			return d
		}
		d.op1 = parts[0]
		d.c1 = parsePercent(parts[1])

	case "FUNC_AND", "FUNC_OR", "FUNC_XOR", "FUNC_STICKY":
		// source, source (boolean)
		if len(parts) < 2 {
			d.invalid = true
			return d
		}
		d.op1 = parts[0]
		d.op2 = parts[1]

	case "FUNC_GREATER", "FUNC_LESS":
		// source, source (numeric compare)
		if len(parts) < 2 {
			d.invalid = true
			return d
		}
		d.op1 = parts[0]
		d.op2 = parts[1]

	case "FUNC_EDGE":
		// source, T1, T2 (T2 may be "<" or "-")
		if len(parts) < 3 {
			d.invalid = true
			return d
		}
		d.op1 = parts[0]
		t1, err := strconv.Atoi(parts[1])
		if err != nil {
			d.invalid = true
			return d
		}
		d.edgeT1 = dsToDuration(t1)
		switch parts[2] {
		case "<":
			d.edgeT2Spec = edgeT2Immediate
		case "-":
			d.edgeT2Spec = edgeT2Unbounded
		default:
			t2, err := strconv.Atoi(parts[2])
			if err != nil {
				d.invalid = true
				return d
			}
			d.edgeT2 = dsToDuration(t2)
			d.edgeT2Spec = edgeT2Numeric
		}

	case "FUNC_TIMER":
		// onTime, offTime (no source)
		if len(parts) < 2 {
			d.invalid = true
			return d
		}
		on, err1 := strconv.Atoi(parts[0])
		off, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			d.invalid = true
			return d
		}
		d.timerOn = dsToDuration(on)
		d.timerOff = dsToDuration(off)

	case "FUNC_NONE", "":
		// Unconfigured switch; nothing to do.
	}

	return d
}

// splitDef splits a comma-separated def string. Strips whitespace.
// Used by parseDef and also by the CF parser later.
func splitDef(def string) []string {
	if def == "" {
		return nil
	}
	parts := strings.Split(def, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// parsePercent converts an EdgeTX percent integer string to [-1, 1].
// "-99" -> -0.99, "100" -> 1.0. Out-of-range clamps. Unparseable -> 0.
func parsePercent(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	v /= 100.0
	if v > 1.0 {
		v = 1.0
	}
	if v < -1.0 {
		v = -1.0
	}
	return v
}
