package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/web"
	"github.com/protomaps/go-pmtiles/pmtiles"
)

// Server hosts the HTTP + WebSocket API.
type Server struct {
	addr      string
	providers *Providers
	webDir    string // if non-empty, serve from filesystem instead of embed

	// Map tile config. mapTilesDir is the path to PMTiles files for
	// offline serving. onlineFallback controls whether to proxy to
	// public tile servers when local tiles aren't available.
	// pmSrv is the embedded protomaps PMTiles HTTP server, lazily
	// initialized on first tile request when mapTilesDir is set.
	// tilesetFiles maps URL tileset names ("osm", "satellite") to
	// PMTiles file basenames on disk ("sp-state-osm", "campinas-sat").
	// warmTilesDir, when non-empty, is the root of a flat directory
	// of recently-fetched tiles served in front of the PMTiles
	// archive. Populated by the internal/tilewarm subsystem.
	mapTilesDir    string
	warmTilesDir   string
	onlineFallback bool
	pmSrv          *pmtiles.Server
	pmSrvOnce      sync.Once
	tilesetFiles   map[string]string
	tilesetFilesMu sync.RWMutex

	mu        sync.RWMutex
	httpSrv   *http.Server
	hub       *hub
	startedAt time.Time
}

// NewServer constructs an API server that will bind to addr (typically
// "127.0.0.1:8080"). Providers must have all callbacks set.
func NewServer(addr string, providers *Providers) *Server {
	return &Server{
		addr:           addr,
		providers:      providers,
		onlineFallback: true,
	}
}

// SetWebDir overrides the embedded web GUI with files from a filesystem
// path. Used during development for fast iteration without rebuilding the
// daemon. Empty string (the default) uses the embedded FS.
func (s *Server) SetWebDir(dir string) {
	s.webDir = dir
}

// Run starts the HTTP listener and the WebSocket broadcast loop. Blocks
// until ctx is cancelled. Run in a goroutine.
func (s *Server) Run(ctx context.Context) error {
	s.mu.Lock()
	s.startedAt = time.Now()
	s.hub = newHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/model", s.handleModel)
	mux.HandleFunc("/api/v1/model/details", s.handleModelDetails)
	mux.HandleFunc("/api/v1/model/image", s.handleModelImage)
	mux.HandleFunc("/api/v1/state", s.handleState)
	mux.HandleFunc("/api/v1/stream", s.handleStream)
	mux.HandleFunc("/api/v1/logs", s.handleLogs)
	mux.HandleFunc("/api/v1/preflight", s.handlePreflight)
	mux.HandleFunc("/api/v1/syscheck/dismiss", s.handleSyscheckDismiss)
	mux.HandleFunc("/api/v1/telemetry", s.handleTelemetry)
	mux.HandleFunc("/api/v1/audio", s.handleAudio)
	mux.HandleFunc("/api/v1/audio/threshold", s.handleAudioThreshold)
	mux.HandleFunc("/api/v1/audio/acknowledge", s.handleAudioAcknowledge)
	mux.HandleFunc("/api/v1/debug/speak", s.handleDebugSpeak)
	mux.HandleFunc("/api/v1/debug/flight-events", s.handleDebugFlightEvents)
	mux.HandleFunc("/api/v1/narrate", s.handleNarrate)
	mux.HandleFunc("/api/v1/recordings", s.handleRecordings)
	mux.HandleFunc("/api/v1/recordings/summary", s.handleRecordingSummary)
	mux.HandleFunc("/api/v1/recordings/detail", s.handleRecordingDetail)
	mux.HandleFunc("/api/v1/recordings/preserve", s.handleRecordingPreserve)
	mux.HandleFunc("/api/v1/recordings/unpreserve", s.handleRecordingUnpreserve)
	mux.HandleFunc("/api/v1/replay/status", s.handleReplayStatus)
	mux.HandleFunc("/api/v1/replay/start", s.handleReplayStart)
	mux.HandleFunc("/api/v1/replay/stop", s.handleReplayStop)
	mux.HandleFunc("/api/v1/model/load", s.handleModelLoad)
	mux.HandleFunc("/api/v1/model/unload", s.handleModelUnload)
	mux.HandleFunc("/api/v1/models", s.handleModels)
	mux.HandleFunc("/api/v1/joysticks", s.handleJoysticks)
	mux.HandleFunc("/api/v1/joystick/select", s.handleJoystickSelect)
	mux.HandleFunc("/api/v1/joystick/release", s.handleJoystickRelease)
	mux.HandleFunc("/api/v1/arm", s.handleArm)
	mux.HandleFunc("/api/v1/arm/confirm", s.handleArmConfirm)
	mux.HandleFunc("/api/v1/arm/checklist", s.handleArmChecklist)
	mux.HandleFunc("/api/v1/weather", s.handleWeather)
	mux.HandleFunc("/api/v1/netclass", s.handleNetClass)
	mux.HandleFunc("/api/v1/recovery", s.handleRecovery)
	mux.HandleFunc("/api/v1/recovery/trigger", s.handleRecoveryTrigger)
	mux.HandleFunc("/api/v1/recovery/dismiss", s.handleRecoveryDismiss)
	mux.HandleFunc("/api/v1/metrics", s.handleMetrics)
	mux.HandleFunc("/metrics", s.handleMetrics)

	// Map tile serving. /tiles/{tileset}/{z}/{x}/{y}.{ext}
	mux.HandleFunc("/tiles/", s.handleTile)

	// Static map assets served from mapTilesDir for fully-offline operation.
	// The map page (web/map/index.html) carries an inline style; only the
	// glyph fonts referenced by that style need to be served from disk.
	//   fonts/{fontstack}/{range}.pbf -> /fonts/{fontstack}/{range}.pbf
	if s.mapTilesDir != "" {
		mapAssets := http.FileServer(http.Dir(s.mapTilesDir))
		mux.Handle("/fonts/", mapAssets)
	}

	// Static GUI at /. The embed.FS path is rooted; for dev iteration,
	// SetWebDir bypasses it.
	if s.webDir != "" {
		log.Printf("api: serving web gui from %s", s.webDir)
		mux.Handle("/", http.FileServer(http.Dir(s.webDir)))
	} else {
		// Embedded FS has files at the root.
		webFS, err := fs.Sub(web.FS, ".")
		if err != nil {
			return fmt.Errorf("web fs: %w", err)
		}
		mux.Handle("/", http.FileServer(http.FS(webFS)))
	}

	s.httpSrv = &http.Server{
		Addr:              s.addr,
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.mu.Unlock()

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("api listen: %w", err)
	}
	log.Printf("api: listening on http://%s", ln.Addr().String())

	// Run the broadcast loop in its own goroutine.
	go s.broadcastLoop(ctx)

	// Run HTTP serve. Shutdown gracefully on ctx done.
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpSrv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// broadcastLoop pushes state to all WS clients at ~10Hz.
func (s *Server) broadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.hub.closeAll()
			return
		case <-ticker.C:
			if s.hub.clientCount() == 0 {
				continue // no clients, skip the snapshot work
			}
			payload, err := json.Marshal(s.providers.snapshot())
			if err != nil {
				log.Printf("api: marshal state: %v", err)
				continue
			}
			s.hub.broadcast(payload)
		}
	}
}

