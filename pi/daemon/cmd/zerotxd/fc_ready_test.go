package main

import "testing"

func TestFCReadyFromMode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Empty / stale-equivalent.
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"tab", "\t", false},

		// Pre-arm OK indicators.
		{"OK", "OK", true},
		{"OK with asterisk", "OK*", true},
		{"OK with bang suffix", "OK!", true},
		{"OK with whitespace", "  OK  ", true},

		// Waiting indicators.
		{"WAIT", "WAIT", false},
		{"WAITING", "WAITING", false},
		{"WAIT with whitespace", "  WAIT  ", false},

		// Pre-arm errors (leading '!').
		{"!ERR", "!ERR", false},
		{"!FS!", "!FS!", false},
		{"!HWFAIL", "!HWFAIL", false},
		{"!RX", "!RX", false},
		{"!STR (stuck stick)", "!STR", false},
		{"!ACC (accel cal)", "!ACC", false},
		{"unknown bang", "!FOO", false},

		// Active flight modes (already armed or about to be).
		{"ANGL", "ANGL", true},
		{"ACRO", "ACRO", true},
		{"MANU", "MANU", true},
		{"NAV", "NAV", true},
		{"RTH", "RTH", true},
		{"HORI", "HORI", true},
		{"WP", "WP", true},
		{"ALTH", "ALTH", true},
		{"POSH", "POSH", true},

		// Conservative fallback: unknown patterns without explicit
		// '!' or WAIT markers are treated as ready.
		{"unknown safe-looking", "FOO", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fcReadyFromMode(c.in); got != c.want {
				t.Errorf("fcReadyFromMode(%q): got %v want %v", c.in, got, c.want)
			}
		})
	}
}
