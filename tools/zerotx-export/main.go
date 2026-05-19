package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AltMode controls how altitude is reported in the export.
type AltMode int

const (
	// AltRelative subtracts the first valid GPS sample's altitude
	// from every emitted ele/coordinate. Useful in Google Earth
	// where the terrain mesh already handles ground elevation;
	// emitting MSL would float the trail above the actual terrain.
	// Default mode.
	AltRelative AltMode = iota

	// AltAbsolute emits gps_alt as-is. Use this when feeding the
	// file to a tool that expects true MSL (e.g. for hazard or
	// terrain-clearance analysis).
	AltAbsolute
)

// Apply returns the altitude value to emit, given the raw sample
// alt and the flight's ground reference.
func (m AltMode) Apply(rawAltMeters, groundAlt int32) int32 {
	if m == AltAbsolute {
		return rawAltMeters
	}
	return rawAltMeters - groundAlt
}

// KMLMode returns the string KML uses in <altitudeMode>.
func (m AltMode) KMLMode() string {
	if m == AltAbsolute {
		return "absolute"
	}
	return "relativeToGround"
}

func describeAltitudeMode(m AltMode) string {
	if m == AltAbsolute {
		return "Altitude: MSL (raw GPS, meters above mean sea level)."
	}
	return "Altitude: meters above takeoff point (first valid GPS sample)."
}

// describeFlightName produces a human label for the metadata <name>
// element. Format: "YYYY-MM-DD HH:MM ModelName".
func describeFlightName(f *Flight) string {
	when := f.Started.In(time.Local).Format("2006-01-02 15:04")
	if f.ModelName == "" {
		return "ZeroTX flight " + when
	}
	return f.ModelName + " — " + when
}

func main() {
	in := flag.String("in", "", "input recording (.db file from zerotxd recorder)")
	out := flag.String("out", "", "output path. Empty = stdout. Extension picks format if -format unset.")
	format := flag.String("format", "", "output format: gpx | kml. Empty = inferred from -out extension; gpx if both empty.")
	altitudeStr := flag.String("altitude", "relative", "altitude reference: relative | msl")
	flag.Parse()

	if *in == "" {
		log.Fatal("missing required -in <recording.db>")
	}

	altMode, err := parseAltitude(*altitudeStr)
	if err != nil {
		log.Fatal(err)
	}

	fmtStr := strings.ToLower(*format)
	if fmtStr == "" {
		if *out != "" {
			fmtStr = inferFormatFromPath(*out)
		}
		if fmtStr == "" {
			fmtStr = "gpx"
		}
	}
	if fmtStr != "gpx" && fmtStr != "kml" {
		log.Fatalf("unknown format %q (want gpx or kml)", fmtStr)
	}

	outPath := *out
	if outPath == "" && !isTerminal() {
		// Writing to a pipe or redirect: stdout.
		outPath = "-"
	}
	if outPath == "" {
		// Interactive: default to <input>.<format> next to the input.
		base := strings.TrimSuffix(*in, filepath.Ext(*in))
		outPath = base + "." + fmtStr
	}

	flight, err := ExtractFlight(*in)
	if err != nil {
		log.Fatalf("extract: %v", err)
	}
	log.Printf("extracted: session=%d samples=%d waypoints=%d started=%s",
		flight.SessionID, len(flight.Track), len(flight.Waypoints),
		flight.Started.In(time.Local).Format(time.RFC3339))

	w, closer, err := openOutput(outPath)
	if err != nil {
		log.Fatalf("open output: %v", err)
	}
	defer closer()

	switch fmtStr {
	case "gpx":
		err = WriteGPX(w, flight, altMode)
	case "kml":
		err = WriteKML(w, flight, altMode)
	}
	if err != nil {
		log.Fatalf("write %s: %v", fmtStr, err)
	}

	if outPath != "-" {
		log.Printf("wrote %s", outPath)
	}
}

func parseAltitude(s string) (AltMode, error) {
	switch strings.ToLower(s) {
	case "relative", "rel", "agl":
		return AltRelative, nil
	case "msl", "absolute", "abs":
		return AltAbsolute, nil
	default:
		return 0, fmt.Errorf("unknown -altitude %q (want relative or msl)", s)
	}
}

func inferFormatFromPath(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".gpx":
		return "gpx"
	case ".kml":
		return "kml"
	}
	return ""
}

func openOutput(path string) (io.Writer, func(), error) {
	if path == "-" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// isTerminal reports whether stdout is attached to a terminal. We
// don't import golang.org/x/term to keep dependencies minimal; the
// stat-based heuristic catches the common cases (interactive vs
// pipe/redirect).
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