// --- HTTP handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{
		Version:      s.providers.Version,
		Uptime:       s.providers.Uptime().String(),
		LinkState:    s.providers.Link().State,
		ModelLoaded:  s.providers.Model().Available,
		JoystickOpen: s.providers.Joystick() != nil,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.providers.Model())
}

func (s *Server) handleModelDetails(w http.ResponseWriter, r *http.Request) {
	if s.providers.ModelDetails == nil {
		writeJSON(w, http.StatusOK, ModelDetails{Available: false})
		return
	}
	writeJSON(w, http.StatusOK, s.providers.ModelDetails())
}

func (s *Server) handleModelImage(w http.ResponseWriter, r *http.Request) {
	if s.providers.ModelImagePath == nil {
		http.NotFound(w, r)
		return
	}
	path := s.providers.ModelImagePath()
	if path == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "max-age=300")
	http.ServeFile(w, r, path)
}

// handleLogs returns log entries since the optional ?since=<RFC3339Nano>
// query param. With no since param, returns the buffer's full contents
// (capped at the buffer's capacity, set in main).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.providers.Logs == nil {
		writeJSON(w, http.StatusOK, LogsResponse{Entries: []LogEntry{}})
		return
	}
	var since time.Time
	if q := r.URL.Query().Get("since"); q != "" {
		if t, err := time.Parse(time.RFC3339Nano, q); err == nil {
			since = t
		}
	}
	entries := s.providers.Logs(since)
	if entries == nil {
		entries = []LogEntry{}
	}
	writeJSON(w, http.StatusOK, LogsResponse{Entries: entries})
}

// handlePreflight returns the aggregate readiness snapshot.
func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if s.providers.Preflight == nil {
		writeJSON(w, http.StatusOK, Preflight{State: "idle"})
		return
	}
	writeJSON(w, http.StatusOK, s.providers.Preflight())
}

