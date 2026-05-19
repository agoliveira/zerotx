// glcd.cpp - implementation.
//
// Drawing model:
//   * Aircraft frame and screen frame share orientation (x right,
//     y down). The aircraft reference symbol is fixed at the screen
//     center (cx, kHorizonCenterY).
//   * The horizon line is drawn at screen position (cx, cy + pitch *
//     pxPerDeg), rotated by -roll degrees around the aircraft
//     reference. Negative because positive roll = right wing down
//     causes the world horizon to appear rotated COUNTERCLOCKWISE
//     in the screen (y-down convention).
//   * Pitch ladder rungs are drawn at world-pitch values R for
//     R in {±10, ±20, ±30}, each rung positioned at y_local =
//     (pitch - R) * pxPerDeg before rotation. Rungs are clipped to
//     the AH area; u8g2 handles bounds.
//   * Roll scale at the top of the AH area: fixed tick marks at
//     -45, -30, -15, 0, +15, +30, +45 degrees and a moving triangle
//     pointer that tracks the roll angle (clamped to ±45° for
//     display; the horizon itself still rotates fully).
//   * Numeric readout strip at the bottom shows P, R, H values.

#include "glcd.h"

#include <SPI.h>
#include <math.h>
#include <stdio.h>
#include <string.h>

namespace zerotx {

void Glcd::begin(Stream& out) {
  pitch_deg      = 0.0f;
  roll_deg       = 0.0f;
  heading_deg    = 0;
  last_update_ms = 0;
  ever_received  = false;
  last_redraw_ms = 0;
  dirty          = true;

  // Pulse /RESET low to cold-start the controller. ST7920 reset
  // pulse min 1us; hold for 10ms to be safe.
  uint8_t reset_pin = hal::pin(hal::HAL_GLCD_RESET);
  pinMode(reset_pin, OUTPUT);
  digitalWrite(reset_pin, LOW);
  delay(10);
  digitalWrite(reset_pin, HIGH);
  delay(100);  // ST7920 init time

  // Construct on the heap so we can pass HAL-resolved CS pin at
  // begin() rather than at static-init time. ~64 bytes + 1024 byte
  // framebuffer.
  uint8_t cs_pin = hal::pin(hal::HAL_GLCD_CS);
  u8g2 = new U8G2_ST7920_128X64_F_HW_SPI(
      U8G2_R0,            // no rotation
      cs_pin,             // CS (active high; u8g2 handles inversion)
      U8X8_PIN_NONE);     // /RESET handled manually above
  if (u8g2 == nullptr) {
    proto::writeError(out, "glcd", "alloc-failed");
    return;
  }

  u8g2->begin();
  u8g2->setBusClock(1000000);  // 1 MHz SPI; ST7920 spec is 530 kHz min cycle, 1 MHz is comfortable
  u8g2->setContrast(255);      // contrast set on the module's pot; this is a no-op for ST7920 but keeps the API symmetric
  u8g2->clearBuffer();
  u8g2->setFont(u8g2_font_5x7_tf);
  u8g2->setDrawColor(1);

  // Initial frame: NO LINK (no telemetry received yet).
  renderNoLink();

  proto::writeEvent(out, "glcd", "ready");
}

void Glcd::tick(uint32_t now_ms, Stream& out) {
  (void)out;
  if (u8g2 == nullptr) return;

  // Stale-link demotion. Once telemetry stops arriving for
  // kLinkTimeoutMs, fall back to NO LINK.
  if (ever_received && (now_ms - last_update_ms) > kLinkTimeoutMs) {
    // Already in NO LINK mode? Only re-render if we just crossed
    // the threshold.
    if (dirty || (now_ms - last_redraw_ms) > 1000) {
      renderNoLink();
      last_redraw_ms = now_ms;
      dirty = false;
    }
    return;
  }

  // Live data: redraw if dirty AND the rate cap allows it.
  if (dirty && (now_ms - last_redraw_ms) >= kMinRedrawMs) {
    renderAttitude();
    last_redraw_ms = now_ms;
    dirty = false;
  }
}

bool Glcd::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  (void)instance;
  if (u8g2 == nullptr) {
    proto::writeError(out, "glcd", "not-initialized");
    return true;
  }

  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "SET") == 0) {
    const char* p = cmd.param();
    if (!p) { proto::writeError(out, "glcd", "missing-param"); return true; }

    if (strcmp(p, "attitude") == 0) {
      const char* pa = cmd.arg(0);
      const char* ra = cmd.arg(1);
      const char* ha = cmd.arg(2);
      if (!pa || !ra || !ha) {
        proto::writeError(out, "glcd", "missing-args");
        return true;
      }
      pitch_deg     = atof(pa);
      roll_deg      = atof(ra);
      heading_deg   = (int16_t)atoi(ha);
      // Clamp pitch to ±90 to keep math sane; some FCs can briefly
      // report values outside this range during aerobatic flight.
      if (pitch_deg < -90.0f) pitch_deg = -90.0f;
      if (pitch_deg > +90.0f) pitch_deg = +90.0f;
      // Roll wraps via the trig functions; no need to clamp.
      // Heading: wrap to [0, 360).
      while (heading_deg < 0)    heading_deg += 360;
      while (heading_deg >= 360) heading_deg -= 360;

      last_update_ms = millis();
      ever_received  = true;
      dirty          = true;
      return true;
    }

    if (strcmp(p, "clear") == 0) {
      ever_received  = false;
      dirty          = true;
      renderNoLink();
      last_redraw_ms = millis();
      return true;
    }

    proto::writeError(out, "glcd", "unknown-param");
    return true;
  }

  if (strcmp(verb, "GET") == 0) {
    uint32_t age_ms = ever_received ? (millis() - last_update_ms) : 0xFFFFFFFFul;
    out.print(F("OK glcd"));
    out.print(F(" pitch=")); out.print(pitch_deg, 1);
    out.print(F(" roll="));  out.print(roll_deg, 1);
    out.print(F(" hdg="));   out.print(heading_deg);
    out.print(F(" age_ms="));
    if (age_ms == 0xFFFFFFFFul) out.print(F("never"));
    else                        out.print(age_ms);
    out.println();
    return true;
  }

  return false;  // unknown verb; dispatcher will emit ERR unknown
}

