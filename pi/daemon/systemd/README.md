# Systemd integration

Run the daemon as a per-user service. Starts when the user logs in,
restarts on crash, logs to journald.

## Install

```bash
# 1. Create config directory and copy env template
mkdir -p ~/.config/zerotx
cp pi/daemon/systemd/zerotxd.env.example ~/.config/zerotx/zerotxd.env
$EDITOR ~/.config/zerotx/zerotxd.env

# 2. Install the unit file
mkdir -p ~/.config/systemd/user
cp pi/daemon/systemd/zerotxd.service ~/.config/systemd/user/

# 3. Reload, enable, start
systemctl --user daemon-reload
systemctl --user enable --now zerotxd

# 4. Tail logs to confirm it came up
journalctl --user -u zerotxd -f
```

## Daemon survives logout

By default a user service stops when you log out. To keep zerotxd
running when you switch users or close the desktop session:

```bash
loginctl enable-linger $USER
```

For a typical workstation use, this is what you want.

## Common operations

```bash
# Restart after rebuilding the daemon binary, editing the env file,
# or any other config change:
systemctl --user restart zerotxd

# Check status (running, last exit code, recent log lines):
systemctl --user status zerotxd

# Stop without disabling autostart:
systemctl --user stop zerotxd

# Disable autostart but keep the unit installed:
systemctl --user disable zerotxd

# Logs (live, last hour, since boot):
journalctl --user -u zerotxd -f
journalctl --user -u zerotxd --since "1 hour ago"
journalctl --user -u zerotxd -b
```

## Editing the unit file

If you edit `~/.config/systemd/user/zerotxd.service` directly, reload
before restart so systemd picks up the new unit:

```bash
systemctl --user daemon-reload
systemctl --user restart zerotxd
```

If you edit `pi/daemon/systemd/zerotxd.service` in the repo, repeat
the install step (cp + daemon-reload + restart).

## Group memberships

Make sure your user account can reach the hardware the daemon
opens. On Debian/Ubuntu/Raspberry Pi OS:

```bash
sudo usermod -aG dialout,input,audio $USER
# Log out and back in for the new groups to apply.
```

- `dialout` for `/dev/ttyACM*` (RP2040, ESP32, Pro Micro VFD)
- `input` for joystick `/dev/input/js*`
- `audio` for ALSA / PulseAudio playback

## Uninstall

```bash
systemctl --user disable --now zerotxd
rm ~/.config/systemd/user/zerotxd.service
systemctl --user daemon-reload
# Env file is yours to keep or remove:
#   rm ~/.config/zerotx/zerotxd.env
```

## Troubleshooting

**Service starts then immediately fails (exit code 1)**: usually a
missing flag or a wrong path in the env file. Check the journal:

```bash
journalctl --user -u zerotxd -n 50
```

**Service hits StartLimitBurst (5 crashes in 60s) and stops trying**:
fix the underlying problem, then explicitly restart:

```bash
systemctl --user reset-failed zerotxd
systemctl --user start zerotxd
```

**Joystick or serial port permission denied**: confirm group
membership took effect (`groups` should list `dialout`, `input`).
You may need to log out and back in.
