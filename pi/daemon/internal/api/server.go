package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/web"
)

// Server hosts the HTTP + WebSocket API.
type Server struct {
	addr      string
	providers *Providers
	webDir    string // if non-empty, serve from filesystem instead of embed

	mu       sync.RWMutex
	httpSrv  *http.Server
	hub      *hub
	startedAt time.Time
}

// NewServer constructs an API server that will bind to addr (typically
// "127.0.0.1:8080"). Providers must have all callbacks set.
func NewServer(addr string, providers *Providers) *Server {
	return &Server{
		addr:      addr,
		providers: providers,
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
	mux.HandleFunc("/api/v1/telemetry", s.handleTelemetry)
	mux.HandleFunc("/api/v1/audio", s.handleAudio)
	mux.HandleFunc("/api/v1/audio/threshold", s.handleAudioThreshold)
	mux.HandleFunc("/api/v1/audio/acknowledge", s.handleAudioAcknowledge)
	mux.HandleFunc("/api/v1/recordings", s.handleRecordings)
	mux.HandleFunc("/api/v1/model/load", s.handleModelLoad)
	mux.HandleFunc("/api/v1/model/unload", s.handleModelUnload)
	mux.HandleFunc("/api/v1/models", s.handleModels)
	mux.HandleFunc("/api/v1/joysticks", s.handleJoysticks)
	mux.HandleFunc("/api/v1/joystick/select", s.handleJoystickSelect)
	mux.HandleFunc("/api/v1/joystick/release", s.handleJoystickRelease)
	mux.HandleFunc("/api/v1/flight/arm", s.handleFlightArm)

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

// handleFlightArm flips the daemon's committed-to-flight state. Used by
// the pre-flight tab when the operator clicks "Ready to fly", and by the
// post-flight flow when landing is detected/confirmed.
func (s *Server) handleFlightArm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.providers.SetFlightArmed == nil {
		http.Error(w, "flight arm signal not supported on this daemon", http.StatusNotImplemented)
		return
	}
	var req ArmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.providers.SetFlightArmed(req.Armed)
	writeJSON(w, http.StatusOK, map[string]bool{"armed": req.Armed})
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
