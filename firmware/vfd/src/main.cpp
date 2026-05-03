// ZeroTX VFD diagnostic display firmware — animated.
//
// Hardware: SparkFun Pro Micro 5V/16MHz driving a Noritake
// CU20025ECPB-W1J 2x20 character VFD in 4-bit HD44780 mode.
// Pinout in platformio.ini.
//
// Wire protocol (Pi -> Pro Micro, ASCII over USB-CDC, line-based):
//
//   L<row><sp><content>\n   text overlay; firmware pauses animation
//                           and shows the line for TEXT_HOLD_MS.
//   C\n                     clear (returns to ambient mode).
//   B<sp><level>\n          brightness 0..3 (0 = max).
//   V\n                     show firmware version banner.
//   E<sp><kind>[<sp><args>]\n
//                           event signal that drives animation:
//     E tick [n]            n CRSF frames in the last sample window
//                           (default 1). Feeds the activity bar.
//     E arm 0|1             edge: arm state changed. Triggers a
//                           sweep across both rows then changes mode.
//     E mode <text>         flight mode change. Brief pulse + cache
//                           the mode string for TEXT slots.
//     E lq <pct>            link quality 0..100. Tints / scales bar.
//     E batt <volts>        battery voltage. Cached, displayed in
//                           idle text rotation.
//     E warn | E critical | E failsafe
//                           transient alarm overlays.
//     E disarmed            edge: returning to AMBIENT.
//
// Unknown commands silently ignored. Backward compatible: old
// daemons that only send L/C/B still work.
//
// Animation:
//   - 30 fps render loop in loop(), time-budgeted so serial reads
//     happen between frames.
//   - 8 CGRAM slots hold partial-fill bar glyphs (1/8 .. 8/8 width).
//     Static set, uploaded once at setup.
//   - Modes: IDLE (orbiting dot), AMBIENT (activity bar bottom, text
//     ticker top), ARMED (busier patterns, brighter feel), EVENT
//     (transient overlay), TEXT (paused, showing operator-pushed
//     line). Transitions are state-driven; arm/disarm/alarm move
//     between them.
//   - Idle timeout: if no E commands arrive for IDLE_TIMEOUT_MS,
//     fall back from AMBIENT/ARMED to IDLE animation.

#include <Arduino.h>
#include <hd44780.h>
#include <hd44780ioClass/hd44780_NTCU20025ECPB_pinIO.h>

// ===== Configuration =====

constexpr const char FW_VERSION[] = "0.2.0";

// 4-bit parallel pin assignment.
// SparkFun Pro Micro 5V/16MHz only breaks out D0-D10, D14-D16,
// D18-D21 on the headers; D11, D12, D13 exist in the 32u4 but
// aren't accessible on the board. We use D4-D9 (six contiguous
// pins on the left edge) so the ribbon routing stays clean.
constexpr uint8_t PIN_RS = 4;
constexpr uint8_t PIN_EN = 5;
constexpr uint8_t PIN_D4 = 6;
constexpr uint8_t PIN_D5 = 7;
constexpr uint8_t PIN_D6 = 8;
constexpr uint8_t PIN_D7 = 9;

constexpr uint8_t LCD_COLS = 20;
constexpr uint8_t LCD_ROWS = 2;

constexpr size_t LINE_BUF_SIZE = 64;

// Frame budget: 30 fps target. Rendering is fast (a handful of
// setCursor + write calls), but we cap at 30 fps to leave plenty
// of slack for serial reads and to keep the bus quiet.
constexpr unsigned long FRAME_INTERVAL_MS = 33;

// How long an L<row> text overlay stays before animation resumes.
constexpr unsigned long TEXT_HOLD_MS = 2000;

// Transient-event overlay duration (sweeps, alarm flashes).
constexpr unsigned long EVENT_DURATION_MS = 800;

// If no E commands arrive for this long, drop from AMBIENT/ARMED
// back to IDLE. Says "I'm here but nothing's happening."
constexpr unsigned long IDLE_TIMEOUT_MS = 6000;

// Banner shown at boot until the daemon takes over.
constexpr const char BANNER_ROW0[] = "ZEROTX VFD          ";
constexpr const char BANNER_ROW1_FMT[] = "fw %s awaiting";

