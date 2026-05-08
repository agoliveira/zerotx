package heartbeat

// null is a no-op Driver. Used when -heartbeat-gpio is left at the
// disabled default, or in tests that don't care about driver state.
type null struct{}

// NewNull returns a Driver that discards all writes.
func NewNull() Driver { return null{} }

// SetValue ignores its argument.
func (null) SetValue(int) error { return nil }

// Close is a no-op.
func (null) Close() error { return nil }
