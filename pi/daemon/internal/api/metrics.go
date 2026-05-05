// Package api: metrics endpoint.
//
// Exposes runtime state as Prometheus exposition format at both
// /metrics (Prometheus convention) and /api/v1/metrics (consistency
// with the rest of the API).
//
// Format reference: https://prometheus.io/docs/instrumenting/exposition_formats/
//
// The endpoint is unauthenticated, like the rest of the daemon's API.
// localhost-only by deployment convention. Curl-friendly for ad-hoc
// inspection; scraper-friendly for an eventual Prometheus server on
// stan.
//
// Design philosophy: pull values from existing subsystem accessors at
// request time. The daemon scale (hundreds of metrics, scraped every
// 30s at most) makes per-request computation trivially cheap. We
// avoid adding atomic counters to hot paths until a specific metric
// proves they're warranted.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"
)

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	wri := &metricsWriter{w: w}

	wri.writeProcess(s)
	wri.writeBuild(s)
	wri.writeArm(s)
	wri.writeRecorder(s)
	wri.writeAudio(s)
	wri.writeWeather(s)
	wri.writeAlerts(s)
	wri.writeNetClass(s)
	wri.writeTileWarm(s)
	wri.writeWS(s)
}

// metricsWriter is a tiny helper to format Prometheus exposition
// lines without a third-party library. Each "section" emits
// `# HELP` and `# TYPE` comments followed by the values.
type metricsWriter struct {
	w io.Writer
}

func (m *metricsWriter) help(name, help string)       { fmt.Fprintf(m.w, "# HELP %s %s\n", name, help) }
func (m *metricsWriter) typ(name, t string)           { fmt.Fprintf(m.w, "# TYPE %s %s\n", name, t) }
func (m *metricsWriter) gauge(name string, v float64) { fmt.Fprintf(m.w, "%s %s\n", name, formatFloat(v)) }
func (m *metricsWriter) gaugeI(name string, v int64)  { fmt.Fprintf(m.w, "%s %d\n", name, v) }
func (m *metricsWriter) blank()                       { fmt.Fprintln(m.w) }