// ===== Globals =====

hd44780_NTCU20025ECPB_pinIO lcd(PIN_RS, PIN_EN, PIN_D4, PIN_D5, PIN_D6, PIN_D7);

char lineBuf[LINE_BUF_SIZE];
size_t lineLen = 0;

// CGRAM: 8 user glyphs, each 5 wide x 8 tall. Slots 0..7 map to
// custom characters 0x00..0x07 when written via lcd.write().
// We use slot 0 = 1-pixel fill (leftmost column), slot 7 = full fill.
// This gives us a smooth 8-step horizontal bar by writing slot N
// per cell.
const byte glyphBar[8][8] PROGMEM = {
  // slot 0: 1px (leftmost column lit)
  {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b00000},
  // slot 1: 2px
  {0b11000, 0b11000, 0b11000, 0b11000, 0b11000, 0b11000, 0b11000, 0b00000},
  // slot 2: 3px
  {0b11100, 0b11100, 0b11100, 0b11100, 0b11100, 0b11100, 0b11100, 0b00000},
  // slot 3: 4px
  {0b11110, 0b11110, 0b11110, 0b11110, 0b11110, 0b11110, 0b11110, 0b00000},
  // slot 4: 5px (full width)
  {0b11111, 0b11111, 0b11111, 0b11111, 0b11111, 0b11111, 0b11111, 0b00000},
  // slot 5: dot center (small pulse glyph)
  {0b00000, 0b00000, 0b01110, 0b11111, 0b11111, 0b01110, 0b00000, 0b00000},
  // slot 6: dot small
  {0b00000, 0b00000, 0b00000, 0b01110, 0b01110, 0b00000, 0b00000, 0b00000},
  // slot 7: tick mark (event flash)
  {0b00000, 0b00001, 0b00010, 0b10100, 0b01000, 0b00000, 0b00000, 0b00000},
};

// Animation state.
enum Mode : uint8_t {
  MODE_BANNER = 0,    // boot banner, before any traffic
  MODE_IDLE,          // long idle: slow orbit
  MODE_AMBIENT,       // tick events flowing: activity bar
  MODE_ARMED,         // arm=1: busier rendering
  MODE_TEXT,          // L-pushed text overlay
  MODE_EVENT,         // transient sweep / alarm flash
};

Mode currentMode = MODE_BANNER;
Mode preEventMode = MODE_BANNER;
unsigned long modeEnteredMs = 0;
unsigned long lastEventCmdMs = 0;
unsigned long lastFrameMs = 0;

// Cached event data (set by E commands, used by render).
volatile uint16_t tickAccumulator = 0;   // tick events since last frame
uint16_t           barLevel        = 0;  // 0..160 (8 cells * 20 pixels per row not really, see render)
char               cachedMode[12]  = {0};
uint8_t            cachedLQ        = 0;
bool               haveLQ          = false;
char               cachedBatt[8]   = {0};
bool               armed           = false;

// Event overlay payload.
enum EventKind : uint8_t {
  EV_NONE = 0,
  EV_ARM_TRANSITION,
  EV_DISARM_TRANSITION,
  EV_MODE_CHANGE,
  EV_WARN,
  EV_CRITICAL,
  EV_FAILSAFE,
};
EventKind currentEvent = EV_NONE;
unsigned long eventFrame = 0;       // frames since event started

// Idle animation state.
uint8_t idleOrbitPos = 0;           // 0..(perimeter cells - 1)

// Frame counter for any animation phase.
uint32_t frameCount = 0;

// ===== Helpers =====

static void writeRow(uint8_t row, const char *content) {
  if (row >= LCD_ROWS) return;
  lcd.setCursor(0, row);
  uint8_t i = 0;
  for (; i < LCD_COLS && content[i] != '\0'; i++) {
    lcd.write(content[i]);
  }
  for (; i < LCD_COLS; i++) {
    lcd.write(' ');
  }
}

