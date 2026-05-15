# ZeroTX Bootstrap

## Purpose and scope

First-time provisioning of the Raspberry Pi 400 from a brick. Bare-metal: OS image to USB SSD, base packages, Go toolchain, Piper TTS, udev rules, audio, browser kiosk, daemon systemd unit, Pi-side optimizations. Audience is me reflashing the system (lost SSD, fresh start, second unit).

For day-to-day operations after the system is up see `docs/OPERATIONS.md`. For physical wiring see `docs/CONNECTIONS.md`. For the architectural picture see `docs/ARCHITECTURE.md`.

## Hardware prerequisites

For first-boot provisioning:

- Pi 400 with built-in keyboard
- USB SSD (the boot drive; capacity TODO finalize after assembly, 256GB+ recommended)
- Monitor (HDMI; can be one of the LCDs once they're connected)
- Mouse (optional; keyboard works for setup)
- Wired Ethernet or known Wi-Fi credentials
- Another machine running `rpi-imager`

The MCU satellites (Mega, ESP32, RP2040), ELRS modules, joystick, and HUB75 panel are not required for bootstrap. Connect them after the Pi is online.

## OS image to SSD

ZeroTX boots from a USB SSD, not an SD card.

1. On the provisioning machine, install `rpi-imager`.
2. Connect the SSD via USB.
3. Launch `rpi-imager`. Choose:
   - Device: Raspberry Pi 400
   - OS: Raspberry Pi OS (64-bit), Bookworm or current stable
   - Storage: the USB SSD
4. Open advanced options (gear icon). Set:
   - Hostname: `zerotx`
   - SSH: enabled, public key auth
   - Username and password
   - Wi-Fi credentials (for first boot connectivity)
   - Locale, keyboard layout, timezone
5. Write the image. Eject when done.

## Pi 400 boot order

The Pi 400 default `BOOT_ORDER=0xf41` (SD, then USB, then loop) already falls through to USB when no SD card is inserted; ZeroTX boots from the SSD without any EEPROM change.

To skip the SD probe and shave a couple of seconds off cold boot:

```
sudo rpi-eeprom-config --edit
```

Set:

```
BOOT_ORDER=0xf14
```

Read right-to-left: 4 = USB, 1 = SD, f = restart loop. Save and reboot.

Verify:

```
vcgencmd bootloader_config | grep BOOT_ORDER
```

## First boot setup

```
sudo apt update
sudo apt -y full-upgrade
sudo reboot
```

After reboot:

```
sudo timedatectl set-timezone America/Sao_Paulo
sudo hostnamectl set-hostname zerotx
```

Confirm SSD is the root filesystem and that swap is sane:

```
df -h /
free -h
```

If swap is missing or undersized:

```
sudo dphys-swapfile swapoff
sudo sed -i 's/^CONF_SWAPSIZE=.*/CONF_SWAPSIZE=2048/' /etc/dphys-swapfile
sudo dphys-swapfile setup
sudo dphys-swapfile swapon
```

## Base packages

```
sudo apt -y install \
  build-essential \
  git \
  curl \
  wget \
  ca-certificates \
  pkg-config \
  libusb-1.0-0-dev \
  libudev-dev \
  alsa-utils \
  libasound2 \
  pulseaudio \
  pulseaudio-utils \
  chromium-browser \
  unclutter \
  xdotool \
  jq
```

PlatformIO (for ESP32 and Mega flashing from the Pi, optional but useful for field updates):

```
pip install --user --break-system-packages platformio
```

## Go toolchain

Use upstream Go, not the apt version (apt is typically a major release behind, and on some Ubuntu derivatives 'apt install golang-go' silently installs gccgo which doesn't parse modern go.mod files).

```
GO_VERSION=1.25.10
cd /tmp
curl -L -O https://go.dev/dl/go${GO_VERSION}.linux-arm64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-arm64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
sudo chmod +x /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version
```

Pinned to 1.25.10 here as a known-good. The daemon's `go.mod` carries both a `go 1.25.0` floor (minimum required) and a `toolchain go1.25.10` directive (the version the toolchain mechanism will auto-fetch if your installed Go is older). So if you install any Go >= 1.21 above, the first `go build` will transparently download 1.25.10 to satisfy the project — but installing the right version up front saves the round trip.

## Piper TTS

The Piper binary is third-party; it lives under `third_party/`, alongside the ONNX voice models that `scripts/fetch-voices.sh` will populate later.

```
mkdir -p ~/zerotx/third_party/piper
cd ~/zerotx/third_party/piper
```

Fetch the Piper release for arm64. Filename and version vary by release; check `https://github.com/rhasspy/piper/releases` and adjust:

```
PIPER_VERSION=2023.11.14-2
PIPER_TARBALL=piper_linux_aarch64.tar.gz
curl -L -O https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/${PIPER_TARBALL}
tar xzf ${PIPER_TARBALL}
rm ${PIPER_TARBALL}
```

Voice models live one directory up; `scripts/fetch-voices.sh` is the supported way to install them (it puts the `.onnx` + `.onnx.json` files under `~/zerotx/third_party/voices/`). For an ad-hoc smoke test you can also fetch one model manually:

```
mkdir -p ~/zerotx/third_party/voices
cd ~/zerotx/third_party/voices
curl -L -O https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx
curl -L -O https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx.json
```

Smoke test:

```
echo "ZeroTX online" | ~/zerotx/third_party/piper/piper \
  --model ~/zerotx/third_party/voices/en_US-amy-medium.onnx \
  --output_file /tmp/test.wav
aplay /tmp/test.wav
```

**TODO**: confirm Piper version and tarball name on next refresh.

## udev rules

The daemon launches against `/dev/serial/by-id/` paths, which are vendor-stable on their own. udev SYMLINKs are optional but ergonomic.

`/etc/udev/rules.d/99-zerotx.rules`:

```
# RP2040 CRSF generator
SUBSYSTEM=="tty", ATTRS{idVendor}=="2e8a", ATTRS{idProduct}=="000a", \
  ATTRS{serial}=="E66138935F3C4824", SYMLINK+="zerotx-rp2040"

# Mega 2560 IO board
SUBSYSTEM=="tty", ATTRS{idVendor}=="2341", ATTRS{idProduct}=="0042", \
  SYMLINK+="zerotx-mega"

# ESP32 panel driver (TODO: confirm idVendor and idProduct of the specific ESP32 board)
SUBSYSTEM=="tty", ATTRS{idVendor}=="<TODO>", ATTRS{idProduct}=="<TODO>", \
  SYMLINK+="zerotx-esp32"
```

Reload:

```
sudo udevadm control --reload-rules
sudo udevadm trigger
```

After replug or reboot, verify:

```
ls -l /dev/zerotx-*
```

## Audio configuration

List available cards:

```
aplay -l
```

Pick the device (typically `card 0` for Pi 400 audio). Set ALSA default in `~/.asoundrc`:

```
pcm.!default { type hw card 0 device 0 }
ctl.!default { type hw card 0 }
```

**TODO**: confirm card index on the final hardware. If using a USB DAC or HDMI audio, adjust accordingly.

Or via PulseAudio:

```
pactl list short sinks
pactl set-default-sink <sink_name>
```

System volume:

```
amixer -c 0 set PCM 80%
sudo alsactl store
```

## GPIO breakout: heartbeat LED and DS3231 RTC

The Pi 400 GPIO header is exposed via a passive breakout board. ZeroTX
currently uses two pins: GPIO 17 for a daemon heartbeat LED, and the
I2C1 bus (GPIO 2/3) for a DS3231 real-time clock module. Pinout
detail in `docs/hardware-pinout.md`.

### Enable I2C

```
sudo raspi-config nonint do_i2c 0
sudo apt-get install -y i2c-tools
```

Reboot or `sudo modprobe i2c-dev`. Verify:

```
ls /dev/i2c-1
i2cdetect -y 1
```

`i2cdetect` should show the bus with no devices yet (DS3231 not wired
or not powered).

### DS3231 RTC

Wire the DS3231 module: VCC to header pin 1 (3V3), GND to header pin 6,
SDA to header pin 3 (GPIO 2), SCL to header pin 5 (GPIO 3). Some
modules ship with an EEPROM at 0x57; that's harmless and unused.

After wiring, confirm detection:

```
i2cdetect -y 1
```

Address `0x68` should show. Add the kernel overlay so the RTC is
exposed as a hardware clock device:

```
sudo sed -i '/^dtparam=i2c_arm=on/a dtoverlay=i2c-rtc,ds3231' /boot/firmware/config.txt
```

(Or hand-edit `/boot/firmware/config.txt` and add `dtoverlay=i2c-rtc,ds3231`
near the existing `dtparam` lines.)

Disable the userspace fake-hwclock that would otherwise compete:

```
sudo apt-get -y remove fake-hwclock
sudo update-rc.d -f fake-hwclock remove
sudo systemctl disable fake-hwclock
```

Edit `/lib/udev/hwclock-set` and comment out the three lines that
return early when `systemd` is in use (the kernel's `hctosys` already
handles the RTC-to-system sync at boot, but the udev rule is harmless
to leave intact on most setups; comment-out is the conservative move
documented in the kernel RTC howto):

```
#if [ -e /run/systemd/system ] ; then
#    exit 0
#fi
```

Reboot. Verify the RTC is recognized:

```
sudo dmesg | grep -i rtc
sudo hwclock -r
```

`dmesg` should show `rtc-ds1307 ... registered as rtc0`. `hwclock -r`
should print the current time. If the RTC battery is fresh and the
chip has never been written, the time will be wrong; set it from the
network-synced kernel clock:

```
sudo hwclock -w
```

After this, the kernel reads the RTC at boot before chrony or any
network is available, so flight recordings get accurate timestamps
even with no network at the field.

### Heartbeat LED (optional)

Wire a small LED + 1k resistor from header pin 11 (GPIO 17) to any
ground pin (e.g. pin 9). Active-high: pin 11 high turns the LED on.

The daemon enables the heartbeat with `-heartbeat-gpio 17`. Default is
`-1` (disabled), so the daemon runs identically without a breakout.

Verify with the line tool while the daemon is stopped:

```
sudo apt-get install -y gpiod
gpioget gpiochip0 17
gpioset gpiochip0 17=1   # LED on
gpioset gpiochip0 17=0   # LED off
```

When the daemon runs with `-heartbeat-gpio 17`, the LED blinks at 1Hz
while the 50Hz mapper loop is healthy, and goes dark on hang.

### GPS (optional)

ZeroTX supports an optional Pi-attached serial GPS module (u-blox M6,
M7, M10 or any NMEA TTL device) on UART3 (header pins 7/29). The
daemon parses NMEA in-process and exposes a state snapshot to other
subsystems. Failure to open the device is non-fatal: the daemon logs
and continues, and consumers fall back to other position sources.

Wire the module: GPS VCC to header pin 1 (3V3) or header pin 4 (5V,
depending on the module's input range), GPS GND to header pin 6 or 9,
GPS TX to header pin 29 (GPIO 5, UART3 RX), GPS RX to header pin 7
(GPIO 4, UART3 TX). Most modules are 3V3-compatible on both rails;
check the datasheet before connecting 5V power.

Enable UART3 in the Pi's device tree:

```
echo 'dtoverlay=uart3' | sudo tee -a /boot/firmware/config.txt
```

Reboot. After boot the device appears as `/dev/ttyAMA1` (the Pi's
primary mini-UART, `/dev/ttyAMA0`, stays where it is and is normally
used by Bluetooth or the serial console).

Verify raw NMEA flows:

```
ls /dev/ttyAMA*
sudo cat /dev/ttyAMA1
```

Garbage on stty defaults usually means the wrong baud. Common GPS
baud rates: 9600 (default for u-blox M6/M7/M10), 38400, 115200. Set
explicitly if `cat` shows nothing readable:

```
stty -F /dev/ttyAMA1 9600 raw -echo
sudo cat /dev/ttyAMA1
```

You should see lines beginning with `$GP...` or `$GN...` arriving at
1 Hz (default) or faster.

Daemon flags:

```
-gps-port /dev/ttyAMA1
-gps-baud 9600
```

Default `-gps-port` is empty (disabled). When set, the daemon opens
the port at startup and runs an internal NMEA parser. The reader
silently absorbs malformed sentences and rate-limits parse-error
logs (one per minute) so a flaky cable doesn't flood the journal.

## Display arrangement

Confirm both LCDs are detected:

```
xrandr
```

Set arrangement (example, adjust output names and resolutions for actual hardware per `docs/CONNECTIONS.md`):

```
xrandr --output HDMI-1 --mode 1920x1080 --pos 0x0 \
       --output HDMI-2 --mode 1920x1080 --pos 1920x0
```

**TODO**: confirm which Pi micro-HDMI port maps to HUD vs Map. Persist the final command via the autostart mechanism chosen below.

## Browser kiosk autostart

Two Chromium kiosks: HUD on one display, Map on the other.

Example systemd user unit `~/.config/systemd/user/zerotx-hud-kiosk.service`:

```
[Unit]
Description=ZeroTX HUD kiosk
After=graphical-session.target zerotxd.service

[Service]
Environment=DISPLAY=:0
ExecStartPre=/bin/sleep 5
ExecStart=/usr/bin/chromium-browser \
  --kiosk \
  --noerrdialogs \
  --disable-infobars \
  --disable-translate \
  --no-first-run \
  --window-position=0,0 \
  --window-size=1920,1080 \
  http://127.0.0.1:8080/hud
Restart=on-failure

[Install]
WantedBy=graphical-session.target
```

Same template for the Map kiosk, with `--window-position=1920,0` and the `/map` URL.

Enable:

```
systemctl --user daemon-reload
systemctl --user enable zerotx-hud-kiosk.service
systemctl --user enable zerotx-map-kiosk.service
loginctl enable-linger $USER
```

Hide cursor:

```
unclutter -idle 1 &
```

Disable screen blanking and DPMS:

```
xset s off
xset -dpms
xset s noblank
```

**TODO**: confirm HUD and Map URL paths served by the daemon. Pick whether kiosks live in systemd user units, LXDE autostart, or another session manager. Document the final choice.

## Daemon systemd unit

`/etc/systemd/system/zerotxd.service`:

```
[Unit]
Description=ZeroTX daemon
After=network-online.target sound.target
Wants=network-online.target

[Service]
Type=simple
User=adilson
WorkingDirectory=/home/adilson/zerotx/pi/daemon
ExecStart=/home/adilson/zerotx/bin/zerotxd \
  -api 127.0.0.1:8080 \
  -model configs/big_talon_zerotx.yml \
  -joystick-name Thrustmaster \
  -piper-binary /home/adilson/zerotx/third_party/piper/piper \
  -web-dir web \
  -port /dev/serial/by-id/usb-Raspberry_Pi_Pico_E66138935F3C4824-if00 \
  -iohub-port /dev/serial/by-id/<MEGA> \
  -site-lat -22.91 -site-lon -47.06 \
  -tilewarm-rate 5 \
  -v
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Enable and start:

```
sudo systemctl daemon-reload
sudo systemctl enable zerotxd.service
sudo systemctl start zerotxd.service
sudo systemctl status zerotxd.service
journalctl -u zerotxd.service -f
```

**TODO**: replace `<MEGA>` with the actual `/dev/serial/by-id/` name once the Mega is connected.

## Pi 400 optimizations for ZeroTX

Goal: reduce CPU load. Currently 70-80% with two browsers running. Pinned for further profiling in `docs/ROADMAP.md`.

### GPU memory split

`/boot/firmware/config.txt`:

```
gpu_mem=128
```

### Disable unused services

```
sudo systemctl disable bluetooth.service
sudo systemctl disable hciuart.service
sudo systemctl disable cups.service
sudo systemctl disable cups-browsed.service
sudo systemctl disable triggerhappy.service
```

(Skip the Bluetooth disables if you actually use Bluetooth on this Pi.)

### CPU governor

For consistent performance:

```
echo 'performance' | sudo tee /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor
```

Persist via `cpufrequtils` (apt) or a small `/etc/systemd/system/cpu-governor.service` unit.

### Browser flags for low CPU

In addition to the kiosk flags above:

```
--disable-features=TranslateUI
--disable-component-extensions-with-background-pages
--disable-background-networking
--disable-renderer-backgrounding
--disable-extensions
--disk-cache-size=33554432
```

**TODO**: profile actual CPU reduction after each flag. Roadmap item.

### tmpfs for noisy directories

`/etc/fstab`:

```
tmpfs /tmp                tmpfs defaults,noatime,size=512M       0 0
tmpfs /var/log            tmpfs defaults,noatime,size=64M        0 0
```

Trade-off: log loss on power cut. Acceptable for ZeroTX since journalctl persistence rarely matters.

## Verification checklist

After provisioning:

1. SSD boot: `cat /proc/cmdline | grep -o 'root=[^ ]*'` shows the SSD UUID, not an SD card.
2. Network: `ping -c 1 1.1.1.1` succeeds.
3. Go toolchain: `go version` reports the expected version.
4. Piper: smoke test from the Piper section produces audible output.
5. udev: each MCU enumerates with the expected SYMLINK in `/dev/`.
6. Daemon: `sudo systemctl status zerotxd.service` is `active (running)`.
7. API: `curl http://127.0.0.1:8080/api/logs` returns JSON.
8. Audio: `aplay /usr/share/sounds/alsa/Front_Center.wav` plays via the configured sink.
9. Both LCDs show their respective kiosks at boot.
10. Both LCDs survive a reboot without manual intervention.

## SSD backup

Once the system is fully provisioned, image the SSD on another machine:

```
sudo dd if=/dev/<ssd_device> of=zerotx-bootstrap-$(date +%Y%m%d).img bs=4M status=progress
```

Compress and store off-Pi.

This is the canonical baseline for cloning to a fresh SSD. Re-image after any major Pi-side change (kernel update, daemon dependency change, audio reconfig, etc.).

## See also

- `docs/ARCHITECTURE.md`: system overview
- `docs/CONNECTIONS.md`: USB topology, including SSD as boot drive
- `docs/OPERATIONS.md`: daemon launch flags, recovery procedures
- `docs/DECISIONS.md`: locked decisions
- `docs/ROADMAP.md`: pinned items including Pi 400 CPU optimization
- `firmware/display/README.md`, `firmware/io/README.md`, `firmware/crsf/README.md`: firmware build and flash procedures
