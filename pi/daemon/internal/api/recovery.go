package api

import (
	"encoding/json"
	"net/http"

	"github.com/agoliveira/zerotx/pi/daemon/internal/recovery"
)

// handleRecovery serves GET /api/v1/recovery.
//
// Returns the current recovery state. When Active=false the response
// is still a well-formed JSON object (with frozen + operator fields
// zero/none) so clients can poll for state transitions without
// special-casing 404. When the subsystem is disabled entirely
// (Providers.Recovery is nil), returns 404 to make the disabled
// state explicit.
func (s *Server) handleRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.Recovery == nil {
		http.Error(w, "recovery subsystem disabled", http.StatusNotFound)
		return
	}
	state := s.providers.Recovery()
	if state == nil {
		// Should not happen in practice (the provider always returns
		// a value), but tolerate it gracefully.
		writeJSON(w, http.StatusOK, recovery.State{Operator: recovery.OperatorPosition{Source: "none"}})
		return
	}
	writeJSON(w, http.StatusOK, *state)
}

// handleRecoveryTrigger serves POST /api/v1/recovery/trigger.
//
// Manual trigger from the GUI. Builds the frozen snapshot from the
// current telemetry view (we'd rather not let the GUI choose what
// to freeze; the snapshot is the daemon's truth at trigger time).
//
// The handler returns 200 with `{active: true, alreadyActive: bool}`.
// alreadyActive=true means the call was a no-op because recovery was
// already running -- not an error, just informational for the GUI.
func (s *Server) handleRecoveryTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.RecoveryTrigger == nil {
		http.Error(w, "recovery subsystem disabled", http.StatusNotFound)
		return
	}
	snap := buildManualSnapshot(s)
	activated := s.providers.RecoveryTrigger(snap)
	writeJSON(w, http.StatusOK, map[string]bool{
		"active":         true,
		"alreadyActive":  !activated,
	})
}

// handleRecoveryDismiss serves POST /api/v1/recovery/dismiss.
func (s *Server) handleRecoveryDismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.RecoveryDismiss == nil {
		http.Error(w, "recovery subsystem disabled", http.StatusNotFound)
		return
	}
	s.providers.RecoveryDismiss()
	writeJSON(w, http.StatusOK, map[string]bool{"active": false})
}

// buildManualSnapshot constructs a recovery.Snapshot from the
// daemon's current telemetry view. The api package treats
// providers.Telemetry as opaque (interface{}) so it doesn't import
// telemetry directly; we round-trip through JSON to extract the
// fields we need.
//
// This is a few cycles slower than a direct typed read but keeps
// the api package decoupled from telemetry types. Manual recovery
// triggers are rare (operator-driven, single-shot) so the cost
// doesn't matter.
//
// If the telemetry provider is absent or fields are missing, the
// returned snapshot has HasGPS=false; the recovery view still
// activates but the kiosk shows "no aircraft position known" and
// the operator falls back to whatever last-known map view they had.
func buildManualSnapshot(s *Server) recovery.Snapshot {
	out := recovery.Snapshot{Mode: "MANUAL"}
	if s.providers.Telemetry == nil {
		return out
	}
	raw := s.providers.Telemetry()
	if raw == nil {
		return out
	}
	// Round-trip via JSON. The telemetry.Snapshot shape is documented
	// in pi/daemon/internal/telemetry/telemetry.go; field names below
	// match its `json:` tags.
	blob, err := json.Marshal(raw)
	if err != nil {
		return out
	}
	var probe struct {
		GPS *struct {
			Data struct {
				LatDeg     float64 `json:"latDeg"`
				LonDeg     float64 `json:"lonDeg"`
				GroundKmh  float64 `json:"groundKmh"`
				HeadingDeg float64 `json:"headingDeg"`
				AltMeters  int32   `json:"altMeters"`
				Sats       uint8   `json:"sats"`
			} `json:"data"`
			Stale bool `json:"stale"`
		} `json:"gps,omitempty"`
		FlightMode *struct {
			Data struct {
				Mode string `json:"mode"`
			} `json:"data"`
		} `json:"flightMode,omitempty"`
	}
	if err := json.Unmarshal(blob, &probe); err != nil {
		return out
	}
	if probe.GPS != nil && !probe.GPS.Stale {
		g := probe.GPS.Data
		out.LatDeg = g.LatDeg
		out.LonDeg = g.LonDeg
		out.AltMeters = g.AltMeters
		out.GroundKmh = g.GroundKmh
		out.HeadingDeg = g.HeadingDeg
		out.HasGPS = g.Sats >= 4 && (g.LatDeg != 0 || g.LonDeg != 0)
	}
	if probe.FlightMode != nil && probe.FlightMode.Data.Mode != "" {
		out.Mode = probe.FlightMode.Data.Mode
	}
	return out
}
