#!/usr/bin/env python3
"""
ZeroTX M1 bench tool.

Connects to the RP2040 firmware over USB-CDC, sends heartbeats and channel
intent, and prints LOG messages and link-state heartbeats coming back.

Usage:
    ./m1_bench.py [--port /dev/ttyACM0]

Commands at the prompt:
    set <ch> <val>      set channel ch (1..16) to raw value (172..1811)
    arm                 arm channel (default: CH5) high
    disarm              arm channel low
    safe                all sticks centered, throttle low, arm low
    throttle <val>      throttle channel (default: CH3) to raw value
    sweep <ch>          fire-and-forget sweep min->max on channel ch
    pause               stop sending CHANNEL_INTENT (heartbeat continues)
    resume              start sending CHANNEL_INTENT again
    state               print last seen MCU log lines + counters
    quit                exit
"""

from __future__ import annotations

import argparse
import glob
import struct
import sys
import threading
import time
from dataclasses import dataclass, field
from typing import Callable, Optional

try:
    import serial
except ImportError:
    print("Missing 'pyserial'. Install with: pip3 install pyserial", file=sys.stderr)
    sys.exit(1)


# ---------- Protocol constants (must match src/protocol.h) ----------

MSG_CHANNEL_INTENT = 0x01
MSG_INPUT_STATE    = 0x02
MSG_HEARTBEAT      = 0x03
MSG_LOG            = 0x14

CHANNELS = 16
CRSF_CH_MIN = 172
CRSF_CH_MID = 992
CRSF_CH_MAX = 1811

HB_PERIOD_S      = 0.100    # 10 Hz, well under firmware's 200ms threshold
INTENT_PERIOD_S  = 0.020    # 50 Hz

THROTTLE_CH = 3   # 1-indexed
ARM_CH      = 5


# ---------- COBS + CRC + framing ----------

def crc16_ccitt_false(data: bytes) -> int:
    crc = 0xFFFF
    for b in data:
        crc ^= b << 8
        for _ in range(8):
            crc = ((crc << 1) ^ 0x1021) & 0xFFFF if (crc & 0x8000) else (crc << 1) & 0xFFFF
    return crc


def cobs_encode(data: bytes) -> bytes:
    out = bytearray([0])
    code_idx = 0
    code = 1
    for byte in data:
        if byte == 0:
            out[code_idx] = code
            code = 1
            code_idx = len(out)
            out.append(0)
        else:
            out.append(byte)
            code += 1
            if code == 0xFF:
                out[code_idx] = code
                code = 1
                code_idx = len(out)
                out.append(0)
    out[code_idx] = code
    return bytes(out)


def cobs_decode(data: bytes) -> Optional[bytes]:
    if not data:
        return None
    out = bytearray()
    i = 0
    while i < len(data):
        code = data[i]
        if code == 0:
            return None
        copy = code - 1
        i += 1
        if i + copy > len(data):
            return None
        out.extend(data[i:i + copy])
        i += copy
        if code != 0xFF and i < len(data):
            out.append(0)
    return bytes(out)


def build_frame(msg_type: int, seq: int, payload: bytes) -> bytes:
    if len(payload) > 256:
        raise ValueError("payload too large")
    inner = bytes([msg_type, seq, len(payload) & 0xFF, (len(payload) >> 8) & 0xFF]) + payload
    crc = crc16_ccitt_false(inner)
    inner += bytes([crc & 0xFF, (crc >> 8) & 0xFF])
    return cobs_encode(inner) + b"\x00"


def parse_frame(decoded: bytes) -> Optional[tuple[int, int, bytes]]:
    if len(decoded) < 6:
        return None
    msg_type = decoded[0]
    seq = decoded[1]
    plen = decoded[2] | (decoded[3] << 8)
    if len(decoded) != 4 + plen + 2:
        return None
    payload = decoded[4:4 + plen]
    got_crc = decoded[4 + plen] | (decoded[4 + plen + 1] << 8)
    calc = crc16_ccitt_false(decoded[:4 + plen])
    if got_crc != calc:
        return None
    return (msg_type, seq, payload)


