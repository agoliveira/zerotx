package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.bug.st/serial"
)

// serialPort wraps a /dev/serial/by-id/ discovery hit. Path is the
// resolved /dev/tty* device; ByID is the symlink filename for
// human-readable identification.
type serialPortInfo struct {
	Path string
	ByID string
}

const serialByIDDir = "/dev/serial/by-id"

// findUSBSerial scans /dev/serial/by-id/ for symlinks whose names
// contain any of the given substrings (case-insensitive). Returns
// resolved /dev/tty* paths plus the original symlink names for
// display. Sorted by symlink name for deterministic output when
// multiple devices match.
//
// Why /dev/serial/by-id/ instead of /dev/ttyACM*: ACM device numbers
// race at boot (which one is ACM0?), but the by-id symlink names
// are stable across reboots and include the USB vendor + serial.
// Every Pi-deployed Linux includes by-id by default through
// systemd-udev.
func findUSBSerial(patterns ...string) ([]serialPortInfo, error) {
	entries, err := os.ReadDir(serialByIDDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no devices at all, not an error
		}
		return nil, fmt.Errorf("read %s: %w", serialByIDDir, err)
	}

	var out []serialPortInfo
	for _, e := range entries {
		name := e.Name()
		lowName := strings.ToLower(name)
		matched := false
		for _, p := range patterns {
			if strings.Contains(lowName, strings.ToLower(p)) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		full := filepath.Join(serialByIDDir, name)
		resolved, err := os.Readlink(full)
		if err != nil {
			continue
		}
		// Symlinks under by-id/ are relative ("../../ttyACM1");
		// resolve to absolute.
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Clean(filepath.Join(serialByIDDir, resolved))
		}
		out = append(out, serialPortInfo{Path: resolved, ByID: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ByID < out[j].ByID })
	return out, nil
}

// openSerialAt opens a port with 8N1 framing at the given baud,
// no flow control. Matches what iohal-config does for the Mega
// and what the daemon does for the RP2040 link.
func openSerialAt(path string, baud int) (serial.Port, error) {
	return serial.Open(path, &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
}

// lineRW is a small wrapper for line-based ASCII protocols (Mega,
// ESP32). Sends one command line, reads response lines until a
// matcher fires or a deadline expires. Silently skips EVENT lines
// and comments so probe code doesn't see the noise.
type lineRW struct {
	port    serial.Port
	br      *bufio.Reader
	timeout time.Duration
}

func newLineRW(p serial.Port, timeout time.Duration) *lineRW {
	return &lineRW{port: p, br: bufio.NewReader(p), timeout: timeout}
}

// send writes one command line, appending \n.
func (r *lineRW) send(cmd string) error {
	_, err := r.port.Write([]byte(cmd + "\n"))
	return err
}

// drain reads anything currently available with a short timeout.
// Used to flush boot banners or async output before sending a
// fresh command.
func (r *lineRW) drain() {
	_ = r.port.SetReadTimeout(50 * time.Millisecond)
	defer r.port.SetReadTimeout(r.timeout)
	for {
		_, err := r.br.ReadString('\n')
		if err != nil {
			return
		}
	}
}

// readUntil reads response lines until matcher returns true on a
// non-noise line, or the timeout expires. Returns all relevant lines
// seen (including the matching one). Empty lines, comments (#...),
// and "EVENT " prefixes are skipped silently.
func (r *lineRW) readUntil(matcher func(line string) bool) ([]string, error) {
	_ = r.port.SetReadTimeout(r.timeout)
	deadline := time.Now().Add(r.timeout)

	var out []string
	for time.Now().Before(deadline) {
		line, err := r.br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, fmt.Errorf("port closed mid-read")
			}
			if line == "" {
				// Read timed out without partial data. Check
				// deadline and continue trying.
				continue
			}
			return out, fmt.Errorf("read: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "EVENT ") {
			continue
		}
		out = append(out, line)
		if matcher(line) {
			return out, nil
		}
	}
	return out, fmt.Errorf("timeout after %s waiting for matching response", r.timeout)
}
