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
	var meta *model.ZeroTXMeta
	if *wrap {
		z, err := model.LoadZeroTX(path)
		if err != nil {
			die(err)
		}
		m = &z.EdgeTX
		bindings = z.ZeroTX.SourceBindings
		meta = &z.ZeroTX
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
	if meta != nil && meta.Airframe != "" {
		fmt.Printf("airframe:     %s\n", meta.Airframe)
	}
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

	if meta != nil && meta.Thresholds != nil {
		printThresholds(meta.Thresholds)
	}

	if meta != nil && strings.TrimSpace(meta.Notes) != "" {
		fmt.Println("Notes:")
		for _, line := range strings.Split(strings.TrimRight(meta.Notes, "\n"), "\n") {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println()
	}
}

// printThresholds writes the alarm-band configuration to stdout in
// the same column-aligned style as the rest of the dump. Domains
// without thresholds are skipped silently.
func printThresholds(t *model.Thresholds) {
	if t == nil {
		return
	}
	hasAny := t.Battery != nil || t.Altitude != nil || t.Distance != nil ||
		t.Link != nil || t.FlightTime != nil
	if !hasAny {
		return
	}
	fmt.Println("Thresholds:")
	if b := t.Battery; b != nil {
		fmt.Printf("  battery:     %dS\n", b.Cells)
		// Show per-cell and derived pack voltages so the operator
		// can sanity-check both at a glance.
		fmt.Printf("               warn  %.2fV/cell  (pack %.2fV)\n",
			b.CellWarnV, b.CellWarnV*float64(b.Cells))
		fmt.Printf("               crit  %.2fV/cell  (pack %.2fV)\n",
			b.CellCritV, b.CellCritV*float64(b.Cells))
		fmt.Printf("               min   %.2fV/cell  (pack %.2fV)\n",
			b.CellMinV, b.CellMinV*float64(b.Cells))
		fmt.Printf("               full  %.2fV/cell  (pack %.2fV)\n",
			b.CellFullV, b.CellFullV*float64(b.Cells))
	}
	if a := t.Altitude; a != nil {
		fmt.Printf("  altitude:    warn %dm   crit %dm\n", a.WarnM, a.CritM)
	}
	if d := t.Distance; d != nil {
		fmt.Printf("  distance:    warn %dm   crit %dm\n", d.WarnM, d.CritM)
	}
	if l := t.Link; l != nil {
		fmt.Printf("  link RSSI:   warn %ddBm  crit %ddBm\n", l.RSSIWarnDBM, l.RSSICritDBM)
		fmt.Printf("  link LQ:     warn %d%%   crit %d%%\n", l.LQWarnPct, l.LQCritPct)
	}
	if ft := t.FlightTime; ft != nil {
		fmt.Printf("  flight_time: warn %ds   crit %ds\n", ft.WarnS, ft.CritS)
	}
	fmt.Println()
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
