// ZeroTX HUB75 display firmware
//
// Target: ESP32 classic (any variant with enough GPIO for HUB75)
// Hardware: two chained P2.5 64x32 panels = 128x32 logical surface
// Library: ESP32-HUB75-MatrixPanel-DMA (mrfaptastic)
//   https://github.com/mrfaptastic/ESP32-HUB75-MatrixPanel-DMA
//
// Build environment: PlatformIO recommended (see platformio.ini in
// this directory). Arduino IDE works too if you install the library
// via the Library Manager.
//
// Protocol: see docs/protocols/display.md
//
// Wiring (default library pinout, change in setup() if your board
// differs):
//   R1=25  G1=26  B1=27
//   R2=14  G2=12  B2=13
//   A=23   B=19   C=5   D=17   E=22 (E is for 1/32 scan; not needed here)
//   LAT=4  OE=15  CLK=16
//
// Power: panels are 5V, can pull 6A peak combined. Use a separate 5V
// rail; do NOT power from the ESP32's USB rail.
//
// Notes on conservatism:
// - All rendering uses the library's built-in font and direct draw
//   primitives. No custom fonts, no double-buffering tricks
// - Mode renderers are structured as full-screen redraws on transition
//   plus minimal updates per state change. Performance is not yet
//   optimized; the panels can refresh at hundreds of Hz so we don't
//   need to be clever
// - Serial parser is a simple line accumulator; no fancy state machine
// - Heartbeat is a fixed 5s interval

#include <Arduino.h>
#include <ESP32-HUB75-MatrixPanel-I2S-DMA.h>

// ===== Configuration =====

constexpr int PANEL_WIDTH = 64;
constexpr int PANEL_HEIGHT = 32;
constexpr int PANELS_NUM = 2;

constexpr int LOGICAL_WIDTH = PANEL_WIDTH * PANELS_NUM;  // 128
constexpr int LOGICAL_HEIGHT = PANEL_HEIGHT;             // 32

constexpr unsigned long HEARTBEAT_INTERVAL_MS = 5000;
constexpr unsigned long IDLE_REDRAW_INTERVAL_MS = 1000;  // clock tick

constexpr const char* FW_VERSION = "0.1.0";

// ===== Library setup =====

MatrixPanel_I2S_DMA *dma_display = nullptr;

// ===== State =====

enum class Mode {
  IDLE,
  PREFLIGHT,
  FLIGHT,
  ALARM,
  RTH,
  POSTFLIGHT
};

struct State {
  // Flight state
  bool   armed       = false;
  bool   armed_known = false;
  float  bat_v       = 0.0f;
  bool   bat_known   = false;
  int    bat_pct     = 0;
  bool   batpct_known = false;
  int    alt_m       = 0;
  bool   alt_known   = false;
  int    dist_m      = 0;
  bool   dist_known  = false;
  int    spd_kmh     = 0;
  bool   spd_known   = false;
  int    link_pct    = 0;
  bool   link_known  = false;
  int    sats        = 0;
  bool   sats_known  = false;
  String flight_mode = "";
  String gps_fix     = "";
  int    time_s      = 0;
  bool   time_known  = false;

  // Alarm overlay
  bool   alarm_active = false;
  String alarm_level  = "";
  String alarm_text   = "";

  // One-shot message
  String msg_text     = "";
  unsigned long msg_started_ms = 0;

  // Brightness
  uint8_t brightness = 80; // 0-255 in library; we accept 0-100 from protocol
};

Mode current_mode = Mode::IDLE;
State state;

// Serial input buffer.
constexpr size_t LINE_BUF_SIZE = 256;
char line_buf[LINE_BUF_SIZE];
size_t line_len = 0;

// Heartbeat timing.
unsigned long last_heartbeat_ms = 0;
unsigned long boot_ms = 0;

// Render dirty flag: set on state changes, cleared after redraw.
bool needs_redraw = true;
unsigned long last_redraw_ms = 0;

// ===== Forward decls =====

void handle_line(const char* line);
void send_ready();
void send_heartbeat();
void send_pong();
void send_error(const char* msg);
void render();
void render_idle();
void render_preflight();
void render_flight();
void render_alarm();
void render_rth();
void render_postflight();

// ===== Setup =====

