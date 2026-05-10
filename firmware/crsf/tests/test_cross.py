#!/usr/bin/env python3
"""
Cross-validate Python and C IPC encoders.

Runs ./test_ipc, captures hex vectors, builds the same vectors with
the bench tool's encoder, and compares byte-for-byte.

Build the C side first:
    gcc -std=c11 -Wall -Wextra -I../src -o test_ipc test_ipc.c ../src/ipc.c
"""
from __future__ import annotations
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent / "tools"))
from m1_bench import build_frame  # noqa: E402


def parse_hex_vectors(text: str) -> dict[tuple[int, int], bytes]:
    """Each line: 'vec type=XX seq=XX HEXBYTES' -> {(type,seq): bytes}"""
    out = {}
    for line in text.strip().splitlines():
        if not line.startswith("vec "):
            continue
        parts = line.split()
        # parts: ['vec', 'type=03', 'seq=2a', 'HEXSTRING']
        t = int(parts[1].split("=")[1], 16)
        s = int(parts[2].split("=")[1], 16)
        raw = bytes.fromhex(parts[3])
        out[(t, s)] = raw
    return out


def main() -> int:
    binary = HERE / "test_ipc"
    if not binary.exists():
        print(f"build first: cd {HERE} && gcc -std=c11 -Wall -Wextra -I../src "
              f"-o test_ipc test_ipc.c ../src/ipc.c", file=sys.stderr)
        return 2
    proc = subprocess.run([str(binary)], capture_output=True, text=True, check=True)
    c_vectors = parse_hex_vectors(proc.stdout)

    # Mirror payloads from test_ipc.c
    intent_centered = b"".join(int(992).to_bytes(2, "little") for _ in range(16))
    intent_zeros = b"\x00" * 32
    cases = [
        (0x03, 42,  b"\x42",                    "heartbeat"),
        (0x01, 0,   intent_centered,            "intent centered"),
        (0x01, 7,   intent_zeros,               "intent all-zeros"),
        (0x14, 1,   b"hello, zerotx",           "log msg"),
    ]
    failed = 0
    for msg_type, seq, payload, label in cases:
        py = build_frame(msg_type, seq, payload)
        c = c_vectors.get((msg_type, seq))
        if c is None:
            print(f"MISS {label}: no C vector for type={msg_type:02x} seq={seq}")
            failed += 1
            continue
        if py == c:
            print(f"OK   {label}: {len(py)} bytes match")
        else:
            print(f"FAIL {label}:")
            print(f"  py: {py.hex()}")
            print(f"  c : {c.hex()}")
            failed += 1
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
