// vfd.cpp - VFD subsystem implementation. Port of the Pro Micro
// firmware's animation engine into the new subsystem framework.
//
// Most of the rendering code is structurally unchanged from the Pro
// Micro version: same modes, same per-mode renderers, same custom
// glyph layout. Differences:
//   - State that was file-scope global is now class members.
//   - The line-protocol parser is gone; commands arrive as parsed
//     proto::Command structs from the framework dispatcher.
//   - Pin numbers come from HAL at begin(), and the hd44780 driver
//     is constructed in-place via placement-new since its pins are
//     ctor args and we don't know them at module-load time.

#include "vfd.h"

#include <new>      // placement new
#include <string.h>
#include <stdlib.h>

#include "../hal.h"

namespace zerotx {

// 8 user CGRAM glyphs. Identical to the Pro Micro firmware - this
// is the bar-fill plus dot/tick set.
static const byte kGlyphs[8][8] PROGMEM = {
  {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b00000}, // 1px
  {0b11000, 0b11000, 0b11000, 0b11000, 0b11000, 0b11000, 0b11000, 0b00000}, // 2px
  {0b11100, 0b11100, 0b11100, 0b11100, 0b11100, 0b11100, 0b11100, 0b00000}, // 3px
  {0b11110, 0b11110, 0b11110, 0b11110, 0b11110, 0b11110, 0b11110, 0b00000}, // 4px
  {0b11111, 0b11111, 0b11111, 0b11111, 0b11111, 0b11111, 0b11111, 0b00000}, // full
  {0b00000, 0b00000, 0b01110, 0b11111, 0b11111, 0b01110, 0b00000, 0b00000}, // dot center
  {0b00000, 0b00000, 0b00000, 0b01110, 0b01110, 0b00000, 0b00000, 0b00000}, // dot small
  {0b00000, 0b00001, 0b00010, 0b10100, 0b01000, 0b00000, 0b00000, 0b00000}, // tick mark
};

static constexpr const char  kBannerRow0[] = "ZEROTX VFD          ";
static constexpr const char  kBannerRow1[] = "fw 0.2.0-hal awaiting";

// =============================================================================
// Lifecycle
// =============================================================================

void Vfd::begin(Stream& out) {
  uint8_t rs = hal::pin(hal::HAL_VFD0_RS);
  uint8_t en = hal::pin(hal::HAL_VFD0_EN);
  uint8_t d4 = hal::pin(hal::HAL_VFD0_D4);
  uint8_t d5 = hal::pin(hal::HAL_VFD0_D5);
  uint8_t d6 = hal::pin(hal::HAL_VFD0_D6);
  uint8_t d7 = hal::pin(hal::HAL_VFD0_D7);

  // Construct the LCD driver in-place. The hd44780 library doesn't
  // support default-construction-then-init, so we delay construction
  // until we know the pin numbers.
  lcd_ = new (lcd_storage_) hd44780_NTCU20025ECPB_pinIO(rs, en, d4, d5, d6, d7);

  int rc = lcd_->begin(kCols, kRows);
  if (rc != 0) {
    char buf[40];
    snprintf(buf, sizeof(buf), "init-failed rc=%d", rc);
    proto::writeError(out, "vfd.0", buf);
    return;
  }

  uploadCustomGlyphs();
  setBrightnessLevel(0);
  showBanner();

  uint32_t now = millis();
  mode_entered_ms_   = now;
  last_event_cmd_ms_ = now;
  last_frame_ms_     = now;
  mode_              = Mode::Banner;

  proto::writeEvent(out, "vfd.0", "ready");
}

// =============================================================================
// Tick: cooperative render loop
// =============================================================================

void Vfd::tick(uint32_t now_ms, Stream& out) {
  (void)out;
  if (!lcd_) return;

  // Frame budget. Render at ~30fps; serial reads happen in main loop
  // between ticks so we don't need to interleave them here.
  if (now_ms - last_frame_ms_ < kFrameIntervalMs) return;
  last_frame_ms_ = now_ms;
  frame_count_++;

  // Idle timeout: drop to IDLE if events have stopped flowing while
  // in AMBIENT or ARMED.
  if (mode_ == Mode::Ambient || mode_ == Mode::Armed) {
    if (now_ms - last_event_cmd_ms_ > kIdleTimeoutMs) {
      enterMode(Mode::Idle);
    }
  }

  switch (mode_) {
    case Mode::Banner:  renderBanner(now_ms);  break;
    case Mode::Idle:    renderIdle();          break;
    case Mode::Ambient: renderAmbient();       break;
    case Mode::Armed:   renderArmed();         break;
    case Mode::Text:    renderText(now_ms);    break;
    case Mode::Event:   renderEvent(now_ms);   break;
  }
}

// =============================================================================
// Helpers
// =============================================================================

void Vfd::writeRow(uint8_t row, const char* content) {
  if (row >= kRows) return;
  lcd_->setCursor(0, row);
  uint8_t i = 0;
  for (; i < kCols && content[i] != '\0'; i++) {
    lcd_->write(content[i]);
  }
  for (; i < kCols; i++) {
    lcd_->write(' ');
  }
}

void Vfd::uploadCustomGlyphs() {
  for (uint8_t slot = 0; slot < 8; slot++) {
    byte g[8];
    for (uint8_t row = 0; row < 8; row++) {
      g[row] = pgm_read_byte(&kGlyphs[slot][row]);
    }
    lcd_->createChar(slot, g);
  }
}

void Vfd::setBrightnessLevel(uint8_t level) {
  if (level > 3) level = 3;
  // Noritake brightness via the function-set extension: 0x28 base +
  // (3 - level) in the low 2 bits gives 0=brightest..3=dimmest as
  // exposed to the operator.
  uint8_t bits = (3 - level) & 0x03;
  lcd_->command(0x28 | bits);
}

void Vfd::showBanner() {
  lcd_->clear();
  writeRow(0, kBannerRow0);
  writeRow(1, kBannerRow1);
}

void Vfd::enterMode(Mode m) {
  if (m == mode_) return;
  mode_ = m;
  mode_entered_ms_ = millis();
}

void Vfd::enterEvent(Event k) {
  pre_event_ = mode_;
  event_kind_ = k;
  enterMode(Mode::Event);
}

// =============================================================================
// Renderers
// =============================================================================

void Vfd::renderBanner(uint32_t now_ms) {
  // Banner is static; transition to IDLE if no traffic for the
  // timeout. Same logic as the Pro Micro version.
  if (now_ms - mode_entered_ms_ >= kIdleTimeoutMs) {
    lcd_->clear();
    enterMode(Mode::Idle);
  }
}

void Vfd::renderIdle() {
  // Single dot orbits the perimeter. Top row L->R, bottom row R->L.
  static char rows[2][kCols + 1] = {{0}, {0}};
  memset(rows[0], ' ', kCols); rows[0][kCols] = 0;
  memset(rows[1], ' ', kCols); rows[1][kCols] = 0;

  uint8_t pos = idle_orbit_pos_ % (kCols * 2);
  if (pos < kCols) {
    rows[0][pos] = 0x06;
  } else {
    rows[1][kCols - 1 - (pos - kCols)] = 0x06;
  }
  if ((frame_count_ & 0x07) == 0) idle_orbit_pos_++;

  writeRow(0, rows[0]);
  writeRow(1, rows[1]);
}

// renderActivityBar: decay the bar by 1px and add weight per
// accumulated tick. Shared between AMBIENT and ARMED.
void Vfd::renderActivityBar(uint16_t weight_per_tick) {
  uint16_t pulled = tick_accumulator_;
  tick_accumulator_ = 0;
  uint32_t target = bar_level_ + (uint32_t)pulled * weight_per_tick;
  if (bar_level_ > 0) target = (target > 1) ? target - 1 : 0;
  if (target > kCols * 5) target = kCols * 5;
  bar_level_ = (uint16_t)target;

  lcd_->setCursor(0, 1);
  for (uint8_t i = 0; i < kCols; i++) {
    uint16_t pxStart = (uint16_t)i * 5;
    if (pxStart >= bar_level_) {
      lcd_->write(' ');
    } else {
      uint16_t into = bar_level_ - pxStart;
      if (into >= 5) {
        lcd_->write((uint8_t)4);
      } else {
        lcd_->write((uint8_t)(into - 1));
      }
    }
  }
}

void Vfd::renderAmbient() {
  char top[kCols + 1];
  if (cached_fmmode_[0] != '\0' && have_lq_) {
    snprintf(top, sizeof(top), "%-12s LQ:%3u", cached_fmmode_, cached_lq_);
  } else if (cached_fmmode_[0] != '\0') {
    snprintf(top, sizeof(top), "%-20s", cached_fmmode_);
  } else if (cached_batt_[0] != '\0') {
    snprintf(top, sizeof(top), "ZEROTX     %-7s", cached_batt_);
  } else {
    snprintf(top, sizeof(top), "ZEROTX             ");
  }
  writeRow(0, top);
  renderActivityBar(/*weight_per_tick=*/6);
}

void Vfd::renderArmed() {
  char top[kCols + 1];
  bool blink = ((frame_count_ / 9) & 0x01) != 0;
  if (cached_fmmode_[0] != '\0' && have_lq_) {
    snprintf(top, sizeof(top), "%c %-10s LQ:%3u",
             blink ? '*' : ' ', cached_fmmode_, cached_lq_);
  } else if (cached_fmmode_[0] != '\0') {
    snprintf(top, sizeof(top), "%c %-18s",
             blink ? '*' : ' ', cached_fmmode_);
  } else {
    snprintf(top, sizeof(top), "%c ARMED              ",
             blink ? '*' : ' ');
  }
  writeRow(0, top);
  renderActivityBar(/*weight_per_tick=*/8);
}

void Vfd::renderEvent(uint32_t now_ms) {
  uint32_t t = now_ms - mode_entered_ms_;
  switch (event_kind_) {
    case Event::ArmTransition: {
      uint8_t pos = (uint8_t)((t * kCols) / kEventDurationMs);
      if (pos >= kCols) pos = kCols - 1;
      char row[kCols + 1];
      memset(row, ' ', kCols); row[kCols] = 0;
      row[pos] = 0x04;
      if (pos > 0) row[pos - 1] = 0x03;
      if (pos > 1) row[pos - 2] = 0x01;
      writeRow(0, row);
      writeRow(1, row);
      break;
    }
    case Event::DisarmTransition: {
      uint8_t pos = (uint8_t)((t * kCols) / kEventDurationMs);
      if (pos >= kCols) pos = kCols - 1;
      uint8_t inv = kCols - 1 - pos;
      char row[kCols + 1];
      memset(row, ' ', kCols); row[kCols] = 0;
      row[inv] = 0x02;
      writeRow(0, row);
      writeRow(1, row);
      break;
    }
    case Event::ModeChange: {
      char top[kCols + 1];
      uint8_t len = (uint8_t)strnlen(cached_fmmode_, sizeof(cached_fmmode_));
      if (len > kCols - 4) len = kCols - 4;
      uint8_t pad = (kCols - len - 4) / 2;
      memset(top, ' ', kCols); top[kCols] = 0;
      bool blink = ((t / 100) & 1) != 0;
      top[pad] = blink ? 0x07 : ' ';
      memcpy(&top[pad + 2], cached_fmmode_, len);
      top[pad + 2 + len + 1] = blink ? 0x07 : ' ';
      writeRow(0, top);
      char row1[kCols + 1];
      memset(row1, ' ', kCols); row1[kCols] = 0;
      writeRow(1, row1);
      break;
    }
    case Event::Warn:
    case Event::Critical:
    case Event::Failsafe: {
      uint32_t period = (event_kind_ == Event::Warn)     ? 200 :
                        (event_kind_ == Event::Critical) ? 120 : 80;
      bool on = ((t / period) & 1) == 0;
      char row[kCols + 1];
      if (on) { memset(row, 0x04, kCols); row[kCols] = 0; }
      else    { memset(row, ' ',  kCols); row[kCols] = 0; }
      writeRow(0, row);
      writeRow(1, row);
      break;
    }
    case Event::None:
      break;
  }
  if (t >= kEventDurationMs) {
    mode_ = pre_event_;
    mode_entered_ms_ = now_ms;
    event_kind_ = Event::None;
  }
}

void Vfd::renderText(uint32_t now_ms) {
  // Display content was set when the SET vfd.0 line command was
  // processed; here we just hold and time-out back to the active
  // animation.
  if (now_ms - mode_entered_ms_ >= kTextHoldMs) {
    mode_ = armed_ ? Mode::Armed : Mode::Ambient;
    mode_entered_ms_ = now_ms;
  }
}

// =============================================================================
// Command dispatch
// =============================================================================

bool Vfd::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance != 0) return false;
  if (!lcd_) {
    proto::writeError(out, "vfd.0", "not-initialized");
    return true;
  }

  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "SET") == 0) return handleSet(cmd, out);
  if (strcmp(verb, "GET") == 0) { handleGet(out); return true; }
  return false;
}