void setup() {
  Serial.begin(115200);
  // Don't wait for serial; the daemon may connect later.

  // Default library pinout. Override here if your wiring differs.
  HUB75_I2S_CFG mxconfig(
    PANEL_WIDTH,    // module width
    PANEL_HEIGHT,   // module height
    PANELS_NUM      // chain length
  );
  // mxconfig.gpio.<pin> = ...; // uncomment + adjust if needed

  dma_display = new MatrixPanel_I2S_DMA(mxconfig);
  if (!dma_display->begin()) {
    // Init failed. Without panels we can still respond to serial,
    // so don't halt; just log.
    Serial.println(F("DISP ERROR \"panel init failed\""));
  }
  dma_display->setBrightness8(state.brightness * 255 / 100);
  dma_display->clearScreen();

  boot_ms = millis();
  send_ready();
  needs_redraw = true;
}

// ===== Main loop =====

void loop() {
  // Drain serial input one byte at a time. Lines are terminated by
  // \n; \r is tolerated and stripped.
  while (Serial.available()) {
    int c = Serial.read();
    if (c < 0) break;
    if (c == '\r') continue;
    if (c == '\n') {
      line_buf[line_len] = '\0';
      handle_line(line_buf);
      line_len = 0;
      continue;
    }
    if (line_len + 1 < LINE_BUF_SIZE) {
      line_buf[line_len++] = (char)c;
    } else {
      // Overflow: drop the line, reset, log.
      line_len = 0;
      send_error("line too long");
    }
  }

  // Heartbeat.
  unsigned long now = millis();
  if (now - last_heartbeat_ms >= HEARTBEAT_INTERVAL_MS) {
    last_heartbeat_ms = now;
    send_heartbeat();
  }

  // Idle clock redraw.
  if (current_mode == Mode::IDLE && now - last_redraw_ms >= IDLE_REDRAW_INTERVAL_MS) {
    needs_redraw = true;
  }

  // Render if dirty.
  if (needs_redraw) {
    render();
    needs_redraw = false;
    last_redraw_ms = now;
  }
}

// ===== Serial input handling =====

// Tokenize a line in-place, respecting double-quoted strings. Returns
// the number of tokens. Tokens point into the buffer (which is
// modified to insert null terminators).
//
// Quoted strings have their quotes stripped.
int tokenize(char* line, char* out_tokens[], int max_tokens) {
  int count = 0;
  char* p = line;
  while (*p && count < max_tokens) {
    while (*p == ' ' || *p == '\t') p++;
    if (!*p) break;
    if (*p == '"') {
      p++;
      char* start = p;
      while (*p && *p != '"') p++;
      if (*p == '"') {
        *p = '\0';
        p++;
      }
      out_tokens[count++] = start;
    } else {
      char* start = p;
      while (*p && *p != ' ' && *p != '\t') p++;
      if (*p) {
        *p = '\0';
        p++;
      }
      out_tokens[count++] = start;
    }
  }
  return count;
}

// Find a key=value arg. Returns the value string or nullptr.
const char* arg_value(char* tokens[], int count, int start, const char* key) {
  size_t key_len = strlen(key);
  for (int i = start; i < count; i++) {
    if (strncmp(tokens[i], key, key_len) == 0 && tokens[i][key_len] == '=') {
      return tokens[i] + key_len + 1;
    }
  }
  return nullptr;
}

// Set the current mode. No-op if already in that mode.
void set_mode(Mode m) {
  if (current_mode == m) return;
  current_mode = m;
  needs_redraw = true;
}