// --- Render: attitude ------------------------------------------------------

void Glcd::renderAttitude(void) {
  u8g2->clearBuffer();
  // Clip horizon/ladder/aircraft drawing to the AH area only. Without
  // this, minor pitch-ladder rungs at small pitch values render into
  // the readout strip below: at pitch=0, the -15deg minor rung lands
  // at y=cy+(0-(-15))*pxPerDeg = 59 (with cy=29, pxPerDeg=2), which
  // is inside the readout strip (y=53..63). The rung is a 10-pixel
  // horizontal segment centered at x=64, so it draws a horizontal
  // line through the H field of the readout (x~52..71), looking
  // like an underscore between the H digits. Bench-visible artifact;
  // not a clock/wiring/font issue as initially suspected.
  u8g2->setClipWindow(0, kAHTop, kWidth - 1, kHorizonBottom);
  drawHorizonAndLadder(pitch_deg, roll_deg);
  drawRollScale(roll_deg);
  drawAircraftSymbol();
  u8g2->setMaxClipWindow();
  drawReadout(pitch_deg, roll_deg, heading_deg);
  u8g2->sendBuffer();
}

// --- Render: no-link banner ------------------------------------------------

void Glcd::renderNoLink(void) {
  u8g2->clearBuffer();
  u8g2->setFont(u8g2_font_7x13B_tf);
  const char* msg1 = "NO LINK";
  int w = u8g2->getStrWidth(msg1);
  u8g2->drawStr((kWidth - w) / 2, 25, msg1);

  u8g2->setFont(u8g2_font_5x7_tf);
  const char* msg2 = "awaiting attitude";
  w = u8g2->getStrWidth(msg2);
  u8g2->drawStr((kWidth - w) / 2, 42, msg2);
  u8g2->sendBuffer();
}

// --- Horizon + pitch ladder ------------------------------------------------

void Glcd::drawHorizonAndLadder(float pitchDeg, float rollDeg) {
  // Rotation: negate roll for screen-y-down coordinate system. Right
  // roll (positive) causes the screen-attached aircraft frame to
  // rotate clockwise, so the world horizon appears to rotate
  // counterclockwise relative to the screen. In math: apply rotation
  // by theta = -rollRad to all body-frame points.
  float rollRad = -rollDeg * 0.01745329f;  // pi/180
  float cosR    = cos(rollRad);
  float sinR    = sin(rollRad);

  const int16_t cx = kCenterX;
  const int16_t cy = kHorizonCenterY;

  // Lambda to rotate a body-frame point and project onto the screen.
  auto plot = [&](int16_t x_local, int16_t y_local, int16_t& sx, int16_t& sy) {
    float xf = (float)x_local;
    float yf = (float)y_local;
    sx = cx + (int16_t)(xf * cosR - yf * sinR);
    sy = cy + (int16_t)(xf * sinR + yf * cosR);
  };

  // Horizon line: body-frame y_local = pitch * pxPerDeg (positive
  // pitch -> horizon below center in pre-rotated body frame).
  int16_t pitchPx = (int16_t)(pitchDeg * (float)kPxPerPitchDeg);
  int16_t x0, y0, x1, y1;
  plot(-kHorizonHalfLen, pitchPx, x0, y0);
  plot(+kHorizonHalfLen, pitchPx, x1, y1);
  u8g2->drawLine(x0, y0, x1, y1);

  // Pitch ladder: rungs at R = ±10, ±20, ±30, with minor ticks at
  // ±5, ±15, ±25 for the "fancier the better" treatment. For each
  // rung at world-pitch R, y_local in body frame is (pitch - R) *
  // pxPerDeg.
  const int8_t majorRungs[] = {-30, -20, -10, 10, 20, 30};
  const int8_t minorRungs[] = {-25, -15, -5, 5, 15, 25};

  for (uint8_t i = 0; i < sizeof(majorRungs) / sizeof(majorRungs[0]); i++) {
    int8_t R = majorRungs[i];
    int16_t y_local = (int16_t)((pitchDeg - (float)R) * (float)kPxPerPitchDeg);
    int16_t lx, ly, rx, ry;
    plot(-kMajorRungHalf, y_local, lx, ly);
    plot(+kMajorRungHalf, y_local, rx, ry);
    u8g2->drawLine(lx, ly, rx, ry);

    // Degree label on each end. Use absolute value; ±-pairs are
    // distinguished by being above vs below the horizon.
    char buf[4];
    snprintf(buf, sizeof(buf), "%d", (int)abs(R));
    int16_t lxOff, lyOff;
    plot(-kMajorRungHalf - 9, y_local, lxOff, lyOff);
    u8g2->drawStr(lxOff - 2, lyOff + 3, buf);
  }

  for (uint8_t i = 0; i < sizeof(minorRungs) / sizeof(minorRungs[0]); i++) {
    int8_t R = minorRungs[i];
    int16_t y_local = (int16_t)((pitchDeg - (float)R) * (float)kPxPerPitchDeg);
    int16_t lx, ly, rx, ry;
    plot(-kMinorRungHalf, y_local, lx, ly);
    plot(+kMinorRungHalf, y_local, rx, ry);
    u8g2->drawLine(lx, ly, rx, ry);
  }
}

