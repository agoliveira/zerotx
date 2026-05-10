// ZeroTX antenna tracker firmware.
//
// Phase 5: runtime configuration via USB-CDC console + NVS persistence.
//
// All site-specific and gimbal-specific values that previously lived
// as compile-time constants are now in a Config struct loaded from
// NVS at boot, with compile-time defaults as fallback. A simple
// terse-AT-style command parser running on core 0 lets you change
// values, save them, and manually drive the servos for installation
// alignment.
//
// Commands:
//   help                                - list all commands
//   cfg show                            - print current config
//   cfg save                            - persist current config to NVS
//   cfg station <lat> <lon> <alt_m>     - set station coordinates
//   cfg pan_ref <az_deg>                - pan-center pulse az reference
//   cfg pan_range <deg>                 - pan servo travel (180/270/360)
//   cfg pan_pulse <min> <center> <max>  - pan pulse widths in us
//   cfg pan_invert <on|off>             - reverse pan direction
//   cfg pan_flip <on|off>               - enable 180-deg flip technique
//   cfg tilt_range <deg>                - tilt servo travel
//   cfg tilt_pulse <min> <center> <max> - tilt pulse widths in us
//   cfg tilt_invert <on|off>            - reverse tilt direction
//   aim <az> <el>                       - manually aim servos
//   pos                                 - show current servo positions
//   stats                               - show parser/telemetry stats
//   defaults                            - reset to compile-time defaults
//   reboot                              - software reset
//
// Architecture (full plan, all phases now implemented):
//   Phase 0: byte pump on core 1, hardware watchdog, USB-CDC log.
//   Phase 1: tee + CRSF parser + GPS extraction.
//   Phase 2: az/el math.
//   Phase 3: LEDC PWM servo driver + slew + self-test.
//   Phase 4: tracking glue (EMA + flip + failsafe).
//   Phase 5 (this firmware): runtime config + USB-CDC commands + NVS.
//
// The byte pump is the safety floor. It runs at the highest possible
// priority on core 1 and is never blocked by code added in any
// later phase. Tracking and command logic live entirely on core 0;
// neither can affect the wire forwarding.
//
// Hardware: ESP32-S3 (QFN56), 16MB QIO flash + 8MB QSPI PSRAM.

#include <Arduino.h>
#include <math.h>
#include <string.h>
#include <stdlib.h>
#include <Preferences.h>
#include <esp_task_wdt.h>
#include <freertos/stream_buffer.h>

constexpr const char* FW_VERSION = "0.6.0-cfg";

// =====================================================================
// Pin map
// =====================================================================
//
// UART1 talks to the MAX490 RS-422 transceiver on the cable side.
// UART2 talks to the ELRS TX module's CRSF input/output.
//
// Reserved for upcoming phases:
//   GPIO 6  = pan servo PWM   (Phase 3)
//   GPIO 7  = tilt servo PWM  (Phase 3)
//   GPIO 8  = I2C SDA         (Phase 5: optional magnetometer)
//   GPIO 9  = I2C SCL         (Phase 5)
//
// Pins to AVOID on this chip / package:
//   GPIO 19, 20  : native USB-OTG (we use UART0/CH343 bridge instead)
//   GPIO 26-32   : SPI flash and QSPI PSRAM internals
//   GPIO 33-37   : not exposed on the WROOM-1 module footprint
//   GPIO 0, 3, 45, 46 : strapping pins; safer to leave for boot use
constexpr int UART1_RX = 17;  // cable side (from MAX490 RO)
constexpr int UART1_TX = 18;  // cable side (to MAX490 DI)
constexpr int UART2_RX = 4;   // ELRS module side (from module TX)
constexpr int UART2_TX = 5;   // ELRS module side (to module RX)

// =====================================================================
// CRSF parameters
// =====================================================================
//
// 420 kbaud matches the rp2040 CRSF generator (firmware/crsf/src/crsf.c).
constexpr int CRSF_BAUD = 420000;

// =====================================================================
// Watchdog
// =====================================================================
constexpr int WDT_TIMEOUT_S = 1;

// =====================================================================
// Tee stream buffer
// =====================================================================
//
// Carries upstream (ELRS -> cable) bytes from the byte pump on core 1
// to the parser on core 0. FreeRTOS stream buffer is single-producer,
// single-consumer, lock-free; perfect fit. Sized to hold ~80ms of
// 420kbaud traffic; the parser typically drains it well within 1ms.
constexpr size_t TELEM_BUFFER_SIZE = 4096;
static StreamBufferHandle_t telem_buffer = nullptr;

// =====================================================================
// CRSF protocol constants
// =====================================================================
//
// CRSF address bytes used as the frame sync byte. Telemetry frames
// from an ELRS module typically use 0xC8 (broadcast / FC address) or
// 0xEA (handset/radio address). We accept either; the CRC validates.
constexpr uint8_t CRSF_SYNC_FC      = 0xC8;
constexpr uint8_t CRSF_SYNC_HANDSET = 0xEA;

// Frame types we care about. Full list is in the CRSF spec; only the
// telemetry types the daemon sees are useful here.
constexpr uint8_t CRSF_FRAME_GPS     = 0x02;
constexpr uint8_t CRSF_FRAME_BATTERY = 0x08;  // not used yet, may log later
constexpr uint8_t CRSF_FRAME_LINK    = 0x14;  // not used yet, may log later
constexpr uint8_t CRSF_FRAME_ATTITUDE= 0x1E;  // not used yet, may log later

// Frame structure:
//   [0]   sync byte
//   [1]   length byte (counts bytes AFTER itself: type + payload + crc)
//   [2]   type
//   [3..] payload
//   [end] CRC8 over [type ... last payload byte]
constexpr size_t CRSF_MAX_FRAME    = 64;
constexpr size_t CRSF_MIN_LEN_BYTE = 2;   // type + crc, payload empty
constexpr size_t CRSF_MAX_LEN_BYTE = 62;  // CRSF spec max

// =====================================================================
// CRC-8 (DVB-S2 polynomial 0xD5)
// =====================================================================
//
// Bit-banged. CRSF runs at most a few hundred frames/sec; the cycle
// cost is negligible vs. avoiding a 256-byte lookup table in flash.
static uint8_t crsf_crc8(const uint8_t* data, size_t len) {
  uint8_t crc = 0;
  for (size_t i = 0; i < len; i++) {
    crc ^= data[i];
    for (int j = 0; j < 8; j++) {
      crc = (crc & 0x80) ? (uint8_t)((crc << 1) ^ 0xD5) : (uint8_t)(crc << 1);
    }
  }
  return crc;
}

// =====================================================================
// GPS frame structure and decode
// =====================================================================
//
// CRSF GPS payload (frame type 0x02), 15 bytes total:
//   [0..3]   latitude    int32 BE, value / 1e7 = degrees
//   [4..7]   longitude   int32 BE, value / 1e7 = degrees
//   [8..9]   groundspeed uint16 BE, value / 10 = km/h
//   [10..11] heading     uint16 BE, value / 100 = degrees (0..360)
//   [12..13] altitude    uint16 BE, value - 1000 = meters (offset by +1000)
//   [14]     satellites  uint8

