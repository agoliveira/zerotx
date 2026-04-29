/*
 * ZeroTX RP2040 firmware - M1 safety floor.
 *
 * Reads channel intent from Pi over USB-CDC (COBS-framed), emits CRSF
 * RC_CHANNELS_PACKED at 50Hz on UART0 to the ELRS module, and falls
 * through HOLD -> FAILSAFE if the Pi heartbeat goes silent.
 */

#include <stdio.h>
#include <string.h>

#include "pico/stdlib.h"
#include "tusb.h"

#include "protocol.h"
#include "ipc.h"
#include "crsf.h"
#include "state.h"
#include "status_led.h"

#define CRSF_BAUD       400000u

static uint8_t s_tx_hb_seq = 0;

static void usb_write_byte(uint8_t b) {
    putchar_raw((char)b);
}

static void handle_frame(const ipc_frame_t *f, uint64_t now_us) {
    switch (f->type) {
    case MSG_HEARTBEAT:
        state_note_heartbeat(now_us);
        break;
    case MSG_CHANNEL_INTENT: {
        if (f->len != ZTX_CHANNELS * 2u) {
            ipc_log("bad CHANNEL_INTENT len=%u", (unsigned)f->len);
            break;
        }
        uint16_t ch[ZTX_CHANNELS];
        for (int i = 0; i < ZTX_CHANNELS; i++) {
            ch[i] = (uint16_t)f->payload[i * 2]
                  | ((uint16_t)f->payload[i * 2 + 1] << 8);
        }
        state_set_channels(ch);
        /* Channel intent is also implicit liveness evidence. */
        state_note_heartbeat(now_us);
        break;
    }
    default:
        ipc_log("unknown msg type 0x%02X", f->type);
        break;
    }
}

int main(void) {
    stdio_init_all();          /* enables USB CDC if pico_enable_stdio_usb=1 */
    setvbuf(stdout, NULL, _IONBF, 0);

    crsf_init(CRSF_BAUD);
    status_led_init();
    ipc_init(usb_write_byte);
    state_init();

    uint64_t last_crsf_us = 0;
    uint64_t last_tx_hb_us = 0;
    uint64_t last_led_us = 0;
    bool announced = false;

    while (true) {
        uint64_t now = time_us_64();

        /* USB enumeration -> exit BOOT -> PENDING. */
        if (stdio_usb_connected()) {
            state_note_usb_ready(now);
            if (!announced) {
                ipc_log("zerotx-fw m1 ready, crsf %lu baud", (unsigned long)CRSF_BAUD);
                announced = true;
            }
        }

        /* Drain any available input bytes into the IPC parser. */
        for (int safety = 0; safety < 256; safety++) {
            int c = getchar_timeout_us(0);
            if (c == PICO_ERROR_TIMEOUT) break;
            ipc_frame_t f;
            if (ipc_feed((uint8_t)c, &f)) {
                handle_frame(&f, now);
            }
        }

        state_tick(now);

        /* CRSF emission at 50 Hz, but only if state permits. */
        if (state_should_emit_crsf()
            && (now - last_crsf_us) >= (uint64_t)ZTX_CRSF_PERIOD_MS * 1000u) {
            uint16_t channels[ZTX_CHANNELS];
            state_get_channels(channels);
            uint8_t frame[CRSF_FRAME_RC_LEN];
            crsf_pack_rc(channels, frame);
            crsf_send(frame, CRSF_FRAME_RC_LEN);
            last_crsf_us = now;
        }

        /* MCU -> Pi heartbeat. */
        if ((now - last_tx_hb_us) >= (uint64_t)ZTX_HEARTBEAT_TX_PERIOD_MS * 1000u) {
            uint8_t seq = s_tx_hb_seq++;
            ipc_send(MSG_HEARTBEAT, seq, &seq, 1);
            last_tx_hb_us = now;
        }

        /* LED refresh at 50 Hz is plenty smooth for these patterns. */
        if ((now - last_led_us) >= 20000u) {
            status_led_update(state_current(), now);
            last_led_us = now;
        }

        /* Friendly tight-loop yield. tud_task() runs inside getchar/putchar paths. */
        sleep_us(100);
    }

    return 0;
}
