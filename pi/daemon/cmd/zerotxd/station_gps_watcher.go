package main

import (
	"context"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/gps"
	"github.com/agoliveira/zerotx/pi/daemon/internal/narrator"
)

// runStationGPSWatcher polls the operator/ground-station GPS reader
// at tick and speaks a single TTS announcement the first time it
// observes a 2D-or-better fix. After speaking once, the goroutine
// exits; loss and re-acquisition events are deliberately not narrated
// to avoid TTS chatter from a flapping fix during pre-flight or in
// flight. The HUD station indicator and the API station block remain
// the live status surfaces; this routine is purely the boot-time
// "GPS works" confirmation pair to the boot greeting.
//
// No-ops if rdr is nil (no operator GPS configured) or narr is nil
// (defensive; should not happen in practice). Stops on ctx.Done.
//
// tick is the polling interval; the production caller passes 1s.
// Tests pass a few milliseconds.
func runStationGPSWatcher(ctx context.Context, rdr *gps.Reader, narr *narrator.Narrator, tick time.Duration) {
	if rdr == nil || narr == nil {
		return
	}
	if tick <= 0 {
		tick = 1 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if rdr.Get().Fix >= gps.Fix2D {
				narr.SpeakStationGPSAcquired()
				return
			}
		}
	}
}
