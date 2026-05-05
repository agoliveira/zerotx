package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/netclass"
	"github.com/agoliveira/zerotx/pi/daemon/internal/tilewarm"
)

// tileWarmStats records the most recent tilewarm run for the metrics
// endpoint to expose. Single-writer (the runTileWarm goroutine);
// readers take the mutex briefly.
type tileWarmStats struct {
	mu          sync.Mutex
	lastRunAt   time.Time
	lastReason  string
	lastResult  tilewarm.Stats
	lastError   string
	totalRuns   int64
	totalErrors int64
}

func (s *tileWarmStats) record(reason string, st tilewarm.Stats, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRunAt = time.Now()
	s.lastReason = reason
	s.lastResult = st
	if err != nil {
		s.lastError = err.Error()
		s.totalErrors++
	} else {
		s.lastError = ""
	}
	s.totalRuns++
}

func (s *tileWarmStats) snapshot() (lastRun time.Time, reason string, result tilewarm.Stats, lastErr string, totalRuns, totalErrors int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRunAt, s.lastReason, s.lastResult, s.lastError, s.totalRuns, s.totalErrors
}

// runTileWarm is the daemon-side glue for the internal/tilewarm
// package. It owns the goroutine cadence (boot + every 24h),
// network-class gating, and the upstream fetcher (Esri World Imagery
// for satellite).
//
// Cancellation: respects ctx; one in-flight Run completes before
// returning when ctx is cancelled mid-pass.
func runTileWarm(ctx context.Context, store tilewarm.Store, cfg tilewarm.Config, ncHolder *netclass.Holder, stats *tileWarmStats) {
	if store == nil {
		return
	}

	doRun := func(reason string) {
		class := currentClass(ncHolder)
		if !class.AllowsBackgroundInternet() {
			log.Printf("tilewarm: skipped (%s; netclass=%s)", reason, class)
			return
		}
		log.Printf("tilewarm: starting (%s) class=%s center=(%.4f,%.4f) radius=%.1fkm zooms=%v maxAge=%s rate=%.1f/s",
			reason, class, cfg.CenterLatDeg, cfg.CenterLonDeg, cfg.RadiusKm,
			cfg.Zooms, cfg.MaxAge, cfg.RatePerSec)
		st, err := tilewarm.Run(ctx, cfg, store, esriSatelliteFetcher)
		if stats != nil {
			stats.record(reason, st, err)
		}
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

// currentClass returns the current network class, or netclass.Home as
// a fallback when the holder is nil (subsystem disabled). Disabling
// netclass means the operator opted out; we treat that as "always
// allowed" rather than blocking everything.
func currentClass(h *netclass.Holder) netclass.Class {
	if h == nil {
		return netclass.Home
	}
	return h.Current()
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
