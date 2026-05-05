package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// printText writes the human-readable summary. Layout uses simple
// section headers and aligned columns; no terminal escapes so output
// pipes cleanly to files.
func printText(w io.Writer, s Summary) {
	fmt.Fprintf(w, "=== FLIGHT SUMMARY ===\n")
	fmt.Fprintf(w, "File:     %s\n", s.File)
	if s.Session.Model != "" {
		fmt.Fprintf(w, "Model:    %s\n", s.Session.Model)
	}
	if !s.Session.StartedAt.IsZero() {
		fmt.Fprintf(w, "Started:  %s\n", s.Session.StartedAt.Local().Format("2006-01-02 15:04:05 MST"))
	}
	if !s.Session.EndedAt.IsZero() {
		fmt.Fprintf(w, "Ended:    %s\n", s.Session.EndedAt.Local().Format("2006-01-02 15:04:05 MST"))
	}
	if s.Duration > 0 {
		fmt.Fprintf(w, "Duration: %s\n", formatDuration(s.Duration))
	}
	if s.Session.Notes != "" {
		fmt.Fprintf(w, "Notes:    %s\n", s.Session.Notes)
	}

	fmt.Fprintf(w, "\n=== TELEMETRY ===\n")
	if s.Stats.SampleCount == 0 {
		fmt.Fprintln(w, "(no telemetry samples)")
	} else {
		fmt.Fprintf(w, "Samples:  %d\n", s.Stats.SampleCount)
		printBattery(w, s.Stats)
		printGPS(w, s.Stats)
		printLink(w, s.Stats)
	}

	fmt.Fprintf(w, "\n=== MODES ===\n")
	if len(s.Modes) == 0 {
		fmt.Fprintln(w, "(no flight-mode reports)")
	} else {
		for _, m := range s.Modes {
			fmt.Fprintf(w, "  T+%-7s  %-12s  %s\n",
				formatOffset(m.Start), m.Mode, formatDuration(m.Duration))
		}
	}

	fmt.Fprintf(w, "\n=== EVENTS ===\n")
	if len(s.Events) == 0 {
		fmt.Fprintln(w, "(no events)")
	} else {
		for _, e := range s.Events {
			printEvent(w, e)
		}
	}

	fmt.Fprintf(w, "\n=== ALERTS ===\n")
	if len(s.Alerts) == 0 {
		fmt.Fprintln(w, "(no alerts)")
	} else {
		for _, e := range s.Alerts {
			printEvent(w, e)
		}
	}
}

func printBattery(w io.Writer, st TelemetryStats) {
	if st.BatVoltsStart != nil && st.BatVoltsEnd != nil {
		fmt.Fprintf(w, "Battery:  %.2fV -> %.2fV (%+.2fV)",
			*st.BatVoltsStart, *st.BatVoltsEnd, *st.BatVoltsEnd-*st.BatVoltsStart)
		if st.BatVoltsMin != nil {
			fmt.Fprintf(w, "  sag-min %.2fV", *st.BatVoltsMin)
		}
		fmt.Fprintln(w)
	}
	if st.BatAmpsPeak != nil {
		fmt.Fprintf(w, "          peak %.1fA", *st.BatAmpsPeak)
		if st.BatMAhEnd != nil {
			fmt.Fprintf(w, ", consumed %d mAh", *st.BatMAhEnd)
		}
		if st.BatPctEnd != nil {
			fmt.Fprintf(w, " (%d%% remaining)", *st.BatPctEnd)
		}
		fmt.Fprintln(w)
	}
}

func printGPS(w io.Writer, st TelemetryStats) {
	parts := []string{}
	if st.GpsAltMaxM != nil {
		parts = append(parts, fmt.Sprintf("max-alt %dm", *st.GpsAltMaxM))
	}
	if st.GpsKmhMax != nil {
		parts = append(parts, fmt.Sprintf("max-spd %.1f km/h", *st.GpsKmhMax))
	}
	if st.GpsSatsMax != nil {
		parts = append(parts, fmt.Sprintf("max-sats %d", *st.GpsSatsMax))
	}
	if len(parts) > 0 {
		fmt.Fprintf(w, "GPS:      %s\n", strings.Join(parts, ", "))
	}
	if st.GpsLatStart != nil && st.GpsLonStart != nil {
		fmt.Fprintf(w, "          start  %.6f, %.6f\n", *st.GpsLatStart, *st.GpsLonStart)
	}
	if st.GpsLatEnd != nil && st.GpsLonEnd != nil {
		fmt.Fprintf(w, "          end    %.6f, %.6f\n", *st.GpsLatEnd, *st.GpsLonEnd)
	}
}

func printLink(w io.Writer, st TelemetryStats) {
	parts := []string{}
	if st.LinkLQMin != nil {
		parts = append(parts, fmt.Sprintf("LQ-min %d%%", *st.LinkLQMin))
	}
	if st.LinkRssiMax != nil {
		parts = append(parts, fmt.Sprintf("RSSI-max %d dBm", *st.LinkRssiMax))
	}
	if len(parts) > 0 {
		fmt.Fprintf(w, "Link:     %s\n", strings.Join(parts, ", "))
	}
}

func printEvent(w io.Writer, e EventRow) {
	level := e.Level
	if level == "" {
		level = "-"
	}
	name := e.Name
	if e.Kind != "" && name != "" {
		name = e.Kind + "/" + name
	} else if name == "" {
		name = e.Kind
	}
	fmt.Fprintf(w, "  T+%-7s  %-9s  %s",
		formatOffset(e.Offset), level, name)
	if e.Detail != nil {
		fmt.Fprintf(w, "  %s", formatDetail(e.Detail))
	}
	fmt.Fprintln(w)
}

// formatDuration renders e.g. 6m12s for log-friendly output.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh%02dm", h, m)
}

// formatOffset is duration-since-arm in m:ss format. Always at least
// m:ss for column alignment.
func formatOffset(d time.Duration) string {
	d = d.Round(time.Second)
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%d:%02d", m, s)
}

// formatDetail renders an event's JSON detail compactly. Map values
// become "k=v" pairs separated by spaces; everything else just
// JSON-encodes.
func formatDetail(detail interface{}) string {
	switch v := detail.(type) {
	case map[string]interface{}:
		var parts []string
		for k, val := range v {
			parts = append(parts, fmt.Sprintf("%s=%v", k, val))
		}
		return strings.Join(parts, " ")
	default:
		b, err := json.Marshal(detail)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
