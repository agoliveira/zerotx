// Command disptest is a standalone bench-test harness for the
// HUB75 display device. It connects to the device's USB-CDC serial
// port and lets you fire individual protocol messages from stdin.
// Inbound messages from the device are printed to stdout.
//
// Example:
//
//	disptest -port /dev/ttyACM1
//	> mode flight
//	> state bat=11.7 alt=124 dist=430
//	> alarm critical "BATTERY EMPTY"
//	> clear-alarm
//	> bright 50
//	> ping
//	> quit
//
// This tool exists so firmware iteration on the ESP32 can happen in
// isolation from the live daemon. Once the firmware behaves
// correctly under disptest, the same protocol gets driven by the
// daemon proper.
//
// The command syntax is the same as the wire protocol but without
// the leading "DISP" token (added automatically) and with case-
// insensitive command names. So "mode flight" sends "DISP MODE
// FLIGHT". For exact wire-format input, use the "raw" command:
//
//	> raw DISP STATE bat=11.7
//
// which sends the line verbatim.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"go.bug.st/serial"

	"github.com/agoliveira/zerotx/pi/daemon/internal/devices/display"
)

func main() {
	port := flag.String("port", "", "serial port path, e.g. /dev/ttyACM1 or /dev/ttyUSB0")
	baud := flag.Int("baud", 115200, "baud rate")
	flag.Parse()

	if *port == "" {
		fmt.Fprintln(os.Stderr, "Usage: disptest -port <path> [-baud <rate>]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Connects to a HUB75 display device and forwards typed commands.")
		fmt.Fprintln(os.Stderr, "Type 'help' once connected for the command list.")
		os.Exit(2)
	}

	mode := &serial.Mode{
		BaudRate: *baud,
		Parity:   serial.NoParity,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		// CP2102/CH340 USB-serial chips on ESP32 dev boards trigger a
		// chip reset when DTR/RTS are toggled at port open (this is
		// how the auto-flash mechanism works). We don't want disptest
		// to reset the ESP32 every time it connects, so we explicitly
		// hold both lines false at open.
		InitialStatusBits: &serial.ModemOutputBits{
			DTR: false,
			RTS: false,
		},
	}
	rwc, err := serial.Open(*port, mode)
	if err != nil {
		log.Fatalf("open %s: %v", *port, err)
	}

	cfg := display.Config{
		SnapshotRate: 500 * time.Millisecond, // 2Hz - quick enough for interactive testing
	}
	drv := display.New(rwc, cfg)
	drv.SetEventHandler(func(ev display.Event) {
		fmt.Printf("<-- %s\n", ev.Raw)
	})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- drv.Run(ctx)
	}()

	fmt.Printf("Connected to %s @ %d baud. Type 'help' or 'quit'.\n", *port, *baud)

	if err := repl(drv); err != nil && err != io.EOF {
		log.Printf("repl: %v", err)
	}

	cancel()
	drv.Close()
	<-runDone
}

func repl(drv *display.Driver) error {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !dispatch(drv, line) {
			return nil // quit was issued
		}
	}
}

// dispatch returns false if the user issued "quit".
func dispatch(drv *display.Driver, line string) bool {
	tokens := strings.Fields(line)
	if len(tokens) == 0 {
		return true
	}
	cmd := strings.ToLower(tokens[0])
	args := tokens[1:]

	switch cmd {
	case "help", "?":
		printHelp()
	case "quit", "exit", "q":
		return false
	case "mode":
		if len(args) != 1 {
			fmt.Println("usage: mode <name>")
			return true
		}
		m := display.Mode(strings.ToUpper(args[0]))
		if !m.IsValid() {
			fmt.Printf("unknown mode: %s\n", args[0])
			fmt.Println("valid: idle, preflight, flight, alarm, rth, postflight")
			return true
		}
		drv.SetMode(m)
	case "state":
		// Parse k=v pairs and build a State partial.
		s := parseStatePartial(args)
		drv.SetState(s)
		// (state stored; sent on next snapshot pulse)
		fmt.Println("(state stored; sent on next snapshot pulse, ~500ms)")
	case "snapshot":
		// Helper: send the current state right now via raw.
		fmt.Println("(no direct snapshot trigger; use 'raw DISP STATE ...' for instant)")
	case "alarm":
		if len(args) < 2 {
			fmt.Println(`usage: alarm <level> "<text>"`)
			return true
		}
		level := display.AlarmLevel(strings.ToLower(args[0]))
		text := strings.Join(args[1:], " ")
		text = strings.Trim(text, `"`)
		drv.FireAlarm(level, text)
	case "clear-alarm", "clear":
		drv.ClearAlarm()
	case "msg":
		if len(args) < 1 {
			fmt.Println(`usage: msg "<text>"`)
			return true
		}
		text := strings.Join(args, " ")
		text = strings.Trim(text, `"`)
		drv.ShowMessage(text)
	case "bright", "brightness":
		if len(args) != 1 {
			fmt.Println("usage: bright <0-100>")
			return true
		}
		var n int
		if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil {
			fmt.Printf("bad number: %s\n", args[0])
			return true
		}
		drv.SetBrightness(n)
	case "ping":
		drv.Ping()
	case "raw":
		// Raw line: send verbatim. For exact wire-protocol testing.
		raw := strings.TrimSpace(strings.TrimPrefix(line, tokens[0]))
		fmt.Printf("(raw line not implemented in disptest - use proper commands above)\n")
		_ = raw
	default:
		fmt.Printf("unknown command: %s (type 'help')\n", cmd)
	}
	return true
}

func parseStatePartial(args []string) display.State {
	s := display.State{}
	for _, a := range args {
		eq := strings.IndexByte(a, '=')
		if eq < 0 {
			continue
		}
		k := a[:eq]
		v := a[eq+1:]
		switch k {
		case "armed":
			b := v == "1" || strings.EqualFold(v, "true")
			s.Armed = display.BoolPtr(b)
		case "bat":
			var f float64
			if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
				s.BatV = display.Float64Ptr(f)
			}
		case "batpct":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				s.BatPct = display.IntPtr(n)
			}
		case "alt":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				s.AltM = display.IntPtr(n)
			}
		case "dist":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				s.DistM = display.IntPtr(n)
			}
		case "spd":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				s.SpdKmh = display.IntPtr(n)
			}
		case "link":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				s.LinkPct = display.IntPtr(n)
			}
		case "sats":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				s.Sats = display.IntPtr(n)
			}
		case "mode":
			s.FlightMode = v
		case "gps":
			s.GpsFix = v
		case "time":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				s.TimeSec = display.IntPtr(n)
			}
		default:
			fmt.Printf("(ignoring unknown field %q)\n", k)
		}
	}
	return s
}

func printHelp() {
	fmt.Println(`Commands:
  mode <name>                  switch render mode
                                 (idle/preflight/flight/alarm/rth/postflight)
  state k=v [k=v...]           update state fields (sent on next snapshot ~500ms)
                                 keys: armed, bat, batpct, alt, dist, spd,
                                       link, sats, mode, gps, time
  alarm <level> "<text>"       fire alarm overlay
                                 (level: info/notice/warning/critical)
  clear-alarm                  remove alarm overlay
  msg "<text>"                 show one-shot scrolling message
  bright <0-100>               set panel brightness
  ping                         request PONG reply
  help                         show this help
  quit                         exit

Inbound messages from the device print as: <-- DISP READY ...`)
}
