// zerotx-iohal-config: configures the Mega 2560 IO board's HAL pin
// map and per-pin flags from a JSON file.
//
// Three modes:
//
//   -show (default)  : query Mega, print current map next to JSON
//                      config (if -config is given), highlight diffs.
//                      Read-only; the device is not modified.
//
//   -export          : query Mega, write current map as JSON to
//                      stdout. Useful for capturing the firmware's
//                      compiled defaults the first time, before
//                      hand-editing.
//
//   -apply           : push the JSON config to the Mega via SET hal
//                      pin / SET hal flag commands. Then SET hal
//                      reboot (unless -no-reboot) so changes take
//                      effect. Verifies the resulting map matches
//                      what was requested.
//
// Connection management is intentionally simple: the tool opens the
// serial port directly, exchanges a small number of request/response
// turns, and exits. It does NOT use internal/iohub because the
// run-once nature doesn't benefit from a long-lived dispatcher.
//
// JSON schema (see the example at ../../configs/iohal.example.json):
//
//   {
//     "pins": {
//       "<pin_name>": { "pin": <number>, "active_low": <bool> },
//       ...
//     }
//   }
//
// pin_name must match a HAL pin id known to the firmware (e.g.
// "led_trackball_green", "vfd0_rs", "relay_0"). Unknown names are
// reported as errors. Pins listed in JSON but not present on the
// firmware are warnings (the firmware version may be older or newer
// than the config). Pins on the firmware but missing from the JSON
// are left unchanged on the Mega.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"go.bug.st/serial"
)

// Config is the on-disk JSON schema.
type Config struct {
	Pins map[string]PinConfig `json:"pins"`
}

type PinConfig struct {
	Pin       uint8 `json:"pin"`
	ActiveLow bool  `json:"active_low,omitempty"`
}

// FlagBitActiveLow mirrors firmware/io/src/hal.h HAL_FLAG_ACTIVE_LOW.
const FlagBitActiveLow uint8 = 0x01

// MapEntry is one parsed line from "GET hal map" output.
type MapEntry struct {
	Index uint8
	Name  string
	Pin   uint8
	Flags uint8
}

func main() {
	var (
		port      = flag.String("port", "", "serial device path of the Mega (required)")
		cfgPath   = flag.String("config", "", "path to JSON config (required for -apply, optional for -show)")
		show      = flag.Bool("show", false, "show current Mega map; if -config given, also show diff")
		export    = flag.Bool("export", false, "write current Mega map as JSON to stdout")
		apply     = flag.Bool("apply", false, "push JSON config to the Mega")
		noReboot  = flag.Bool("no-reboot", false, "stage changes in EEPROM but don't reboot (changes apply at next manual reboot)")
		readWait  = flag.Duration("read-timeout", 3*time.Second, "max time to wait for Mega response per request")
		bootDelay = flag.Duration("boot-delay", 4*time.Second, "after reboot, time to wait for Mega to come back before re-querying")
	)
	flag.Parse()

	if *port == "" {
		log.Fatal("-port is required")
	}
	// Default mode is -show if nothing else specified.
	if !*export && !*apply && !*show {
		*show = true
	}
	// Mutually exclusive sanity.
	if (boolToInt(*export) + boolToInt(*apply) + boolToInt(*show)) > 1 {
		log.Fatal("specify at most one of -show, -export, -apply")
	}

	dev, err := openSerial(*port)
	if err != nil {
		log.Fatalf("open %s: %v", *port, err)
	}
	defer dev.Close()

	rw := newRW(dev, *readWait)

	// Discard any pending boot/event noise from the Mega before our
	// first request. Most readers will be in the middle of their boot
	// banner; give them a moment to settle.
	time.Sleep(500 * time.Millisecond)
	rw.drain()

	switch {
	case *export:
		if err := runExport(rw); err != nil {
			log.Fatal(err)
		}

	case *apply:
		if *cfgPath == "" {
			log.Fatal("-apply requires -config")
		}
		cfg, err := loadConfig(*cfgPath)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		if err := runApply(rw, cfg, *noReboot, *bootDelay); err != nil {
			log.Fatal(err)
		}

	case *show:
		var cfg *Config
		if *cfgPath != "" {
			c, err := loadConfig(*cfgPath)
			if err != nil {
				log.Fatalf("config: %v", err)
			}
			cfg = c
		}
		if err := runShow(rw, cfg); err != nil {
			log.Fatal(err)
		}
	}
}