void handle_line(const char* line) {
  // Make a writable copy because tokenize mutates.
  static char buf[LINE_BUF_SIZE];
  strncpy(buf, line, sizeof(buf));
  buf[sizeof(buf) - 1] = '\0';

  char* tokens[24];
  int n = tokenize(buf, tokens, 24);
  if (n < 2) return;
  if (strcmp(tokens[0], "DISP") != 0) return;

  const char* cmd = tokens[1];

  if (strcmp(cmd, "MODE") == 0) {
    if (n < 3) return;
    if (strcmp(tokens[2], "IDLE") == 0) set_mode(Mode::IDLE);
    else if (strcmp(tokens[2], "PREFLIGHT") == 0) set_mode(Mode::PREFLIGHT);
    else if (strcmp(tokens[2], "FLIGHT") == 0) set_mode(Mode::FLIGHT);
    else if (strcmp(tokens[2], "ALARM") == 0) set_mode(Mode::ALARM);
    else if (strcmp(tokens[2], "RTH") == 0) set_mode(Mode::RTH);
    else if (strcmp(tokens[2], "POSTFLIGHT") == 0) set_mode(Mode::POSTFLIGHT);
    else send_error("unknown mode");
  }
  else if (strcmp(cmd, "STATE") == 0) {
    const char* v;
    if ((v = arg_value(tokens, n, 2, "armed")) != nullptr) {
      state.armed = (v[0] == '1');
      state.armed_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "bat")) != nullptr) {
      state.bat_v = atof(v);
      state.bat_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "batpct")) != nullptr) {
      state.bat_pct = atoi(v);
      state.batpct_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "alt")) != nullptr) {
      state.alt_m = atoi(v);
      state.alt_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "dist")) != nullptr) {
      state.dist_m = atoi(v);
      state.dist_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "spd")) != nullptr) {
      state.spd_kmh = atoi(v);
      state.spd_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "link")) != nullptr) {
      state.link_pct = atoi(v);
      state.link_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "sats")) != nullptr) {
      state.sats = atoi(v);
      state.sats_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "mode")) != nullptr) {
      state.flight_mode = v;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "gps")) != nullptr) {
      state.gps_fix = v;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "time")) != nullptr) {
      state.time_s = atoi(v);
      state.time_known = true;
      needs_redraw = true;
    }
  }
  else if (strcmp(cmd, "ALARM") == 0) {
    if (n < 4) return;
    state.alarm_active = true;
    state.alarm_level = tokens[2];
    state.alarm_text = tokens[3];
    set_mode(Mode::ALARM);
  }
  else if (strcmp(cmd, "CLEAR-ALARM") == 0) {
    state.alarm_active = false;
    state.alarm_level = "";
    state.alarm_text = "";
    needs_redraw = true;
    // Mode does not auto-revert; daemon issues new MODE if needed.
  }
  else if (strcmp(cmd, "MSG") == 0) {
    if (n < 3) return;
    state.msg_text = tokens[2];
    state.msg_started_ms = millis();
    needs_redraw = true;
  }
  else if (strcmp(cmd, "BRIGHTNESS") == 0) {
    if (n < 3) return;
    int pct = atoi(tokens[2]);
    if (pct < 0) pct = 0;
    if (pct > 100) pct = 100;
    state.brightness = pct;
    if (dma_display) {
      dma_display->setBrightness8(pct * 255 / 100);
    }
  }
  else if (strcmp(cmd, "PING") == 0) {
    send_pong();
  }
  // Unknown commands ignored silently per protocol spec.
}

// ===== Outbound messages =====

void send_ready() {
  Serial.print(F("DISP READY version="));
  Serial.print(FW_VERSION);
  Serial.print(F(" panels="));
  Serial.print(PANELS_NUM);
  Serial.print(F(" w="));
  Serial.print(LOGICAL_WIDTH);
  Serial.print(F(" h="));
  Serial.println(LOGICAL_HEIGHT);
}

void send_heartbeat() {
  unsigned long uptime_s = (millis() - boot_ms) / 1000;
  Serial.print(F("DISP HEARTBEAT uptime="));
  Serial.println(uptime_s);
}

void send_pong() {
  Serial.println(F("DISP PONG"));
}

void send_error(const char* msg) {
  Serial.print(F("DISP ERROR \""));
  Serial.print(msg);
  Serial.println(F("\""));
}

// ===== Rendering =====

void render() {
  if (!dma_display) return;
  dma_display->clearScreen();

  switch (current_mode) {
    case Mode::IDLE:       render_idle(); break;
    case Mode::PREFLIGHT:  render_preflight(); break;
    case Mode::FLIGHT:     render_flight(); break;
    case Mode::ALARM:      render_alarm(); break;
    case Mode::RTH:        render_rth(); break;
    case Mode::POSTFLIGHT: render_postflight(); break;
  }
}

// IDLE: dim "ZEROTX" centered, with uptime in seconds in the corner.
// Conservative: no clock yet (no RTC), just shows the device is alive.
void render_idle() {
  dma_display->setTextColor(dma_display->color565(64, 64, 80));
  dma_display->setTextSize(1);
  dma_display->setCursor(LOGICAL_WIDTH / 2 - 21, LOGICAL_HEIGHT / 2 - 4);
  dma_display->print("ZEROTX");
}

// PREFLIGHT: shows "READY?" and any known link/gps state.
void render_preflight() {
  dma_display->setTextColor(dma_display->color565(255, 200, 0));
  dma_display->setTextSize(1);
  dma_display->setCursor(2, 2);
  dma_display->print("PREFLIGHT");
  dma_display->setTextColor(dma_display->color565(120, 120, 120));
  dma_display->setCursor(2, 14);
  if (state.gps_fix.length() > 0) {
    dma_display->print("GPS:");
    dma_display->print(state.gps_fix);
  }
  dma_display->setCursor(2, 24);
  if (state.sats_known) {
    dma_display->print("SATS:");
    dma_display->print(state.sats);
  }
}