static inline int32_t read_be_i32(const uint8_t* p) {
  return ((int32_t)p[0] << 24) | ((int32_t)p[1] << 16) |
         ((int32_t)p[2] << 8)  |  (int32_t)p[3];
}
static inline uint16_t read_be_u16(const uint8_t* p) {
  return ((uint16_t)p[0] << 8) | (uint16_t)p[1];
}

struct GpsFrame {
  double  lat_deg;
  double  lon_deg;
  float   speed_kmh;
  float   heading_deg;
  int     altitude_m;
  uint8_t sats;
};

static bool parse_gps_payload(const uint8_t* payload, size_t len, GpsFrame& out) {
  if (len < 15) return false;
  out.lat_deg     = (double)read_be_i32(payload + 0)  / 1e7;
  out.lon_deg     = (double)read_be_i32(payload + 4)  / 1e7;
  out.speed_kmh   = (float) read_be_u16(payload + 8)  / 10.0f;
  out.heading_deg = (float) read_be_u16(payload + 10) / 100.0f;
  out.altitude_m  = (int)   read_be_u16(payload + 12) - 1000;
  out.sats        = payload[14];
  return true;
}

// =====================================================================
// Runtime configuration
// =====================================================================
//
// All site- and gimbal-specific values live in a Config struct
// loaded from NVS at boot. Compile-time DEFAULT_CONFIG provides
// fallback values for first boot or after `defaults` command.
//
// Persistent via the Preferences library (NVS namespace "zerotx").
// Each field stored under a short key for forward-compatibility:
// missing keys fall back to defaults, so adding new config fields
// in future firmware versions doesn't invalidate existing NVS data.
//
// Persisted values:
//   Station:    lat, lon, alt_m
//   Pan axis:   ref_az_deg, range_deg, min_us, center_us, max_us,
//               invert, flip_enabled
//   Tilt axis:  range_deg, min_us, center_us, max_us, invert
//
// NOT persisted (compile-time tuning constants, see below):
//   EMA_ALPHA, FLIP_HYSTERESIS_DEG, FAILSAFE_HOLD_MS,
//   SERVO_SLEW_STEP_US, SERVO_SLEW_HZ, watchdog timeout, baud rates,
//   pin assignments, LEDC channel assignments.

struct Config {
  // Station coordinates
  double  station_lat;        // degrees, -90..+90
  double  station_lon;        // degrees, -180..+180
  double  station_alt_m;      // meters above sea level

  // Pan axis
  float   pan_ref_az_deg;     // azimuth at which pan-center pulse points
  float   pan_range_deg;      // mechanical travel for full pulse range
  int     pan_min_us;         // minimum pulse width
  int     pan_center_us;      // center pulse width
  int     pan_max_us;         // maximum pulse width
  bool    pan_invert;         // reverse pan direction
  bool    pan_flip_enabled;   // use 180-deg flip technique (180-deg servos)

  // Tilt axis
  float   tilt_range_deg;
  int     tilt_min_us;
  int     tilt_center_us;
  int     tilt_max_us;
  bool    tilt_invert;
};

// Compile-time defaults. Adjust to match a typical install; the user
// runs `defaults` then `cfg save` to reset NVS to these values.
//
// Station defaults are roughly Campinas - useful for sanity-checking
// the math but NOT correct for a real installation. The first thing
// any operator does is `cfg station <real_lat> <real_lon> <real_alt>`
// followed by `cfg save`.
constexpr Config DEFAULT_CONFIG = {
  // station
  -22.9123, -47.0610, 685.0,
  // pan: north reference, 180-deg servo, 1000..2000us, no invert, flip on
  0.0f, 180.0f, 1000, 1500, 2000, false, true,
  // tilt: 180-deg servo, 1000..2000us, no invert
  180.0f, 1000, 1500, 2000, false
};

static Config cfg = DEFAULT_CONFIG;
static Preferences prefs;

static void config_load() {
  prefs.begin("zerotx", true);  // read-only

  cfg.station_lat       = prefs.getDouble("st_lat",   DEFAULT_CONFIG.station_lat);
  cfg.station_lon       = prefs.getDouble("st_lon",   DEFAULT_CONFIG.station_lon);
  cfg.station_alt_m     = prefs.getDouble("st_alt",   DEFAULT_CONFIG.station_alt_m);

  cfg.pan_ref_az_deg    = prefs.getFloat ("pn_ref",   DEFAULT_CONFIG.pan_ref_az_deg);
  cfg.pan_range_deg     = prefs.getFloat ("pn_rng",   DEFAULT_CONFIG.pan_range_deg);
  cfg.pan_min_us        = prefs.getInt   ("pn_min",   DEFAULT_CONFIG.pan_min_us);
  cfg.pan_center_us     = prefs.getInt   ("pn_ctr",   DEFAULT_CONFIG.pan_center_us);
  cfg.pan_max_us        = prefs.getInt   ("pn_max",   DEFAULT_CONFIG.pan_max_us);
  cfg.pan_invert        = prefs.getBool  ("pn_inv",   DEFAULT_CONFIG.pan_invert);
  cfg.pan_flip_enabled  = prefs.getBool  ("pn_flip",  DEFAULT_CONFIG.pan_flip_enabled);

  cfg.tilt_range_deg    = prefs.getFloat ("tl_rng",   DEFAULT_CONFIG.tilt_range_deg);
  cfg.tilt_min_us       = prefs.getInt   ("tl_min",   DEFAULT_CONFIG.tilt_min_us);
  cfg.tilt_center_us    = prefs.getInt   ("tl_ctr",   DEFAULT_CONFIG.tilt_center_us);
  cfg.tilt_max_us       = prefs.getInt   ("tl_max",   DEFAULT_CONFIG.tilt_max_us);
  cfg.tilt_invert       = prefs.getBool  ("tl_inv",   DEFAULT_CONFIG.tilt_invert);

  prefs.end();
}

static void config_save() {
  prefs.begin("zerotx", false);  // read-write

  prefs.putDouble("st_lat",   cfg.station_lat);
  prefs.putDouble("st_lon",   cfg.station_lon);
  prefs.putDouble("st_alt",   cfg.station_alt_m);

  prefs.putFloat ("pn_ref",   cfg.pan_ref_az_deg);
  prefs.putFloat ("pn_rng",   cfg.pan_range_deg);
  prefs.putInt   ("pn_min",   cfg.pan_min_us);
  prefs.putInt   ("pn_ctr",   cfg.pan_center_us);
  prefs.putInt   ("pn_max",   cfg.pan_max_us);
  prefs.putBool  ("pn_inv",   cfg.pan_invert);
  prefs.putBool  ("pn_flip",  cfg.pan_flip_enabled);

  prefs.putFloat ("tl_rng",   cfg.tilt_range_deg);
  prefs.putInt   ("tl_min",   cfg.tilt_min_us);
  prefs.putInt   ("tl_ctr",   cfg.tilt_center_us);
  prefs.putInt   ("tl_max",   cfg.tilt_max_us);
  prefs.putBool  ("tl_inv",   cfg.tilt_invert);

  prefs.end();
}

