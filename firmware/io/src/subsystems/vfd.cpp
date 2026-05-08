// vfd.cpp - VFD subsystem implementation. Animation engine for the
// Noritake CU20025ECPB-W1J 2x20 character VFD. Two instances:
// vfd.0 on HAL_VFD0_* pins, vfd.1 on HAL_VFD1_* pins. Each runs the
// engine independently with its own cached telemetry and timing.

#include "vfd.h"

#include <new>      // placement new
#include <string.h>
#include <stdlib.h>

namespace zerotx {

// ----- Per-instance HAL pin slot lookup --------------------------------------

const Vfd::PinSlots Vfd::kPinsForInstance[kInstanceCount] = {
  { hal::HAL_VFD0_RS, hal::HAL_VFD0_EN,
    hal::HAL_VFD0_D4, hal::HAL_VFD0_D5,
    hal::HAL_VFD0_D6, hal::HAL_VFD0_D7 },
  { hal::HAL_VFD1_RS, hal::HAL_VFD1_EN,
    hal::HAL_VFD1_D4, hal::HAL_VFD1_D5,
    hal::HAL_VFD1_D6, hal::HAL_VFD1_D7 },
};

const char* const Vfd::kInstanceLabel[kInstanceCount] = {
  "vfd.0", "vfd.1",
};

// ----- Custom glyphs (shared, uploaded to each instance) ---------------------

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
static constexpr const char  kBannerRow1[] = "fw 0.3.0-multi      ";

// =============================================================================
// Lifecycle
// =============================================================================

void Vfd::begin(Stream& out) {
  uint32_t now = millis();

  for (uint8_t i = 0; i < kInstanceCount; ++i) {
    Instance& inst = instances_[i];

    // Zero out POD state. We don't rely on the default constructor
    // for an in-place struct of this size, partly because the LCD
    // pointer needs to start as nullptr before placement-new sets it.
    inst.tick_accumulator   = 0;
    inst.bar_level          = 0;
    inst.cached_fmmode[0]   = '\0';
    inst.cached_lq          = 0;
    inst.have_lq            = false;
    inst.cached_batt[0]     = '\0';
    inst.armed              = false;
    inst.mode               = Mode::Banner;
    inst.pre_event          = Mode::Banner;
    inst.event_kind         = EventKind::None;
    inst.mode_entered_ms    = now;
    inst.last_event_cmd_ms  = now;
    inst.last_frame_ms      = now;
    inst.frame_count        = 0;
    inst.idle_orbit_pos     = 0;
    inst.lcd                = nullptr;

    const PinSlots& slots = kPinsForInstance[i];
    uint8_t rs = hal::pin(slots.rs);
    uint8_t en = hal::pin(slots.en);
    uint8_t d4 = hal::pin(slots.d4);
    uint8_t d5 = hal::pin(slots.d5);
    uint8_t d6 = hal::pin(slots.d6);
    uint8_t d7 = hal::pin(slots.d7);

    // Construct the LCD driver in-place. The hd44780 library doesn't
    // support default-construction-then-init, so we delay construction
    // until we know the pin numbers.
    auto* lcd = new (inst.lcd_storage)
        hd44780_NTCU20025ECPB_pinIO(rs, en, d4, d5, d6, d7);

    int rc = lcd->begin(kCols, kRows);
    if (rc != 0) {
      // Init failed: typical cause is no display connected. Leave
      // inst.lcd as nullptr so tick()/handle() skip this instance
      // cleanly. The placement-new'd object is still in storage but
      // we never call into it.
      char buf[40];
      snprintf(buf, sizeof(buf), "init-failed rc=%d", rc);
      proto::writeError(out, kInstanceLabel[i], buf);
      continue;
    }

    inst.lcd = lcd;
    uploadCustomGlyphs(inst);
    setBrightnessLevel(inst, 0);
    showBanner(inst);

    proto::writeEvent(out, kInstanceLabel[i], "ready");
  }
}

// =============================================================================
// Tick: cooperative render loop per instance
// =============================================================================

void Vfd::tick(uint32_t now_ms, Stream& out) {
  (void)out;

  for (uint8_t i = 0; i < kInstanceCount; ++i) {
    Instance& inst = instances_[i];
    if (!inst.lcd) continue;

    // Frame budget. Render at ~30fps; serial reads happen in main loop
    // between ticks so we don't need to interleave them here.
    if (now_ms - inst.last_frame_ms < kFrameIntervalMs) continue;
    inst.last_frame_ms = now_ms;
    inst.frame_count++;

    // Idle timeout: drop to IDLE if events have stopped flowing while
    // in AMBIENT or ARMED.
    if (inst.mode == Mode::Ambient || inst.mode == Mode::Armed) {
      if (now_ms - inst.last_event_cmd_ms > kIdleTimeoutMs) {
        enterMode(inst, Mode::Idle);
      }
    }

    switch (inst.mode) {
      case Mode::Banner:  renderBanner(inst, now_ms);  break;
      case Mode::Idle:    renderIdle(inst);            break;
      case Mode::Ambient: renderAmbient(inst);         break;
      case Mode::Armed:   renderArmed(inst);           break;
      case Mode::Text:    renderText(inst, now_ms);    break;
      case Mode::Event:   renderEvent(inst, now_ms);   break;
    }
  }
}

// =============================================================================
// Helpers
// =============================================================================

void Vfd::writeRow(Instance& inst, uint8_t row, const char* content) {
  if (row >= kRows) return;
  inst.lcd->setCursor(0, row);
  uint8_t i = 0;
  for (; i < kCols && content[i] != '\0'; i++) {
    inst.lcd->write(content[i]);
  }
  for (; i < kCols; i++) {
    inst.lcd->write(' ');
  }
}

void Vfd::uploadCustomGlyphs(Instance& inst) {
  for (uint8_t slot = 0; slot < 8; slot++) {
    byte g[8];
    for (uint8_t row = 0; row < 8; row++) {
      g[row] = pgm_read_byte(&kGlyphs[slot][row]);
    }
    inst.lcd->createChar(slot, g);
  }
}

void Vfd::setBrightnessLevel(Instance& inst, uint8_t level) {
  if (level > 3) level = 3;
  // Noritake brightness via the function-set extension: 0x28 base +
  // (3 - level) in the low 2 bits gives 0=brightest..3=dimmest as
  // exposed to the operator.
  uint8_t bits = (3 - level) & 0x03;
  inst.lcd->command(0x28 | bits);
}

void Vfd::showBanner(Instance& inst) {
  inst.lcd->clear();
  writeRow(inst, 0, kBannerRow0);
  writeRow(inst, 1, kBannerRow1);
}

void Vfd::enterMode(Instance& inst, Mode m) {
  if (m == inst.mode) return;
  inst.mode = m;
  inst.mode_entered_ms = millis();
}

void Vfd::enterEvent(Instance& inst, EventKind k) {
  inst.pre_event  = inst.mode;
  inst.event_kind = k;
  enterMode(inst, Mode::Event);
}

// =============================================================================
// Renderers
// =============================================================================

void Vfd::renderBanner(Instance& inst, uint32_t now_ms) {
  // Banner is static; transition to IDLE if no traffic for the
  // timeout.
  if (now_ms - inst.mode_entered_ms >= kIdleTimeoutMs) {
    inst.lcd->clear();
    enterMode(inst, Mode::Idle);
  }
}

void Vfd::renderIdle(Instance& inst) {
  // Single dot orbits the perimeter. Top row L->R, bottom row R->L.
  char rows[2][kCols + 1] = {{0}, {0}};
  memset(rows[0], ' ', kCols); rows[0][kCols] = 0;
  memset(rows[1], ' ', kCols); rows[1][kCols] = 0;

  uint8_t pos = inst.idle_orbit_pos % (kCols * 2);
  if (pos < kCols) {
    rows[0][pos] = 0x06;
  } else {
    rows[1][kCols - 1 - (pos - kCols)] = 0x06;
  }
  if ((inst.frame_count & 0x07) == 0) inst.idle_orbit_pos++;

  writeRow(inst, 0, rows[0]);
  writeRow(inst, 1, rows[1]);
}

// renderActivityBar: decay the bar by 1px and add weight per
// accumulated tick. Shared between AMBIENT and ARMED.
void Vfd::renderActivityBar(Instance& inst, uint16_t weight_per_tick) {
  uint16_t pulled = inst.tick_accumulator;
  inst.tick_accumulator = 0;
  uint32_t target = inst.bar_level + (uint32_t)pulled * weight_per_tick;
  if (inst.bar_level > 0) target = (target > 1) ? target - 1 : 0;
  if (target > kCols * 5) target = kCols * 5;
  inst.bar_level = (uint16_t)target;

  inst.lcd->setCursor(0, 1);
  for (uint8_t i = 0; i < kCols; i++) {
    uint16_t pxStart = (uint16_t)i * 5;
    if (pxStart >= inst.bar_level) {
      inst.lcd->write(' ');
    } else {
      uint16_t into = inst.bar_level - pxStart;
      if (into >= 5) {
        inst.lcd->write((uint8_t)4);
      } else {
        inst.lcd->write((uint8_t)(into - 1));
      }
    }
  }
}

void Vfd::renderAmbient(Instance& inst) {
  char top[kCols + 1];
  if (inst.cached_fmmode[0] != '\0' && inst.have_lq) {
    snprintf(top, sizeof(top), "%-12s LQ:%3u", inst.cached_fmmode, inst.cached_lq);
  } else if (inst.cached_fmmode[0] != '\0') {
    snprintf(top, sizeof(top), "%-20s", inst.cached_fmmode);
  } else if (inst.cached_batt[0] != '\0') {
    snprintf(top, sizeof(top), "ZEROTX     %-7s", inst.cached_batt);
  } else {
    snprintf(top, sizeof(top), "ZEROTX             ");
  }
  writeRow(inst, 0, top);
  renderActivityBar(inst, /*weight_per_tick=*/6);
}

void Vfd::renderArmed(Instance& inst) {
  char top[kCols + 1];
  bool blink = ((inst.frame_count / 9) & 0x01) != 0;
  if (inst.cached_fmmode[0] != '\0' && inst.have_lq) {
    snprintf(top, sizeof(top), "%c %-10s LQ:%3u",
             blink ? '*' : ' ', inst.cached_fmmode, inst.cached_lq);
  } else if (inst.cached_fmmode[0] != '\0') {
    snprintf(top, sizeof(top), "%c %-18s",
             blink ? '*' : ' ', inst.cached_fmmode);
  } else {
    snprintf(top, sizeof(top), "%c ARMED              ",
             blink ? '*' : ' ');
  }
  writeRow(inst, 0, top);
  renderActivityBar(inst, /*weight_per_tick=*/8);
}

void Vfd::renderEvent(Instance& inst, uint32_t now_ms) {
  uint32_t t = now_ms - inst.mode_entered_ms;
  switch (inst.event_kind) {
    case EventKind::ArmTransition: {
      uint8_t pos = (uint8_t)((t * kCols) / kEventDurationMs);
      if (pos >= kCols) pos = kCols - 1;
      char row[kCols + 1];
      memset(row, ' ', kCols); row[kCols] = 0;
      row[pos] = 0x04;
      if (pos > 0) row[pos - 1] = 0x03;
      if (pos > 1) row[pos - 2] = 0x01;
      writeRow(inst, 0, row);
      writeRow(inst, 1, row);
      break;
    }
    case EventKind::DisarmTransition: {
      uint8_t pos = (uint8_t)((t * kCols) / kEventDurationMs);
      if (pos >= kCols) pos = kCols - 1;
      uint8_t inv = kCols - 1 - pos;
      char row[kCols + 1];
      memset(row, ' ', kCols); row[kCols] = 0;
      row[inv] = 0x02;
      writeRow(inst, 0, row);
      writeRow(inst, 1, row);
      break;
    }
    case EventKind::ModeChange: {
      char top[kCols + 1];
      uint8_t len = (uint8_t)strnlen(inst.cached_fmmode, sizeof(inst.cached_fmmode));
      if (len > kCols - 4) len = kCols - 4;
      uint8_t pad = (kCols - len - 4) / 2;
      memset(top, ' ', kCols); top[kCols] = 0;
      bool blink = ((t / 100) & 1) != 0;
      top[pad] = blink ? 0x07 : ' ';
      memcpy(&top[pad + 2], inst.cached_fmmode, len);
      top[pad + 2 + len + 1] = blink ? 0x07 : ' ';
      writeRow(inst, 0, top);
      char row1[kCols + 1];
      memset(row1, ' ', kCols); row1[kCols] = 0;
      writeRow(inst, 1, row1);
      break;
    }
    case EventKind::Warn:
    case EventKind::Critical:
    case EventKind::Failsafe: {
      uint32_t period = (inst.event_kind == EventKind::Warn)     ? 200 :
                        (inst.event_kind == EventKind::Critical) ? 120 : 80;
      bool on = ((t / period) & 1) == 0;
      char row[kCols + 1];
      if (on) { memset(row, 0x04, kCols); row[kCols] = 0; }
      else    { memset(row, ' ',  kCols); row[kCols] = 0; }
      writeRow(inst, 0, row);
      writeRow(inst, 1, row);
      break;
    }
    case EventKind::None:
      break;
  }
  if (t >= kEventDurationMs) {
    inst.mode = inst.pre_event;
    inst.mode_entered_ms = now_ms;
    inst.event_kind = EventKind::None;
  }
}

void Vfd::renderText(Instance& inst, uint32_t now_ms) {
  // Display content was set when the SET vfd.<n> line command was
  // processed; here we just hold and time-out back to the active
  // animation.
  if (now_ms - inst.mode_entered_ms >= kTextHoldMs) {
    inst.mode = inst.armed ? Mode::Armed : Mode::Ambient;
    inst.mode_entered_ms = now_ms;
  }
}

// =============================================================================
// Command dispatch
// =============================================================================

bool Vfd::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance >= kInstanceCount) return false;
  Instance& inst = instances_[instance];
  if (!inst.lcd) {
    proto::writeError(out, kInstanceLabel[instance], "not-initialized");
    return true;
  }

  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "SET") == 0) return handleSet(instance, inst, cmd, out);
  if (strcmp(verb, "GET") == 0) { handleGet(instance, inst, out); return true; }
  return false;
}

