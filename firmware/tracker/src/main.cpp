// ZeroTX antenna tracker firmware.
//
// Phase 0: pure CRSF byte-pump. The tracker sits inline between the
// daemon-side MAX490 RS-422 transceiver and the ELRS TX module's CRSF
// interface, forwarding bytes transparently in both directions.
// No CRSF parsing, no servo control, no math. Just bytes through.
//
// Architecture (full plan, only Phase 0 implemented here):
//   Phase 0 (this firmware):
//     - Byte pump on core 1 at highest priority.
//     - Hardware watchdog with 1s timeout.
//     - USB-CDC log output over native USB.
//
//   Phase 1: CRSF telemetry sniffer on core 0. Reads bytes via a tee
//     from the byte pump (non-blocking ring buffer); parses CRSF GPS
//     frames (type 0x02). Must never starve or block the byte pump.
//
//   Phase 2: az/el math from aircraft GPS + station GPS coords.
//     Haversine for great-circle bearing, atan2 for elevation.
//
//   Phase 3: LEDC PWM on GPIO 6 (pan) and GPIO 7 (tilt). Slew-rate
//     limiting to avoid servo hunt and reduce mechanical stress.
//
//   Phase 4: glue az/el outputs to servo angles. Failsafe behavior on
//     telemetry loss.
//
//   Phase 5: USB-CDC calibration interface. Station coords + servo
//     offsets stored in NVS flash.
//
// The byte pump on core 1 is the safety floor of this design. It runs
// at the highest possible priority and is never blocked by code added
// in later phases. Every Phase 1+ feature must be implementable on
// core 0 without adding work to the byte pump.
//
// Hardware: ESP32-S3-WROOM-1, N16R8 (16MB flash, 8MB octal PSRAM).

#include <Arduino.h>
#include <esp_task_wdt.h>

constexpr const char* FW_VERSION = "0.1.0-bytepump";

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
// Pins to AVOID on the ESP32-S3-WROOM-1 N16R8 module:
//   GPIO 19, 20  : native USB-OTG (this firmware uses USB-CDC log here)
//   GPIO 26-32   : SPI flash and octal PSRAM internals
//   GPIO 33-37   : not exposed on WROOM-1 module (PSRAM internals)
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
// CRSF v3 negotiates up to 2.25 Mbaud; if the project ever moves to
// v3 this constant and the daemon-side baud need to change together.
constexpr int CRSF_BAUD = 420000;

// =====================================================================
// Watchdog
// =====================================================================
//
// 1-second hardware watchdog. The byte-pump task feeds it on every
// iteration. If the pump hangs even briefly the chip resets and the
// link recovers; this is the conservative behavior we want for a
// safety-critical inline forwarder. CRSF will fail-safe during the
// reset interval.
constexpr int WDT_TIMEOUT_S = 1;

// =====================================================================
// UART instances
// =====================================================================
HardwareSerial uartCable(1);  // UART1 (RS-422 / MAX490 side)
HardwareSerial uartElrs(2);   // UART2 (ELRS module side)

// =====================================================================
// Forward declarations
// =====================================================================
void byte_pump_task(void* pvParameters);

// =====================================================================
// setup()
// =====================================================================
void setup() {
  // Native USB-CDC for log output. arduino_usb_cdc_on_boot=1 in
  // platformio.ini routes Serial through the chip's USB peripheral on
  // GPIO 19/20, no external bridge required.
  Serial.begin(115200);
  delay(500);  // give the USB host time to enumerate before printing

  Serial.println();
  Serial.printf("=== zerotx-tracker fw %s ===\n", FW_VERSION);
  Serial.println("Phase 0: pure CRSF byte-pump (no tracking logic)");
  Serial.println();

  // Configure UART1 (cable / MAX490 side)
  uartCable.begin(CRSF_BAUD, SERIAL_8N1, UART1_RX, UART1_TX);
  uartCable.setRxBufferSize(512);
  uartCable.setTxBufferSize(512);

  // Configure UART2 (ELRS module side)
  uartElrs.begin(CRSF_BAUD, SERIAL_8N1, UART2_RX, UART2_TX);
  uartElrs.setRxBufferSize(512);
  uartElrs.setTxBufferSize(512);

  Serial.printf("UART1 (cable): RX=GP%d TX=GP%d @ %d baud\n",
                UART1_RX, UART1_TX, CRSF_BAUD);
  Serial.printf("UART2 (ELRS):  RX=GP%d TX=GP%d @ %d baud\n",
                UART2_RX, UART2_TX, CRSF_BAUD);

  // Hardware watchdog
  esp_task_wdt_init(WDT_TIMEOUT_S, true);
  Serial.printf("watchdog: %ds, panic-on-timeout\n", WDT_TIMEOUT_S);

  // Pin the byte pump to core 1 at the highest priority. Core 0 is
  // reserved for upcoming Phase 1+ tasks (CRSF parser, tracker math,
  // servo control); they must never starve the pump.
  BaseType_t rc = xTaskCreatePinnedToCore(
      byte_pump_task,
      "byte_pump",
      4096,                       // stack bytes
      nullptr,                    // params
      configMAX_PRIORITIES - 1,   // highest priority
      nullptr,                    // task handle (unused)
      1);                         // pin to core 1
  if (rc != pdPASS) {
    Serial.println("FATAL: failed to start byte_pump task");
    // Fall through; loop() will keep running on core 0 and emit
    // heartbeats, but no CRSF traffic will flow. Useful diagnostic
    // signal vs. silent total failure.
  } else {
    Serial.println("byte_pump task running on core 1");
  }

  Serial.println("ready");
  Serial.println();
}

// =====================================================================
// byte_pump_task: forward UART1 <-> UART2 transparently
// =====================================================================
//
// Pinned to core 1 at highest priority. Single responsibility: pump
// bytes. NEVER add CRSF parsing, math, or servo logic in this loop;
// any code added here risks degrading worst-case forwarding latency
// on the safety-critical RC control path.
//
// In Phase 1 the parser will tee from the byte pump via a non-blocking
// stream FIFO that this task writes into. The pump remains unchanged;
// the FIFO write is bounded and never blocks.
void byte_pump_task(void* pvParameters) {
  esp_task_wdt_add(nullptr);  // register THIS task with the watchdog

  uint8_t buf[64];

  for (;;) {
    // Cable -> ELRS (downstream: daemon control commands, RC channels)
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
    }

    esp_task_wdt_reset();

    // Yield one tick (1ms at the default 1kHz tick rate). This caps
    // the worst-case latency added by the pump at ~1ms above the
    // UART hardware buffering itself, well below the 20ms CRSF frame
    // cadence. For comparison, one byte at 420kbaud takes ~24us.
    vTaskDelay(1);
  }
}

// =====================================================================
// loop(): heartbeat on core 0
// =====================================================================
//
// Arduino's loop() runs on core 0 by default. We use it for a periodic
// status line over USB-CDC; substantial work belongs in its own
// pinned task once Phase 1 lands.
void loop() {
  static uint32_t last_log_ms = 0;
  uint32_t now = millis();
  if (now - last_log_ms >= 5000) {
    last_log_ms = now;
    Serial.printf("heartbeat uptime=%lus\n", (unsigned long)(now / 1000));
  }
  delay(100);
}