// labeled emits `name{k1="v1",k2="v2"} value`. Labels are sorted by
// key for deterministic output (helpful when diffing scraped values
// or running tests).
func (m *metricsWriter) labeled(name string, labels map[string]string, v float64) {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%s=%q`, k, labels[k])
	}
	b.WriteByte('}')
	fmt.Fprintf(m.w, "%s %s\n", b.String(), formatFloat(v))
}

func formatFloat(v float64) string {
	// Prometheus accepts standard float notation; integer-valued
	// floats get printed without trailing .0 for readability.
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

// ----------------------------------------------------------------------------
// Process & runtime
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeProcess(s *Server) {
	uptime := time.Duration(0)
	if s.providers.Uptime != nil {
		uptime = s.providers.Uptime()
	}
	m.help("zerotx_uptime_seconds", "Daemon uptime in seconds")
	m.typ("zerotx_uptime_seconds", "gauge")
	m.gauge("zerotx_uptime_seconds", uptime.Seconds())
	m.blank()

	m.help("zerotx_goroutines", "Number of currently running goroutines")
	m.typ("zerotx_goroutines", "gauge")
	m.gaugeI("zerotx_goroutines", int64(runtime.NumGoroutine()))
	m.blank()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.help("zerotx_mem_heap_alloc_bytes", "Heap memory currently allocated in bytes")
	m.typ("zerotx_mem_heap_alloc_bytes", "gauge")
	m.gaugeI("zerotx_mem_heap_alloc_bytes", int64(ms.HeapAlloc))
	m.blank()

	m.help("zerotx_mem_sys_bytes", "Total memory obtained from the OS in bytes")
	m.typ("zerotx_mem_sys_bytes", "gauge")
	m.gaugeI("zerotx_mem_sys_bytes", int64(ms.Sys))
	m.blank()

	m.help("zerotx_gc_cycles_total", "Total GC cycles completed")
	m.typ("zerotx_gc_cycles_total", "counter")
	m.gaugeI("zerotx_gc_cycles_total", int64(ms.NumGC))
	m.blank()
}

// ----------------------------------------------------------------------------
// Build info (version exposed as a label so scrapers can carry it
// alongside flapping values)
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeBuild(s *Server) {
	m.help("zerotx_build_info", "Daemon build info; constant 1, version on label")
	m.typ("zerotx_build_info", "gauge")
	m.labeled("zerotx_build_info", map[string]string{"version": s.providers.Version}, 1)
	m.blank()
}

// ----------------------------------------------------------------------------
// Arm subsystem
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeArm(s *Server) {
	if s.providers.Arm == nil {
		return
	}
	snap := s.providers.Arm()
	// Snapshot is an opaque interface{} (avoid arm-package import in
	// the api package). Marshal to JSON to extract the state field
	// without depending on the concrete type.
	state := "unknown"
	if data, err := json.Marshal(snap); err == nil {
		var probe struct {
			State string `json:"state"`
		}
		if json.Unmarshal(data, &probe) == nil && probe.State != "" {
			state = probe.State
		}
	}
	m.help("zerotx_arm_state_info", "Current arm state; constant 1, state on label")
	m.typ("zerotx_arm_state_info", "gauge")
	m.labeled("zerotx_arm_state_info", map[string]string{"state": state}, 1)
	m.blank()
}

// ----------------------------------------------------------------------------
// Recorder
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeRecorder(s *Server) {
	// Recording is active iff the arm state is ARMED (or any other
	// state where the recorder is writing). FlightEvents returns
	// last-session events too, so it can't tell us "right now" on
	// its own. Gate via the arm state we already extracted.
	active := 0
	if s.providers.Arm != nil {
		snap := s.providers.Arm()
		if data, err := json.Marshal(snap); err == nil {
			var probe struct {
				State string `json:"state"`
			}
			if json.Unmarshal(data, &probe) == nil && probe.State == "ARMED" {
				active = 1
			}
		}
	}
	m.help("zerotx_recorder_session_active", "1 if a flight recording is in progress, 0 otherwise")
	m.typ("zerotx_recorder_session_active", "gauge")
	m.gaugeI("zerotx_recorder_session_active", int64(active))
	m.blank()
}

// ----------------------------------------------------------------------------
// Audio
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeAudio(s *Server) {
	if s.providers.Audio == nil {
		return
	}
	a := s.providers.Audio()
	m.help("zerotx_audio_threshold_info", "Current audio threshold; constant 1, threshold on label")
	m.typ("zerotx_audio_threshold_info", "gauge")
	m.labeled("zerotx_audio_threshold_info", map[string]string{"threshold": a.Threshold}, 1)
	m.blank()

	// ActiveAlarms is interface{} (avoid audio-package import in
	// the api package). Try the common shapes: a slice, or marshal
	// to JSON and count the array length.
	count := 0
	switch v := a.ActiveAlarms.(type) {
	case nil:
	case []interface{}:
		count = len(v)
	default:
		if data, err := json.Marshal(a.ActiveAlarms); err == nil {
			var arr []interface{}
			if json.Unmarshal(data, &arr) == nil {
				count = len(arr)
			}
		}
	}
	m.help("zerotx_audio_active_alarms", "Number of currently active audio alarms")
	m.typ("zerotx_audio_active_alarms", "gauge")
	m.gaugeI("zerotx_audio_active_alarms", int64(count))
	m.blank()
}

// ----------------------------------------------------------------------------
// Weather
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeWeather(s *Server) {
	if s.providers.WeatherCurrent == nil {
		return
	}
	_, lat, lon, _, ok := s.providers.WeatherCurrent()

	available := 0
	if ok {
		available = 1
	}
	m.help("zerotx_weather_available", "1 if weather data is currently cached, 0 otherwise")
	m.typ("zerotx_weather_available", "gauge")
	m.gaugeI("zerotx_weather_available", int64(available))
	m.blank()

	if ok {
		m.help("zerotx_weather_resolved_lat", "Latitude of the resolved weather location")
		m.typ("zerotx_weather_resolved_lat", "gauge")
		m.gauge("zerotx_weather_resolved_lat", lat)
		m.blank()

		m.help("zerotx_weather_resolved_lon", "Longitude of the resolved weather location")
		m.typ("zerotx_weather_resolved_lon", "gauge")
		m.gauge("zerotx_weather_resolved_lon", lon)
		m.blank()
	}
}

// ----------------------------------------------------------------------------
// Weather alerts
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeAlerts(s *Server) {
	if s.providers.WeatherAlerts == nil {
		return
	}
	alerts := s.providers.WeatherAlerts()
	bySeverity := map[string]int{"notice": 0, "warning": 0, "critical": 0}
	for _, a := range alerts {
		bySeverity[a.Severity]++
	}
	m.help("zerotx_wxalerts_active", "Active weather alerts by severity")
	m.typ("zerotx_wxalerts_active", "gauge")
	for sev, n := range bySeverity {
		m.labeled("zerotx_wxalerts_active", map[string]string{"severity": sev}, float64(n))
	}
	m.blank()

	m.help("zerotx_wxalerts_active_total", "Total number of active weather alerts (across severities)")
	m.typ("zerotx_wxalerts_active_total", "gauge")
	m.gaugeI("zerotx_wxalerts_active_total", int64(len(alerts)))
	m.blank()
}

// ----------------------------------------------------------------------------
// Netclass
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeNetClass(s *Server) {
	if s.providers.NetClassGet == nil {
		return
	}
	class, updatedAt := s.providers.NetClassGet()
	m.help("zerotx_netclass_info", "Current network class; constant 1, class on label")
	m.typ("zerotx_netclass_info", "gauge")
	m.labeled("zerotx_netclass_info", map[string]string{"class": class}, 1)
	m.blank()

	if !updatedAt.IsZero() {
		m.help("zerotx_netclass_changed_seconds", "Seconds since the netclass last changed")
		m.typ("zerotx_netclass_changed_seconds", "gauge")
		m.gauge("zerotx_netclass_changed_seconds", time.Since(updatedAt).Seconds())
		m.blank()
	}
}

// ----------------------------------------------------------------------------
// Tilewarm
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeTileWarm(s *Server) {
	if s.providers.TileWarmStats == nil {
		return
	}
	stats := s.providers.TileWarmStats()
	if stats == nil {
		return
	}

	m.help("zerotx_tilewarm_runs_total", "Total tilewarm runs since daemon start")
	m.typ("zerotx_tilewarm_runs_total", "counter")
	m.gaugeI("zerotx_tilewarm_runs_total", stats.TotalRuns)
	m.blank()

	m.help("zerotx_tilewarm_errors_total", "Total tilewarm runs that ended in error")
	m.typ("zerotx_tilewarm_errors_total", "counter")
	m.gaugeI("zerotx_tilewarm_errors_total", stats.TotalErrors)
	m.blank()

	if !stats.LastRunAt.IsZero() {
		m.help("zerotx_tilewarm_last_run_seconds", "Seconds since the last tilewarm run finished")
		m.typ("zerotx_tilewarm_last_run_seconds", "gauge")
		m.gauge("zerotx_tilewarm_last_run_seconds", time.Since(stats.LastRunAt).Seconds())
		m.blank()

		m.help("zerotx_tilewarm_last_considered", "Tiles considered in the most recent run")
		m.typ("zerotx_tilewarm_last_considered", "gauge")
		m.gaugeI("zerotx_tilewarm_last_considered", int64(stats.LastConsidered))
		m.blank()

		m.help("zerotx_tilewarm_last_skipped", "Tiles skipped (fresh) in the most recent run")
		m.typ("zerotx_tilewarm_last_skipped", "gauge")
		m.gaugeI("zerotx_tilewarm_last_skipped", int64(stats.LastSkipped))
		m.blank()

		m.help("zerotx_tilewarm_last_fetched", "Tiles fetched in the most recent run")
		m.typ("zerotx_tilewarm_last_fetched", "gauge")
		m.gaugeI("zerotx_tilewarm_last_fetched", int64(stats.LastFetched))
		m.blank()

		m.help("zerotx_tilewarm_last_errors", "Per-tile errors in the most recent run")
		m.typ("zerotx_tilewarm_last_errors", "gauge")
		m.gaugeI("zerotx_tilewarm_last_errors", int64(stats.LastErrors))
		m.blank()
	}
}

// ----------------------------------------------------------------------------
// WebSocket clients
// ----------------------------------------------------------------------------

func (m *metricsWriter) writeWS(s *Server) {
	count := 0
	if s.hub != nil {
		count = s.hub.ClientCount()
	}
	m.help("zerotx_ws_clients", "Number of currently connected WebSocket stream clients")
	m.typ("zerotx_ws_clients", "gauge")
	m.gaugeI("zerotx_ws_clients", int64(count))
	m.blank()
}