static void writeRowPadded(uint8_t row, const char *s, uint8_t maxLen) {
  if (row >= LCD_ROWS) return;
  lcd.setCursor(0, row);
  uint8_t i = 0;
  for (; i < LCD_COLS && i < maxLen && s[i] != '\0'; i++) lcd.write(s[i]);
  for (; i < LCD_COLS; i++) lcd.write(' ');
}

static void uploadCustomGlyphs() {
  for (uint8_t slot = 0; slot < 8; slot++) {
    byte g[8];
    for (uint8_t row = 0; row < 8; row++) {
      g[row] = pgm_read_byte(&glyphBar[slot][row]);
    }
    lcd.createChar(slot, g);
  }
}

static void showBanner() {
  lcd.clear();
  writeRow(0, BANNER_ROW0);
  char row1[LCD_COLS + 1];
  snprintf(row1, sizeof(row1), BANNER_ROW1_FMT, FW_VERSION);
  writeRow(1, row1);
}

static void setBrightness(uint8_t level) {
  if (level > 3) level = 3;
  uint8_t bits = (3 - level) & 0x03;
  lcd.command(0x28 | bits);
}

// ===== Animation rendering =====

// renderIdle: a single dot orbits the perimeter at ~1 cell/200ms.
// Perimeter = 2*(20-1) + 2*(2-1) = 40 - 2 = 38 cells (corners not
// double-counted). We just walk top row L->R, then bottom R->L for
// a simple back-and-forth.
static void renderIdle() {
  // Clear first then place dot — cheap; 40 cells written per call
  // is fine at 30 fps.
  static char rows[2][LCD_COLS + 1] = {{0}, {0}};
  memset(rows[0], ' ', LCD_COLS); rows[0][LCD_COLS] = 0;
  memset(rows[1], ' ', LCD_COLS); rows[1][LCD_COLS] = 0;

  // Walk position: 0..LCD_COLS-1 = top row L->R,
  //                LCD_COLS..(2*LCD_COLS-1) = bottom row R->L.
  uint8_t pos = idleOrbitPos % (LCD_COLS * 2);
  if (pos < LCD_COLS) {
    rows[0][pos] = 0x06; // small dot glyph (slot 6)
  } else {
    rows[1][LCD_COLS - 1 - (pos - LCD_COLS)] = 0x06;
  }
  // Slow it down: advance every 6 frames (~200ms at 30fps).
  if ((frameCount & 0x07) == 0) idleOrbitPos++;

  writeRow(0, rows[0]);
  writeRow(1, rows[1]);
}

// renderAmbient: top row shows mode + LQ if known, otherwise slow
// "ZEROTX" ticker. Bottom row is a horizontal activity bar driven
// by tickAccumulator (decays 1/frame).
static void renderAmbient() {
  // Top row text. Compose: "MODE  LQ:NN" or "ZEROTX  uptime tick".
  char top[LCD_COLS + 1];
  if (cachedMode[0] != '\0' && haveLQ) {
    snprintf(top, sizeof(top), "%-12s LQ:%3u", cachedMode, cachedLQ);
  } else if (cachedMode[0] != '\0') {
    snprintf(top, sizeof(top), "%-20s", cachedMode);
  } else if (cachedBatt[0] != '\0') {
    snprintf(top, sizeof(top), "ZEROTX     %-7s", cachedBatt);
  } else {
    snprintf(top, sizeof(top), "ZEROTX             ");
  }
  writeRow(0, top);

  // Bottom row: activity bar from barLevel (units = pixels, with
  // each cell holding 5 pixels via slots 0..4).
  // Sample: drain tickAccumulator into barLevel with decay so bursts
  // visibly rise and fall.
  uint16_t pulled = tickAccumulator;
  tickAccumulator = 0;
  // Each tick adds ~6 pixels of bar (tunable). Decay is 1 pixel/frame.
  uint32_t target = barLevel + (uint32_t)pulled * 6;
  if (barLevel > 0) target = (target > 1) ? target - 1 : 0;
  if (target > LCD_COLS * 5) target = LCD_COLS * 5;
  barLevel = (uint16_t)target;

  // Render bar. Each cell is 5 columns of 5 pixels. We have 5
  // partial-fill glyphs (slots 0..4) representing 1..5 pixels lit.
  // For each cell index i (0..LCD_COLS-1):
  //   pxStart = i * 5
  //   if pxStart >= barLevel -> blank
  //   else if pxStart + 5 <= barLevel -> full (slot 4)
  //   else partial (slot (barLevel - pxStart - 1))
  lcd.setCursor(0, 1);
  for (uint8_t i = 0; i < LCD_COLS; i++) {
    uint16_t pxStart = (uint16_t)i * 5;
    if (pxStart >= barLevel) {
      lcd.write(' ');
    } else {
      uint16_t into = barLevel - pxStart;
      if (into >= 5) {
        lcd.write((uint8_t)4); // full
      } else {
        lcd.write((uint8_t)(into - 1)); // partial 0..3 -> slots 0..3
      }
    }
  }
}

