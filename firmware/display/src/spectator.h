// Spectator: WiFi SoftAP with HTTP + WebSocket dashboard for
// onlookers at the field.
//
// Self-contained AP, no internet pass-through. Clients (phones,
// laptops) connect to SSID "ZeroTX-Spectator" with the WPA2
// password baked in below, then open http://192.168.4.1/ to see
// a live read-only HUD.
//
// Architecture: this module is fully decoupled from the panel
// telemetry pipeline. main.cpp calls spectator::push_state() with
// a snapshot whenever it has fresh data; spectator broadcasts to
// connected clients at SPECTATOR_HZ. No globals shared, no struct
// layout coupling.
//
// Coexistence with the HUB75 panel is the main risk. WiFi shares
// the radio core; under spectator load the panel I2S DMA may see
// jitter. Bench-test under realistic spectator counts before
// trusting in flight; if jitter is visible, lower SPECTATOR_HZ
// or limit max clients.

#pragma once

#include <Arduino.h>

namespace spectator {

// SoftAP credentials.
constexpr const char *SSID     = "ZeroTX-Spectator";
constexpr const char *PASSWORD = "p\xC3\xA9" "dogalo";  // UTF-8 'é', WPA2 min 8 bytes

// Dashboard tick rate. 5Hz feels live without saturating the WiFi
// link or competing too aggressively with the panel I2S DMA.
constexpr unsigned long TICK_INTERVAL_MS = 200;  // 5Hz

// Hard cap on concurrent spectator clients.
constexpr int MAX_CLIENTS = 4;

// Snapshot of telemetry state for the spectator dashboard. main.cpp
// fills this from its State struct and pushes it via push_state().
// All fields are independent of the panel's internal types so this
// header can compile without including main.cpp's headers.
struct Snapshot {
  bool   armed_known;
  bool   armed;
  const char *flight_mode;   // non-null, may be ""
  bool   alt_known;
  int    alt_m;
  bool   dist_known;
  int    dist_m;
  bool   spd_known;
  int    spd_kmh;
  bool   link_known;
  int    link_pct;
  bool   bat_known;
  float  bat_v;
  bool   time_known;
  int    time_s;
  bool   alarm_active;
  const char *alarm_level;   // non-null, may be ""
};

// Initialize the AP, start the HTTP and WebSocket servers. Called
// once from setup() AFTER the panel is initialized so a WiFi
// init failure can't blackhole the panel.
void begin();

// Update the cached state. Cheap; copies primitive fields. main.cpp
// calls this whenever it processes new telemetry. Pointer fields
// are copied as pointers (caller guarantees lifetime; in practice
// they point into the State struct's String members which outlive
// any single tick).
void push_state(const Snapshot &s);

// Service incoming HTTP and WebSocket traffic, push periodic
// state updates. Call from loop() as often as possible. Internally
// rate-limits its broadcasts to TICK_INTERVAL_MS.
void tick();

}  // namespace spectator
