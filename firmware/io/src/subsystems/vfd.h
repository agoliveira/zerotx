// vfd.h - Noritake CU20025ECPB-W1J 2x20 character VFD subsystem.
//
// 4-bit HD44780-mode parallel interface via the hd44780 Arduino
// library (Bill Perry / duinoWitchery). Pin assignments come from
// HAL.
//
// Multi-instance: vfd.0 (HAL_VFD0_*) and vfd.1 (HAL_VFD1_*). Both
// instances run the same animation engine independently; each has
// its own cached telemetry, mode, and timing. The daemon addresses
// them separately via "vfd.0" / "vfd.1" in the protocol.
//
// Animation engine: six modes: BANNER, IDLE, AMBIENT, ARMED, TEXT,
// EVENT. The daemon drives mode transitions via SET commands and
// pushes telemetry snippets that the firmware uses to render mode-
// appropriate screens. The firmware does the per-frame rendering
// locally so the daemon only sends events at human-relevant cadences.
//
// Protocol (replace <n> with 0 or 1):
//
//   SET vfd.<n> mode <banner|idle|ambient|armed>
//                                       Force a mode. Daemon uses
//                                       this for boot banner and to
//                                       force-IDLE on shutdown.
//   SET vfd.<n> brightness <0..3>       0=brightest.
//   SET vfd.<n> line <row> <text...>    L-style overlay; pauses
//                                       animation for ~2s. text may
//                                       contain spaces (all remaining
//                                       tokens are joined with single
//                                       spaces).
//   SET vfd.<n> clear                   Clear display, return to
//                                       AMBIENT.
//   SET vfd.<n> tick [<n>]              Activity ping; feeds the
//                                       ambient/armed activity bar.
//                                       Default n=1.
//   SET vfd.<n> arm <0|1>               Arm-state edge. Triggers a
//                                       sweep transition.
//   SET vfd.<n> fmmode <text>           Cache flight-mode label and
//                                       trigger a brief MODE_CHANGE
//                                       event overlay.
//   SET vfd.<n> lq <pct>                Cache link-quality 0..100.
//   SET vfd.<n> batt <text>             Cache battery voltage string.
//   SET vfd.<n> alarm <warn|critical|failsafe>
//                                       Trigger an alarm overlay.
//   SET vfd.<n> disarmed                Disarm without sweep; just
//                                       drop to AMBIENT.
//
//   GET vfd.<n>                         Respond with current display
//                                       mode + cached state.

#ifndef ZEROTX_IO_VFD_H
#define ZEROTX_IO_VFD_H

#include <Arduino.h>
#include <hd44780.h>
#include <hd44780ioClass/hd44780_NTCU20025ECPB_pinIO.h>

#include "../hal.h"
#include "../subsystem.h"

namespace zerotx {

class Vfd : public Subsystem {
public:
  Vfd() {}

  static constexpr uint8_t kInstanceCount = 2;

  const char* name() const override { return "vfd"; }
  uint8_t count() const override { return kInstanceCount; }

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

  enum class EventKind : uint8_t {
    None = 0, ArmTransition, DisarmTransition, ModeChange,
    Warn, Critical, Failsafe,
  };

  // Per-instance state. Everything that varies between vfd.0 and
  // vfd.1 lives here. The class itself holds an array of these.
  struct Instance {
    // Cached telemetry from daemon
    uint16_t tick_accumulator;
    uint16_t bar_level;       // 0..(kCols * 5) pixels
    char     cached_fmmode[12];
    uint8_t  cached_lq;
    bool     have_lq;
    char     cached_batt[8];
    bool     armed;

    // Animation state
    Mode      mode;
    Mode      pre_event;
    EventKind event_kind;
    uint32_t  mode_entered_ms;
    uint32_t  last_event_cmd_ms;
    uint32_t  last_frame_ms;
    uint32_t  frame_count;
    uint8_t   idle_orbit_pos;

    // Display driver. Constructed in-place during begin() once we
    // know the resolved pin numbers. Set to nullptr if begin()
    // failed; tick()/handle() check this before touching the LCD.
    hd44780_NTCU20025ECPB_pinIO* lcd;
    alignas(hd44780_NTCU20025ECPB_pinIO) uint8_t lcd_storage[
        sizeof(hd44780_NTCU20025ECPB_pinIO)];
  };

  Instance instances_[kInstanceCount];

  // HAL pin slots per instance. Indexed by instance number.
  struct PinSlots {
    hal::HalPinId rs, en, d4, d5, d6, d7;
  };
  static const PinSlots kPinsForInstance[kInstanceCount];

  // Stable instance labels for protocol responses ("vfd.0", "vfd.1").
  static const char* const kInstanceLabel[kInstanceCount];

  // ----- Helpers (now per-instance) -----
  void writeRow(Instance& inst, uint8_t row, const char* content);
  void uploadCustomGlyphs(Instance& inst);
  void setBrightnessLevel(Instance& inst, uint8_t level);
  void showBanner(Instance& inst);
  void enterMode(Instance& inst, Mode m);
  void enterEvent(Instance& inst, EventKind k);

  // Per-mode renderers.
  void renderBanner(Instance& inst, uint32_t now_ms);
  void renderIdle(Instance& inst);
  void renderAmbient(Instance& inst);
  void renderArmed(Instance& inst);
  void renderEvent(Instance& inst, uint32_t now_ms);
  void renderText(Instance& inst, uint32_t now_ms);

  void renderActivityBar(Instance& inst, uint16_t weight_per_tick);

  // Command dispatch helpers.
  bool handleSet(uint8_t index, Instance& inst, const proto::Command& cmd, Stream& out);
  void handleGet(uint8_t index, Instance& inst, Stream& out);
  static const char* modeName(Mode m);
};

}  // namespace zerotx

#endif  // ZEROTX_IO_VFD_H