// renderArmed: same shape as ambient but with a busier top-row
// pattern and a heartbeat dot pulsing where the LQ would sit.
static void renderArmed() {
  char top[LCD_COLS + 1];
  // Heartbeat blinks every ~600ms (every 18 frames at 30fps).
  bool blink = ((frameCount / 9) & 0x01) != 0;
  if (cachedMode[0] != '\0' && haveLQ) {
    snprintf(top, sizeof(top), "%c %-10s LQ:%3u",
             blink ? '*' : ' ', cachedMode, cachedLQ);
  } else if (cachedMode[0] != '\0') {
    snprintf(top, sizeof(top), "%c %-18s",
             blink ? '*' : ' ', cachedMode);
  } else {
    snprintf(top, sizeof(top), "%c ARMED              ",
             blink ? '*' : ' ');
  }
  writeRow(0, top);

  // Bar: same as ambient but each tick weighs 8 px (more vivid).
  uint16_t pulled = tickAccumulator;
  tickAccumulator = 0;
  uint32_t target = barLevel + (uint32_t)pulled * 8;
  if (barLevel > 0) target = (target > 1) ? target - 1 : 0;
  if (target > LCD_COLS * 5) target = LCD_COLS * 5;
  barLevel = (uint16_t)target;

  lcd.setCursor(0, 1);
  for (uint8_t i = 0; i < LCD_COLS; i++) {
    uint16_t pxStart = (uint16_t)i * 5;
    if (pxStart >= barLevel) {
      lcd.write(' ');
    } else {
      uint16_t into = barLevel - pxStart;
      if (into >= 5) {
        lcd.write((uint8_t)4);
      } else {
        lcd.write((uint8_t)(into - 1));
      }
    }
  }
}

// renderEvent: transient overlay over whatever the previous mode
// was rendering. Different visuals per kind. Returns to preEventMode
// after EVENT_DURATION_MS.
static void renderEvent() {
  unsigned long t = millis() - modeEnteredMs;
  switch (currentEvent) {
    case EV_ARM_TRANSITION: {
      // Sweep: a bright bar runs left to right across both rows.
      uint8_t pos = (uint8_t)((t * LCD_COLS) / EVENT_DURATION_MS);
      if (pos >= LCD_COLS) pos = LCD_COLS - 1;
      char row[LCD_COLS + 1];
      memset(row, ' ', LCD_COLS); row[LCD_COLS] = 0;
      row[pos] = 0x04; // full-fill glyph
      if (pos > 0) row[pos - 1] = 0x03;
      if (pos > 1) row[pos - 2] = 0x01;
      writeRow(0, row);
      writeRow(1, row);
      break;
    }
    case EV_DISARM_TRANSITION: {
      // Sweep right to left, dimmer.
      uint8_t pos = (uint8_t)((t * LCD_COLS) / EVENT_DURATION_MS);
      if (pos >= LCD_COLS) pos = LCD_COLS - 1;
      uint8_t inv = LCD_COLS - 1 - pos;
      char row[LCD_COLS + 1];
      memset(row, ' ', LCD_COLS); row[LCD_COLS] = 0;
      row[inv] = 0x02;
      writeRow(0, row);
      writeRow(1, row);
      break;
    }
    case EV_MODE_CHANGE: {
      // Pulse: cached mode name centered, surrounded by ticks.
      char top[LCD_COLS + 1];
      uint8_t len = (uint8_t)strnlen(cachedMode, sizeof(cachedMode));
      if (len > LCD_COLS - 4) len = LCD_COLS - 4;
      uint8_t pad = (LCD_COLS - len - 4) / 2;
      memset(top, ' ', LCD_COLS); top[LCD_COLS] = 0;
      bool blink = ((t / 100) & 1) != 0;
      top[pad] = blink ? 0x07 : ' ';
      memcpy(&top[pad + 2], cachedMode, len);
      top[pad + 2 + len + 1] = blink ? 0x07 : ' ';
      writeRow(0, top);
      // bottom: faint bar
      char row1[LCD_COLS + 1];
      memset(row1, ' ', LCD_COLS); row1[LCD_COLS] = 0;
      writeRow(1, row1);
      break;
    }
    case EV_WARN:
    case EV_CRITICAL:
    case EV_FAILSAFE: {
      // Alarm flash: fill both rows with full-bar glyph, blank,
      // repeat. Faster blink for higher severity.
      unsigned long period = (currentEvent == EV_WARN) ? 200 :
                             (currentEvent == EV_CRITICAL) ? 120 : 80;
      bool on = ((t / period) & 1) == 0;
      char row[LCD_COLS + 1];
      if (on) {
        memset(row, 0x04, LCD_COLS); row[LCD_COLS] = 0;
      } else {
        memset(row, ' ', LCD_COLS); row[LCD_COLS] = 0;
      }
      writeRow(0, row);
      writeRow(1, row);
      break;
    }
    default:
      break;
  }
  if (t >= EVENT_DURATION_MS) {
    currentMode = preEventMode;
    modeEnteredMs = millis();
    currentEvent = EV_NONE;
  }
}

