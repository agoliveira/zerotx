package heartbeat

import (
	"fmt"

	"github.com/warthog618/go-gpiocdev"
)

// real wraps a gpiocdev line as a Driver.
type real struct {
	line *gpiocdev.Line
}

// NewReal opens the given line on the named GPIO chip and returns
// a Driver that writes the line. The line is configured as an output
// initialized low. Caller closes via the returned Driver's Close().
//
// chip is typically "gpiochip0" on a Raspberry Pi. line is the BCM
// GPIO number (i.e. for header pin 11 on a Pi 400 use line=17).
//
// The constructor labels the line "zerotx-heartbeat" so it shows up
// recognizably in lsgpio / gpioinfo.
func NewReal(chip string, line int) (Driver, error) {
	if chip == "" {
		chip = "gpiochip0"
	}
	l, err := gpiocdev.RequestLine(
		chip, line,
		gpiocdev.AsOutput(0),
		gpiocdev.WithConsumer("zerotx-heartbeat"),
	)
	if err != nil {
		return nil, fmt.Errorf("heartbeat: open %s line %d: %w", chip, line, err)
	}
	return &real{line: l}, nil
}

// SetValue writes the line high or low.
func (r *real) SetValue(v int) error {
	if v != 0 {
		v = 1
	}
	return r.line.SetValue(v)
}

// Close releases the line.
func (r *real) Close() error {
	if r.line == nil {
		return nil
	}
	err := r.line.Close()
	r.line = nil
	return err
}