// handleSyscheckDismiss flips the operator-acknowledgement gate.
// POST-only; no body required. Returns 204 on success. Idempotent
// at the daemon level (a second dismiss after the first is a no-op
// and does not change the dismissedAt timestamp).
//
// Returns 409 Conflict if the daemon's preflight check is not yet
// Ready (e.g. a blocking device is down). This is the server-side
// backstop for the status page's button-disabling behavior: even if
// a stale browser somehow sends a dismiss when the page should have
// the button greyed out, the daemon refuses. The 409 body contains
// the same blockers list the page would have shown.
func (s *Server) handleSyscheckDismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.SyscheckDismiss == nil {
		http.Error(w, "syscheck not configured", http.StatusNotImplemented)
		return
	}
	// Preflight check. If the daemon publishes a Preflight provider,
	// consult it before allowing the dismiss. The previous behavior
	// was unconditional dismiss; this gate adds the device-down
	// rejection while preserving the no-Preflight-provider case
	// (mock servers, tests) which falls through to dismiss as before.
	if s.providers.Preflight != nil {
		pf := s.providers.Preflight()
		if !pf.Ready {
			writeJSON(w, http.StatusConflict, struct {
				Error    string   `json:"error"`
				Blockers []string `json:"blockers"`
			}{
				Error:    "preflight not ready",
				Blockers: pf.Blockers,
			})
			return
		}
	}
	s.providers.SyscheckDismiss()
	w.WriteHeader(http.StatusNoContent)
}

