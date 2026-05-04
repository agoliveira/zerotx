// Package tilewarm opportunistically refreshes a small subset of the
// satellite tile cache when the daemon thinks it's at home. The
// motivation is that the cold satellite PMTiles archive (~50 GB
// statewide) is built once and rarely rebuilt, but Esri's imagery
// can update meaningfully (new construction, terrain changes, fresh
// crop colors) on a months-to-years timescale. tilewarm keeps a
// small directory of recent JPGs around the operator's flying area
// and serves them in front of the cold archive.
//
// Design notes:
//
//   - Pure compute + I/O abstraction. The package owns the tile-list
//     enumeration, staleness check, and write-cycle pacing. It does
//     NOT own goroutines, network classification, or the HTTP serving
//     path; the daemon glues those in.
//
//   - The tile store is an interface, so tests use an in-memory fake
//     and the production code uses a small filesystem-backed store
//     (also in this package, see store.go).
//
//   - Network classification is stubbed. The daemon decides whether
//     to call Run at all; tilewarm trusts the caller. This split
//     keeps tilewarm simple and ready for the future internal/netclass
//     subsystem without coupling.
//
//   - "Stale" is time-based: re-fetch tiles whose store-recorded mtime
//     is older than MaxAge. Visit-based ("only refresh tiles you've
//     actually flown over") was considered and rejected for v1: it
//     misses the "I'm going somewhere new tomorrow" case, and the
//     bookkeeping is more complex than the value justifies.
package tilewarm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"golang.org/x/time/rate"
)

// Tile identifies a single tile in the standard slippy-map scheme:
// origin top-left, x increases east, y increases south.
type Tile struct {
	Z int
	X int
	Y int
}

// Store is the persistence abstraction. Implementations supply the
// store age and write paths; tilewarm asks for ages and writes JPG
// bytes when fresh.
type Store interface {
	// Age returns the wall-clock age of the stored tile. ok=false
	// means the tile is not in the store.
	Age(t Tile, now time.Time) (age time.Duration, ok bool)

	// Put writes the tile bytes to the store. The store is
	// responsible for atomic writes, directory creation, and
	// recording the timestamp used by Age.
	Put(t Tile, data []byte) error
}

// Fetcher retrieves a single tile from the upstream source. The
// daemon supplies the implementation (typically the Esri World
// Imagery URL builder + an http.Client). Errors abort that tile;
// the warmer continues with the next one.
type Fetcher func(ctx context.Context, t Tile) ([]byte, error)

// Config controls the warmer's behaviour.
type Config struct {
	// CenterLatDeg, CenterLonDeg define the geographic centre of the
	// warmed region. Typically the operator's home/site coordinates.
	CenterLatDeg, CenterLonDeg float64

	// RadiusKm is the half-side of the bounding square around the
	// centre that defines the warmed region. 5 km covers a
	// comfortable flying area at most clubs.
	RadiusKm float64

	// Zooms is the list of zoom levels to warm. Order doesn't matter;
	// duplicates are deduplicated. v1 default is [15, 16].
	Zooms []int

	// MaxAge is the staleness threshold. Tiles whose store age exceeds
	// this are re-fetched. Tiles missing from the store are always
	// fetched.
	MaxAge time.Duration

	// RatePerSec caps the upstream request rate. tilewarm runs in
	// background and is intentionally gentler than sat-download.
	// Default 2 req/s.
	RatePerSec float64
}

// DefaultConfig returns sensible v1 defaults: zooms 15-16, 5km radius,
// 30-day max age, 2 req/s.
func DefaultConfig(latDeg, lonDeg float64) Config {
	return Config{
		CenterLatDeg: latDeg,
		CenterLonDeg: lonDeg,
		RadiusKm:     5,
		Zooms:        []int{15, 16},
		MaxAge:       30 * 24 * time.Hour,
		RatePerSec:   2,
	}
}

// Stats summarises the outcome of a single Run.
type Stats struct {
	Considered int
	Skipped    int // present in store and fresh
	Fetched    int
	Errors     int
	Duration   time.Duration
}

func (s Stats) String() string {
	return fmt.Sprintf("considered=%d skipped=%d fetched=%d errors=%d in %s",
		s.Considered, s.Skipped, s.Fetched, s.Errors, s.Duration.Round(time.Second))
}