// =============================================================================
// Serial I/O
// =============================================================================

func openSerial(path string) (serial.Port, error) {
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	return serial.Open(path, mode)
}

// rw wraps the port with a buffered reader and a deadline-bounded
// readLine that handles "EVENT" and noise lines transparently.
type rw struct {
	port    serial.Port
	br      *bufio.Reader
	timeout time.Duration
}

func newRW(p serial.Port, timeout time.Duration) *rw {
	return &rw{port: p, br: bufio.NewReader(p), timeout: timeout}
}

// send writes one command line.
func (r *rw) send(cmd string) error {
	_, err := r.port.Write([]byte(cmd + "\n"))
	return err
}

// drain reads anything currently available in the buffer with a
// short timeout. Used to flush boot banners.
func (r *rw) drain() {
	r.port.SetReadTimeout(50 * time.Millisecond)
	defer r.port.SetReadTimeout(r.timeout)
	for {
		_, err := r.br.ReadString('\n')
		if err != nil {
			return
		}
	}
}

// readUntil reads response lines until f returns true on a line, or
// the read deadline expires. Returns all lines that f saw (including
// the final one). EVENT lines are skipped. Comment lines (#...) are
// also skipped.
func (r *rw) readUntil(f func(line string) bool) ([]string, error) {
	r.port.SetReadTimeout(r.timeout)
	deadline := time.Now().Add(r.timeout)

	var out []string
	for time.Now().Before(deadline) {
		line, err := r.br.ReadString('\n')
		if err != nil {
		if errors.Is(err, io.EOF) {
				return out, fmt.Errorf("port closed mid-read")
			}
			return out, fmt.Errorf("read: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "EVENT ") {
			// Surface unsolicited events so the operator knows the
			// device is doing something, but don't return them.
			continue
		}
		out = append(out, line)
		if f(line) {
			return out, nil
		}
	}
	return out, fmt.Errorf("timed out waiting for response")
}

// =============================================================================
// HAL map: query and parse
// =============================================================================

// queryMap sends GET hal map and parses the response into entries.
//
// Response format from the firmware (firmware/io/src/subsystems/hal_subsystem.cpp):
//
//   > hal map source=<eeprom|defaults> count=<N>
//   > hal pin <index> <name> <pin-num> 0x<flags-hex>
//   > hal pin ...
//
// We collect all the per-pin lines, terminating after we've seen
// <count> of them.
func queryMap(r *rw) ([]MapEntry, string, error) {
	if err := r.send("GET hal map"); err != nil {
		return nil, "", fmt.Errorf("send: %w", err)
	}

	var (
		expected = -1
		entries  []MapEntry
		source   string
	)

	_, err := r.readUntil(func(line string) bool {
		if !strings.HasPrefix(line, "> hal ") {
			return false
		}
		body := strings.TrimPrefix(line, "> ")
		// "hal map source=eeprom count=22"
		if strings.HasPrefix(body, "hal map ") {
			rest := strings.TrimPrefix(body, "hal map ")
			for _, kv := range strings.Fields(rest) {
				k, v, ok := splitKV(kv)
				if !ok {
					continue
				}
				switch k {
				case "source":
					source = v
				case "count":
					var n int
					fmt.Sscanf(v, "%d", &n)
					expected = n
				}
			}
			return false
		}
		// "hal pin <i> <name> <pin> 0x<flags>"
		if strings.HasPrefix(body, "hal pin ") {
			rest := strings.TrimPrefix(body, "hal pin ")
			fields := strings.Fields(rest)
			if len(fields) < 4 {
				return false
			}
			var idx, pin uint8
			var flags uint8
			fmt.Sscanf(fields[0], "%d", &idx)
			name := fields[1]
			fmt.Sscanf(fields[2], "%d", &pin)
			// flags is "0xNN"
			var fv uint
			fmt.Sscanf(fields[3], "0x%x", &fv)
			flags = uint8(fv)
			entries = append(entries, MapEntry{
				Index: idx, Name: name, Pin: pin, Flags: flags,
			})
		}
		// Terminate when we've collected the expected count.
		return expected > 0 && len(entries) >= expected
	})
	if err != nil {
		return nil, "", err
	}
	return entries, source, nil
}

func splitKV(s string) (k, v string, ok bool) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// =============================================================================
// Modes
// =============================================================================

func runExport(r *rw) error {
	entries, _, err := queryMap(r)
	if err != nil {
		return err
	}
	cfg := Config{Pins: make(map[string]PinConfig, len(entries))}
	for _, e := range entries {
		cfg.Pins[e.Name] = PinConfig{
			Pin:       e.Pin,
			ActiveLow: (e.Flags & FlagBitActiveLow) != 0,
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg)
}

func runShow(r *rw, cfg *Config) error {
	entries, source, err := queryMap(r)
	if err != nil {
		return err
	}
	fmt.Printf("Mega map (source=%s, count=%d):\n", source, len(entries))

	// Sort for stable output.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Index < entries[j].Index })

	if cfg == nil {
		// No config to compare; print plain map.
		for _, e := range entries {
			fmt.Printf("  %-22s pin=%-3d flags=0x%02x\n", e.Name, e.Pin, e.Flags)
		}
		return nil
	}

	// Diff: compare each Mega entry with the config.
	var diffs []string
	for _, e := range entries {
		c, ok := cfg.Pins[e.Name]
		if !ok {
			fmt.Printf("  %-22s pin=%-3d flags=0x%02x  (not in config)\n", e.Name, e.Pin, e.Flags)
			continue
		}
		desiredFlags := uint8(0)
		if c.ActiveLow {
			desiredFlags |= FlagBitActiveLow
		}
		marker := "  "
		if c.Pin != e.Pin || desiredFlags != e.Flags {
			marker = "* "
			diffs = append(diffs, fmt.Sprintf("%s: pin %d->%d flags 0x%02x->0x%02x",
				e.Name, e.Pin, c.Pin, e.Flags, desiredFlags))
		}
		fmt.Printf("%s%-22s pin=%-3d flags=0x%02x   (config: pin=%-3d flags=0x%02x)\n",
			marker, e.Name, e.Pin, e.Flags, c.Pin, desiredFlags)
	}
	// Config entries the firmware doesn't recognize.
	known := make(map[string]bool, len(entries))
	for _, e := range entries {
		known[e.Name] = true
	}
	for name := range cfg.Pins {
		if !known[name] {
			fmt.Printf("? %-22s in config but not on firmware (version skew?)\n", name)
		}
	}

	if len(diffs) == 0 {
		fmt.Println("\nIn sync.")
		return nil
	}
	fmt.Printf("\n%d difference(s):\n", len(diffs))
	for _, d := range diffs {
		fmt.Printf("  - %s\n", d)
	}
	fmt.Println("\nRun with -apply to push the config.")
	return nil
}

func runApply(r *rw, cfg *Config, noReboot bool, bootDelay time.Duration) error {
	entries, _, err := queryMap(r)
	if err != nil {
		return err
	}

	known := make(map[string]MapEntry, len(entries))
	for _, e := range entries {
		known[e.Name] = e
	}
	// Check for unknown names BEFORE making any changes.
	for name := range cfg.Pins {
		if _, ok := known[name]; !ok {
			return fmt.Errorf("config has %q which is not on this firmware (version skew?)", name)
		}
	}

	type change struct {
		name        string
		pinChanged  bool
		flagChanged bool
		newPin      uint8
		newFlags    uint8
	}
	var changes []change

	for _, e := range entries {
		c, ok := cfg.Pins[e.Name]
		if !ok {
			continue
		}
		desiredFlags := uint8(0)
		if c.ActiveLow {
			desiredFlags |= FlagBitActiveLow
		}
		ch := change{name: e.Name, newPin: c.Pin, newFlags: desiredFlags}
		if c.Pin != e.Pin {
			ch.pinChanged = true
		}
		if desiredFlags != e.Flags {
			ch.flagChanged = true
		}
		if ch.pinChanged || ch.flagChanged {
			changes = append(changes, ch)
		}
	}

	if len(changes) == 0 {
		fmt.Println("Already in sync; nothing to apply.")
		return nil
	}

	fmt.Printf("Applying %d change(s):\n", len(changes))
	for _, ch := range changes {
		if ch.pinChanged {
			cmd := fmt.Sprintf("SET hal pin %s %d", ch.name, ch.newPin)
			fmt.Printf("  %s\n", cmd)
			if err := r.send(cmd); err != nil {
				return fmt.Errorf("send: %w", err)
			}
			if _, err := r.readUntil(isResponseOrError); err != nil {
				return fmt.Errorf("after %s: %w", cmd, err)
			}
		}
		if ch.flagChanged {
			cmd := fmt.Sprintf("SET hal flag %s 0x%02x", ch.name, ch.newFlags)
			fmt.Printf("  %s\n", cmd)
			if err := r.send(cmd); err != nil {
				return fmt.Errorf("send: %w", err)
			}
			if _, err := r.readUntil(isResponseOrError); err != nil {
				return fmt.Errorf("after %s: %w", cmd, err)
			}
		}
	}

	if noReboot {
		fmt.Println("\nStaged in EEPROM. Run SET hal reboot (or power-cycle the Mega) to apply.")
		return nil
	}

	fmt.Println("\nRebooting Mega...")
	if err := r.send("SET hal reboot"); err != nil {
		return fmt.Errorf("send reboot: %w", err)
	}
	// Don't wait for a response - the firmware reboots before flushing.

	// Wait for the device to come back. USB-CDC re-enumeration on
	// Linux usually completes within 2-3 seconds.
	time.Sleep(bootDelay)
	r.drain()

	// Verify by re-querying.
	fmt.Println("Verifying...")
	entries2, _, err := queryMap(r)
	if err != nil {
		return fmt.Errorf("post-reboot query: %w (the Mega may still be coming back; try -show in a moment)", err)
	}
	known2 := make(map[string]MapEntry, len(entries2))
	for _, e := range entries2 {
		known2[e.Name] = e
	}
	bad := 0
	for _, ch := range changes {
		got, ok := known2[ch.name]
		if !ok {
			fmt.Printf("  ! %s: missing after reboot\n", ch.name)
			bad++
			continue
		}
		if got.Pin != ch.newPin || got.Flags != ch.newFlags {
			fmt.Printf("  ! %s: pin=%d flags=0x%02x (expected pin=%d flags=0x%02x)\n",
				ch.name, got.Pin, got.Flags, ch.newPin, ch.newFlags)
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d entries did not match after reboot", bad)
	}
	fmt.Println("Done. All changes verified.")
	return nil
}

// isResponseOrError is the read-until predicate for SET commands.
// The firmware emits exactly one ">" or "!" line per SET, so we
// stop on the first one we see.
func isResponseOrError(line string) bool {
	return strings.HasPrefix(line, "> ") || strings.HasPrefix(line, "! ")
}

// =============================================================================
// Config file
// =============================================================================

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var c Config
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Pins == nil {
		return nil, fmt.Errorf("%s: missing 'pins' map", path)
	}
	return &c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