static void config_print() {
  Serial.println("--- config ---");
  Serial.printf("station   lat=%.7f lon=%.7f alt=%.1fm\n",
                cfg.station_lat, cfg.station_lon, cfg.station_alt_m);
  Serial.printf("pan       ref_az=%.2fdeg range=%.1fdeg pulse=%d/%d/%d invert=%s flip=%s\n",
                cfg.pan_ref_az_deg, cfg.pan_range_deg,
                cfg.pan_min_us, cfg.pan_center_us, cfg.pan_max_us,
                cfg.pan_invert      ? "on" : "off",
                cfg.pan_flip_enabled? "on" : "off");
  Serial.printf("tilt      range=%.1fdeg pulse=%d/%d/%d invert=%s\n",
                cfg.tilt_range_deg,
                cfg.tilt_min_us, cfg.tilt_center_us, cfg.tilt_max_us,
                cfg.tilt_invert ? "on" : "off");
  Serial.println("--------------");
}

// =====================================================================
// Az/el computation
// =====================================================================
//
// Spherical earth model, radius 6371 km. Accurate to within ~0.5%
// over expected FPV ranges (< 10 km); ellipsoid corrections aren't
// worth the complexity at these scales.

constexpr double EARTH_RADIUS_M = 6371000.0;

struct AzEl {
  double az_deg;     // 0..360, 0 = N, 90 = E
  double el_deg;     // -90..90, 0 = horizon, +90 = zenith
  double dist_m;     // great-circle ground distance, meters
};

// Compute azimuth and elevation FROM (lat_a, lon_a, alt_a) TO
// (lat_b, lon_b, alt_b). Coordinates in degrees, altitudes in meters.
static AzEl compute_az_el(double lat_a, double lon_a, double alt_a,
                          double lat_b, double lon_b, double alt_b) {
  double phi_a = lat_a * M_PI / 180.0;
  double phi_b = lat_b * M_PI / 180.0;
  double d_lon = (lon_b - lon_a) * M_PI / 180.0;

  // Great-circle initial bearing from a to b
  double y = sin(d_lon) * cos(phi_b);
  double x = cos(phi_a) * sin(phi_b) - sin(phi_a) * cos(phi_b) * cos(d_lon);
  double az_rad = atan2(y, x);
  double az_deg = az_rad * 180.0 / M_PI;
  if (az_deg < 0.0) az_deg += 360.0;

  // Haversine for great-circle ground distance
  double d_phi = phi_b - phi_a;
  double sin_dphi_2 = sin(d_phi / 2.0);
  double sin_dlon_2 = sin(d_lon / 2.0);
  double a = sin_dphi_2 * sin_dphi_2 +
             cos(phi_a) * cos(phi_b) * sin_dlon_2 * sin_dlon_2;
  double c = 2.0 * atan2(sqrt(a), sqrt(1.0 - a));
  double dist = EARTH_RADIUS_M * c;

  // Elevation: atan2 of altitude difference vs ground distance.
  // dist_m near zero (aircraft directly above station) is rare in
  // practice but well-handled by atan2 anyway: el approaches +/- 90.
  double alt_diff = alt_b - alt_a;
  double el_rad = atan2(alt_diff, dist);
  double el_deg = el_rad * 180.0 / M_PI;

  return AzEl{az_deg, el_deg, dist};
}

// =====================================================================
// Servo subsystem
// =====================================================================
//
// Two hobby servos via the ESP32 LEDC peripheral, hardware PWM on
// dedicated GPIOs (no CPU jitter). Standard hobby servo PWM:
// 50Hz frame rate, 12-bit duty resolution gives ~4.88us/step
// (finer than any hobby servo can resolve mechanically; rounding in
// the slew loop is fine).
//
// Pulse widths are now per-axis cfg.{pan,tilt}_{min,center,max}_us.
// Defaults (1000..2000us) match the universal hobby PWM range
// supported by virtually all servos. Configure via:
//   cfg pan_pulse  <min> <center> <max>
//   cfg tilt_pulse <min> <center> <max>
// followed by `cfg save`.
//
// Slew limiter:
//   100Hz update task on core 0 ramps current_us toward target_us
//   by at most SERVO_SLEW_STEP_US per tick. With step=30us at 100Hz,
//   that caps the slew at 3000us/s which is well under what any
//   standard hobby servo can mechanically do (slow analog ~1500us/s,
//   midrange digital ~5000us/s, top-tier digital 8000+us/s). The
//   limit primarily protects against software-bug-class jumps and
//   smooths current draw. Tunable via the constant below.

constexpr int SERVO_PAN_PIN  = 6;
constexpr int SERVO_TILT_PIN = 7;
constexpr int SERVO_PAN_CHANNEL  = 0;
constexpr int SERVO_TILT_CHANNEL = 1;

constexpr int SERVO_FREQ_HZ          = 50;
constexpr int SERVO_RESOLUTION_BITS  = 12;
constexpr int SERVO_PERIOD_US        = 20000;  // 1/50Hz
constexpr int SERVO_DUTY_FULL_SCALE  = 1 << SERVO_RESOLUTION_BITS;  // 4096

constexpr int SERVO_SLEW_HZ       = 100;
constexpr int SERVO_SLEW_STEP_US  = 30;

struct ServoState {
  int          channel;
  int          pin;
  int          current_us;
  volatile int target_us;     // written from any task; read by slew loop
  const char*  name;
};

static ServoState pan_servo  = {
  SERVO_PAN_CHANNEL,  SERVO_PAN_PIN,
  1500, 1500, "pan"   // current_us / target_us seeded; updated at servo_init
};
static ServoState tilt_servo = {
  SERVO_TILT_CHANNEL, SERVO_TILT_PIN,
  1500, 1500, "tilt"
};

// Convert microseconds (within the 20ms period) to LEDC duty value.
// us=1000 -> duty = 1000 * 4096 / 20000 = 204.8, rounded.
static inline uint32_t us_to_duty(int us) {
  // Cast through float to keep the integer rounding sane.
  return (uint32_t)((float)us * (float)SERVO_DUTY_FULL_SCALE / (float)SERVO_PERIOD_US + 0.5f);
}

// Internal: write a raw pulse width to the LEDC peripheral.
// Caller must already have clamped to per-axis bounds.
static void servo_write_us(const ServoState& s, int us) {
  ledcWrite(s.channel, us_to_duty(us));
}

