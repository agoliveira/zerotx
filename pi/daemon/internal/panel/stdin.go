package panel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"
)

// StdinPanel reads single-line commands from stdin and updates panel
// state. Useful for solo bench testing: type "SA 1" and the panel
// reports SA at position 1 immediately.
//
// Command syntax (one per line, whitespace-separated):
//
//	switch     SA 1            set SA to position 1
//	selector   6POS 3          set 6POS to position 3
//	button     rth on          press rth (also: off, 0, 1, true, false)
//	show                       print current state to stderr
//	help                       print command reference
//	quit                       same as Ctrl-D, exits the REPL
//
// Bare names work too: "SA 1" is short for "switch SA 1". The panel
// guesses the kind from the value: numeric -> switch, true/false -> button.
type StdinPanel struct {
	state
	reader io.Reader
	writer io.Writer
}

// NewStdinPanel constructs a StdinPanel. By default reads from os.Stdin.
// Pass alternate io.Reader for testing.
func NewStdinPanel(r io.Reader, w io.Writer) *StdinPanel {
	return &StdinPanel{
		state:  newState(),
		reader: r,
		writer: w,
	}
}

// Run reads commands until EOF or ctx is cancelled. Blocks; run in a
// goroutine.
func (p *StdinPanel) Run(ctx context.Context) error {
	fmt.Fprintln(p.writer, "panel REPL: type 'help' for commands, Ctrl-D to exit")
	scanner := bufio.NewScanner(p.reader)
	// Scan in its own goroutine so ctx cancellation is honored.
	lines := make(chan string)
	go func() {
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			p.handle(line)
		}
	}
}

func (p *StdinPanel) handle(line string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	parts := strings.Fields(line)

	// Single-word commands.
	if len(parts) == 1 {
		switch parts[0] {
		case "show", "list", "ls":
			p.dump()
		case "help", "?":
			p.printHelp()
		case "quit", "exit":
			fmt.Fprintln(p.writer, "(use Ctrl-D to exit cleanly)")
		default:
			fmt.Fprintf(p.writer, "panel: unknown command: %s (try 'help')\n", parts[0])
		}
		return
	}

	// "tap NAME [duration_ms]" — momentary press: sets switch to position 2,
	// waits, then back to 0. Used to satisfy EDGE windows that aren't
	// reachable by typing two separate commands. Default duration: 200ms.
	if parts[0] == "tap" {
		if len(parts) < 2 {
			fmt.Fprintln(p.writer, "panel: usage: tap NAME [duration_ms]")
			return
		}
		name := parts[1]
		durMs := 200
		if len(parts) >= 3 {
			if v, err := strconv.Atoi(parts[2]); err == nil && v > 0 {
				durMs = v
			}
		}
		go func(name string, dur time.Duration) {
			p.state.setSwitch(name, 2)
			log.Printf("panel: tap %s (down)", name)
			time.Sleep(dur)
			p.state.setSwitch(name, 0)
			log.Printf("panel: tap %s (up, %dms)", name, durMs)
		}(name, time.Duration(durMs)*time.Millisecond)
		return
	}

	// Strip optional leading 'switch'/'selector'/'button' prefix.
	kind := ""
	rest := parts
	switch parts[0] {
	case "switch", "sw":
		kind = "switch"
		rest = parts[1:]
	case "selector", "sel":
		kind = "selector"
		rest = parts[1:]
	case "button", "btn":
		kind = "button"
		rest = parts[1:]
	}
	if len(rest) < 2 {
		fmt.Fprintf(p.writer, "panel: usage: [switch|selector|button] NAME VALUE\n")
		return
	}
	name := rest[0]
	value := rest[1]

	if kind == "" {
		// Guess from name + value:
		//   - 6P-prefix names (6POS, 6P15) -> selector
		//   - bool-y values (on/off/true/false/yes/no) -> button
		//   - everything else -> switch
		upper := strings.ToUpper(name)
		switch {
		case strings.HasPrefix(upper, "6P"):
			kind = "selector"
		case isBool(value):
			kind = "button"
		default:
			kind = "switch"
		}
	}

	switch kind {
	case "switch":
		v, err := strconv.Atoi(value)
		if err != nil {
			fmt.Fprintf(p.writer, "panel: switch position must be integer, got %q\n", value)
			return
		}
		p.state.setSwitch(name, v)
		log.Printf("panel: %s = %d", name, v)
	case "selector":
		v, err := strconv.Atoi(value)
		if err != nil {
			fmt.Fprintf(p.writer, "panel: selector position must be integer, got %q\n", value)
			return
		}
		p.state.setSelector(name, v)
		log.Printf("panel: selector %s = %d", name, v)
	case "button":
		b, err := parseBool(value)
		if err != nil {
			fmt.Fprintf(p.writer, "panel: %v\n", err)
			return
		}
		p.state.setButton(name, b)
		log.Printf("panel: button %s = %v", name, b)
	}
}

func (p *StdinPanel) printHelp() {
	fmt.Fprintln(p.writer, "  switch NAME N    set 3-pos switch (also: 'sw', or just 'SA 1')")
	fmt.Fprintln(p.writer, "  selector NAME N  set rotary selector (also: 'sel')")
	fmt.Fprintln(p.writer, "  button NAME B    set momentary button (also: 'btn'); B is on/off/true/false/0/1")
	fmt.Fprintln(p.writer, "  tap NAME [ms]    momentary press: pos 2 for ms (default 200), then 0")
	fmt.Fprintln(p.writer, "  show             print current state")
	fmt.Fprintln(p.writer, "  help             this message")
}

func (p *StdinPanel) dump() {
	p.state.mu.RLock()
	defer p.state.mu.RUnlock()
	fmt.Fprintln(p.writer, "switches:")
	for k, v := range p.state.switches {
		fmt.Fprintf(p.writer, "  %s: %d\n", k, v)
	}
	fmt.Fprintln(p.writer, "selectors:")
	for k, v := range p.state.selectors {
		fmt.Fprintf(p.writer, "  %s: %d\n", k, v)
	}
	fmt.Fprintln(p.writer, "buttons:")
	for k, v := range p.state.buttons {
		fmt.Fprintf(p.writer, "  %s: %v\n", k, v)
	}
}

// Switch implements Panel.
func (p *StdinPanel) Switch(name string) (int, bool) { return p.state.getSwitch(name) }

// Selector implements Panel.
func (p *StdinPanel) Selector(name string) (int, bool) { return p.state.getSelector(name) }

// Button implements Panel.
func (p *StdinPanel) Button(name string) (bool, bool) { return p.state.getButton(name) }

// LastUpdate implements Panel.
func (p *StdinPanel) LastUpdate() time.Time { return p.state.lastUpdate() }

// Snapshot implements Panel.
func (p *StdinPanel) Snapshot() Snapshot { return p.state.snapshot() }

func isBool(s string) bool {
	switch strings.ToLower(s) {
	case "on", "off", "true", "false", "yes", "no":
		return true
	}
	return false
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "1", "yes":
		return true, nil
	case "off", "false", "0", "no":
		return false, nil
	}
	return false, fmt.Errorf("expected on/off/true/false/0/1, got %q", s)
}
