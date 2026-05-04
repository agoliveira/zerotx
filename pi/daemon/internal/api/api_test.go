package api

import (
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
