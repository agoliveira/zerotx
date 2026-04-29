// zerotx-inspect dumps a parsed view of an EdgeTX or ZeroTX model file.
// Useful sanity tool while developing the parser; will likely fold into
// zerotxctl when that arrives.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
)

func main() {
	wrap := flag.Bool("zerotx", false, "treat input as a ZeroTX wrapper file (zerotx + edgetx sections)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: zerotx-inspect [-zerotx] <model.yml>")
		os.Exit(2)
	}
	path := flag.Arg(0)

	var m *model.EdgeTXModel
	var bindings map[string]model.Binding
	if *wrap {
		z, err := model.LoadZeroTX(path)
		if err != nil {
			die(err)
		}
		m = &z.EdgeTX
		bindings = z.ZeroTX.SourceBindings
	} else {
		var err error
		m, err = model.LoadEdgeTX(path)
		if err != nil {
			die(err)
		}
	}

	fmt.Printf("model:        %s (semver %s)\n", m.Header.Name, m.Semver)
	fmt.Printf("mix entries:  %d\n", len(m.MixData))
	fmt.Printf("flight modes: %d\n", len(m.FlightModeData))
	fmt.Printf("logical sw:   %d\n", len(m.LogicalSw))
	fmt.Printf("custom fn:    %d\n", len(m.CustomFn))
	fmt.Printf("sensors:      %d\n", len(m.TelemetrySensors))
	fmt.Printf("extras keys:  %d (preserved as Tier 2)\n", len(m.Extras))
	fmt.Println()

	fmt.Println("Channels:")
	for ch := 0; ch < 32; ch++ {
		mix := m.MixForChannel(ch)
		if mix == nil {
			continue
		}
		src := mix.SrcRaw
		// Resolve I0..IN -> input name if available.
		if strings.HasPrefix(src, "I") {
			if n, err := atoi(src[1:]); err == nil {
				if name := m.InputName(n); name != "" {
					src = fmt.Sprintf("%s (%s)", src, name)
				}
			}
		}
		bind := ""
		if b, ok := bindings[mix.SrcRaw]; ok {
			bind = "  binding: " + describeBinding(b)
		}
		name := mix.Name
		if name == "" {
			name = "-"
		}
		fmt.Printf("  CH%-2d  %-22s  weight=%-4d  name=%-8s%s\n",
			ch, src, mix.Weight, name, bind)
	}
	fmt.Println()

	if len(m.FlightModeData) > 0 {
		fmt.Println("Flight modes:")
		for i := 0; i < 16; i++ {
			fm, ok := m.FlightModeData[i]
			if !ok {
				continue
			}
			sw := fm.Swtch
			if sw == "" {
				sw = "(default)"
			}
			fmt.Printf("  %d  %-12s on %s\n", i, fm.Name, sw)
		}
		fmt.Println()
	}
}

func describeBinding(b model.Binding) string {
	switch {
	case b.Axis != nil:
		return fmt.Sprintf("device=%s axis=%d", b.Device, *b.Axis)
	case b.Button != nil:
		return fmt.Sprintf("device=%s button=%d kind=%s", b.Device, *b.Button, b.Kind)
	case b.Switch != nil:
		return fmt.Sprintf("device=%s switch=%d kind=%s", b.Device, *b.Switch, b.Kind)
	case b.Selector != nil:
		return fmt.Sprintf("device=%s selector=%d", b.Device, *b.Selector)
	}
	return fmt.Sprintf("device=%s (unspecified)", b.Device)
}

func atoi(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
