// vfd.h - Noritake CU20025ECPB-W1J 2x20 character VFD subsystem.
//
// 4-bit HD44780-mode parallel interface via the hd44780 Arduino
// library (Bill Perry / duinoWitchery). Pin assignments come from
// HAL (HAL_VFD0_RS, HAL_VFD0_EN, HAL_VFD0_D4..D7).
//
// Animation engine: six modes: BANNER, IDLE, AMBIENT, ARMED, TEXT, EVENT. The daemon
// drives mode transitions via SET commands and pushes telemetry
// snippets that the firmware uses to render mode-appropriate
// screens. The firmware does the per-frame rendering locally so
// the daemon only sends events at human-relevant cadences.
//
// Protocol:
//
//   SET vfd.0 mode <banner|idle|ambient|armed>
//                                       Force a mode. Daemon uses
//                                       this for boot banner and to
//                                       force-IDLE on shutdown.
//   SET vfd.0 brightness <0..3>         0=brightest.
//   SET vfd.0 line <row> <text...>      L-style overlay; pauses
//                                       animation for ~2s. text may
//                                       contain spaces (all remaining
//                                       tokens are joined with single
//                                       spaces).
//   SET vfd.0 clear                     Clear display, return to
//                                       AMBIENT.
//   SET vfd.0 tick [<n>]                Activity ping; feeds the
//                                       ambient/armed activity bar.
//                                       Default n=1.
//   SET vfd.0 arm <0|1>                 Arm-state edge. Triggers a
//                                       sweep transition.
//   SET vfd.0 fmmode <text>             Cache flight-mode label and
//                                       trigger a brief MODE_CHANGE
//                                       event overlay. (Renamed from
//                                       legacy "mode" to disambiguate
//                                       from display-mode SET.)
//   SET vfd.0 lq <pct>                  Cache link-quality 0..100.
//   SET vfd.0 batt <text>               Cache battery voltage string.
//   SET vfd.0 alarm <warn|critical|failsafe>
//                                       Trigger an alarm overlay.
//   SET vfd.0 disarmed                  Disarm without sweep; just
//                                       drop to AMBIENT.
//
//   GET vfd.0                           Respond with current display
//                                       mode + cached state.

#ifndef ZEROTX_IO_VFD_H
#define ZEROTX_IO_VFD_H

#include <Arduino.h>
#include <hd44780.h>
#include <hd44780ioClass/hd44780_NTCU20025ECPB_pinIO.h>

#include "../subsystem.h"

namespace zerotx {

class Vfd : public Subsystem {
public:
  Vfd() {}

  const char* name() const override { return "vfd"; }
  uint8_t count() const override { return 1; }   // single instance for now

  void begin(Stream& out) override;
  void tick(uint32_t now_ms, Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  // ----- Display constants -----
  static constexpr uint8_t  kCols = 20;
  static constexpr uint8_t  kRows = 2;
  static constexpr uint32_t kFrameIntervalMs = 33;   // ~30 fps
  static constexpr uint32_t kTextHoldMs      = 2000;
  static constexpr uint32_t kEventDurationMs = 800;
  static constexpr uint32_t kIdleTimeoutMs   = 6000;

  // ----- Mode + event state -----
  enum class Mode : uint8_t {
    Banner = 0, Idle, Ambient, Armed, Text, Event,
  };

  enum class Event : uint8_t {
    None = 0, ArmTransition, DisarmTransition, ModeChange,
    Warn, Critical, Failsafe,
  };

  // ----- Cached telemetry from daemon -----
  uint16_t tick_accumulator_ = 0;
  uint16_t bar_level_        = 0;       // 0..(kCols * 5) pixels
  char     cached_fmmode_[12] = {0};
  uint8_t  cached_lq_        = 0;
  bool     have_lq_          = false;
  char     cached_batt_[8]   = {0};
  bool     armed_            = false;

  // ----- Animation state -----
  Mode     mode_         = Mode::Banner;
  Mode     pre_event_    = Mode::Banner;
  Event    event_kind_   = Event::None;
  uint32_t mode_entered_ms_ = 0;
  uint32_t last_event_cmd_ms_ = 0;
  uint32_t last_frame_ms_     = 0;
  uint32_t frame_count_       = 0;
  uint8_t  idle_orbit_pos_    = 0;

  // ----- Display driver. Constructed in begin() once we know the
  // resolved pin numbers. Heap-allocated via placement-new into a
  // static buffer to avoid dynamic allocation on AVR. -----
  hd44780_NTCU20025ECPB_pinIO* lcd_ = nullptr;
  alignas(hd44780_NTCU20025ECPB_pinIO) uint8_t lcd_storage_[
      sizeof(hd44780_NTCU20025ECPB_pinIO)];

  // ----- Helpers -----
  void writeRow(uint8_t row, const char* content);
  void uploadCustomGlyphs();
  void setBrightnessLevel(uint8_t level);
  void showBanner();
  void enterMode(Mode m);
  void enterEvent(Event k);

  // Per-mode renderers.
  void renderBanner(uint32_t now_ms);
  void renderIdle();
  void renderAmbient();
  void renderArmed();
  void renderEvent(uint32_t now_ms);
  void renderText(uint32_t now_ms);

  void renderActivityBar(uint16_t weight_per_tick);

  // Command dispatch helpers.
  bool handleSet(const proto::Command& cmd, Stream& out);
  void handleGet(Stream& out);
  static const char* modeName(Mode m);
};

}  // namespace zerotx

#endif  // ZEROTX_IO_VFD_H
