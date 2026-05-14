package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// daemonPort is the port the zerotxd daemon binds for its web UI.
// The bench tool's coexistence check probes this port to detect a
// running daemon. Kept here as a const (and not a flag) because if
// the daemon's port ever changes the bench tool needs to be
// rebuilt with the new value -- silent miss is worse than
// requiring a rebuild.
const daemonPort = 8080

// daemonRunningCheck reports whether the zerotxd daemon appears to
// be running on this Pi. Used by main() to refuse-and-exit on
// startup. Two probes; either positive answer means yes:
//
//  1. systemctl is-active zerotxd. Catches the standard production
//     deployment path. Returns "active" only when systemd believes
//     the unit is up. Other states (inactive, failed, unknown) are
//     all treated as "daemon not running for our purposes".
//
//  2. TCP probe of 127.0.0.1:8080. Catches dev runs where the daemon
//     is started manually (go run, ./zerotxd) outside systemd. A
//     successful Dial means something is listening; we don't try to
//     verify it's actually zerotxd (any service on :8080 is a
//     conflict for the operator's mental model anyway).
//
// Returns (running, reason). reason is a short explanation suitable
// for the startup error message.
func daemonRunningCheck(ctx context.Context) (bool, string) {
	if active, ok := systemctlIsActive(ctx, "zerotxd"); ok && active {
		return true, "systemctl reports zerotxd is active"
	}

	if portBound(daemonPort) {
		return true, fmt.Sprintf("port %d is bound (daemon's web UI port)", daemonPort)
	}

	return false, ""
}

// systemctlIsActive runs `systemctl is-active <unit>` with a 1s
// timeout. The bool return is whether the command itself worked
// (systemctl was available, ran cleanly); the first return value
// is whether the unit is currently active. On systems without
// systemd the command may return a nonzero exit but a usable
// stdout (e.g. "unknown"), so we treat exit code as advisory.
func systemctlIsActive(ctx context.Context, unit string) (active bool, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "systemctl", "is-active", unit)
	out, _ := cmd.Output()
	state := strings.TrimSpace(string(out))
	if state == "" {
		return false, false
	}
	return state == "active", true
}

// portBound attempts a quick TCP dial against 127.0.0.1:port. A
// successful connection means something is listening; we close the
// connection immediately and return true. Connection refused (the
// expected "nothing here") returns false. Other errors (timeout,
// permission) return false too -- this check is for the happy path
// of "running daemon vs no daemon", not a network diagnostic.
func portBound(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
