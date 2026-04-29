# EdgeTX YAML format notes

This file captures EdgeTX YAML quirks the daemon's parsers need to handle.
Discovered while building the source resolver and validating against
real-world models. Keep up to date as new quirks turn up.

## Function name reference

Logical switch `func` field in EdgeTX YAML uses these enum values:

| YAML value             | Common name | Parameters       | Notes                             |
|------------------------|-------------|------------------|-----------------------------------|
| FUNC_NONE              | --          | -                |                                   |
| FUNC_VEQUAL            | a == x      | source, constant |                                   |
| FUNC_VALMOSTEQUAL      | a ~ x       | source, constant | tolerance match                   |
| FUNC_VPOS              | a > x       | source, constant |                                   |
| FUNC_VNEG              | a < x       | source, constant |                                   |
| FUNC_APOS              | \|a\| > x   | source, constant |                                   |
| FUNC_ANEG              | \|a\| < x   | source, constant |                                   |
| FUNC_AND               | AND         | source, source   | both true                         |
| FUNC_OR                | OR          | source, source   | either true                       |
| FUNC_XOR               | XOR         | source, source   | exactly one true                  |
| FUNC_EDGE              | EDGE        | source,T1,T2     | held T1..T2 then released         |
| FUNC_GREATER           | a > b       | source, source   |                                   |
| FUNC_LESS              | a < b       | source, source   |                                   |
| FUNC_TIMER             | TIMER       | onSec, offSec    | repeating                         |
| FUNC_STICKY            | STICKY      | source, source   | V1 sets, V2 resets (both edges)   |
| FUNC_DIFFEGREATER      | dx >= n     | source, constant | delta from previous tick          |
| FUNC_ADIFFEGREATER     | \|dx\| >= n | source, constant | absolute delta                    |

Notes:
- The two delta functions are NOT named DPOS/DAPOS; the YAML uses the
  cumbersome names DIFFEGREATER and ADIFFEGREATER. (Double-E is intentional.)

## EDGE T2 special characters

In the manual, EDGE's T2 (max active duration) accepts special values
displayed as `--` (no max) or `<<` (fire as soon as T1 reached without
waiting for source deactivation). In YAML, these are encoded as single
characters:

- `<` (single) means "fire immediately at T1"  (manual: `<<`)
- `-` (single) means "no upper bound"           (manual: `--`)

Examples from x-tudo.yml:

```yaml
def: SH2,50,200    # both T1 and T2 numeric
def: SH2,100,<     # fire at T1, no wait for deactivation
def: SH2,50,-      # no upper bound on hold time
```

## Time units

In the YAML:

- `delay` and `duration` fields on logical switches: integers in
  **0.1-second units**. `delay: 10` means 1.0 second.
- `T1` and `T2` inside EDGE's `def` string: same convention.
  `def: SH2,50,200` is "held 5.0 to 20.0 seconds".

(The EdgeTX manual displays these in seconds; Companion translates
during edit; the YAML stores the raw integer count.)

## Telemetry sensor labels

Labels are limited to **4 characters** in EdgeTX. The companion truncates
when you type longer names. So a sensor logically called "RxBatt" stores as:

```yaml
telemetrySensors:
  0:
    label: RxBa     # truncated
```

The resolver must match the truncated label, not the full name. In real
flight controller telemetry, the sensor source emits short codes (e.g.
"RxBt" from Crossfire) which Companion may abbreviate further.

## Source name conventions in operands

Names that show up in `def`, `andsw`, and CF `swtch` fields:

| Form          | Meaning                                   |
|---------------|-------------------------------------------|
| `Thr`, `Ail`  | stick alias (resolves via inputNames)     |
| `I0`..`IN`    | input by index                            |
| `SA`..`SH`    | bare switch (value source)                |
| `SA0`..`SH2`  | switch position match (boolean)           |
| `6POS`        | bare selector                             |
| `6P00`..`6P15`| selector position match                   |
| `L1`..`L64`   | logic switch state                        |
| `!L3`, `!SF2` | negation (boolean only, parses for value) |
| `FM0`..`FM8`  | flight mode                               |
| `MAX`         | always +1 / true                          |
| `NONE`, `--`  | "no source"                               |
| `P1`..`P3`    | radio pot (deferred on ZeroTX)            |
| `SL1`..`SL2`  | radio slider (deferred)                   |
| `TrmH/V/A/E`  | trim (deferred)                           |
| `GV1`..`GV9`  | global variable (deferred)                |
| numeric       | percent constant; `-99` means -0.99       |

## Custom function `def` field quirks

PLAY_TRACK and PLAY_SOUND defs include null-padded filename strings:

```yaml
def: "armed\x00\x00\x00,1,1x"
```

Trim trailing nulls when parsing. The format after trimming is comma-
separated:

| Function          | Def fields                          |
|-------------------|-------------------------------------|
| OVERRIDE_CHANNEL  | channel, value, enabled             |
| PLAY_TRACK        | filename, repeat-id, repeat-pattern |
| PLAY_SOUND        | sound-name, volume, count           |
| RESET             | target, mode                        |
| INSTANT_TRIM      | mode                                |

Channel index in OVERRIDE_CHANNEL is **0-indexed**, matching mixData's
destCh convention. So `def: 0,-100,1` overrides CH0 (the first channel,
which in Big Talon is throttle) to -100% when the trigger switch is true.

## Mix sources I deferred

Real EdgeTX models commonly use:

- `swtch:` per mix-entry conditional (only apply this line if switch true)
- `mltpx: REPL` to replace previous accumulators for same destCh
- `mltpx: MULTIPLY` for multiplicative mixing

ZeroTX phase 2 doesn't implement these. The current mapper sums all mix
entries for a destCh with `mltpx: ADD`. Phase M3 work to land full mixer
math.
