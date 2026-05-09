# Bootstrap (minimal)

Provisioning the Pi 400 for ZeroTX with the smallest practical install: Pi OS Lite, X with no window manager, two Chromium kiosks. Goal is a clean, fast boot to two operational kiosks (HUD + Map) with no desktop, no greeter, no extras the daemon doesn't need.

The companion document `BOOTSTRAP.md` covers the full Pi OS Bookworm desktop path. They share the hardware overlay sections (RTC, GPS UART, audio); cross-references below point at the canonical step in `BOOTSTRAP.md` rather than duplicating.

Audience is a re-flash from a brick: lost SSD, fresh start, second unit.

## What you need

- Pi 400 (with two micro-HDMI to HDMI cables)
- USB SSD
- Two displays (HUD + Map)
- USB keyboard + mouse for setup only (the running system is headless after this)
- A second machine with `rpi-imager`
- Network for first boot; field operation does not require it

## Image

Use `rpi-imager` on the provisioning machine. Choose:

- Device: Raspberry Pi 400
- OS: **Raspberry Pi OS Lite (64-bit)**, Bookworm or current stable. Not the desktop image.
- Storage: the USB SSD

Open advanced options. Set:

- Hostname: `zerotx`
- SSH: enabled, public key auth (paste your key)
- Username: `zerotx` (the rest of this doc assumes that)
- Password: any
- Wi-Fi credentials: optional. Set them only if you want network for setup. Field operation runs without.
- Locale, keyboard layout, timezone

Write, eject, plug into the Pi.

## Boot order

Same as the full-desktop path. See `BOOTSTRAP.md` § "Pi 400 boot order". One change: the EEPROM tweak `BOOT_ORDER=0xf14` skips the SD probe and shaves a couple of seconds. Apply it on the minimal install too.

## First boot

```
sudo apt update
sudo apt -y full-upgrade
sudo timedatectl set-timezone America/Sao_Paulo   # or your zone
sudo hostnamectl set-hostname zerotx
sudo reboot
```

Confirm SSD is the root filesystem:

```
findmnt /
```

Should report a USB-attached `nvme0n1pX` or `sdaX`, not `mmcblk0pX`.

## Networking: non-blocking

Field use means no Wi-Fi available. Without changes, `multi-user.target` waits up to 90s for `*-wait-online.service` to give up. Disable both:

```
sudo systemctl disable --now NetworkManager-wait-online.service
sudo systemctl mask NetworkManager-wait-online.service
sudo systemctl disable --now systemd-networkd-wait-online.service
sudo systemctl mask systemd-networkd-wait-online.service
```

The daemon's systemd unit (below) uses `Wants=network.target` rather than `network-online.target`, so it does not block on this either.

## Disable unused services

Pi OS Lite ships fewer services than the desktop image, but a few common ones still load:

```
for svc in bluetooth.service hciuart.service \
           triggerhappy.service \
           ModemManager.service \
           avahi-daemon.service avahi-daemon.socket \
           cups.service cups-browsed.service ; do
    sudo systemctl disable --now "$svc" 2>/dev/null || true
done
```

The `2>/dev/null || true` swallows "service not found" for the ones that aren't installed on Lite — the script is idempotent across image variants.

If you actually use Bluetooth on this Pi, drop the bluetooth/hciuart pair from the list.

## Install minimal kiosk packages

The full list:

```
sudo apt -y install \
    xserver-xorg-core xserver-xorg-input-libinput \
    xserver-xorg-video-fbdev xserver-xorg-video-vc4 \
    xinit x11-xserver-utils \
    unclutter \
    chromium-browser \
    alsa-utils \
    curl ca-certificates
```

What that pulls in:

- `xserver-xorg-core`, `xinit` — the X server and `startx`
- `xserver-xorg-input-libinput` — input driver (needed even if no keyboard at runtime)
- `xserver-xorg-video-fbdev`, `xserver-xorg-video-vc4` — video drivers; the right one is auto-selected at start
- `x11-xserver-utils` — provides `xset` and `xrandr`
- `unclutter` — hides the cursor when idle
- `chromium-browser` — the only kiosk renderer
- `alsa-utils` — `aplay`, `amixer` for the audio path the daemon uses
- `curl`, `ca-certificates` — health-check probe in `.xinitrc`, plus ca-certificates for any HTTPS the daemon does

Notably absent: no Wayfire, no labwc, no LXDE, no greetd, no plymouth, no display manager. X starts from the user's shell on tty1 and Chromium does its own window management.

## Auto-login on tty1

Drop a getty override:

```
sudo systemctl edit getty@tty1
```

Editor opens. Add:

```
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin zerotx --noclear %I $TERM
```

Save and exit. The empty `ExecStart=` line is required: it clears the inherited value, then the second one replaces it.

Reload:

```
sudo systemctl daemon-reload
sudo systemctl restart getty@tty1
```

Reboot to verify login lands on tty1 as `zerotx` without prompting.

## Start X on login