bool Vfd::handleSet(uint8_t index, Instance& inst, const proto::Command& cmd, Stream& out) {
  const char* label = kInstanceLabel[index];
  const char* p = cmd.param();
  if (!p) {
    proto::writeError(out, label, "missing-param");
    return true;
  }

  inst.last_event_cmd_ms = millis();

  if (strcmp(p, "mode") == 0) {
    const char* m = cmd.arg(0);
    if (!m) { proto::writeError(out, label, "missing-mode"); return true; }
    if      (strcmp(m, "banner")  == 0) { showBanner(inst);     enterMode(inst, Mode::Banner); }
    else if (strcmp(m, "idle")    == 0) { inst.lcd->clear();    enterMode(inst, Mode::Idle); }
    else if (strcmp(m, "ambient") == 0) { enterMode(inst, Mode::Ambient); }
    else if (strcmp(m, "armed")   == 0) { inst.armed = true; enterMode(inst, Mode::Armed); }
    else { proto::writeError(out, label, "invalid-mode"); }
    return true;
  }

  if (strcmp(p, "brightness") == 0) {
    const char* lvl = cmd.arg(0);
    if (!lvl) { proto::writeError(out, label, "missing-level"); return true; }
    int v = atoi(lvl);
    if (v < 0 || v > 3) { proto::writeError(out, label, "invalid-level"); return true; }
    setBrightnessLevel(inst, (uint8_t)v);
    return true;
  }

  if (strcmp(p, "clear") == 0) {
    inst.lcd->clear();
    enterMode(inst, Mode::Ambient);
    return true;
  }

  if (strcmp(p, "line") == 0) {
    const char* rowStr = cmd.arg(0);
    if (!rowStr) { proto::writeError(out, label, "missing-row"); return true; }
    int row = atoi(rowStr);
    if (row < 0 || row >= kRows) {
      proto::writeError(out, label, "invalid-row"); return true;
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
    writeRow(inst, (uint8_t)row, buf);
    enterMode(inst, Mode::Text);
    return true;
  }

  if (strcmp(p, "tick") == 0) {
    uint16_t n = 1;
    const char* nStr = cmd.arg(0);
    if (nStr) {
      int v = atoi(nStr);
      if (v > 0) n = (uint16_t)v;
    }
    inst.tick_accumulator += n;
    if (inst.mode == Mode::Banner || inst.mode == Mode::Idle) {
      enterMode(inst, inst.armed ? Mode::Armed : Mode::Ambient);
    }
    return true;
  }

  if (strcmp(p, "arm") == 0) {
    const char* a = cmd.arg(0);
    if (!a) { proto::writeError(out, label, "missing-arm-state"); return true; }
    bool was = inst.armed;
    inst.armed = (*a == '1');
    if (inst.armed != was) {
      enterEvent(inst, inst.armed ? EventKind::ArmTransition : EventKind::DisarmTransition);
    }
    return true;
  }

  if (strcmp(p, "fmmode") == 0) {
    const char* m = cmd.arg(0);
    if (!m) { proto::writeError(out, label, "missing-fmmode"); return true; }
    strncpy(inst.cached_fmmode, m, sizeof(inst.cached_fmmode) - 1);
    inst.cached_fmmode[sizeof(inst.cached_fmmode) - 1] = '\0';
    enterEvent(inst, EventKind::ModeChange);
    return true;
  }

  if (strcmp(p, "lq") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, label, "missing-lq"); return true; }
    int n = atoi(v);
    if (n < 0) n = 0;
    if (n > 100) n = 100;
    inst.cached_lq = (uint8_t)n;
    inst.have_lq = true;
    return true;
  }

  if (strcmp(p, "batt") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, label, "missing-batt"); return true; }
    strncpy(inst.cached_batt, v, sizeof(inst.cached_batt) - 1);
    inst.cached_batt[sizeof(inst.cached_batt) - 1] = '\0';
    return true;
  }

  if (strcmp(p, "alarm") == 0) {
    const char* k = cmd.arg(0);
    if (!k) { proto::writeError(out, label, "missing-kind"); return true; }
    if      (strcmp(k, "warn")     == 0) enterEvent(inst, EventKind::Warn);
    else if (strcmp(k, "critical") == 0) enterEvent(inst, EventKind::Critical);
    else if (strcmp(k, "failsafe") == 0) enterEvent(inst, EventKind::Failsafe);
    else { proto::writeError(out, label, "invalid-alarm"); return true; }
    return true;
  }

  if (strcmp(p, "disarmed") == 0) {
    inst.armed = false;
    enterMode(inst, Mode::Ambient);
    return true;
  }

  proto::writeError(out, label, "unknown-param");
  return true;
}

void Vfd::handleGet(uint8_t index, Instance& inst, Stream& out) {
  char body[80];
  snprintf(body, sizeof(body),
      "%s mode=%s armed=%d fmmode=%s lq=%s%u batt=%s",
      kInstanceLabel[index],
      modeName(inst.mode),
      inst.armed ? 1 : 0,
      inst.cached_fmmode[0] ? inst.cached_fmmode : "-",
      inst.have_lq ? "" : "-", inst.have_lq ? inst.cached_lq : 0,
      inst.cached_batt[0] ? inst.cached_batt : "-");
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