# ---------- Bench state ----------

@dataclass
class Bench:
    port: serial.Serial
    channels: list[int] = field(default_factory=lambda: [CRSF_CH_MID] * CHANNELS)
    sending_intent: bool = True
    sending_heartbeat: bool = True
    tx_seq: int = 0
    rx_hb_count: int = 0
    rx_log_count: int = 0
    last_log: str = ""
    stop: threading.Event = field(default_factory=threading.Event)
    write_lock: threading.Lock = field(default_factory=threading.Lock)

    def __post_init__(self):
        # Safe defaults: throttle low, arm low.
        self.channels[THROTTLE_CH - 1] = CRSF_CH_MIN
        self.channels[ARM_CH - 1] = CRSF_CH_MIN

    def write_frame(self, msg_type: int, payload: bytes):
        seq = self.tx_seq
        self.tx_seq = (self.tx_seq + 1) & 0xFF
        frame = build_frame(msg_type, seq, payload)
        with self.write_lock:
            self.port.write(frame)


# ---------- Background threads ----------

def sender(b: Bench):
    last_hb = 0.0
    last_intent = 0.0
    while not b.stop.is_set():
        now = time.monotonic()
        if b.sending_heartbeat and (now - last_hb) >= HB_PERIOD_S:
            try:
                b.write_frame(MSG_HEARTBEAT, bytes([b.tx_seq & 0xFF]))
            except Exception as e:
                print(f"\n[tx error] {e}", flush=True)
                return
            last_hb = now
        if b.sending_intent and (now - last_intent) >= INTENT_PERIOD_S:
            payload = struct.pack("<16H", *b.channels)
            try:
                b.write_frame(MSG_CHANNEL_INTENT, payload)
            except Exception as e:
                print(f"\n[tx error] {e}", flush=True)
                return
            last_intent = now
        time.sleep(0.005)


def receiver(b: Bench, on_log: Callable[[str], None]):
    buf = bytearray()
    while not b.stop.is_set():
        try:
            chunk = b.port.read(256)
        except Exception as e:
            print(f"\n[rx error] {e}", flush=True)
            return
        if not chunk:
            continue
        for byte in chunk:
            if byte == 0:
                if buf:
                    decoded = cobs_decode(bytes(buf))
                    buf.clear()
                    if decoded is None:
                        continue
                    parsed = parse_frame(decoded)
                    if parsed is None:
                        continue
                    msg_type, _seq, payload = parsed
                    if msg_type == MSG_HEARTBEAT:
                        b.rx_hb_count += 1
                    elif msg_type == MSG_LOG:
                        text = payload.decode("utf-8", errors="replace")
                        b.rx_log_count += 1
                        b.last_log = text
                        on_log(text)
            else:
                buf.append(byte)


# ---------- REPL ----------

HELP = """\
commands:
  set <ch> <val>      ch 1..16, val 172..1811
  arm | disarm        toggle CH5 (configurable in code)
  throttle <val>      CH3 (configurable in code)
  safe                center all, throttle low, arm low
  sweep <ch>          fire-and-forget min->max sweep
  pause               stop heartbeat AND intent (simulates Pi crash)
  idle                stop intent only, keep heartbeat (channels held)
  resume              re-enable heartbeat and intent
  state               counters and last log line
  help                this
  quit                exit
"""


