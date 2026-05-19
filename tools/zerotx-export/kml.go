package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// WriteKML serializes f as KML 2.2 to w. altMode controls altitude
// emission AND KML's <altitudeMode> tag (which Google Earth uses
// for the 3D extrusion ladder):
//
//   AltAbsolute  -> altitudeMode=absolute, ele uses raw gps_alt (MSL).
//                   Google Earth renders the trail at true sea-level
//                   altitude, which can look way too high above the
//                   terrain when the field is on a plateau.
//   AltRelative  -> altitudeMode=relativeToGround, ele uses
//                   gps_alt - GroundAlt. Google Earth renders the
//                   trail above the local terrain mesh.
func WriteKML(w io.Writer, f *Flight, altMode AltMode) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}

	k := kmlRoot{XMLNS: "http://www.opengis.net/kml/2.2"}
	k.Doc.Name = describeFlightName(f)
	k.Doc.Description = describeAltitudeMode(altMode)

	// Inline styles. Two: one for waypoints (yellow pin), one for
	// the track (red line, extruded down to ground to show altitude).
	k.Doc.Styles = []kmlStyle{
		{
			ID: "wp",
			IconStyle: &kmlIconStyle{
				Icon: kmlIcon{Href: "http://maps.google.com/mapfiles/kml/paddle/ylw-blank.png"},
			},
		},
		{
			ID: "track",
			LineStyle: &kmlLineStyle{
				Color: "ff3040ff", // KABGR: alpha=ff, B=30, G=40, R=ff -> red
				Width: 3,
			},
			PolyStyle: &kmlPolyStyle{
				Color: "4d3040ff", // semi-transparent red for the curtain
			},
		},
	}

	kmlAltMode := altMode.KMLMode()

	for _, wp := range f.Waypoints {
		k.Doc.Placemarks = append(k.Doc.Placemarks, kmlPlacemark{
			Name:      kmlWaypointLabel(wp),
			StyleURL:  "#wp",
			TimeStamp: &kmlTimeStamp{When: formatKMLTime(wp.Time)},
			Point: &kmlPoint{
				AltitudeMode: kmlAltMode,
				Coordinates:  kmlCoord(wp.LonDeg, wp.LatDeg, altMode.Apply(wp.AltMeters, f.GroundAlt)),
			},
		})
	}

	// The track itself. <gx:Track> is more accurate (one timestamp
	// per coordinate) but is an extension. Plain <LineString> is
	// universally supported; we trade off temporal animation in
	// Google Earth for portability.
	if len(f.Track) > 0 {
		coords := make([]string, 0, len(f.Track))
		for _, s := range f.Track {
			coords = append(coords, kmlCoord(s.LonDeg, s.LatDeg, altMode.Apply(s.AltMeters, f.GroundAlt)))
		}
		k.Doc.Placemarks = append(k.Doc.Placemarks, kmlPlacemark{
			Name:     "Flight track",
			StyleURL: "#track",
			LineString: &kmlLineString{
				Extrude:      1,
				Tessellate:   1,
				AltitudeMode: kmlAltMode,
				Coordinates:  strings.Join(coords, " "),
			},
		})
	}

	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(k); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// kmlCoord formats a single lon,lat,alt tuple. KML uses lon-first
// coordinates (the opposite of GPX/everyone-else).
func kmlCoord(lon, lat float64, alt int32) string {
	return fmt.Sprintf("%.7f,%.7f,%d", lon, lat, alt)
}

// formatKMLTime emits RFC3339 with local timezone offset. Same
// rationale as the GPX path: operator-readable.
func formatKMLTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(time.Local).Format(time.RFC3339)
}

func kmlWaypointLabel(w Waypoint) string {
	if w.Detail == "" {
		return w.Name
	}
	return fmt.Sprintf("%s (%s)", w.Name, w.Detail)
}

// --- XML structs ---

type kmlRoot struct {
	XMLName xml.Name `xml:"kml"`
	XMLNS   string   `xml:"xmlns,attr"`
	Doc     kmlDoc   `xml:"Document"`
}

type kmlDoc struct {
	Name        string         `xml:"name,omitempty"`
	Description string         `xml:"description,omitempty"`
	Styles      []kmlStyle     `xml:"Style"`
	Placemarks  []kmlPlacemark `xml:"Placemark"`
}

type kmlStyle struct {
	ID        string        `xml:"id,attr"`
	IconStyle *kmlIconStyle `xml:"IconStyle,omitempty"`
	LineStyle *kmlLineStyle `xml:"LineStyle,omitempty"`
	PolyStyle *kmlPolyStyle `xml:"PolyStyle,omitempty"`
}

type kmlIconStyle struct {
	Icon kmlIcon `xml:"Icon"`
}

type kmlIcon struct {
	Href string `xml:"href"`
}

type kmlLineStyle struct {
	Color string `xml:"color"`
	Width int    `xml:"width"`
}

type kmlPolyStyle struct {
	Color string `xml:"color"`
}

type kmlPlacemark struct {
	Name       string         `xml:"name,omitempty"`
	StyleURL   string         `xml:"styleUrl,omitempty"`
	TimeStamp  *kmlTimeStamp  `xml:"TimeStamp,omitempty"`
	Point      *kmlPoint      `xml:"Point,omitempty"`
	LineString *kmlLineString `xml:"LineString,omitempty"`
}

type kmlTimeStamp struct {
	When string `xml:"when"`
}

type kmlPoint struct {
	AltitudeMode string `xml:"altitudeMode,omitempty"`
	Coordinates  string `xml:"coordinates"`
}

type kmlLineString struct {
	Extrude      int    `xml:"extrude"`
	Tessellate   int    `xml:"tessellate"`
	AltitudeMode string `xml:"altitudeMode,omitempty"`
	Coordinates  string `xml:"coordinates"`
}