bool Vfd::handleSet(const proto::Command& cmd, Stream& out) {
  const char* p = cmd.param();
  if (!p) {
    proto::writeError(out, "vfd.0", "missing-param");
    return true;
  }

  last_event_cmd_ms_ = millis();

  if (strcmp(p, "mode") == 0) {
    const char* m = cmd.arg(0);
    if (!m) { proto::writeError(out, "vfd.0", "missing-mode"); return true; }
    if      (strcmp(m, "banner")  == 0) { showBanner();  enterMode(Mode::Banner); }
    else if (strcmp(m, "idle")    == 0) { lcd_->clear(); enterMode(Mode::Idle); }
    else if (strcmp(m, "ambient") == 0) { enterMode(Mode::Ambient); }
    else if (strcmp(m, "armed")   == 0) { armed_ = true; enterMode(Mode::Armed); }
    else { proto::writeError(out, "vfd.0", "invalid-mode"); }
    return true;
  }

  if (strcmp(p, "brightness") == 0) {
    const char* lvl = cmd.arg(0);
    if (!lvl) { proto::writeError(out, "vfd.0", "missing-level"); return true; }
    int v = atoi(lvl);
    if (v < 0 || v > 3) { proto::writeError(out, "vfd.0", "invalid-level"); return true; }
    setBrightnessLevel((uint8_t)v);
    return true;
  }

  if (strcmp(p, "clear") == 0) {
    lcd_->clear();
    enterMode(Mode::Ambient);
    return true;
  }

  if (strcmp(p, "line") == 0) {
    const char* rowStr = cmd.arg(0);
    if (!rowStr) { proto::writeError(out, "vfd.0", "missing-row"); return true; }
    int row = atoi(rowStr);
    if (row < 0 || row >= kRows) {
      proto::writeError(out, "vfd.0", "invalid-row"); return true;
    }
    // Concatenate remaining tokens as space-separated text. The
    // protocol parser tokenizes whitespace; we glue tokens back here
    // so multi-word lines work.
    char buf[kCols + 1];
    size_t pos = 0;
    for (size_t i = 1; ; ++i) {
      const char* tok = cmd.arg(i);
      if (!tok) break;
      if (pos > 0 && pos < sizeof(buf) - 1) buf[pos++] = ' ';
      while (*tok && pos < sizeof(buf) - 1) buf[pos++] = *tok++;
    }
    buf[pos] = '\0';
    writeRow((uint8_t)row, buf);
    enterMode(Mode::Text);
    return true;
  }

  if (strcmp(p, "tick") == 0) {
    uint16_t n = 1;
    const char* nStr = cmd.arg(0);
    if (nStr) {
      int v = atoi(nStr);
      if (v > 0) n = (uint16_t)v;
    }
    tick_accumulator_ += n;
    if (mode_ == Mode::Banner || mode_ == Mode::Idle) {
      enterMode(armed_ ? Mode::Armed : Mode::Ambient);
    }
    return true;
  }

  if (strcmp(p, "arm") == 0) {
    const char* a = cmd.arg(0);
    if (!a) { proto::writeError(out, "vfd.0", "missing-arm-state"); return true; }
    bool was = armed_;
    armed_ = (*a == '1');
    if (armed_ != was) {
      enterEvent(armed_ ? Event::ArmTransition : Event::DisarmTransition);
    }
    return true;
  }

  if (strcmp(p, "fmmode") == 0) {
    const char* m = cmd.arg(0);
    if (!m) { proto::writeError(out, "vfd.0", "missing-fmmode"); return true; }
    strncpy(cached_fmmode_, m, sizeof(cached_fmmode_) - 1);
    cached_fmmode_[sizeof(cached_fmmode_) - 1] = '\0';
    enterEvent(Event::ModeChange);
    return true;
  }

  if (strcmp(p, "lq") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, "vfd.0", "missing-lq"); return true; }
    int n = atoi(v);
    if (n < 0) n = 0;
    if (n > 100) n = 100;
    cached_lq_ = (uint8_t)n;
    have_lq_ = true;
    return true;
  }

  if (strcmp(p, "batt") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, "vfd.0", "missing-batt"); return true; }
    strncpy(cached_batt_, v, sizeof(cached_batt_) - 1);
    cached_batt_[sizeof(cached_batt_) - 1] = '\0';
    return true;
  }

  if (strcmp(p, "alarm") == 0) {
    const char* k = cmd.arg(0);
    if (!k) { proto::writeError(out, "vfd.0", "missing-kind"); return true; }
    if      (strcmp(k, "warn")     == 0) enterEvent(Event::Warn);
    else if (strcmp(k, "critical") == 0) enterEvent(Event::Critical);
    else if (strcmp(k, "failsafe") == 0) enterEvent(Event::Failsafe);
    else { proto::writeError(out, "vfd.0", "invalid-alarm"); return true; }
    return true;
  }

  if (strcmp(p, "disarmed") == 0) {
    armed_ = false;
    enterMode(Mode::Ambient);
    return true;
  }

  proto::writeError(out, "vfd.0", "unknown-param");
  return true;
}

void Vfd::handleGet(Stream& out) {
  char body[80];
  snprintf(body, sizeof(body),
      "vfd.0 mode=%s armed=%d fmmode=%s lq=%s%u batt=%s",
      modeName(mode_),
      armed_ ? 1 : 0,
      cached_fmmode_[0] ? cached_fmmode_ : "-",
      have_lq_ ? "" : "-", have_lq_ ? cached_lq_ : 0,
      cached_batt_[0] ? cached_batt_ : "-");
  proto::writeResponse(out, body);
}

const char* Vfd::modeName(Mode m) {
  switch (m) {
    case Mode::Banner:  return "banner";
    case Mode::Idle:    return "idle";
    case Mode::Ambient: return "ambient";
    case Mode::Armed:   return "armed";
    case Mode::Text:    return "text";
    case Mode::Event:   return "event";
  }
  return "unknown";
}

}  // namespace zerotx