// handleTelemetry returns the current FC telemetry snapshot. Empty
// object when no telemetry has ever been received (operator may be
// flying without telemetry, or the link hasn't carried any yet).
func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	if s.providers.Telemetry == nil {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	t := s.providers.Telemetry()
	if t == nil {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleAudio returns the current audio subsystem state.
func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if s.providers.Audio == nil {
		writeJSON(w, http.StatusOK, AudioInfo{Threshold: "notice"})
		return
	}
	writeJSON(w, http.StatusOK, s.providers.Audio())
}

// handleAudioThreshold accepts {"level": "warning"} and updates the
// daemon's audio threshold. Returns 400 on invalid level.
func (s *Server) handleAudioThreshold(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if s.providers.SetAudioThreshold == nil {
		http.Error(w, "audio not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.providers.SetAudioThreshold(body.Level); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAudioAcknowledge accepts either {"name": "bat-low"} (single)
// or {"all": true} (all alarms).
func (s *Server) handleAudioAcknowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string `json:"name"`
		All  bool   `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.All {
		if s.providers.AcknowledgeAll != nil {
			s.providers.AcknowledgeAll()
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if body.Name == "" {
		http.Error(w, "name or all required", http.StatusBadRequest)
		return
	}
	if s.providers.Acknowledge != nil {
		s.providers.Acknowledge(body.Name)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDebugSpeak accepts {"text": "...", "level": "info|notice|warning|critical"}
// and runs it through the TTS engine. Useful for verifying voice
// quality, cache behaviour, and end-to-end TTS plumbing without
// needing a live arm event. Returns 202 (queued) immediately;
// synthesis and playback happen asynchronously on the player's
// worker goroutine. Returns 503 if TTS isn't configured.
func (s *Server) handleDebugSpeak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.Speak == nil {
		http.Error(w, "TTS not configured (start daemon with -piper-binary)", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Text  string `json:"text"`
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	if body.Level == "" {
		body.Level = "notice"
	}
	s.providers.Speak(body.Text, body.Level)
	w.WriteHeader(http.StatusAccepted)
}

// handleDebugFlightEvents returns the events logged for the current
// armed session, or empty if not armed / pre-arm. Useful for
// debugging the flight-event detector without requiring a saved
// recording. Returns the raw event list as JSON.
func (s *Server) handleDebugFlightEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.FlightEvents == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	evs, err := s.providers.FlightEvents()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if evs == nil {
		evs = []interface{}{}
	}
	writeJSON(w, http.StatusOK, evs)
}

// handleNarrate returns or updates the periodic-narration config.
//
//	GET  -> {"interval": "60s", "fields": ["battery","distance"]}
//	POST -> same body shape; on success, persisted to disk and
//	        applied immediately. Validation errors are 400 with a
//	        message; everything else is 500.
func (s *Server) handleNarrate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.providers.NarrateConfig == nil {
			writeJSON(w, http.StatusOK, NarrateConfig{Interval: "0s", Fields: []string{}})
			return
		}
		cfg := s.providers.NarrateConfig()
		if cfg.Fields == nil {
			cfg.Fields = []string{}
		}
		writeJSON(w, http.StatusOK, cfg)
	case http.MethodPost:
		if s.providers.NarrateConfigSet == nil {
			http.Error(w, "narration config not available", http.StatusServiceUnavailable)
			return
		}
		var body NarrateConfig
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.providers.NarrateConfigSet(body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

// handleRecordings lists saved flight recordings on disk. Newest
// first. Empty array when recording is disabled or no flights have
// been recorded yet.
func (s *Server) handleRecordings(w http.ResponseWriter, r *http.Request) {
	if s.providers.Recordings == nil {
		writeJSON(w, http.StatusOK, []Recording{})
		return
	}
	recs, err := s.providers.Recordings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if recs == nil {
		recs = []Recording{}
	}
	writeJSON(w, http.StatusOK, recs)
}

// handleRecordingSummary returns aggregate stats for a saved
// recording. Query parameter ?name=<basename> selects the file.
// 404 if the file is missing; 400 if name is empty.
func (s *Server) handleRecordingSummary(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if s.providers.Summarize == nil {
		http.Error(w, "summarize not configured", http.StatusServiceUnavailable)
		return
	}
	out, err := s.providers.Summarize(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRecordingDetail returns the full content of a saved
// recording: session metadata, every event, every telemetry sample.
// Replay UI's data source. Query parameter ?name=<basename>.
// Response sizes are typically 150-200 KB for a 10-minute flight;
// the daemon ships the whole thing in one response (no pagination).
func (s *Server) handleRecordingDetail(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if s.providers.RecordingDetail == nil {
		http.Error(w, "recording detail not configured", http.StatusServiceUnavailable)
		return
	}
	out, err := s.providers.RecordingDetail(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRecordingPreserve marks a saved recording so the cleanup
// sweep skips it. Body: {"name": "<basename>"}. POST only.
// Idempotent: preserving an already-preserved recording overwrites
// the sidecar (effectively a no-op for the cleanup sweep). Returns
// 404 if the named recording does not exist, 400 if the name fails
// validation, 503 if the provider was not configured (recorder is
// the NoOpRecorder).
func (s *Server) handleRecordingPreserve(w http.ResponseWriter, r *http.Request) {
	s.handleRecordingPreserveSet(w, r, true)
}

// handleRecordingUnpreserve removes the .preserve sidecar from a
// saved recording. Body: {"name": "<basename>"}. POST only.
// Idempotent: unpreserving a non-preserved recording is a quiet
// success. Same error codes as handleRecordingPreserve.
func (s *Server) handleRecordingUnpreserve(w http.ResponseWriter, r *http.Request) {
	s.handleRecordingPreserveSet(w, r, false)
}

// handleRecordingPreserveSet is the shared body of the preserve /
// unpreserve handlers; the only behavioural difference is the
// preserve bool the provider gets passed.
func (s *Server) handleRecordingPreserveSet(w http.ResponseWriter, r *http.Request, preserve bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	prov := s.providers.RecordingPreserve
	if !preserve {
		prov = s.providers.RecordingUnpreserve
	}
	if prov == nil {
		http.Error(w, "recording preservation not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if err := prov(body.Name); err != nil {
		// fs.ErrNotExist from the recorder maps to 404; everything
		// else (name validation, write/remove I/O failure) is a 400
		// because the caller can usually fix it (wrong name, etc.).
		// Validation errors carry the name in the message so the
		// operator sees what got rejected.
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"preserved": preserve})
}

// handleReplayStatus returns the current replay session state.
// GET only. Always returns 200; check the Active field for truthiness.
func (s *Server) handleReplayStatus(w http.ResponseWriter, r *http.Request) {
	if s.providers.ReplaySnapshot == nil {
		writeJSON(w, http.StatusOK, ReplayInfo{Active: false})
		return
	}
	writeJSON(w, http.StatusOK, s.providers.ReplaySnapshot())
}

// handleReplayStart marks a replay session active. Body JSON: {name}.
// Refuses (409 Conflict) if the aircraft is armed -- replay must
// never run during a real flight. Refuses (409) if a different
// replay session is already active (the same name is idempotent).
// Returns 200 with the new snapshot on success.
func (s *Server) handleReplayStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.ReplayStart == nil {
		http.Error(w, "replay not configured", http.StatusNotImplemented)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if err := s.providers.ReplayStart(req.Name); err != nil {
		// The provider distinguishes armed-conflict and
		// already-active-different-name conflicts via the error;
		// the wire format is JSON with an 'error' field either way.
		// Status 409 because both are conflicts with current state.
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if s.providers.ReplaySnapshot != nil {
		writeJSON(w, http.StatusOK, s.providers.ReplaySnapshot())
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleReplayStop clears the replay session. POST-only. Idempotent
// at the daemon level; calling stop with no active session is a
// no-op and returns 204.
func (s *Server) handleReplayStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.ReplayStop == nil {
		http.Error(w, "replay not configured", http.StatusNotImplemented)
		return
	}
	s.providers.ReplayStop()
	w.WriteHeader(http.StatusNoContent)
}

// handleModelLoad parses a JSON body {"path": "..."} and asks the
// daemon to load the model. Atomic stack swap on success.
func (s *Server) handleModelLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.LoadModel == nil {
		http.Error(w, "model loading not supported on this daemon", http.StatusNotImplemented)
		return
	}
	var req LoadModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := s.providers.LoadModel(req.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "loaded"})
}

// handleModelUnload tears down the active stack and goes IDLE.
func (s *Server) handleModelUnload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.UnloadModel == nil {
		http.Error(w, "model unloading not supported on this daemon", http.StatusNotImplemented)
		return
	}
	s.providers.UnloadModel()
	writeJSON(w, http.StatusOK, map[string]string{"status": "unloaded"})
}

// handleModels returns the list of *.yml files in the directory given
// by the ?dir= query param. Each entry is parsed for its model name on
// a best-effort basis. Subdirectories are not traversed.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.providers.ListModels == nil {
		writeJSON(w, http.StatusOK, []ModelFile{})
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		http.Error(w, "?dir= is required", http.StatusBadRequest)
		return
	}
	entries, err := s.providers.ListModels(dir)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []ModelFile{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleJoysticks returns the currently-connected SDL joystick devices.
func (s *Server) handleJoysticks(w http.ResponseWriter, r *http.Request) {
	if s.providers.Joysticks == nil {
		writeJSON(w, http.StatusOK, []JoystickDevice{})
		return
	}
	devs := s.providers.Joysticks()
	if devs == nil {
		devs = []JoystickDevice{}
	}
	writeJSON(w, http.StatusOK, devs)
}

// handleJoystickSelect opens the joystick at the requested index and
// installs it as the active device. Refuses to swap during an active
// flight unless emergency=true.
func (s *Server) handleJoystickSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.SelectJoystick == nil {
		http.Error(w, "joystick selection not supported on this daemon", http.StatusNotImplemented)
		return
	}
	var req SelectJoystickRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.providers.SelectJoystick(req.Index, req.Emergency); err != nil {
		// 409 Conflict is the right code when state forbids the swap.
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "selected"})
}

// handleJoystickRelease closes the active joystick. Subject to the same
// armed-for-flight protection.
func (s *Server) handleJoystickRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.ReleaseJoystick == nil {
		http.Error(w, "joystick release not supported on this daemon", http.StatusNotImplemented)
		return
	}
	if err := s.providers.ReleaseJoystick(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

// handleArm returns the current arm state machine snapshot (state +
// inputs). GET only. Returns 501 if the daemon doesn't have an arm
// machine wired up.
func (s *Server) handleArm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.Arm == nil {
		http.Error(w, "arm state not supported on this daemon", http.StatusNotImplemented)
		return
	}
	writeJSON(w, http.StatusOK, s.providers.Arm())
}

// handleArmConfirm fires the operator's confirm action. POST only.
// Body is empty; the act of POSTing IS the confirm. The state
// machine accepts confirms silently if not in ARMING_REQUESTED, so
// callers shouldn't expect this endpoint to return state — they
// should subscribe to arm events for the actual outcome.
func (s *Server) handleArmConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.ArmConfirm == nil {
		http.Error(w, "arm confirm not supported on this daemon", http.StatusNotImplemented)
		return
	}
	s.providers.ArmConfirm()
	writeJSON(w, http.StatusOK, map[string]string{"ok": "confirm"})
}

// handleArmChecklist updates the operator-checklist gate on the arm
// state machine. POST { "ok": bool }. The default at boot is false:
// arming is denied until something explicitly says the checklist is
// satisfied (or the operator has opted out of the checklist
// policy entirely, in which case the GUI sends true).
func (s *Server) handleArmChecklist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.ArmChecklist == nil {
		http.Error(w, "arm checklist not supported on this daemon", http.StatusNotImplemented)
		return
	}
	var req ArmChecklistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.providers.ArmChecklist(req.Ok)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": req.Ok})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.providers.snapshot())
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	conn, err := upgradeWS(w, r)
	if err != nil {
		// upgradeWS already wrote a 400 response.
		return
	}
	s.hub.addClient(conn)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("api: write json: %v", err)
	}
}

// withCORS adds permissive CORS headers. The API is localhost-only by
// default, but a GUI served from a different origin (file://, dev
// server) needs CORS to talk to it.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