In `~/.bash_profile` (create if it doesn't exist):

```
if [ -z "$DISPLAY" ] && [ "$(tty)" = "/dev/tty1" ]; then
    exec startx
fi
```

This runs `startx` only on tty1, only when no X session is already running. Other ttys (e.g. SSH) get a normal shell.

Make sure `~/.bashrc` is sourced from `.bash_profile` if you're used to PATH/aliases there:

```
[ -f ~/.bashrc ] && . ~/.bashrc
```

(Add at the top of `.bash_profile` before the `startx` block.)

## .xinitrc: the kiosk launcher

`startx` runs `~/.xinitrc`. This file is the entire X session: no window manager, just disable screensaver, position the displays, wait for the daemon, launch two Chromium kiosks.

```
#!/bin/sh
# Disable screensaver, DPMS, and screen blanking. Without these the
# kiosks would dim themselves after a few minutes of no input.
xset s off
xset -dpms
xset s noblank

# Hide the mouse cursor after 1s idle. Even though there's no mouse
# at runtime, X draws a cursor at startup and it sits there.
unclutter -idle 1 -root &

# Position the two displays side by side. The output names depend on
# how the kernel labels the Pi 400's two micro-HDMI ports. Run
# `xrandr` once to see what your hardware reports (typical: HDMI-1
# and HDMI-2, or HDMI-A-1 and HDMI-A-2). Adjust the names below.
# Mode is auto (whatever the displays advertise as preferred).
xrandr --output HDMI-1 --auto --pos 0x0 \
       --output HDMI-2 --auto --right-of HDMI-1

# Wait for the daemon's HTTP server. zerotxd.service starts in
# parallel with the X session, so the kiosks would otherwise fail
# to load on the first try and depend on Chromium's own retry. A
# couple of curl probes is faster and quieter.
until curl -s -o /dev/null http://127.0.0.1:8080/api/v1/health ; do
    sleep 0.5
done

# Common Chromium flags shared by both kiosks.
common_flags="--kiosk --noerrdialogs --disable-infobars \
    --disable-translate --no-first-run --no-default-browser-check \
    --disable-features=TranslateUI \
    --disable-component-extensions-with-background-pages \
    --disable-background-networking \
    --disable-renderer-backgrounding \
    --disable-extensions \
    --disk-cache-size=33554432"

# HUD on the left display. Each kiosk needs its own user-data-dir;
# Chromium locks the profile and refuses to launch a second instance
# against the same one. /tmp is tmpfs so the dirs vanish on reboot,
# which is what we want for a stateless kiosk.
chromium-browser $common_flags \
    --user-data-dir=/tmp/chromium-hud \
    --window-position=0,0 --window-size=1920,1080 \
    http://127.0.0.1:8080/hud/ &

# Map on the right display. Adjust the X offset to match the left
# display's width.
chromium-browser $common_flags \
    --user-data-dir=/tmp/chromium-map \
    --window-position=1920,0 --window-size=1920,1080 \
    http://127.0.0.1:8080/map/ &

# Keep the X session alive as long as either kiosk is running. If
# both exit, X exits and the user gets dropped back to the shell on
# tty1 — at which point .bash_profile re-launches startx.
wait
```

Make it executable:

```
chmod +x ~/.xinitrc
```

If your displays land in different positions or different resolutions, run `xrandr` from an SSH session into the running Pi and edit the `xrandr` and `--window-position`/`--window-size` lines accordingly.

## Daemon binary

Copy a pre-built `zerotxd` binary to `/usr/local/bin/zerotxd` and make it executable. Building on the Pi works but adds Go toolchain to the install footprint; cross-compile from a workstation when possible:

```
# On a workstation, in the repo:
GOOS=linux GOARCH=arm64 go build -o /tmp/zerotxd ./pi/daemon/cmd/zerotxd
scp /tmp/zerotxd zerotx@<pi-host>:/tmp/
# On the Pi:
sudo install -m 0755 /tmp/zerotxd /usr/local/bin/zerotxd
```

Static asset directory (web UI, sounds, recordings) lives under `/home/zerotx/zerotx/`:

```
zerotx@cartman:~$ ls zerotx/
recordings  sounds  web  ...
```

Adjust paths in the systemd unit below if you put them elsewhere.

## Daemon systemd unit

`/etc/systemd/system/zerotxd.service`:

```
[Unit]
Description=ZeroTX daemon
Wants=network.target
After=network.target

[Service]
Type=simple
User=zerotx
WorkingDirectory=/home/zerotx
ExecStart=/usr/local/bin/zerotxd \
    -port /dev/ttyACM0 \
    -iohub-port /dev/ttyACM1 \
    -web-dir /home/zerotx/zerotx/pi/daemon/web \
    -recordings-dir /home/zerotx/zerotx/recordings \
    -sounds-dir /home/zerotx/zerotx/sounds \
    -piper-binary /usr/local/bin/piper \
    -model /home/zerotx/zerotx/configs/big_talon_zerotx.yml \
    -site-lat -22.91 -site-lon -47.06 \
    -gps-port /dev/ttyAMA1 \
    -heartbeat-gpio 17

Restart=on-failure
RestartSec=5

# Resource hygiene: the daemon doesn't need elevated privileges and
# shouldn't touch root-owned files. ProtectSystem= and friends guard
# against accidents in development; remove if they conflict with a
# specific feature you add later.
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/home/zerotx/zerotx/recordings /tmp /run /var/run
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

`Wants=network.target` (not `network-online.target`) is the key non-blocking line. The daemon doesn't need internet to start; weather and tile fetches degrade gracefully when offline.

Enable:

```
sudo systemctl daemon-reload
sudo systemctl enable --now zerotxd.service
```

Verify:

```
systemctl status zerotxd.service
journalctl -u zerotxd -f
```

## Hardware overlays

These are unchanged from the full-desktop path. See `BOOTSTRAP.md` for the detailed steps; summary of the edits to `/boot/firmware/config.txt`:

- `dtoverlay=i2c-rtc,ds3231` — DS3231 RTC on header pins 1/3/5/6
- `dtoverlay=uart3` — Pi-attached GPS on header pins 7/29
- `gpu_mem=128` — 128MB to the GPU; helps with two Chromium kiosks
- `dtparam=audio=on` — onboard audio (or `dtoverlay=hifiberry-dac` etc. for an external DAC)

After editing config.txt, reboot. Verify each:

```
ls /sys/class/rtc/                    # rtc0 should exist
ls /dev/ttyAMA*                       # ttyAMA1 for GPS
vcgencmd get_mem gpu                  # 128M
aplay -l                              # audio devices listed
```

## Boot speed

Measure the baseline:

```
systemd-analyze
systemd-analyze blame | head -20
systemd-analyze critical-chain
```

Typical Pi 400 + Pi OS Lite + this setup: 18–25 seconds from kernel start to `multi-user.target`, plus 3–5 seconds for X + Chromium to reach the kiosk pages. Total cold boot to operational kiosks: ~25–30 seconds.

If `systemd-analyze blame` flags a service taking >5s and you don't need it, mask it. Common culprits on Lite:

- `apt-daily.service`, `apt-daily-upgrade.service` — disable if the Pi is rarely online
- `man-db.service` — slow first-run, mask
- `e2scrub_all.service` — mask
- `dpkg-db-backup.service` — mask

Mask with `sudo systemctl mask <name>`. Re-run `systemd-analyze blame` to confirm the bottleneck moved.

## Optional: tmpfs for noisy directories

Trade durability for write reduction (Pi 400 with USB SSD doesn't really need this; included for parity with `BOOTSTRAP.md`).

`/etc/fstab`:

```
tmpfs /tmp                tmpfs defaults,noatime,size=512M       0 0
tmpfs /var/log            tmpfs defaults,noatime,size=64M        0 0
```

Note: `journalctl` history is lost on reboot if `/var/log` is tmpfs. ZeroTX's own log buffer (the `/api/v1/logs` endpoint) doesn't depend on disk-persisted journals, so this is fine.

## Verification checklist

After provisioning, cold-boot the Pi and confirm:

1. **SSD boot**: `findmnt /` reports a USB-attached drive (not `mmcblk0`).
2. **Auto-login**: tty1 lands on the `zerotx` shell without a prompt.
3. **X starts**: both displays show the kiosks; no desktop, no taskbar, no cursor visible.
4. **HUD kiosk**: the left display shows the live HUD with telemetry placeholders or real data.
5. **Map kiosk**: the right display shows the map centered on the configured site (or the operator's GPS lock if station GPS is configured and locked).
6. **Daemon healthy**: `curl http://127.0.0.1:8080/api/v1/health` returns 200 with the expected version.
7. **No `network-online` waits**: `systemd-analyze blame` shows no `*-wait-online.service` entries.
8. **Boot time**: `systemd-analyze` reports `multi-user.target` reached in under ~25s. The kiosks should be visible within ~5s after that.
9. **Audio**: `speaker-test -t sine -f 440 -l 1` plays through the configured output. The daemon's TTS works.
10. **No mouse cursor stuck**: cursor disappears within 1s of any incidental movement (USB hot-plug, etc.).

## Differences from `BOOTSTRAP.md`

What this minimal path drops:

- No desktop environment (no LXDE, no Wayfire, no labwc, no Pi-side window manager).
- No display manager / greeter (no lightdm, no greetd).
- No file manager, browser bookmarks, office suite, etc. — Pi OS Lite has none of these.
- No first-run wizard.
- No Bluetooth / cups by default.

What it adds:

- `~/.xinitrc` is the entire session and kiosk launcher. The original BOOTSTRAP used systemd user units gated on `graphical-session.target`, which assumes a display manager.
- `~/.bash_profile` triggers `startx` on tty1 login. No greeter intermediate.
- `network-online.target` waits are masked, not just bypassed.

What stays the same:

- Hardware overlays (RTC, GPS UART, audio, GPIO heartbeat).
- Daemon binary path, systemd unit (with the `Wants=network.target` switch).
- Audio stack (ALSA + Piper).
- Boot order EEPROM tweak.
- Verification checklist.

Pick which doc is canonical for your fleet. If you ever want a "lab" Pi with a desktop for development, follow `BOOTSTRAP.md` instead — same hardware, same daemon, different host environment.
