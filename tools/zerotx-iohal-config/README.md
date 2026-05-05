# zerotx-iohal-config

Configures the Mega 2560 IO board's HAL pin map and per-pin flags
from a JSON file. Talks directly to the Mega over USB-CDC. Does not
go through the daemon.

## Why

The Mega's pin assignments and polarity flags live in EEPROM and can
be reconfigured at runtime via the `SET hal pin/flag` protocol
commands. This tool automates that: read a JSON file, diff against
the Mega's current state, push changes, reboot.

## Build

```
cd tools/zerotx-iohal-config
go mod tidy   # first time only
go build
```

## Usage

```
# Capture the Mega's current map as JSON (good first step):
./zerotx-iohal-config -port /dev/ttyACM0 -export > ~/.config/zerotx/iohal.json

# Edit the JSON to suit your wiring, then preview the diff:
./zerotx-iohal-config -port /dev/ttyACM0 -config ~/.config/zerotx/iohal.json

# Push the config (reboots the Mega and verifies):
./zerotx-iohal-config -port /dev/ttyACM0 -config ~/.config/zerotx/iohal.json -apply

# Same but stage in EEPROM without auto-reboot (apply at next manual reboot):
./zerotx-iohal-config -port /dev/ttyACM0 -config ~/.config/zerotx/iohal.json -apply -no-reboot
```

## Flags

```
-port <path>          serial device of the Mega (required)
-config <path>        JSON config (required for -apply)
-show                 (default) show current map; with -config, show diff
-export               print current map as JSON to stdout
-apply                push config to Mega; reboot unless -no-reboot
-no-reboot            stage in EEPROM but skip reboot
-read-timeout <dur>   max wait per response (default 3s)
-boot-delay <dur>     post-reboot wait before re-querying (default 4s)
```

## JSON schema

```json
{
  "pins": {
    "<pin_name>": {
      "pin": <integer 2-69>,
      "active_low": <bool, optional, default false>
    }
  }
}
```

`pin_name` must match a HAL pin identifier known to the firmware.
`GET hal map` lists them all; `-export` does the same in JSON form.

`active_low` is the only flag currently exposed. When true, the
firmware inverts polarity for that pin's output: LOW = active.
Default project-wide is active-high (HIGH = active = energized).
Set this only for boards that wire their input through an inverting
transistor stage.

See `configs/iohal.example.json` for a full template.

## Recovery

If a bad config breaks things, the Mega's USB Serial0 (pins 0/1) is
hardcoded and cannot be remapped. So this tool always works:

```
# Reset to compiled defaults:
echo "SET hal reset-defaults" > /dev/ttyACM0
echo "SET hal reboot" > /dev/ttyACM0
```

Or run the tool again with a corrected config.
