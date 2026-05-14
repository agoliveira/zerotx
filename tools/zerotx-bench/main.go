// zerotx-bench: hardware diagnostic and testing tool. Bench-only,
// never deployed in the field. Probes every device the Pi 400 can
// talk to (MCUs over USB-CDC, breakout-board peripherals over I2C/
// UART/GPIO, USB joystick, audio, HDMI, ELRS-via-RP2040), runs
// interactive tests, and exports a baseline YAML for the runtime
// self-check (separate tool, separate scope).
//
// Coexistence: refuses to start if zerotxd is detected running on
// the same machine. The MCU probes need exclusive USB-CDC access
// and there's no clean way to share. The check is conservative --
// false-positive (refusing to start when daemon isn't really
// running) is an annoyance; false-negative (running concurrently
// with daemon) corrupts the channel buffer mid-flight prep.
//
// Network: by default the web UI binds 0.0.0.0:8081 so the
// operator can browse from a laptop on the same network. Use
// -bind 127.0.0.1:8081 for localhost-only access if the Pi is
// on an untrusted network.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed all:web
var webFS embed.FS

const benchPort = 8081

func main() {
	bind := flag.String("bind", fmt.Sprintf("0.0.0.0:%d", benchPort),
		"address and port to bind the web UI (use 127.0.0.1:PORT for localhost-only)")
	skipCoexistCheck := flag.Bool("skip-coexist-check", false,
		"start even if zerotxd appears to be running. Only use this if you know what you're doing.")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !*skipCoexistCheck {
		if running, reason := daemonRunningCheck(ctx); running {
			fmt.Fprintf(os.Stderr,
				"zerotx-bench: refusing to start: zerotxd is running (%s)\n\n"+
					"Stop the daemon first:\n"+
					"    sudo systemctl stop zerotxd\n\n"+
					"The bench tool needs exclusive USB-CDC access to the MCUs;\n"+
					"running both at once corrupts the channel buffer and may\n"+
					"cause unsafe arm-state transitions if an aircraft is connected.\n\n"+
					"If you are certain no aircraft is connected and you want to\n"+
					"start anyway, pass -skip-coexist-check.\n", reason)
			os.Exit(1)
		}
	}

	registry := NewRegistry()
	// Probes are registered in subsequent commits as their
	// implementations land. For now the registry stays empty and
	// the UI shows "no probes registered yet" -- exercising the
	// framework end-to-end without committing to any specific
	// probe.
	registerProbes(registry)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/probes", handleListProbes(registry))
	mux.HandleFunc("/api/probes/", handleProbeAction(registry))
	mux.HandleFunc("/api/probes/run-all", handleRunAll(registry))

	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("zerotx-bench: web assets missing: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(webRoot)))

	srv := &http.Server{
		Addr:              *bind,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Shutdown on SIGINT/SIGTERM. 5s grace for in-flight probes.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Printf("shutting down")
		cancel()
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		_ = srv.Shutdown(shCtx)
	}()

	log.Printf("zerotx-bench listening on %s", *bind)
	log.Printf("probes registered: %d", len(registry.List()))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}

// registerProbes is the single entry point where each probe type
// registers itself with the registry. Probes land in subsequent
// commits; this function grows as they're added.
func registerProbes(r *Registry) {
	// Breakout-board peripherals (commit B).
	r.Register(rtcProbe{})
	r.Register(gpsProbe{})
	r.Register(ledProbe{})

	// USB peripherals (commit C).
	r.Register(joystickProbe{})
	r.Register(audioProbe{})

	// Phase D: MCU probes (Mega, RP2040, ESP32, ELRS).
	// Phase E: HDMI displays + baseline export.
}

// --- HTTP handlers ---

// probeListItem is the per-probe summary returned by GET /api/probes.
type probeListItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	WiringRef  string `json:"wiringRef,omitempty"`
	Skipped    bool   `json:"skipped"`
	LastResult Result `json:"lastResult"`
	Tests      []testListItem `json:"tests,omitempty"`
}

type testListItem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func handleListProbes(r *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		probes := r.List()
		out := make([]probeListItem, 0, len(probes))
		for _, p := range probes {
			tests := p.Tests()
			ts := make([]testListItem, len(tests))
			for i, t := range tests {
				ts[i] = testListItem{ID: t.ID, Label: t.Label, Description: t.Description}
			}
			out = append(out, probeListItem{
				ID:         p.ID(),
				Name:       p.Name(),
				Category:   p.Category(),
				WiringRef:  p.WiringRef(),
				Skipped:    r.IsSkipped(p.ID()),
				LastResult: r.LastResult(p.ID()),
				Tests:      ts,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleProbeAction routes /api/probes/{id}/{action} requests:
//
//	POST .../run         - run the probe, return Result
//	POST .../skip        - toggle the skip flag (body: {"skip": bool})
//	POST .../tests/{tid} - run a named test action for this probe
func handleProbeAction(r *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// Strip the /api/probes/ prefix.
		rest := req.URL.Path[len("/api/probes/"):]
		if rest == "" || rest == "run-all" {
			http.NotFound(w, req)
			return
		}
		// Split into (probeID, action, ...rest)
		parts := splitPath(rest)
		if len(parts) < 2 {
			http.Error(w, "expected /api/probes/{id}/{action}", http.StatusBadRequest)
			return
		}
		id, action := parts[0], parts[1]
		switch action {
		case "run":
			if req.Method != http.MethodPost {
				http.Error(w, "POST required", http.StatusMethodNotAllowed)
				return
			}
			res := r.RunProbe(req.Context(), id)
			writeJSON(w, http.StatusOK, res)
		case "skip":
			if req.Method != http.MethodPost {
				http.Error(w, "POST required", http.StatusMethodNotAllowed)
				return
			}
			var body struct {
				Skip bool `json:"skip"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
				return
			}
			r.SetSkipped(id, body.Skip)
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "skipped": body.Skip})
		case "tests":
			if len(parts) < 3 {
				http.Error(w, "expected /api/probes/{id}/tests/{tid}", http.StatusBadRequest)
				return
			}
			handleTestRun(r, id, parts[2])(w, req)
		default:
			http.NotFound(w, req)
		}
	}
}

func handleTestRun(r *Registry, probeID, testID string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		p := r.Get(probeID)
		if p == nil {
			http.Error(w, "no such probe: "+probeID, http.StatusNotFound)
			return
		}
		var action *TestAction
		for i := range p.Tests() {
			t := p.Tests()[i]
			if t.ID == testID {
				action = &t
				break
			}
		}
		if action == nil {
			http.Error(w, "no such test: "+testID, http.StatusNotFound)
			return
		}
		start := time.Now()
		output, err := action.Run(req.Context())
		took := time.Since(start)
		resp := map[string]any{
			"output": output,
			"took":   took.String(),
		}
		if err != nil {
			resp["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleRunAll(r *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		probes := r.List()
		results := make(map[string]Result, len(probes))
		for _, p := range probes {
			results[p.ID()] = r.RunProbe(req.Context(), p.ID())
		}
		writeJSON(w, http.StatusOK, results)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// splitPath splits a URL tail on '/' filtering empty segments.
// Avoids strings.Split's empty-leading-or-trailing surprises.
func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			if i > start {
				out = append(out, p[start:i])
			}
			start = i + 1
		}
	}
	return out
}