// Public API: set target pulse width. The slew task will ramp toward
// it. Out-of-range values are clamped silently to the per-axis
// configured bounds.
void servo_set_pan_us(int us) {
  if (us < cfg.pan_min_us) us = cfg.pan_min_us;
  if (us > cfg.pan_max_us) us = cfg.pan_max_us;
  pan_servo.target_us = us;
}
void servo_set_tilt_us(int us) {
  if (us < cfg.tilt_min_us) us = cfg.tilt_min_us;
  if (us > cfg.tilt_max_us) us = cfg.tilt_max_us;
  tilt_servo.target_us = us;
}

// Initialize LEDC channels and write the configured center pulse to
// both axes. Called from setup() AFTER config_load() so cfg has
// valid values.
static void servo_init() {
  ledcSetup(pan_servo.channel,  SERVO_FREQ_HZ, SERVO_RESOLUTION_BITS);
  ledcAttachPin(pan_servo.pin,  pan_servo.channel);
  ledcSetup(tilt_servo.channel, SERVO_FREQ_HZ, SERVO_RESOLUTION_BITS);
  ledcAttachPin(tilt_servo.pin, tilt_servo.channel);

  pan_servo.current_us  = cfg.pan_center_us;
  pan_servo.target_us   = cfg.pan_center_us;
  tilt_servo.current_us = cfg.tilt_center_us;
  tilt_servo.target_us  = cfg.tilt_center_us;

  servo_write_us(pan_servo,  cfg.pan_center_us);
  servo_write_us(tilt_servo, cfg.tilt_center_us);

  Serial.printf("servos: pan GP%d ch%d (%d/%d/%d us), tilt GP%d ch%d (%d/%d/%d us), %dHz, %d-bit\n",
                pan_servo.pin,  pan_servo.channel,
                cfg.pan_min_us, cfg.pan_center_us, cfg.pan_max_us,
                tilt_servo.pin, tilt_servo.channel,
                cfg.tilt_min_us, cfg.tilt_center_us, cfg.tilt_max_us,
                SERVO_FREQ_HZ, SERVO_RESOLUTION_BITS);
}

// Slew loop: ramps current_us toward target_us by at most
// SERVO_SLEW_STEP_US per tick at SERVO_SLEW_HZ rate.
static void slew_one(ServoState& s) {
  int target = s.target_us;
  int delta = target - s.current_us;
  if (delta >  SERVO_SLEW_STEP_US) delta =  SERVO_SLEW_STEP_US;
  if (delta < -SERVO_SLEW_STEP_US) delta = -SERVO_SLEW_STEP_US;
  if (delta != 0) {
    s.current_us += delta;
    servo_write_us(s, s.current_us);
  }
}

void servo_task(void* pvParameters) {
  const TickType_t period = pdMS_TO_TICKS(1000 / SERVO_SLEW_HZ);
  TickType_t last = xTaskGetTickCount();
  for (;;) {
    slew_one(pan_servo);
    slew_one(tilt_servo);
    vTaskDelayUntil(&last, period);
  }
}

// Boot self-test: sweep each axis low->high->center to confirm the
// LEDC channels are wired correctly and the servos move as expected.
// Sweep range is the configured pulse range with 10% margin from
// each end (avoids slamming against unknown mechanical stops).
// Blocks the calling context for ~3.6s. Safe to run alongside the
// byte pump (different core).
static void servo_self_test() {
  Serial.println("servo self-test: starting sweep");

  int pan_span    = cfg.pan_max_us  - cfg.pan_min_us;
  int tilt_span   = cfg.tilt_max_us - cfg.tilt_min_us;
  int pan_low     = cfg.pan_min_us  + pan_span  / 10;
  int pan_high    = cfg.pan_max_us  - pan_span  / 10;
  int tilt_low    = cfg.tilt_min_us + tilt_span / 10;
  int tilt_high   = cfg.tilt_max_us - tilt_span / 10;

  servo_set_pan_us(cfg.pan_center_us);
  servo_set_tilt_us(cfg.tilt_center_us);
  delay(800);

  Serial.println("  pan: low");
  servo_set_pan_us(pan_low);             delay(700);
  Serial.println("  pan: high");
  servo_set_pan_us(pan_high);            delay(700);
  Serial.println("  pan: center");
  servo_set_pan_us(cfg.pan_center_us);   delay(500);

  Serial.println("  tilt: low");
  servo_set_tilt_us(tilt_low);           delay(700);
  Serial.println("  tilt: high");
  servo_set_tilt_us(tilt_high);          delay(700);
  Serial.println("  tilt: center");
  servo_set_tilt_us(cfg.tilt_center_us); delay(500);

  Serial.println("servo self-test: complete");
}

// =====================================================================
// Tracking subsystem
// =====================================================================
//
// Glues the az/el output of the math layer to the servo subsystem.
// Three pieces:
//   1. Input EMA filter on az/el to smooth GPS noise.
//   2. aim_at(): az/el -> pan/tilt servo angles, with the standard
//      front/rear flip technique for 180-degree pan servos and
//      hysteresis around the flip boundary.
//   3. Failsafe via "no commands while stale": if no GPS frames
//      arrive, no calls to servo_set_*_us happen and the slew loop
//      simply maintains the last commanded position.
//
// Per-axis configuration (cfg.pan_*, cfg.tilt_*) determines:
//   - Pulse mapping (min/center/max us, range_deg)
//   - Direction inversion (cfg.*_invert)
//   - Flip technique enable (cfg.pan_flip_enabled - turn off for
//     270- or 360-deg pan servos)
//
// Compile-time tuning (NOT in cfg):

constexpr float    EMA_ALPHA            = 0.3f;
constexpr float    FLIP_HYSTERESIS_DEG  = 5.0f;
constexpr uint32_t FAILSAFE_HOLD_MS     = 1500;

// EMA state (initialized lazily on first frame)
static bool   ema_initialized   = false;
static float  az_filtered_deg   = 0.0f;
static float  el_filtered_deg   = 0.0f;

// Hysteresis state for the front/rear flip
static bool   flip_active       = false;

// Last GPS frame timestamp; 0 = never seen telemetry
static volatile uint32_t g_last_gps_ms = 0;

// Apply exponential moving average to incoming az/el. Handles the
// azimuth wraparound at 0/360 by working in shortest-delta space.
static void apply_ema(float az_in, float el_in) {
  if (!ema_initialized) {
    az_filtered_deg = az_in;
    el_filtered_deg = el_in;
    ema_initialized = true;
    return;
  }

  el_filtered_deg = EMA_ALPHA * el_in + (1.0f - EMA_ALPHA) * el_filtered_deg;

  float delta = az_in - az_filtered_deg;
  while (delta >  180.0f) delta -= 360.0f;
  while (delta < -180.0f) delta += 360.0f;
  az_filtered_deg += EMA_ALPHA * delta;
  if (az_filtered_deg <    0.0f) az_filtered_deg += 360.0f;
  if (az_filtered_deg >= 360.0f) az_filtered_deg -= 360.0f;
}

