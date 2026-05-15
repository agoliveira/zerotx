# Changelog

This file tracks notable changes going forward. Earlier history lives
in the git log; the bulk of pre-public work was tracked there rather
than here. Append new entries at the top as they land.

## Unreleased

### Repo layout: binaries consolidated

Compiled outputs and downloaded third-party tools now live in two
clearly-named top-level directories instead of being scattered across
four locations.

**Before:**

```
pi/daemon/bin/zerotxd                    Go daemon
tools/<name>/<name>                      Go tools (inline builds)
firmware/crsf/build/*.uf2                firmware artifacts
bin/piper/                               downloaded Piper TTS
voices/                                  downloaded ONNX models
```

**After:**

```
/bin/                ZeroTX's compiled outputs (daemon, tools,
                     firmware .uf2 + .elf)
/third_party/        downloaded tools and data (piper/, voices/)
```

Migration for existing deployments (one-time):

```sh
cd ~/zerotx
mv bin    third_party/piper
mv voices third_party/voices
make clean && make
sudo systemctl daemon-reload && sudo systemctl restart zerotxd
```

The systemd unit's `ExecStart` and `PIPER_BINARY` env paths changed;
`daemon-reload` picks up the new unit. The daemon's `-voices-dir`
flag default now points to `$HOME/zerotx/third_party/voices`, matching
the migration target. The `make tools` target is new; `make` (no args)
now builds daemon + tools + firmware.

