// ZeroTX antenna tracker firmware.
//
// Phase 2: Phase 1 + az/el math.
//
// Builds on Phase 1's CRSF parser: every successfully decoded GPS
// frame now also gets pointed-at math run on it. Station coordinates
// are hardcoded constants (Phase 5 will move them to NVS flash with
// a USB-CDC calibration command); the math is spherical-earth
// haversine for great-circle distance and atan2 for elevation. No
// servos yet; this phase just logs az/el alongside the raw GPS so
// the math can be sanity-checked on the bench.
//
// Architecture (full plan):
//   Phase 0: byte pump on core 1, hardware watchdog, USB-CDC log.
//   Phase 1: tee + CRSF parser + GPS extraction.
//   Phase 2 (this firmware): az/el math added.
//   Phase 3: LEDC PWM driving 2 servos.
//   Phase 4: glue az/el outputs to servo angles. Failsafe behavior.
//   Phase 5: USB-CDC calibration interface, NVS-stored station coords.
//
// The byte pump is the safety floor. It runs at the highest possible
// priority on core 1 and is never blocked by code added in any later
// phase. Every Phase 1+ feature lives on core 0.
//
// Hardware: ESP32-S3 (QFN56), 16MB QIO flash + 8MB QSPI PSRAM.

#include <Arduino.h>
#include <math.h>
#include <esp_task_wdt.h>
#include <freertos/stream_buffer.h>

constexpr const char* FW_VERSION = "0.3.0-azel";

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
// 420 kbaud matches the rp2040 CRSF generator (rp2040/src/crsf.c).
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
// Station coordinates
// =====================================================================
//
// PLACEHOLDER: edit these to match the actual installation site
// before field use. Phase 5 will move them to NVS flash with a
// USB-CDC calibration command, so they survive across firmware
// updates without source edits. For now: hardcoded.
//
// Defaults are roughly Campinas, useful for sanity-checking the
// math but NOT correct for the rural flying field where the tracker
// will deploy.
constexpr double STATION_LAT   = -22.9123;   // degrees
constexpr double STATION_LON   = -47.0610;   // degrees
constexpr double STATION_ALT_M =   685.0;    // meters above sea level

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

// =====================================================================
// setup()
// =====================================================================
void setup() {
  Serial.begin(115200);
  delay(500);

  Serial.println();
  Serial.printf("=== zerotx-tracker fw %s ===\n", FW_VERSION);
  Serial.println("Phase 2: byte pump + parser + az/el math");
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

  Serial.println("ready");
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
                  AzEl ae = compute_az_el(STATION_LAT, STATION_LON,
                                          STATION_ALT_M,
                                          gps.lat_deg, gps.lon_deg,
                                          (double)gps.altitude_m);
                  Serial.printf("TRK az=%.2f el=%.2f dist=%.0fm\n",
                                ae.az_deg, ae.el_deg, ae.dist_m);
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
    Serial.printf("heartbeat uptime=%lus frames=%u gps=%u bad_crc=%u dropped=%u\n",
                  (unsigned long)(now / 1000),
                  (unsigned)g_frames_seen,
                  (unsigned)g_frames_gps,
                  (unsigned)g_frames_bad_crc,
                  (unsigned)g_bytes_dropped);
  }
  delay(100);
}
