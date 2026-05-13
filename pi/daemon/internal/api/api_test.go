package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/panel"
	"github.com/gorilla/websocket"
)

// makeProviders builds a Providers with simple deterministic values.
func makeProviders() *Providers {
	var ch [ipc.Channels]uint16
	for i := range ch {
		ch[i] = ipc.CrsfChMid
	}
	ch[0] = ipc.CrsfChMin
	ch[4] = ipc.CrsfChMax
	return &Providers{
		Channels: func() [ipc.Channels]uint16 { return ch },
		Logic: func() map[string]bool {
			return map[string]bool{"L1": true, "L2": false, "L3": true, "L4": false}
		},
		Panel: func() panel.Snapshot {
			return panel.Snapshot{
				Switches:  map[string]int{"SF": 2, "SH": 0},
				Selectors: map[string]int{"6POS": 5},
				Buttons:   map[string]bool{},
			}
		},
		Joystick: func() *JoystickSnapshot {
			return &JoystickSnapshot{
				Name:    "TestStick",
				Axes:    []float64{0, 0, -1.0, 0, 0},
				Buttons: []bool{false, false},
			}
		},
		Link: func() LinkSnapshot {
			return LinkSnapshot{State: "active", Port: "/dev/ttyACM1"}
		},
		Model: func() ModelSummary {
			return ModelSummary{
				Name: "Big Talon", Mixes: 11, Logic: 4, CustomFn: 14, Sensors: 24,
				Available: true,
			}
		},
		Version: "test",
		Uptime:  func() time.Duration { return 12 * time.Second },
	}
}

func TestHandleHealth(t *testing.T) {
	srv := NewServer("", makeProviders())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	srv.handleHealth(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d", rr.Code)
	}
	var resp HealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Version != "test" || !resp.ModelLoaded || !resp.JoystickOpen {
		t.Errorf("response: %+v", resp)
	}
}