// renderText: hold the L-pushed lines until TEXT_HOLD_MS has passed.
// We don't redraw inside this mode (lcd content was set when L was
// processed); just check the timer.
static void renderText() {
  if (millis() - modeEnteredMs >= TEXT_HOLD_MS) {
    currentMode = armed ? MODE_ARMED : MODE_AMBIENT;
    modeEnteredMs = millis();
  }
}

static void renderBanner() {
  // Banner is static; nothing to redraw. Transition to IDLE if the
  // daemon hasn't said anything for IDLE_TIMEOUT_MS.
  if (millis() - modeEnteredMs >= IDLE_TIMEOUT_MS) {
    lcd.clear();
    currentMode = MODE_IDLE;
    modeEnteredMs = millis();
  }
}

static void enterMode(Mode m) {
  if (m == currentMode) return;
  currentMode = m;
  modeEnteredMs = millis();
}

static void enterEvent(EventKind k) {
  preEventMode = currentMode;
  currentEvent = k;
  enterMode(MODE_EVENT);
}

// ===== Command processor =====

static void processL(const char *line, size_t len) {
  if (len < 4) return;
  if (line[1] < '0' || line[1] > '9') return;
  if (line[2] != ' ') return;
  uint8_t row = line[1] - '0';
  writeRow(row, &line[3]);
  enterMode(MODE_TEXT);
  lastEventCmdMs = millis();
}

static void processB(const char *line, size_t len) {
  if (len < 3) return;
  if (line[1] != ' ') return;
  uint8_t level = (uint8_t)(line[2] - '0');
  setBrightness(level);
}