// Convert pan angle in degrees (-pan_range_deg/2 .. +pan_range_deg/2,
// 0 = center pulse) to pulse width. Honors cfg.pan_invert.
static int pan_angle_to_us(float pan_deg) {
  if (cfg.pan_invert) pan_deg = -pan_deg;
  float us_per_deg = (float)(cfg.pan_max_us - cfg.pan_min_us) / cfg.pan_range_deg;
  int us = cfg.pan_center_us + (int)(pan_deg * us_per_deg);
  if (us < cfg.pan_min_us) us = cfg.pan_min_us;
  if (us > cfg.pan_max_us) us = cfg.pan_max_us;
  return us;
}

// Convert tilt angle in degrees (0 = horizon-front, 90 = zenith,
// up to 180 = horizon-rear when flip is engaged) to pulse width.
// Honors cfg.tilt_invert.
//
// Mapping: tilt_deg=0 -> tilt_min_us, full sweep proportional to
// cfg.tilt_range_deg. With cfg.tilt_range_deg=180 and full pulse
// range, the entire 0..180-deg arc maps linearly through the pulse
// span; useful for the flip technique where the servo sweeps past
// zenith into the rear hemisphere.
static int tilt_angle_to_us(float tilt_deg) {
  if (tilt_deg <    0.0f) tilt_deg =   0.0f;
  if (tilt_deg >  180.0f) tilt_deg = 180.0f;
  if (cfg.tilt_invert) tilt_deg = cfg.tilt_range_deg - tilt_deg;
  float us_per_deg = (float)(cfg.tilt_max_us - cfg.tilt_min_us) / cfg.tilt_range_deg;
  int us = cfg.tilt_min_us + (int)(tilt_deg * us_per_deg);
  if (us < cfg.tilt_min_us) us = cfg.tilt_min_us;
  if (us > cfg.tilt_max_us) us = cfg.tilt_max_us;
  return us;
}

// Aim antennas at the given azimuth and elevation. Implements the
// front/rear flip with hysteresis for 180-degree pan servos when
// cfg.pan_flip_enabled is true:
//   - Front pose: pan_deg = az_rel,        tilt_deg = el
//   - Rear  pose: pan_deg = az_rel +/- 180, tilt_deg = 180 - el
// With cfg.pan_flip_enabled false (270/360-deg servos), pan_deg is
// always az_rel and the pan_angle_to_us mapping handles the larger
// range. Note: 360-deg servos with this firmware do NOT track
// continuously across the wraparound (179deg -> -179deg takes the
// long way); the firmware doesn't yet maintain an unwrap variable.
//
// az_deg in 0..360, el_deg typically 0..90. The az_deg input is
// expected to already be EMA-smoothed.
static void aim_at(float az_deg, float el_deg) {
  // Normalize azimuth relative to pan reference, into -180..+180
  float az_rel = az_deg - cfg.pan_ref_az_deg;
  while (az_rel >  180.0f) az_rel -= 360.0f;
  while (az_rel < -180.0f) az_rel += 360.0f;

  float pan_deg, tilt_deg;

  if (!cfg.pan_flip_enabled) {
    // No flip: just map azimuth straight through. Suitable for
    // 270- or 360-deg pan servos where there's enough mechanical
    // travel to cover the rear hemisphere directly.
    pan_deg  = az_rel;
    tilt_deg = el_deg;
    flip_active = false;
  } else {
    // 180-deg pan servo with flip technique.
    float abs_az_rel = fabsf(az_rel);

    // Hysteresis around |az_rel|=90 prevents oscillation.
    if (flip_active) {
      flip_active = (abs_az_rel > (90.0f - FLIP_HYSTERESIS_DEG));
    } else {
      flip_active = (abs_az_rel > (90.0f + FLIP_HYSTERESIS_DEG));
    }

    if (!flip_active) {
      pan_deg  = az_rel;        // -90..+90
      tilt_deg = el_deg;        // 0..90 (above horizon, front)
    } else {
      pan_deg  = (az_rel > 0.0f) ? (az_rel - 180.0f) : (az_rel + 180.0f);
      tilt_deg = 180.0f - el_deg;  // 90..180 (above horizon, rear)
    }
  }

  servo_set_pan_us( pan_angle_to_us(pan_deg)  );
  servo_set_tilt_us(tilt_angle_to_us(tilt_deg));
}

// =====================================================================
// USB-CDC command parser
// =====================================================================
//
// Terse line-oriented command interface running on core 0. Reads
// from Serial one character at a time, accumulates into a line
// buffer, and dispatches on newline. Echo on, basic backspace
// handling, and a "> " prompt after each line. Designed to be
// driven from `pio device monitor` or any plain serial terminal.
//
// All commands are non-blocking with respect to the byte pump; the
// parser task runs at normal priority on core 0 and never touches
// the byte pump's data path.

static const int CMD_LINE_MAX = 128;
static const int CMD_MAX_ARGS = 8;

// Forward decl, defined below
static void cmd_dispatch(int argc, char** argv);

// Forward decl for stats counters (defined further down). Declaring
// here lets cmd_stats reference them.
extern volatile uint32_t g_frames_seen;
extern volatile uint32_t g_frames_bad_crc;
extern volatile uint32_t g_frames_gps;
extern volatile uint32_t g_bytes_dropped;

// Tokenize an in-place line buffer into argv[]. Returns argc.
// Modifies the buffer (writes nulls between tokens).
static int tokenize(char* line, char** argv, int max_args) {
  int argc = 0;
  char* p = line;
  while (argc < max_args) {
    while (*p == ' ' || *p == '\t') p++;
    if (*p == 0) break;
    argv[argc++] = p;
    while (*p != 0 && *p != ' ' && *p != '\t') p++;
    if (*p != 0) {
      *p++ = 0;
    }
  }
  return argc;
}

// Parse "on" / "off" / "1" / "0" / "true" / "false". Returns true if
// recognized (and writes result to *out); false otherwise.
static bool parse_bool(const char* s, bool* out) {
  if (!strcmp(s, "on")  || !strcmp(s, "1") || !strcmp(s, "true")) {
    *out = true;  return true;
  }
  if (!strcmp(s, "off") || !strcmp(s, "0") || !strcmp(s, "false")) {
    *out = false; return true;
  }
  return false;
}

// --- Top-level command handlers ---

static void cmd_help() {
  Serial.println();
  Serial.println("commands:");
  Serial.println("  help                                    list commands");
  Serial.println("  cfg show                                show current config");
  Serial.println("  cfg save                                persist config to NVS");
  Serial.println("  cfg station <lat> <lon> <alt_m>         set station coords");
  Serial.println("  cfg pan_ref <az_deg>                    pan-center pulse az");
  Serial.println("  cfg pan_range <deg>                     pan travel (180/270/360)");
  Serial.println("  cfg pan_pulse <min> <ctr> <max>         pan pulse widths in us");
  Serial.println("  cfg pan_invert <on|off>                 reverse pan direction");
  Serial.println("  cfg pan_flip <on|off>                   enable 180-deg flip");
  Serial.println("  cfg tilt_range <deg>                    tilt travel");
  Serial.println("  cfg tilt_pulse <min> <ctr> <max>        tilt pulse widths in us");
  Serial.println("  cfg tilt_invert <on|off>                reverse tilt direction");
  Serial.println("  aim <az> <el>                           manually drive servos");
  Serial.println("  pos                                     show servo positions");
  Serial.println("  stats                                   show parser stats");
  Serial.println("  defaults                                reset cfg to compile defaults");
  Serial.println("  reboot                                  software reset");
  Serial.println();
}