// --- Roll scale at top of AH area ------------------------------------------

void Glcd::drawRollScale(float rollDeg) {
  // Fixed tick marks across the top of the screen at -45, -30, -15,
  // 0, +15, +30, +45 degrees. Each tick is 1 pixel wide; the 0°
  // tick is taller.
  const int8_t ticks[] = {-45, -30, -15, 0, 15, 30, 45};
  // Display extent: ±45° maps to (kCenterX - 50) ... (kCenterX + 50).
  // 1 px = 0.9° in this strip.
  const int16_t halfRange = 50;

  for (uint8_t i = 0; i < sizeof(ticks) / sizeof(ticks[0]); i++) {
    int8_t t = ticks[i];
    int16_t x = kCenterX + (int16_t)((int16_t)t * halfRange / 45);
    int16_t y0 = kRollScaleTop;
    int16_t y1 = kRollScaleTop + ((t == 0) ? kRollScaleH : kRollScaleH - 2);
    u8g2->drawVLine(x, y0, y1 - y0 + 1);
  }

  // Moving pointer triangle at roll position (clamped for display).
  float clamped = rollDeg;
  if (clamped < -45.0f) clamped = -45.0f;
  if (clamped > +45.0f) clamped = +45.0f;
  int16_t px = kCenterX + (int16_t)((int16_t)(clamped * 50.0f / 45.0f));
  int16_t py = kRollScaleTop + kRollScaleH + 1;
  // Filled triangle pointing UP toward the scale.
  u8g2->drawTriangle(
      px,                   py + kRollPointerSz,
      px - kRollPointerSz,  py + kRollPointerSz + kRollPointerSz,
      px + kRollPointerSz,  py + kRollPointerSz + kRollPointerSz);
}

// --- Fixed aircraft reference symbol ---------------------------------------

void Glcd::drawAircraftSymbol(void) {
  // "Stub-wing-with-dot" symbol: a small dot in the center flanked
  // by short horizontal lines representing wing stubs. The whole
  // symbol is intentionally NOT rotated by roll -- it represents the
  // aircraft's own attitude, which is always level with respect to
  // itself.
  const int16_t cx = kCenterX;
  const int16_t cy = kHorizonCenterY;

  u8g2->drawHLine(cx - 14, cy, 6);    // left wing stub
  u8g2->drawHLine(cx + 8,  cy, 6);    // right wing stub
  u8g2->drawPixel(cx, cy);            // fuselage dot
  u8g2->drawPixel(cx - 1, cy);
  u8g2->drawPixel(cx + 1, cy);
}

// --- Numeric readout strip at bottom ---------------------------------------

void Glcd::drawReadout(float pitchDeg, float rollDeg, int16_t hdgDeg) {
  // 1-pixel separator above the strip.
  u8g2->drawHLine(0, kReadoutTop - 1, kWidth);

  // "P-12 R+08 H087" style; 5x7 font fits ~21 chars in 105 px, so we
  // have room. Use signed printing with explicit + sign for clarity.
  char buf[24];
  snprintf(buf, sizeof(buf), "P%+03d R%+03d H%03d",
           (int)pitchDeg, (int)rollDeg, (int)hdgDeg);
  u8g2->drawStr(2, kReadoutTop + 8, buf);
}

}  // namespace zerotx
