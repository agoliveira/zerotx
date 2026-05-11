// glcd.h - 128x64 graphic LCD (ST7920 controller, serial mode) subsystem.
//
// Renders a "cool factor" artificial horizon with pitch ladder, roll
// indicator, and numeric readout of pitch / roll / heading. The
// daemon pushes attitude updates over the IO protocol at ~10 Hz;
// the firmware re-renders the frame whenever new data arrives, or
// reverts to a "NO LINK" screen when telemetry has gone stale for
// more than ~1.5 s.
//
// Hardware: ST7920 128x64 graphic LCD module wired for 3-wire serial
// mode (PSB tied to GND). The Mega's hardware SPI peripheral drives
// SID (= MOSI, pin 51) and CLK (= SCK, pin 52); CS and /RESET are
// HAL-remappable. Quirk: the ST7920's CS is ACTIVE HIGH, unlike most
// SPI chips. The u8g2 ST7920 constructor handles this inversion.
//
// Protocol:
//   SET glcd attitude <pitch> <roll> <hdg>
//                                  Push new attitude. Values are
//                                  floats: pitch [-90,+90] degrees,
//                                  roll [-180,+180] degrees, hdg
//                                  [0,360) degrees. Roll convention:
//                                  positive = right wing down.
//                                  Pitch: positive = nose up.
//                                  Heading: magnetic or true,
//                                  whichever the FC reports.
//   SET glcd clear                 Clear to "NO LINK" screen
//                                  immediately. Equivalent to letting
//                                  the freshness timer expire.
//   GET glcd                       Respond with last-update age and
//                                  current displayed values.

#ifndef ZEROTX_IO_GLCD_H
#define ZEROTX_IO_GLCD_H

#include <Arduino.h>
#include <U8g2lib.h>

#include "../hal.h"
#include "../subsystem.h"

namespace zerotx {

class Glcd : public Subsystem {
public:
  Glcd() {}

  const char* name() const override { return "glcd"; }

  void begin(Stream& out) override;
  void tick(uint32_t now_ms, Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  // ----- Display constants -----
  static constexpr uint8_t  kWidth          = 128;
  static constexpr uint8_t  kHeight         = 64;
  // Re-render at most this often (avoid CPU saturation when daemon
  // pushes more frequently than the LCD can paint). u8g2 full-buffer
  // ST7920 SPI takes ~15ms; cap at 30 fps to leave headroom for the
  // other subsystems' ticks.
  static constexpr uint32_t kMinRedrawMs    = 33;
  // No-link timeout: if no attitude update arrives within this
  // window, drop to the "NO LINK" screen. The daemon nominally
  // pushes at 10 Hz; 1.5 s gives ~15 frames of slack before we
  // declare the link dead.
  static constexpr uint32_t kLinkTimeoutMs  = 1500;

  // ----- Geometry constants -----
  // AH widget area: top of screen down to a thin numeric strip at
  // the bottom. The roll indicator scale lives in the top ~8 px of
  // the AH area; the horizon and pitch ladder fill the rest.
  static constexpr int16_t  kAHTop          = 0;
  static constexpr int16_t  kAHBottom       = 51;  // 0..51 = 52 px
  static constexpr int16_t  kReadoutTop     = 53;  // 53..63 = 11 px
  static constexpr int16_t  kCenterX        = kWidth / 2;
  // AH widget center is the middle of the AH area, not the screen.
  // Roll indicator at top of AH area; horizon centered below it.
  static constexpr int16_t  kRollScaleTop   = 0;
  static constexpr int16_t  kRollScaleH     = 7;
  static constexpr int16_t  kHorizonTop     = kRollScaleH;
  static constexpr int16_t  kHorizonBottom  = kAHBottom;
  static constexpr int16_t  kHorizonCenterY = (kHorizonTop + kHorizonBottom) / 2;
  // Pixels of vertical horizon offset per degree of pitch. With a
  // 38-pixel half-height and ~20 visible degrees each way, ~1.9
  // px/deg is the right scale. Rounded to 2 for integer math.
  static constexpr int8_t   kPxPerPitchDeg  = 2;
  // Horizon line half-length (each side from center). Stretched
  // longer than half-width so rotation still covers screen at high
  // bank angles.
  static constexpr int16_t  kHorizonHalfLen = 80;
  // Pitch ladder rung half-lengths.
  static constexpr int16_t  kMajorRungHalf  = 10;
  static constexpr int16_t  kMinorRungHalf  = 5;
  // Roll scale pointer triangle size.
  static constexpr int16_t  kRollPointerSz  = 3;

  // ----- Cached state from daemon -----
  float    pitch_deg;     // [-90, +90]; positive = nose up
  float    roll_deg;      // [-180, +180]; positive = right wing down
  int16_t  heading_deg;   // [0, 360); int because that's how it displays
  uint32_t last_update_ms;
  bool     ever_received;

  // ----- Render scheduling -----
  uint32_t last_redraw_ms;
  bool     dirty;         // new data arrived; redraw on next tick

  // u8g2 instance: ST7920 128x64 in 4-wire SW SPI by default, but
  // we use hardware SPI for speed. The constructor wants CS (and
  // optionally /RESET). MOSI=51 and SCK=52 are implicit hardware
  // SPI pins on the Mega.
  //
  // Note: U8G2 ST7920 over hardware SPI ignores /RESET via the
  // constructor and expects the application to pulse it manually
  // at begin() if desired. We do that in begin() using HAL_GLCD_RESET.
  U8G2_ST7920_128X64_F_HW_SPI* u8g2;

  // ----- Render helpers -----
  void renderAttitude(void);
  void renderNoLink(void);
  void drawHorizonAndLadder(float pitchDeg, float rollDeg);
  void drawRollScale(float rollDeg);
  void drawAircraftSymbol(void);
  void drawReadout(float pitchDeg, float rollDeg, int16_t hdgDeg);
};

}  // namespace zerotx

#endif  // ZEROTX_IO_GLCD_H
