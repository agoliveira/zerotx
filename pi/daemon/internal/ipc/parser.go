package ipc

// Parser turns a byte stream of COBS-framed frames into discrete Frame values.
// Frames are delimited by 0x00. Bytes are accumulated until a delimiter
// arrives, then COBS-decoded and CRC-validated.
//
// Parser is not safe for concurrent use; the typical pattern is one goroutine
// reading from a serial port that owns its own Parser.
type Parser struct {
	buf      []byte
	overflow bool
}

// NewParser returns a fresh parser with no buffered bytes.
func NewParser() *Parser {
	return &Parser{buf: make([]byte, 0, MaxFrameCOBS)}
}

// Reset clears any buffered bytes. Useful after a wire-level error to discard
// the partial frame.
func (p *Parser) Reset() {
	p.buf = p.buf[:0]
	p.overflow = false
}

// Feed appends bytes and returns any complete frames it can decode. Bytes
// causing overflow or decode errors are silently dropped at the next 0x00.
// Errors that come from validation (length, CRC) are returned alongside any
// frames already decoded in this call.
func (p *Parser) Feed(in []byte) ([]Frame, error) {
	var frames []Frame
	var lastErr error
	for _, b := range in {
		if b == 0 {
			if p.overflow || len(p.buf) == 0 {
				p.buf = p.buf[:0]
				p.overflow = false
				continue
			}
			decoded, err := COBSDecode(p.buf)
			p.buf = p.buf[:0]
			if err != nil {
				lastErr = err
				continue
			}
			f, err := ParseFrame(decoded)
			if err != nil {
				lastErr = err
				continue
			}
			// ParseFrame returned a slice into `decoded`, which is a fresh
			// allocation, so the caller can hold the payload safely until they
			// process it. Copy here to be defensive against future reuse.
			pl := make([]byte, len(f.Payload))
			copy(pl, f.Payload)
			f.Payload = pl
			frames = append(frames, f)
			continue
		}
		if len(p.buf) >= MaxFrameCOBS {
			p.overflow = true
			continue
		}
		p.buf = append(p.buf, b)
	}
	return frames, lastErr
}