func TestHandleModel(t *testing.T) {
	srv := NewServer("", makeProviders())
	rr := httptest.NewRecorder()
	srv.handleModel(rr, httptest.NewRequest("GET", "/api/v1/model", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	var m ModelSummary
	json.Unmarshal(rr.Body.Bytes(), &m)
	if m.Name != "Big Talon" || m.Mixes != 11 {
		t.Errorf("model: %+v", m)
	}
}

func TestHandleState(t *testing.T) {
	srv := NewServer("", makeProviders())
	rr := httptest.NewRecorder()
	srv.handleState(rr, httptest.NewRequest("GET", "/api/v1/state", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	var s State
	if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(s.Channels) != ipc.Channels {
		t.Errorf("channels length: got %d want %d", len(s.Channels), ipc.Channels)
	}
	if s.Channels[0] != ipc.CrsfChMin {
		t.Errorf("ch[0]: got %d", s.Channels[0])
	}
	if !s.Logic["L1"] || s.Logic["L2"] {
		t.Errorf("logic: %v", s.Logic)
	}
	if s.Panel.Switches["SF"] != 2 {
		t.Errorf("panel SF: %v", s.Panel.Switches)
	}
	if s.Joystick == nil || s.Joystick.Axes[2] != -1.0 {
		t.Errorf("joystick: %+v", s.Joystick)
	}
	if s.Station != nil {
		t.Errorf("station should be omitted when provider is nil, got %+v", s.Station)
	}
}

// TestHandleState_StationOmittedRaw verifies that an unconfigured station
// produces no `"station":` key in the wire JSON at all (the omitempty on
// the *StationSnapshot pointer handles this). UI consumers rely on this
// to decide whether to show any station-GPS UI at all.
func TestHandleState_StationOmittedRaw(t *testing.T) {
	srv := NewServer("", makeProviders())
	rr := httptest.NewRecorder()
	srv.handleState(rr, httptest.NewRequest("GET", "/api/v1/state", nil))
	if !json.Valid(rr.Body.Bytes()) {
		t.Fatal("response not valid JSON")
	}
	if bytes.Contains(rr.Body.Bytes(), []byte(`"station"`)) {
		t.Errorf("station key should be absent when provider is nil; body=%s", rr.Body.String())
	}
}

// TestHandleState_StationPresent_NoFix confirms that with a station
// provider returning a no-fix snapshot, the block is emitted with
// Available=false and no lat/lon.
func TestHandleState_StationPresent_NoFix(t *testing.T) {
	p := makeProviders()
	p.Station = func() *StationSnapshot {
		return &StationSnapshot{
			Available: false,
			Fix:       "none",
			Sats:      0,
		}
	}
	srv := NewServer("", p)
	rr := httptest.NewRecorder()
	srv.handleState(rr, httptest.NewRequest("GET", "/api/v1/state", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	var s State
	if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Station == nil {
		t.Fatal("station block missing")
	}
	if s.Station.Available {
		t.Error("Available should be false when no fix")
	}
	if s.Station.Fix != "none" {
		t.Errorf("Fix=%q want 'none'", s.Station.Fix)
	}
	if s.Station.LatDeg != 0 || s.Station.LonDeg != 0 {
		t.Errorf("position should be zero with no fix; got %v,%v", s.Station.LatDeg, s.Station.LonDeg)
	}
}

// TestHandleState_StationPresent_With3DFix confirms a 3D fix populates
// position fields and Available=true.
func TestHandleState_StationPresent_With3DFix(t *testing.T) {
	p := makeProviders()
	p.Station = func() *StationSnapshot {
		return &StationSnapshot{
			Available:  true,
			Fix:        "3D",
			Sats:       11,
			HDOP:       0.8,
			LatDeg:     -22.95,
			LonDeg:     -47.05,
			AltMeters:  650.0,
			SpeedKmh:   0.5,
			HeadingDeg: 187.3,
		}
	}
	srv := NewServer("", p)
	rr := httptest.NewRecorder()
	srv.handleState(rr, httptest.NewRequest("GET", "/api/v1/state", nil))
	var s State
	if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Station == nil || !s.Station.Available || s.Station.Fix != "3D" {
		t.Fatalf("station unexpected: %+v", s.Station)
	}
	if s.Station.Sats != 11 || s.Station.HDOP != 0.8 {
		t.Errorf("sats/HDOP wrong: %+v", s.Station)
	}
	if s.Station.LatDeg != -22.95 || s.Station.LonDeg != -47.05 {
		t.Errorf("lat/lon wrong: %+v", s.Station)
	}
}

// TestWSStream_BasicBroadcast spins up the server on an ephemeral port,
// connects a client, and verifies that broadcasts arrive.
func TestWSStream_BasicBroadcast(t *testing.T) {
	// Pick a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	srv := NewServer(addr, makeProviders())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Run(ctx) }()

	// Wait for server to start listening.
	time.Sleep(100 * time.Millisecond)

	// Connect a WS client.
	u := url.URL{Scheme: "ws", Host: addr, Path: "/api/v1/stream"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Receive a message within 1.5s (broadcast tick is 100ms).
	conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var s State
	if err := json.Unmarshal(msg, &s); err != nil {
		t.Fatalf("decode: %v\nmsg: %s", err, string(msg))
	}
	if len(s.Channels) != ipc.Channels {
		t.Errorf("channels: got %d want %d", len(s.Channels), ipc.Channels)
	}
	if !s.Logic["L1"] {
		t.Errorf("L1 should be true in test fixture")
	}
}

// TestWSStream_MultipleClients verifies all connected clients receive
// each broadcast.
func TestWSStream_MultipleClients(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	srv := NewServer(addr, makeProviders())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	u := url.URL{Scheme: "ws", Host: addr, Path: "/api/v1/stream"}
	clients := make([]*websocket.Conn, 3)
	for i := range clients {
		c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			t.Fatalf("dial #%d: %v", i, err)
		}
		clients[i] = c
		defer c.Close()
	}

	// Each should get a frame within the deadline.
	for i, c := range clients {
		c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Errorf("client #%d read: %v", i, err)
			continue
		}
		if !strings.Contains(string(msg), "\"channels\"") {
			t.Errorf("client #%d: unexpected payload: %s", i, string(msg))
		}
	}
}

// TestWSStream_ClientDisconnectCleanup ensures the hub forgets disconnected
// clients (no goroutine/memory leak per connection).
func TestWSStream_ClientDisconnectCleanup(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	srv := NewServer(addr, makeProviders())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	u := url.URL{Scheme: "ws", Host: addr, Path: "/api/v1/stream"}
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for hub to register.
	time.Sleep(50 * time.Millisecond)
	if srv.hub.clientCount() != 1 {
		t.Errorf("clientCount after connect: got %d want 1", srv.hub.clientCount())
	}
	c.Close()
	// Allow read pump to detect disconnect.
	time.Sleep(200 * time.Millisecond)
	if srv.hub.clientCount() != 0 {
		t.Errorf("clientCount after disconnect: got %d want 0", srv.hub.clientCount())
	}
}

// TestServer_HTTPRoundTrip exercises a full HTTP GET via the running server.
func TestServer_HTTPRoundTrip(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	srv := NewServer(addr, makeProviders())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://%s/api/v1/health", addr))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: %s", ct)
	}
}

// ----------------------------------------------------------------------------
// Weather endpoint with astro folded in.
// ----------------------------------------------------------------------------

// fakeWeather is the opaque payload type the WeatherCurrent provider
// returns. Mimics weather.Weather just enough that JSON encoding
// produces the field names the GUI consumes. We don't import the
// real package here to keep the api test independent.
type fakeWeather struct {
	LatDeg    float64   `json:"latDeg"`
	LonDeg    float64   `json:"lonDeg"`
	FetchedAt time.Time `json:"fetchedAt"`
	Source    string    `json:"source"`
}

func makeWeatherProviders(haveCoords, haveData bool) *Providers {
	p := makeProviders()
	p.WeatherCurrent = func() (interface{}, float64, float64, string, bool) {
		if !haveCoords {
			return nil, 0, 0, "", false
		}
		if !haveData {
			return nil, -22.91, -47.06, "site", false
		}
		return fakeWeather{
			LatDeg:    -22.95,
			LonDeg:    -47.07,
			FetchedAt: time.Now().UTC(),
			Source:    "fake",
		}, -22.91, -47.06, "site", true
	}
	p.WeatherFetch = func(_ context.Context, lat, lon float64) (interface{}, error) {
		return fakeWeather{
			LatDeg:    lat,
			LonDeg:    lon,
			FetchedAt: time.Now().UTC(),
			Source:    "fake",
		}, nil
	}
	return p
}

func TestHandleWeather_NoCoords_404(t *testing.T) {
	srv := NewServer("", makeWeatherProviders(false, false))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/weather", nil)
	srv.handleWeather(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHandleWeather_CoordsButNoData_503(t *testing.T) {
	srv := NewServer("", makeWeatherProviders(true, false))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/weather", nil)
	srv.handleWeather(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["coordSource"] != "site" {
		t.Errorf("coordSource = %v, want site", body["coordSource"])
	}
}

func TestHandleWeather_Cached_IncludesAstro(t *testing.T) {
	srv := NewServer("", makeWeatherProviders(true, true))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/weather", nil)
	srv.handleWeather(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["weather"] == nil {
		t.Errorf("response missing weather field")
	}
	if body["coordSource"] != "site" {
		t.Errorf("coordSource = %v, want site", body["coordSource"])
	}
	astroBlock, ok := body["astro"].(map[string]interface{})
	if !ok {
		t.Fatalf("astro field missing or not an object: %v", body["astro"])
	}
	if _, ok := astroBlock["sunPosition"].(map[string]interface{}); !ok {
		t.Errorf("astro.sunPosition missing")
	}
	if _, ok := astroBlock["sun"].(map[string]interface{}); !ok {
		t.Errorf("astro.sun missing")
	}
	if _, ok := astroBlock["moon"].(map[string]interface{}); !ok {
		t.Errorf("astro.moon missing")
	}
	// Confirm sun position uses the resolver's coords (not zero/zero).
	// At Campinas in early May, civil time (~13:00 UTC), the sun is
	// always above the horizon so elevation should be positive most
	// of the time. We don't assert a specific value (depends on test
	// run time) but we assert it's a number that decoded properly.
	pos := astroBlock["sunPosition"].(map[string]interface{})
	if _, ok := pos["azimuthDeg"].(float64); !ok {
		t.Errorf("sunPosition.azimuthDeg not a number")
	}
	if _, ok := pos["elevationDeg"].(float64); !ok {
		t.Errorf("sunPosition.elevationDeg not a number")
	}
}

func TestHandleWeather_ExplicitCoords_IncludesAstro(t *testing.T) {
	srv := NewServer("", makeWeatherProviders(true, true))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/weather?lat=-22.91&lon=-47.06", nil)
	srv.handleWeather(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["astro"] == nil {
		t.Errorf("explicit-coords response missing astro field")
	}
	if _, exists := body["coordSource"]; exists {
		t.Errorf("explicit-coords response should not have coordSource")
	}
}

func TestHandleWeather_BadCoords_400(t *testing.T) {
	srv := NewServer("", makeWeatherProviders(true, true))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/weather?lat=banana&lon=-47", nil)
	srv.handleWeather(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleWeather_OutOfRange_400(t *testing.T) {
	srv := NewServer("", makeWeatherProviders(true, true))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/weather?lat=200&lon=-47", nil)
	srv.handleWeather(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for lat=200", rr.Code)
	}
}

// TestHandleSyscheckDismiss_ReadyAllowsDismiss: when preflight is
// Ready, dismiss succeeds (204) and SyscheckDismiss is invoked.
func TestHandleSyscheckDismiss_ReadyAllowsDismiss(t *testing.T) {
	dismissed := false
	providers := &Providers{
		Preflight: func() Preflight {
			return Preflight{Ready: true}
		},
		SyscheckDismiss: func() { dismissed = true },
	}
	srv := NewServer("", providers)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/syscheck/dismiss", nil)
	srv.handleSyscheckDismiss(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !dismissed {
		t.Errorf("SyscheckDismiss provider was not invoked")
	}
}

// TestHandleSyscheckDismiss_NotReadyReturns409: when preflight is
// NOT Ready, dismiss returns 409 Conflict, does NOT invoke the
// provider, and surfaces the blockers list in the response.
func TestHandleSyscheckDismiss_NotReadyReturns409(t *testing.T) {
	dismissed := false
	providers := &Providers{
		Preflight: func() Preflight {
			return Preflight{
				Ready:    false,
				Blockers: []string{"device down: rp2040", "device down: hdmi-displays"},
			}
		},
		SyscheckDismiss: func() { dismissed = true },
	}
	srv := NewServer("", providers)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/syscheck/dismiss", nil)
	srv.handleSyscheckDismiss(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", rr.Code)
	}
	if dismissed {
		t.Errorf("SyscheckDismiss invoked despite not-ready preflight")
	}
	body := rr.Body.String()
	if !strings.Contains(body, "rp2040") || !strings.Contains(body, "hdmi-displays") {
		t.Errorf("response body should contain blocker names, got: %s", body)
	}
}

// TestHandleSyscheckDismiss_NoPreflightProviderFallsThrough: backwards-
// compat path. If no Preflight provider is wired (mock servers, old
// tests), the dismiss should succeed unconditionally as before.
func TestHandleSyscheckDismiss_NoPreflightProviderFallsThrough(t *testing.T) {
	dismissed := false
	providers := &Providers{
		// Preflight intentionally nil
		SyscheckDismiss: func() { dismissed = true },
	}
	srv := NewServer("", providers)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/syscheck/dismiss", nil)
	srv.handleSyscheckDismiss(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !dismissed {
		t.Errorf("dismiss did not invoke provider in no-Preflight mode")
	}
}

// === Replay endpoints ===

// TestHandleReplayStatus_Idle: with no replay active, returns
// Active=false. Default state on a freshly-started daemon.
func TestHandleReplayStatus_Idle(t *testing.T) {
	providers := &Providers{
		ReplaySnapshot: func() ReplayInfo {
			return ReplayInfo{Active: false}
		},
	}
	srv := NewServer("", providers)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/replay/status", nil)
	srv.handleReplayStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
	var got ReplayInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rr.Body.String())
	}
	if got.Active {
		t.Errorf("expected Active=false, got %+v", got)
	}
}

// TestHandleReplayStatus_NoProviderGracefulDefault: when no
// ReplaySnapshot provider is wired (e.g. legacy callers, tests),
// the endpoint still returns a valid Idle response rather than
// 500ing or 503ing.
func TestHandleReplayStatus_NoProviderGracefulDefault(t *testing.T) {
	srv := NewServer("", &Providers{}) // no ReplaySnapshot
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/replay/status", nil)
	srv.handleReplayStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

// TestHandleReplayStart_HappyPath: name is valid, provider succeeds,
// 200 with the new snapshot.
func TestHandleReplayStart_HappyPath(t *testing.T) {
	started := ""
	providers := &Providers{
		ReplayStart: func(name string) error {
			started = name
			return nil
		},
		ReplaySnapshot: func() ReplayInfo {
			return ReplayInfo{Active: true, Name: started}
		},
	}
	srv := NewServer("", providers)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/replay/start",
		strings.NewReader(`{"name": "flight-42.db"}`))
	srv.handleReplayStart(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if started != "flight-42.db" {
		t.Errorf("provider received name=%q, want flight-42.db", started)
	}
}

// TestHandleReplayStart_ArmedReturns409: the provider's safety gate
// (armed-state check) bubbles up as 409. Critical test: this is the
// flight-safety backstop that should never silently fail.
func TestHandleReplayStart_ArmedReturns409(t *testing.T) {
	providers := &Providers{
		ReplayStart: func(name string) error {
			return fmt.Errorf("cannot start replay while armed")
		},
	}
	srv := NewServer("", providers)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/replay/start",
		strings.NewReader(`{"name": "flight-42.db"}`))
	srv.handleReplayStart(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "armed") {
		t.Errorf("body should mention 'armed', got: %s", rr.Body.String())
	}
}

// TestHandleReplayStart_EmptyName: 400 on empty name. Path-traversal
// defenses live in the provider, not the handler; we don't test
// those here, but the empty-name check is at the handler layer.
func TestHandleReplayStart_EmptyName(t *testing.T) {
	providers := &Providers{
		ReplayStart: func(name string) error { return nil },
	}
	srv := NewServer("", providers)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/replay/start",
		strings.NewReader(`{"name": ""}`))
	srv.handleReplayStart(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// TestHandleReplayStop_HappyPath: returns 204 and the provider is
// called. Idempotent at the daemon level so we don't bother
// asserting the provider returned anything.
func TestHandleReplayStop_HappyPath(t *testing.T) {
	stopped := false
	providers := &Providers{
		ReplayStop: func() { stopped = true },
	}
	srv := NewServer("", providers)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/replay/stop", nil)
	srv.handleReplayStop(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !stopped {
		t.Errorf("ReplayStop provider was not invoked")
	}
}
