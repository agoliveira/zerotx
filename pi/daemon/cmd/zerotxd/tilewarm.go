package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/tilewarm"
)

// runTileWarm is the daemon-side glue for the internal/tilewarm
// package. It owns the goroutine cadence (boot + every 24h),
// network classification (currently stubbed to "always home"),
// and the upstream fetcher (Esri World Imagery for satellite).
//
// Cancellation: respects ctx; one in-flight Run completes before
// returning when ctx is cancelled mid-pass.
func runTileWarm(ctx context.Context, store tilewarm.Store, cfg tilewarm.Config) {
	if store == nil {
		return
	}

	doRun := func(reason string) {
		if !isHome() {
			log.Printf("tilewarm: skipped (%s; not at home)", reason)
			return
		}
		log.Printf("tilewarm: starting (%s) center=(%.4f,%.4f) radius=%.1fkm zooms=%v maxAge=%s rate=%.1f/s",
			reason, cfg.CenterLatDeg, cfg.CenterLonDeg, cfg.RadiusKm,
			cfg.Zooms, cfg.MaxAge, cfg.RatePerSec)
		st, err := tilewarm.Run(ctx, cfg, store, esriSatelliteFetcher)
		if err != nil {
			log.Printf("tilewarm: error: %v", err)
			return
		}
		log.Printf("tilewarm: %s", st)
	}

	// Run once shortly after boot, not immediately, so the daemon
	// finishes its other startup work first. 60s is comfortable.
	bootDelay := time.NewTimer(60 * time.Second)
	defer bootDelay.Stop()

	periodic := time.NewTicker(24 * time.Hour)
	defer periodic.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-bootDelay.C:
			doRun("boot")
		case <-periodic.C:
			doRun("periodic")
		}
	}
}

// isHome is the network-classification stub. Always returns true for
// v1; the future internal/netclass subsystem will replace this with
// per-SSID logic (ethernet always home, configured WiFi SSIDs home,
// everything else field).
func isHome() bool {
	return true
}

// esriSatelliteFetcher fetches a satellite tile from Esri's World
// Imagery service. The URL scheme is the same as the existing
// onlineTileURL helper in internal/api; duplicated here since we
// can't import that (api -> tilewarm-glue would be circular).
func esriSatelliteFetcher(ctx context.Context, t tilewarm.Tile) ([]byte, error) {
	url := fmt.Sprintf(
		"https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/%d/%d/%d",
		t.Z, t.Y, t.X,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZeroTX/1.0 tilewarm (https://github.com/agoliveira/zerotx)")

	resp, err := tileWarmHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// tileWarmHTTPClient is the HTTP client for warm-cache fetches. Longer
// timeout than the live tile proxy because tilewarm runs in the
// background and a slow tile is fine.
var tileWarmHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}