static void cmd_aim(int argc, char** argv) {
  if (argc < 3) {
    Serial.println("usage: aim <az_deg> <el_deg>");
    return;
  }
  float az = strtof(argv[1], nullptr);
  float el = strtof(argv[2], nullptr);
  // Bypass EMA - this is direct manual aim. The tracker still uses
  // the global flip_active state for hysteresis continuity.
  aim_at(az, el);
  Serial.printf("aiming az=%.2f el=%.2f -> pan=%dus tilt=%dus flip=%s\n",
                az, el, pan_servo.target_us, tilt_servo.target_us,
                flip_active ? "on" : "off");
}

static void cmd_pos() {
  Serial.printf("pan:  current=%dus target=%dus\n",
                pan_servo.current_us, pan_servo.target_us);
  Serial.printf("tilt: current=%dus target=%dus\n",
                tilt_servo.current_us, tilt_servo.target_us);
  Serial.printf("flip: %s\n", flip_active ? "active" : "inactive");
  if (ema_initialized) {
    Serial.printf("ema:  az=%.2f el=%.2f\n", az_filtered_deg, el_filtered_deg);
  } else {
    Serial.println("ema:  uninitialized (no GPS frame yet)");
  }
}

static void cmd_stats() {
  Serial.printf("uptime: %lus\n", (unsigned long)(millis() / 1000));
  Serial.printf("frames=%u gps=%u bad_crc=%u dropped=%u\n",
                (unsigned)g_frames_seen, (unsigned)g_frames_gps,
                (unsigned)g_frames_bad_crc, (unsigned)g_bytes_dropped);
  uint32_t last_gps = g_last_gps_ms;
  if (last_gps == 0) {
    Serial.println("telemetry: never seen");
  } else {
    Serial.printf("telemetry age: %lums\n",
                  (unsigned long)(millis() - last_gps));
  }
}

static void cmd_defaults() {
  cfg = DEFAULT_CONFIG;
  Serial.println("config reset to compile-time defaults (in RAM only)");
  Serial.println("run `cfg save` to persist to NVS");
}

// --- cfg subcommand dispatch ---

static void cmd_cfg(int argc, char** argv) {
  if (argc == 1 || !strcmp(argv[1], "show")) {
    config_print();
    return;
  }

  if (!strcmp(argv[1], "save")) {
    config_save();
    Serial.println("config saved to NVS");
    return;
  }

  if (!strcmp(argv[1], "station")) {
    if (argc < 5) {
      Serial.println("usage: cfg station <lat> <lon> <alt_m>");
      return;
    }
    cfg.station_lat   = strtod(argv[2], nullptr);
    cfg.station_lon   = strtod(argv[3], nullptr);
    cfg.station_alt_m = strtod(argv[4], nullptr);
    Serial.printf("station = %.7f %.7f %.1fm\n",
                  cfg.station_lat, cfg.station_lon, cfg.station_alt_m);
    return;
  }

  if (!strcmp(argv[1], "pan_ref")) {
    if (argc < 3) { Serial.println("usage: cfg pan_ref <az_deg>"); return; }
    cfg.pan_ref_az_deg = strtof(argv[2], nullptr);
    Serial.printf("pan_ref = %.2f deg\n", cfg.pan_ref_az_deg);
    return;
  }

  if (!strcmp(argv[1], "pan_range")) {
    if (argc < 3) { Serial.println("usage: cfg pan_range <deg>"); return; }
    cfg.pan_range_deg = strtof(argv[2], nullptr);
    Serial.printf("pan_range = %.1f deg\n", cfg.pan_range_deg);
    return;
  }

  if (!strcmp(argv[1], "pan_pulse")) {
    if (argc < 5) {
      Serial.println("usage: cfg pan_pulse <min> <center> <max>");
      return;
    }
    cfg.pan_min_us    = atoi(argv[2]);
    cfg.pan_center_us = atoi(argv[3]);
    cfg.pan_max_us    = atoi(argv[4]);
    Serial.printf("pan_pulse = %d / %d / %d us\n",
                  cfg.pan_min_us, cfg.pan_center_us, cfg.pan_max_us);
    return;
  }

  if (!strcmp(argv[1], "pan_invert")) {
    bool v;
    if (argc < 3 || !parse_bool(argv[2], &v)) {
      Serial.println("usage: cfg pan_invert <on|off>"); return;
    }
    cfg.pan_invert = v;
    Serial.printf("pan_invert = %s\n", v ? "on" : "off");
    return;
  }

  if (!strcmp(argv[1], "pan_flip")) {
    bool v;
    if (argc < 3 || !parse_bool(argv[2], &v)) {
      Serial.println("usage: cfg pan_flip <on|off>"); return;
    }
    cfg.pan_flip_enabled = v;
    Serial.printf("pan_flip = %s\n", v ? "on" : "off");
    return;
  }

  if (!strcmp(argv[1], "tilt_range")) {
    if (argc < 3) { Serial.println("usage: cfg tilt_range <deg>"); return; }
    cfg.tilt_range_deg = strtof(argv[2], nullptr);
    Serial.printf("tilt_range = %.1f deg\n", cfg.tilt_range_deg);
    return;
  }

  if (!strcmp(argv[1], "tilt_pulse")) {
    if (argc < 5) {
      Serial.println("usage: cfg tilt_pulse <min> <center> <max>");
      return;
    }
    cfg.tilt_min_us    = atoi(argv[2]);
    cfg.tilt_center_us = atoi(argv[3]);
    cfg.tilt_max_us    = atoi(argv[4]);
    Serial.printf("tilt_pulse = %d / %d / %d us\n",
                  cfg.tilt_min_us, cfg.tilt_center_us, cfg.tilt_max_us);
    return;
  }

  if (!strcmp(argv[1], "tilt_invert")) {
    bool v;
    if (argc < 3 || !parse_bool(argv[2], &v)) {
      Serial.println("usage: cfg tilt_invert <on|off>"); return;
    }
    cfg.tilt_invert = v;
    Serial.printf("tilt_invert = %s\n", v ? "on" : "off");
    return;
  }

  Serial.printf("unknown cfg subcommand: %s (try 'help')\n", argv[1]);
}

// --- Top-level dispatch ---

