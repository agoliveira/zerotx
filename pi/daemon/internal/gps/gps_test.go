package gps

import (
	"context"
	"io"
	"testing"
	"time"
)

// pipeRC adapts an io.PipeReader to io.ReadCloser. The pipe writer
// closes are signaled by writing-side Close, which yields io.EOF on
// read, which the Reader handles cleanly.
type pipeRC struct{ *io.PipeReader }

func (p pipeRC) Close() error { return p.PipeReader.Close() }

// TestReader_StreamsState writes a couple of sentences through a pipe
// and confirms the Reader picks up the resulting state.
func TestReader_StreamsState(t *testing.T) {
	pr, pw := io.Pipe()
	r := New(pipeRC{pr})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Close()

	gga := withChecksum("GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,")
	rmc := withChecksum("GPRMC,225446,A,4916.45,N,12311.12,W,100.0,084.4,191194,003.1,W")

	go func() {
		_, _ = pw.Write([]byte(gga + "\r\n" + rmc + "\r\n"))
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		s := r.Get()
		if s.Sats == 8 && s.SpeedKmh > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state never settled, last: %+v", r.Get())
}

// TestReader_BadLineDoesntStop confirms that a malformed sentence in
// the middle of the stream doesn't stop the reader from picking up
// the next valid one.
func TestReader_BadLineDoesntStop(t *testing.T) {
	pr, pw := io.Pipe()
	r := New(pipeRC{pr})
	r.errLogInterval = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Close()

	good := withChecksum("GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,")

	go func() {
		_, _ = pw.Write([]byte(
			"garbage line with no dollar sign\r\n" +
				"$GPGGA,not,a,real,sentence,*ZZ\r\n" +
				good + "\r\n",
		))
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r.Get().Sats == 8 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("good sentence after bad ones never landed: %+v", r.Get())
}

// TestReader_GetIsThreadSafe confirms many concurrent Get calls don't
// race against the parser goroutine. With -race this would fail if
// the state were a plain pointer.
func TestReader_GetIsThreadSafe(t *testing.T) {
	pr, pw := io.Pipe()
	r := New(pipeRC{pr})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Close()

	gga := withChecksum("GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,")

	// Continuous writer.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_, _ = pw.Write([]byte(gga + "\r\n"))
		}
		_ = pw.Close()
	}()

	// Concurrent readers.
	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 1000; j++ {
				_ = r.Get()
			}
		}()
	}

	<-done
}

// TestReader_CloseIsIdempotent confirms double Close is safe.
func TestReader_CloseIsIdempotent(t *testing.T) {
	pr, _ := io.Pipe()
	r := New(pipeRC{pr})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
