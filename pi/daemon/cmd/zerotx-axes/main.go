// zerotx-axes opens a joystick and prints its live axis/button/hat state
// in a refreshing terminal display. Use it to identify which physical input
// (stick X, throttle lever, rudder rocker, etc) corresponds to which SDL
// axis index for your specific hardware, then use those numbers in your
// ZeroTX model file's source_bindings section.
//
// Usage:
//
//	./bin/zerotx-axes                      # opens joystick #0
//	./bin/zerotx-axes -name "Thrustmaster" # by name substring
//	./bin/zerotx-axes -joystick 1          # by SDL index
//
// Press Ctrl-C to exit. Output uses ANSI cursor controls; works in any
// modern terminal.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/joystick"
)

func main() {
	runtime.LockOSThread()

	idx := flag.Int("joystick", -1, "joystick index (default 0; -1 picks first)")
	name := flag.String("name", "", "open first joystick whose name contains this substring")
	hz := flag.Int("hz", 30, "refresh rate")
	flag.Parse()

	if err := joystick.Init(); err != nil {
		log.Fatal(err)
	}
	defer joystick.Quit()

	devs := joystick.List()
	if len(devs) == 0 {
		fmt.Fprintln(os.Stderr, "no joysticks attached")
		os.Exit(1)
	}

	var r *joystick.Reader
	var err error
	if *name != "" {
		r, _, err = joystick.OpenByName(*name)
	} else {
		open := *idx
		if open < 0 {
			open = 0
		}
		r, err = joystick.Open(open)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigC
		cancel()
	}()

	go func() { _ = r.Run(ctx) }()

	fmt.Printf("device:  %s  (axes=%d buttons=%d hats=%d)\n",
		r.Name(), r.NumAxes(), r.NumButtons(), r.NumHats())
	fmt.Println("press Ctrl-C to exit. move each input one at a time to identify it.")
	fmt.Println()

	// Reserve enough vertical space for the live block.
	lines := r.NumAxes() + 2 + (r.NumButtons()+15)/16 + r.NumHats() + 2
	for i := 0; i < lines; i++ {
		fmt.Println()
	}

	period := time.Second / time.Duration(*hz)
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println()
			return
		case <-ticker.C:
		}
		render(r, lines)
	}
}

// render reprints the live status block. Uses ANSI cursor-up to overwrite
// the previous render in place.
func render(r *joystick.Reader, lines int) {
	// Move cursor up to the top of our block.
	fmt.Printf("\033[%dA", lines)

	for i := 0; i < r.NumAxes(); i++ {
		v := r.Axis(i)
		bar := axisBar(v, 40)
		fmt.Printf("\033[K  axis %2d  %+6d  %s\n", i, v, bar)
	}
	fmt.Print("\033[K\n")

	// Buttons in rows of 16.
	for row := 0; row < (r.NumButtons()+15)/16; row++ {
		fmt.Printf("\033[K  buttons %2d-%-2d  ", row*16, row*16+15)
		for i := row * 16; i < row*16+16 && i < r.NumButtons(); i++ {
			if r.Button(i) {
				fmt.Print("[X]")
			} else {
				fmt.Print("[ ]")
			}
		}
		fmt.Println()
	}

	for i := 0; i < r.NumHats(); i++ {
		fmt.Printf("\033[K  hat %d  %s\n", i, hatName(r.Hat(i)))
	}
	fmt.Print("\033[K\n")
}

// axisBar returns a 40-cell bar centered at zero. -32768 = full-left,
// +32767 = full-right.
func axisBar(v int16, width int) string {
	half := width / 2
	pos := int(int64(v) * int64(half) / 32767)
	if pos > half {
		pos = half
	}
	if pos < -half {
		pos = -half
	}
	b := strings.Repeat("-", width)
	mid := half
	if pos == 0 {
		// Center cursor.
		return string(b[:mid]) + "|" + string(b[mid+1:])
	}
	bs := []byte(b)
	bs[mid] = '|'
	if pos > 0 {
		for i := mid + 1; i <= mid+pos && i < width; i++ {
			bs[i] = '#'
		}
	} else {
		for i := mid - 1; i >= mid+pos && i >= 0; i-- {
			bs[i] = '#'
		}
	}
	return string(bs)
}

func hatName(v uint8) string {
	switch v {
	case 0:
		return "centered"
	case 1:
		return "up"
	case 2:
		return "right"
	case 3:
		return "up-right"
	case 4:
		return "down"
	case 6:
		return "down-right"
	case 8:
		return "left"
	case 9:
		return "up-left"
	case 12:
		return "down-left"
	}
	return fmt.Sprintf("0x%02x", v)
}
