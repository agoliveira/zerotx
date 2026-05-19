package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"time"
)

// WriteGPX serializes f as GPX 1.1 to w. altMode controls how
// altitudes are reported in <ele>: AltAbsolute = raw gps_alt (MSL),
// AltRelative = gps_alt - f.GroundAlt.
//
// GPX has no standard "altitude is relative" flag; the <ele>
// number's reference is whatever the writer puts there. We emit
// a <metadata><desc> note that names the reference, so analysts
// loading the file in foreign tools see what to expect.
func WriteGPX(w io.Writer, f *Flight, altMode AltMode) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	g := gpxRoot{
		Version: "1.1",
		Creator: "zerotx-export",
		XMLNS:   "http://www.topografix.com/GPX/1/1",
	}
	g.Metadata.Name = describeFlightName(f)
	g.Metadata.Desc = describeAltitudeMode(altMode)
	g.Metadata.Time = formatGPXTime(f.Started)

	// Waypoints first so they appear in the metadata-friendly
	// position. GPX tools display them as named points.
	for _, wp := range f.Waypoints {
		g.Waypoints = append(g.Waypoints, gpxWpt{
			Lat:  wp.LatDeg,
			Lon:  wp.LonDeg,
			Ele:  altMode.Apply(wp.AltMeters, f.GroundAlt),
			Time: formatGPXTime(wp.Time),
			Name: gpxWaypointLabel(wp),
		})
	}

	// One track with one segment containing every sample.
	trk := gpxTrk{Name: "Flight"}
	for _, s := range f.Track {
		trk.Seg.Points = append(trk.Seg.Points, gpxTrkpt{
			Lat:  s.LatDeg,
			Lon:  s.LonDeg,
			Ele:  altMode.Apply(s.AltMeters, f.GroundAlt),
			Time: formatGPXTime(s.Time),
		})
	}
	g.Trk = trk

	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(g); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// formatGPXTime emits time in RFC3339 with local timezone offset
// so the operator sees flight times in their own clock zone.
// GPX consumers all accept timezone offsets (the standard predates
// the "everything is UTC" convention).
func formatGPXTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(time.Local).Format(time.RFC3339)
}

// gpxWaypointLabel composes the operator-facing waypoint name,
// including any one-line extra from summarizeDetail.
func gpxWaypointLabel(w Waypoint) string {
	if w.Detail == "" {
		return w.Name
	}
	return fmt.Sprintf("%s (%s)", w.Name, w.Detail)
}

// --- XML structs. Field order in struct = order in output. ---

type gpxRoot struct {
	XMLName  xml.Name `xml:"gpx"`
	Version  string   `xml:"version,attr"`
	Creator  string   `xml:"creator,attr"`
	XMLNS    string   `xml:"xmlns,attr"`
	Metadata struct {
		Name string `xml:"name,omitempty"`
		Desc string `xml:"desc,omitempty"`
		Time string `xml:"time,omitempty"`
	} `xml:"metadata"`
	Waypoints []gpxWpt `xml:"wpt"`
	Trk       gpxTrk   `xml:"trk"`
}

type gpxWpt struct {
	Lat  float64 `xml:"lat,attr"`
	Lon  float64 `xml:"lon,attr"`
	Ele  int32   `xml:"ele,omitempty"`
	Time string  `xml:"time,omitempty"`
	Name string  `xml:"name,omitempty"`
}

type gpxTrk struct {
	Name string    `xml:"name"`
	Seg  gpxTrkseg `xml:"trkseg"`
}

type gpxTrkseg struct {
	Points []gpxTrkpt `xml:"trkpt"`
}

type gpxTrkpt struct {
	Lat  float64 `xml:"lat,attr"`
	Lon  float64 `xml:"lon,attr"`
	Ele  int32   `xml:"ele,omitempty"`
	Time string  `xml:"time,omitempty"`
}