// processE: parse "E <kind> [args]" and update animation state.
// Caller passes the line; we tokenize in place.
static void processE(char *line, size_t len) {
  if (len < 3) return;
  // Skip "E "
  char *p = line + 2;
  // Find kind end (space or NUL).
  char *kind = p;
  while (*p && *p != ' ') p++;
  bool hasArg = (*p == ' ');
  if (hasArg) { *p = '\0'; p++; }
  // p now points at args (or empty string).

  lastEventCmdMs = millis();

  if (strcmp(kind, "tick") == 0) {
    uint16_t n = 1;
    if (hasArg) {
      int v = atoi(p);
      if (v > 0) n = (uint16_t)v;
    }
    tickAccumulator += n;
    if (currentMode == MODE_BANNER || currentMode == MODE_IDLE) {
      enterMode(armed ? MODE_ARMED : MODE_AMBIENT);
    }
  } else if (strcmp(kind, "arm") == 0 && hasArg) {
    bool wasArmed = armed;
    armed = (*p == '1');
    if (armed != wasArmed) {
      enterEvent(armed ? EV_ARM_TRANSITION : EV_DISARM_TRANSITION);
    }
  } else if (strcmp(kind, "mode") == 0 && hasArg) {
    strncpy(cachedMode, p, sizeof(cachedMode) - 1);
    cachedMode[sizeof(cachedMode) - 1] = '\0';
    enterEvent(EV_MODE_CHANGE);
  } else if (strcmp(kind, "lq") == 0 && hasArg) {
    int v = atoi(p);
    if (v < 0) v = 0;
    if (v > 100) v = 100;
    cachedLQ = (uint8_t)v;
    haveLQ = true;
  } else if (strcmp(kind, "batt") == 0 && hasArg) {
    strncpy(cachedBatt, p, sizeof(cachedBatt) - 1);
    cachedBatt[sizeof(cachedBatt) - 1] = '\0';
  } else if (strcmp(kind, "warn") == 0) {
    enterEvent(EV_WARN);
  } else if (strcmp(kind, "critical") == 0) {
    enterEvent(EV_CRITICAL);
  } else if (strcmp(kind, "failsafe") == 0) {
    enterEvent(EV_FAILSAFE);
  } else if (strcmp(kind, "disarmed") == 0) {
    armed = false;
    enterMode(MODE_AMBIENT);
  }
  // Unknown kinds ignored.
}

static void processLine(char *line, size_t len) {
  if (len == 0) return;
  switch (line[0]) {
    case 'L': processL(line, len); break;
    case 'C': lcd.clear(); enterMode(MODE_AMBIENT); break;
    case 'B': processB(line, len); break;
    case 'V': showBanner(); enterMode(MODE_BANNER); break;
    case 'E': processE(line, len); break;
    default: break; // tolerate version skew
  }
}

// ===== Arduino entry points =====

void setup() {
  Serial.begin(115200);

  int rc = lcd.begin(LCD_COLS, LCD_ROWS);
  if (rc != 0) {
    pinMode(LED_BUILTIN, OUTPUT);
    while (true) {
      digitalWrite(LED_BUILTIN, HIGH);
      delay(100);
      digitalWrite(LED_BUILTIN, LOW);
      delay(100);
    }
  }

  uploadCustomGlyphs();
  setBrightness(0);
  showBanner();
  modeEnteredMs = millis();
  lastEventCmdMs = millis();
}

void loop() {
  // 1. Drain serial input opportunistically. Multiple lines may
  //    arrive between frames; process them all so we don't accumulate
  //    backlog.
  while (Serial.available() > 0) {
    int c = Serial.read();
    if (c < 0) break;
    if (c == '\r') continue;
    if (c == '\n') {
      lineBuf[lineLen] = '\0';
      processLine(lineBuf, lineLen);
      lineLen = 0;
      continue;
    }
    if (lineLen + 1 < LINE_BUF_SIZE) {
      lineBuf[lineLen++] = (char)c;
    } else {
      lineLen = 0; // overflow; resync on next \n
    }
  }

  // 2. Render at FRAME_INTERVAL_MS.
  unsigned long now = millis();
  if (now - lastFrameMs < FRAME_INTERVAL_MS) return;
  lastFrameMs = now;
  frameCount++;

  // 3. Idle timeout: if events have stopped flowing, fall back to
  //    IDLE animation. Skip if we're in a transient/text mode.
  if (currentMode == MODE_AMBIENT || currentMode == MODE_ARMED) {
    if (now - lastEventCmdMs > IDLE_TIMEOUT_MS) {
      enterMode(MODE_IDLE);
    }
  }

  // 4. Render current mode.
  switch (currentMode) {
    case MODE_BANNER:  renderBanner();  break;
    case MODE_IDLE:    renderIdle();    break;
    case MODE_AMBIENT: renderAmbient(); break;
    case MODE_ARMED:   renderArmed();   break;
    case MODE_TEXT:    renderText();    break;
    case MODE_EVENT:   renderEvent();   break;
  }
}