// Run performs one warming pass: enumerate tiles in the configured
// region, check store ages, fetch stale/missing tiles at the
// configured rate, store them. Cancellable via context. Returns the
// stats; an error is returned only for hard configuration faults
// (e.g. invalid bounds). Per-tile fetch failures are counted as
// errors but do not abort the pass.
func Run(ctx context.Context, cfg Config, store Store, fetch Fetcher) (Stats, error) {
	if store == nil {
		return Stats{}, errors.New("tilewarm: store is required")
	}
	if fetch == nil {
		return Stats{}, errors.New("tilewarm: fetcher is required")
	}
	if cfg.RadiusKm <= 0 {
		return Stats{}, errors.New("tilewarm: RadiusKm must be > 0")
	}
	if cfg.MaxAge <= 0 {
		return Stats{}, errors.New("tilewarm: MaxAge must be > 0")
	}
	if cfg.RatePerSec <= 0 {
		cfg.RatePerSec = 2
	}
	if len(cfg.Zooms) == 0 {
		cfg.Zooms = []int{15, 16}
	}

	tiles := enumerateTiles(cfg)
	limiter := rate.NewLimiter(rate.Limit(cfg.RatePerSec), 1)

	start := time.Now()
	var st Stats
	st.Considered = len(tiles)

	for _, t := range tiles {
		if ctx.Err() != nil {
			break
		}
		age, present := store.Age(t, time.Now())
		if present && age <= cfg.MaxAge {
			st.Skipped++
			continue
		}

		if err := limiter.Wait(ctx); err != nil {
			break
		}

		data, err := fetch(ctx, t)
		if err != nil {
			st.Errors++
			log.Printf("tilewarm: fetch z=%d x=%d y=%d: %v", t.Z, t.X, t.Y, err)
			continue
		}
		if err := store.Put(t, data); err != nil {
			st.Errors++
			log.Printf("tilewarm: store z=%d x=%d y=%d: %v", t.Z, t.X, t.Y, err)
			continue
		}
		st.Fetched++
	}
	st.Duration = time.Since(start)
	return st, nil
}

// enumerateTiles converts the centre+radius config into a sorted list
// of unique Tile values across the configured zoom levels. Sorted by
// (z, x, y) so the warm pass is deterministic; tests can rely on the
// order, and the first tile fetched is always the same one.
func enumerateTiles(cfg Config) []Tile {
	bbox := bboxAroundCenter(cfg.CenterLatDeg, cfg.CenterLonDeg, cfg.RadiusKm)

	seen := map[Tile]struct{}{}
	for _, z := range cfg.Zooms {
		if z < 0 || z > 22 {
			continue
		}
		x0, y0 := lonLatToTile(bbox.West, bbox.North, z)
		x1, y1 := lonLatToTile(bbox.East, bbox.South, z)
		for x := x0; x <= x1; x++ {
			for y := y0; y <= y1; y++ {
				seen[Tile{Z: z, X: x, Y: y}] = struct{}{}
			}
		}
	}
	out := make([]Tile, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Z != out[j].Z {
			return out[i].Z < out[j].Z
		}
		if out[i].X != out[j].X {
			return out[i].X < out[j].X
		}
		return out[i].Y < out[j].Y
	})
	return out
}

// bbox is the lat/lon bounding box of the warmed region.
type bbox struct {
	North, South, East, West float64
}

// bboxAroundCenter returns a square bounding box of half-side
// radiusKm centred on (latDeg, lonDeg). Approximation: 1 degree of
// latitude is ~111 km everywhere; longitude scales with cos(lat).
// At Brazilian club latitudes (-22°) the longitude scale is ~103
// km/deg, so the returned "square" is slightly elongated east-west
// in degree space but maps to a true square on the ground. For
// 5 km radii this is well within tile granularity.
func bboxAroundCenter(latDeg, lonDeg, radiusKm float64) bbox {
	const kmPerDegLat = 111.0
	dLat := radiusKm / kmPerDegLat
	cosLat := math.Cos(latDeg * math.Pi / 180)
	if cosLat < 0.01 {
		cosLat = 0.01
	}
	dLon := radiusKm / (kmPerDegLat * cosLat)
	return bbox{
		North: latDeg + dLat,
		South: latDeg - dLat,
		East:  lonDeg + dLon,
		West:  lonDeg - dLon,
	}
}

// lonLatToTile is the standard slippy-map lon/lat -> tile (x, y)
// conversion at zoom z. Returned values are clamped to valid tile
// indices for the zoom level.
func lonLatToTile(lonDeg, latDeg float64, z int) (x, y int) {
	n := math.Exp2(float64(z))
	xf := (lonDeg + 180.0) / 360.0 * n
	latRad := latDeg * math.Pi / 180
	yf := (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n
	x = clampInt(int(math.Floor(xf)), 0, int(n)-1)
	y = clampInt(int(math.Floor(yf)), 0, int(n)-1)
	return x, y
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