// FLIGHT: 3-tile layout. Battery (left), altitude (middle), distance (right).
// Each tile ~42px wide, label on top, value below.
void render_flight() {
  uint16_t cyan   = dma_display->color565(0, 200, 200);
  uint16_t white  = dma_display->color565(255, 255, 255);
  uint16_t yellow = dma_display->color565(255, 220, 0);
  dma_display->setTextSize(1);

  // Battery tile (0..42)
  dma_display->setTextColor(cyan);
  dma_display->setCursor(2, 2);
  dma_display->print("BAT");
  dma_display->setTextColor(white);
  dma_display->setCursor(2, 14);
  if (state.bat_known) {
    dma_display->print(state.bat_v, 1);
    dma_display->print('V');
  } else {
    dma_display->print("--");
  }
  if (state.batpct_known) {
    dma_display->setTextColor(yellow);
    dma_display->setCursor(2, 24);
    dma_display->print(state.bat_pct);
    dma_display->print('%');
  }

  // Altitude tile (44..86)
  dma_display->setTextColor(cyan);
  dma_display->setCursor(46, 2);
  dma_display->print("ALT");
  dma_display->setTextColor(white);
  dma_display->setCursor(46, 14);
  if (state.alt_known) {
    dma_display->print(state.alt_m);
    dma_display->print('m');
  } else {
    dma_display->print("--");
  }

  // Distance tile (88..127)
  dma_display->setTextColor(cyan);
  dma_display->setCursor(90, 2);
  dma_display->print("DST");
  dma_display->setTextColor(white);
  dma_display->setCursor(90, 14);
  if (state.dist_known) {
    if (state.dist_m >= 1000) {
      dma_display->print(state.dist_m / 1000.0, 1);
      dma_display->print('k');
    } else {
      dma_display->print(state.dist_m);
      dma_display->print('m');
    }
  } else {
    dma_display->print("--");
  }
}

// ALARM: full-width banner. Color depends on level.
void render_alarm() {
  uint16_t bg, fg;
  if (state.alarm_level == "critical") {
    bg = dma_display->color565(160, 0, 0);
    fg = dma_display->color565(255, 255, 255);
  } else if (state.alarm_level == "warning") {
    bg = dma_display->color565(160, 100, 0);
    fg = dma_display->color565(255, 255, 255);
  } else {
    bg = dma_display->color565(0, 0, 80);
    fg = dma_display->color565(255, 255, 255);
  }
  dma_display->fillRect(0, 0, LOGICAL_WIDTH, LOGICAL_HEIGHT, bg);
  dma_display->setTextColor(fg);
  dma_display->setTextSize(1);
  dma_display->setCursor(2, 2);
  dma_display->print("ALARM");
  dma_display->setCursor(2, 14);
  // Truncate text to fit. ~21 chars at size 1.
  String t = state.alarm_text;
  if (t.length() > 21) t = t.substring(0, 21);
  dma_display->print(t);
}

// RTH: arrow + distance.
void render_rth() {
  uint16_t green = dma_display->color565(0, 200, 0);
  dma_display->setTextColor(green);
  dma_display->setTextSize(1);
  dma_display->setCursor(2, 2);
  dma_display->print("RTH");
  dma_display->setCursor(2, 14);
  if (state.dist_known) {
    dma_display->print(state.dist_m);
    dma_display->print("m HOME");
  } else {
    dma_display->print("HOME");
  }
}

// POSTFLIGHT: simple summary text. Daemon issues MSG to scroll
// the narration; this stays static showing key numbers.
void render_postflight() {
  uint16_t white = dma_display->color565(200, 200, 200);
  dma_display->setTextColor(white);
  dma_display->setTextSize(1);
  dma_display->setCursor(2, 2);
  dma_display->print("FLIGHT END");
  dma_display->setCursor(2, 14);
  if (state.time_known) {
    int m = state.time_s / 60;
    int s = state.time_s % 60;
    dma_display->print(m);
    dma_display->print(':');
    if (s < 10) dma_display->print('0');
    dma_display->print(s);
  }
  dma_display->setCursor(2, 24);
  if (state.alt_known) {
    dma_display->print("PEAK ");
    dma_display->print(state.alt_m);
    dma_display->print('m');
  }
}