def cmd_loop(b: Bench):
    while not b.stop.is_set():
        try:
            line = input("ztx> ").strip()
        except (EOFError, KeyboardInterrupt):
            print()
            b.stop.set()
            return
        if not line:
            continue
        parts = line.split()
        cmd = parts[0].lower()
        try:
            if cmd in ("quit", "exit", "q"):
                b.stop.set()
                return
            elif cmd == "help":
                print(HELP, end="")
            elif cmd == "set" and len(parts) == 3:
                ch = int(parts[1])
                val = int(parts[2])
                if not 1 <= ch <= 16:
                    print("ch out of range")
                    continue
                val = max(CRSF_CH_MIN, min(CRSF_CH_MAX, val))
                b.channels[ch - 1] = val
            elif cmd == "arm":
                b.channels[ARM_CH - 1] = CRSF_CH_MAX
                print(f"CH{ARM_CH} -> high")
            elif cmd == "disarm":
                b.channels[ARM_CH - 1] = CRSF_CH_MIN
                print(f"CH{ARM_CH} -> low")
            elif cmd == "throttle" and len(parts) == 2:
                val = max(CRSF_CH_MIN, min(CRSF_CH_MAX, int(parts[1])))
                b.channels[THROTTLE_CH - 1] = val
            elif cmd == "safe":
                for i in range(CHANNELS):
                    b.channels[i] = CRSF_CH_MID
                b.channels[THROTTLE_CH - 1] = CRSF_CH_MIN
                b.channels[ARM_CH - 1] = CRSF_CH_MIN
                print("safe defaults applied")
            elif cmd == "sweep" and len(parts) == 2:
                ch = int(parts[1])
                if not 1 <= ch <= 16:
                    print("ch out of range")
                    continue
                threading.Thread(target=_sweep, args=(b, ch), daemon=True).start()
            elif cmd == "pause":
                b.sending_intent = False
                b.sending_heartbeat = False
                print("all tx paused. firmware should HOLD ~200ms then FAILSAFE ~600ms after that.")
            elif cmd == "idle":
                b.sending_intent = False
                b.sending_heartbeat = True
                print("intent paused, heartbeat continues. firmware stays in OK with held channels.")
            elif cmd == "resume":
                b.sending_intent = True
                b.sending_heartbeat = True
                print("tx resumed.")
            elif cmd == "state":
                print(f"  tx_seq={b.tx_seq}  rx_hb={b.rx_hb_count}  rx_log={b.rx_log_count}")
                print(f"  last log: {b.last_log!r}")
                print(f"  channels: {b.channels}")
                print(f"  hb={'on' if b.sending_heartbeat else 'off'}  intent={'on' if b.sending_intent else 'off'}")
            else:
                print("unknown. 'help' for commands.")
        except ValueError:
            print("bad arguments")


def _sweep(b: Bench, ch: int):
    steps = 60
    for i in range(steps + 1):
        if b.stop.is_set():
            return
        b.channels[ch - 1] = int(CRSF_CH_MIN + (CRSF_CH_MAX - CRSF_CH_MIN) * i / steps)
        time.sleep(0.05)
    for i in range(steps + 1):
        if b.stop.is_set():
            return
        b.channels[ch - 1] = int(CRSF_CH_MAX - (CRSF_CH_MAX - CRSF_CH_MIN) * i / steps)
        time.sleep(0.05)


# ---------- Main ----------

def autodetect_port() -> Optional[str]:
    candidates = sorted(glob.glob("/dev/ttyACM*") + glob.glob("/dev/ttyUSB*"))
    return candidates[0] if candidates else None


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--port", help="serial device, e.g. /dev/ttyACM0", default=None)
    ap.add_argument("--baud", type=int, default=115200,
                    help="baud (USB CDC ignores this but pyserial wants a value)")
    args = ap.parse_args()

    port = args.port or autodetect_port()
    if not port:
        print("no serial port found. plug RP2040 and retry, or pass --port", file=sys.stderr)
        return 2

    print(f"opening {port}...")
    try:
        ser = serial.Serial(port, args.baud, timeout=0.05)
    except Exception as e:
        print(f"failed to open {port}: {e}", file=sys.stderr)
        return 2

    b = Bench(port=ser)
    print("link up. type 'help' for commands.")

    def on_log(text: str):
        print(f"\n[mcu] {text}", flush=True)
        print("ztx> ", end="", flush=True)

    threads = [
        threading.Thread(target=sender, args=(b,), daemon=True),
        threading.Thread(target=receiver, args=(b, on_log), daemon=True),
    ]
    for t in threads:
        t.start()

    try:
        cmd_loop(b)
    finally:
        b.stop.set()
        time.sleep(0.1)
        ser.close()
        print("bye.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