static void cmd_dispatch(int argc, char** argv) {
  if (argc == 0) return;

  if (!strcmp(argv[0], "help"))     { cmd_help();           return; }
  if (!strcmp(argv[0], "cfg"))      { cmd_cfg(argc, argv);  return; }
  if (!strcmp(argv[0], "aim"))      { cmd_aim(argc, argv);  return; }
  if (!strcmp(argv[0], "pos"))      { cmd_pos();            return; }
  if (!strcmp(argv[0], "stats"))    { cmd_stats();          return; }
  if (!strcmp(argv[0], "defaults")) { cmd_defaults();       return; }

  if (!strcmp(argv[0], "reboot")) {
    Serial.println("rebooting...");
    delay(100);
    ESP.restart();
    return;
  }

  Serial.printf("unknown command: %s (try 'help')\n", argv[0]);
}

// --- Command task: line reader + dispatcher ---

void cmd_task(void* pvParameters) {
  char line[CMD_LINE_MAX];
  size_t n = 0;
  Serial.print("> ");
  for (;;) {
    while (Serial.available() > 0) {
      int c = Serial.read();
      if (c < 0) break;

      if (c == '\r') continue;       // ignore CR (CRLF)
      if (c == '\n') {
        Serial.println();
        line[n] = 0;
        if (n > 0) {
          char* argv[CMD_MAX_ARGS];
          int argc = tokenize(line, argv, CMD_MAX_ARGS);
          cmd_dispatch(argc, argv);
        }
        n = 0;
        Serial.print("> ");
      } else if (c == 0x7f || c == 0x08) {  // backspace / delete
        if (n > 0) {
          n--;
          Serial.print("\b \b");
        }
      } else if (n < CMD_LINE_MAX - 1 && c >= 0x20 && c < 0x7f) {
        line[n++] = (char)c;
        Serial.write((uint8_t)c);   // echo
      }
    }
    vTaskDelay(pdMS_TO_TICKS(20));
  }
}

// =====================================================================
// Diagnostic counters (parser updates, loop() samples)
// =====================================================================
//
// volatile for cross-core read (loop on core 0 reads while parser on
// core 0 writes; same core but task-switched). Not strictly needed
// for atomicity since uint32_t reads/writes are atomic on Xtensa, but
// volatile prevents the compiler from caching reads across calls.
volatile uint32_t g_frames_seen    = 0;
volatile uint32_t g_frames_bad_crc = 0;
volatile uint32_t g_frames_gps     = 0;
volatile uint32_t g_bytes_dropped  = 0;  // parser tee-buffer overflow

// =====================================================================
// UART instances
// =====================================================================
HardwareSerial uartCable(1);  // UART1 (RS-422 / MAX490 side)
HardwareSerial uartElrs(2);   // UART2 (ELRS module side)

// =====================================================================
// Forward declarations
// =====================================================================
void byte_pump_task(void* pvParameters);
void parser_task(void* pvParameters);
void servo_task(void* pvParameters);
void cmd_task(void* pvParameters);

// =====================================================================
// setup()
// =====================================================================
void setup() {
  Serial.begin(115200);
  delay(500);

  Serial.println();
  Serial.printf("=== zerotx-tracker fw %s ===\n", FW_VERSION);
  Serial.println("Phase 5: tracking + USB-CDC config + NVS persistence");
  Serial.println();

  uartCable.begin(CRSF_BAUD, SERIAL_8N1, UART1_RX, UART1_TX);
  uartCable.setRxBufferSize(512);
  uartCable.setTxBufferSize(512);

  uartElrs.begin(CRSF_BAUD, SERIAL_8N1, UART2_RX, UART2_TX);
  uartElrs.setRxBufferSize(512);
  uartElrs.setTxBufferSize(512);

  Serial.printf("UART1 (cable): RX=GP%d TX=GP%d @ %d baud\n",
                UART1_RX, UART1_TX, CRSF_BAUD);
  Serial.printf("UART2 (ELRS):  RX=GP%d TX=GP%d @ %d baud\n",
                UART2_RX, UART2_TX, CRSF_BAUD);

  esp_task_wdt_init(WDT_TIMEOUT_S, true);
  Serial.printf("watchdog: %ds, panic-on-timeout\n", WDT_TIMEOUT_S);

  // Load persisted configuration. Falls back to compile-time defaults
  // for any keys not present in NVS, so first-boot or after-flash-erase
  // produces sensible values without needing a special path.
  config_load();
  config_print();

  // Allocate the parser tee. Single-byte trigger level (the parser
  // wakes as soon as ANY byte arrives; it batches in its own buffer).
  telem_buffer = xStreamBufferCreate(TELEM_BUFFER_SIZE, 1);
  if (!telem_buffer) {
    Serial.println("FATAL: failed to allocate telem_buffer");
  } else {
    Serial.printf("telem_buffer: %u bytes\n", (unsigned)TELEM_BUFFER_SIZE);
  }

  // Byte pump on core 1, top priority.
  BaseType_t rc = xTaskCreatePinnedToCore(
      byte_pump_task, "byte_pump", 4096, nullptr,
      configMAX_PRIORITIES - 1, nullptr, 1);
  if (rc != pdPASS) {
    Serial.println("FATAL: failed to start byte_pump task");
  } else {
    Serial.println("byte_pump task running on core 1");
  }

  // Parser on core 0, normal priority. Lower than byte pump
  // categorically (different cores, but explicit anyway).
  rc = xTaskCreatePinnedToCore(
      parser_task, "crsf_parser", 4096, nullptr,
      1, nullptr, 0);
  if (rc != pdPASS) {
    Serial.println("FATAL: failed to start parser task");
  } else {
    Serial.println("crsf_parser task running on core 0");
  }

  // Servo subsystem. Initialize LEDC channels (writes configured
  // center pulse to both axes) before starting the slew task.
  // config_load() must already have run.
  servo_init();

  // Slew loop on core 0 at SERVO_SLEW_HZ (100Hz). Same priority as
  // the parser; they cooperate via task switching. NOT registered
  // with the watchdog: a hung servo loop should not panic the byte
  // pump, which is on core 1 and independent.
  rc = xTaskCreatePinnedToCore(
      servo_task, "servo_slew", 2048, nullptr,
      1, nullptr, 0);
  if (rc != pdPASS) {
    Serial.println("FATAL: failed to start servo task");
  } else {
    Serial.println("servo_slew task running on core 0");
  }

  // USB-CDC command parser on core 0. Reads one line at a time from
  // the serial console, dispatches commands. Used for runtime config
  // and manual servo aim during installation alignment.
  rc = xTaskCreatePinnedToCore(
      cmd_task, "cmd_parser", 4096, nullptr,
      1, nullptr, 0);
  if (rc != pdPASS) {
    Serial.println("FATAL: failed to start cmd task");
  } else {
    Serial.println("cmd_parser task running on core 0");
  }

  // Boot self-test sweep. Blocks ~3.6s. Skip by commenting out if
  // the tracker is already on the pole with antennas attached and
  // you don't want them moving on every boot.
  servo_self_test();

  Serial.println("ready (type 'help' for commands)");
  Serial.println();
}

// =====================================================================
// byte_pump_task: forward UART1 <-> UART2 transparently, tee upstream
// =====================================================================
//
// Pinned to core 1 at top priority. Forwards every byte in both
// directions. On the ELRS -> cable direction, bytes are ALSO tee'd
// into the parser stream buffer with a 0-tick timeout: if the buffer
// is full the tee silently drops the bytes (g_bytes_dropped tracks
// this). The byte pump's primary job is wire forwarding, never the
// tee.
void byte_pump_task(void* pvParameters) {
  esp_task_wdt_add(nullptr);

  uint8_t buf[64];

  for (;;) {
    // Cable -> ELRS (downstream: daemon control, RC channels)
    int n = uartCable.available();
    if (n > 0) {
      if (n > (int)sizeof(buf)) n = sizeof(buf);
      uartCable.read(buf, n);
      uartElrs.write(buf, n);
    }

    // ELRS -> Cable (upstream: telemetry frames)
    n = uartElrs.available();
    if (n > 0) {
      if (n > (int)sizeof(buf)) n = sizeof(buf);
      uartElrs.read(buf, n);
      uartCable.write(buf, n);

      // Non-blocking tee to the parser.
      if (telem_buffer) {
        size_t sent = xStreamBufferSend(telem_buffer, buf, n, 0);
        if (sent < (size_t)n) {
          g_bytes_dropped += (n - sent);
        }
      }
    }

    esp_task_wdt_reset();
    vTaskDelay(1);
  }
}

// =====================================================================
// parser_task: CRSF frame state machine, GPS extraction
// =====================================================================
//
// Reads bytes from the tee stream buffer, runs a 3-state CRSF frame
// parser (sync, length, payload+crc), validates CRC, and on a valid
// GPS frame logs the decoded position to USB-CDC.
//
// Other frame types (BATTERY, LINK_STATS, ATTITUDE, etc.) are recognized
// at the type-byte level but their payloads are not currently parsed.
// They count toward g_frames_seen so we can sanity-check that telemetry
// is flowing.
void parser_task(void* pvParameters) {
  uint8_t batch[64];
  uint8_t frame[CRSF_MAX_FRAME];

  enum State { WAIT_SYNC, WAIT_LEN, COLLECT_PAYLOAD };
  State state = WAIT_SYNC;
  size_t pos = 0;       // next write index in frame[]
  size_t need = 0;      // bytes still needed for current frame

  for (;;) {
    // Block waiting for at least one byte; batch up to 64 at a time.
    // 100ms timeout lets us yield politely when no traffic flows.
    size_t got = xStreamBufferReceive(telem_buffer, batch, sizeof(batch),
                                      pdMS_TO_TICKS(100));
    if (got == 0) continue;

    for (size_t i = 0; i < got; i++) {
      uint8_t b = batch[i];

      switch (state) {
        case WAIT_SYNC:
          if (b == CRSF_SYNC_FC || b == CRSF_SYNC_HANDSET) {
            frame[0] = b;
            pos = 1;
            state = WAIT_LEN;
          }
          break;

        case WAIT_LEN:
          if (b < CRSF_MIN_LEN_BYTE || b > CRSF_MAX_LEN_BYTE) {
            // Bad length, resync. Probably a stray byte that
            // happened to look like a sync.
            state = WAIT_SYNC;
            break;
          }
          frame[1] = b;
          need = b;
          pos = 2;
          state = COLLECT_PAYLOAD;
          break;

        case COLLECT_PAYLOAD:
          frame[pos++] = b;
          need--;
          if (need == 0) {
            // Frame complete. Frame layout:
            //   frame[0]      sync
            //   frame[1]      len
            //   frame[2]      type
            //   frame[3..]    payload
            //   frame[pos-1]  CRC (last byte)
            // CRC covers [type ... last payload byte], i.e.
            // frame[2] through frame[pos-2], length = pos - 3.
            uint8_t crc_calc = crsf_crc8(&frame[2], pos - 3);
            uint8_t crc_recv = frame[pos - 1];

            g_frames_seen++;
            if (crc_calc != crc_recv) {
              g_frames_bad_crc++;
            } else {
              uint8_t type = frame[2];
              const uint8_t* payload = &frame[3];
              size_t payload_len = pos - 4;  // exclude sync, len, type, crc

              if (type == CRSF_FRAME_GPS) {
                g_frames_gps++;
                GpsFrame gps;
                if (parse_gps_payload(payload, payload_len, gps)) {
                  Serial.printf(
                    "GPS lat=%.7f lon=%.7f alt=%dm spd=%.1fkm/h hdg=%.2f sats=%u\n",
                    gps.lat_deg, gps.lon_deg, gps.altitude_m,
                    gps.speed_kmh, gps.heading_deg, gps.sats);

                  // Compute pointing direction from station to aircraft.
                  AzEl ae = compute_az_el(cfg.station_lat, cfg.station_lon,
                                          cfg.station_alt_m,
                                          gps.lat_deg, gps.lon_deg,
                                          (double)gps.altitude_m);
                  Serial.printf("TRK az=%.2f el=%.2f dist=%.0fm\n",
                                ae.az_deg, ae.el_deg, ae.dist_m);

                  // Smooth and command the servos.
                  apply_ema((float)ae.az_deg, (float)ae.el_deg);
                  aim_at(az_filtered_deg, el_filtered_deg);

                  // Mark fresh telemetry for the heartbeat status.
                  g_last_gps_ms = millis();
                }
              }
              // Other types ignored. We could log BATTERY voltage,
              // LINK_STATS RSSI, etc. in later phases if useful.
            }
            state = WAIT_SYNC;
          }
          break;
      }
    }
  }
}

// =====================================================================
// loop(): heartbeat + parser stats on core 0
// =====================================================================
void loop() {
  static uint32_t last_log_ms = 0;
  uint32_t now = millis();
  if (now - last_log_ms >= 5000) {
    last_log_ms = now;

    // Tracking state. The mechanical behavior is the same in all
    // three states (the slew loop holds last commanded position when
    // no new commands arrive); this is just a status indicator for
    // the operator.
    const char* track_state;
    char age_str[32] = "";
    uint32_t last_gps = g_last_gps_ms;
    if (last_gps == 0) {
      track_state = "no-telem";
    } else {
      uint32_t age_ms = now - last_gps;
      track_state = (age_ms < FAILSAFE_HOLD_MS) ? "tracking" : "hold";
      snprintf(age_str, sizeof(age_str), " age=%lums",
               (unsigned long)age_ms);
    }

    Serial.printf("heartbeat uptime=%lus frames=%u gps=%u bad_crc=%u dropped=%u %s%s\n",
                  (unsigned long)(now / 1000),
                  (unsigned)g_frames_seen,
                  (unsigned)g_frames_gps,
                  (unsigned)g_frames_bad_crc,
                  (unsigned)g_bytes_dropped,
                  track_state,
                  age_str);
  }
  delay(100);
}
