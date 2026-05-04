package weather

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Service coordinates a Source, a Cache, and a CoordResolver. It owns
// a background goroutine (started by Run) that periodically refreshes
// the cached weather for the observer's current location.
//
// Two consumer-facing methods:
//
//   - GetCurrent: "give me the weather wherever I am right now". Reads
//     from the cache without triggering a fetch. Returns an empty
//     Weather and ok=false when there's no resolved location or no
//     cached entry. This is the modal UI's path.
//
//   - Get: "give me the weather at these specific coordinates".
//     Returns cache hit if recent enough, otherwise fetches inline and
//     caches the result. This is the developer-convenience path
//     (?lat=...&lon=... on the API).
//
// The Service is designed to be silently absent when offline: a fetch
// failure is logged but does not propagate as an error to the modal,
// because there's nothing the user can do about it. The cached entry
// (if any) keeps serving until a fresh fetch succeeds.
type Service struct {
	src     Source
	cache   *Cache
	resolve CoordResolver

	refreshInterval time.Duration
	maxAge          time.Duration

	mu     sync.Mutex
	closed bool
}

// Options is the constructor input for Service.
type Options struct {
	// Source produces weather data. Required.
	Source Source

	// Cache stores fetched results. Required.
	Cache *Cache

	// Resolver returns the current observer coordinates. Required.
	Resolver CoordResolver

	// RefreshInterval is the cadence at which the background loop
	// fetches new data when coordinates resolve. Empty defaults to
	// 10 minutes (the home-network value; conservative networks will
	// override this once the netclass package ships).
	RefreshInterval time.Duration

	// MaxAge is the cutoff beyond which a cached entry is considered
	// too old to serve as-is from Get. The entry is still returned
	// (with its original FetchedAt) but Get will trigger an inline
	// fetch first. Empty defaults to 1 hour.
	MaxAge time.Duration
}

// New constructs a Service. Callers must call Run in a goroutine to
// start the background refresher.
func New(opts Options) (*Service, error) {
	if opts.Source == nil {
		return nil, errors.New("weather: Source is required")
	}
	if opts.Cache == nil {
		return nil, errors.New("weather: Cache is required")
	}
	if opts.Resolver == nil {
		return nil, errors.New("weather: Resolver is required")
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = 10 * time.Minute
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = 1 * time.Hour
	}
	return &Service{
		src:             opts.Source,
		cache:           opts.Cache,
		resolve:         opts.Resolver,
		refreshInterval: opts.RefreshInterval,
		maxAge:          opts.MaxAge,
	}, nil
}

// Run starts the background refresher. Blocks until ctx is cancelled.
// Run is safe to invoke once per Service; subsequent calls return
// immediately.
func (s *Service) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("weather: Service already run")
	}
	s.closed = true
	s.mu.Unlock()

	// Eager first refresh on startup if coordinates are available.
	s.refreshOnce(ctx)

	t := time.NewTicker(s.refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.refreshOnce(ctx)
		}
	}
}

// GetCurrent returns the cached weather for the observer's current
// location. ok=false on no resolved coordinates or no cached entry.
// Does not trigger a fetch (the background loop owns that path).
func (s *Service) GetCurrent() (Weather, string, bool) {
	lat, lon, src, ok := s.resolve()
	if !ok {
		return Weather{}, "", false
	}
	w, ok := s.cache.Get(lat, lon)
	if !ok {
		return Weather{}, src, false
	}
	return w, src, true
}

// Get returns the cached weather for explicit coordinates. If the
// cache entry is missing or older than MaxAge, an inline fetch is
// attempted; on fetch error, returns the stale cache entry if one
// exists, or a fresh error.
func (s *Service) Get(ctx context.Context, latDeg, lonDeg float64) (Weather, error) {
	now := time.Now()
	cached, hit := s.cache.Get(latDeg, lonDeg)
	if hit && now.Sub(cached.FetchedAt) < s.maxAge {
		return cached, nil
	}

	fresh, err := s.src.Fetch(ctx, latDeg, lonDeg)
	if err != nil {
		if hit {
			// Serve stale rather than error out.
			return cached, nil
		}
		return Weather{}, fmt.Errorf("weather: fetch: %w", err)
	}
	// Cache under the requested coords too: Open-Meteo snaps to its
	// nearest grid point, so the response's LatDeg/LonDeg may round to
	// a different key than what was asked for. Aliasing keeps subsequent
	// lookups by the original request coords cheap.
	s.cache.PutAt(latDeg, lonDeg, fresh)
	return fresh, nil
}

// refreshOnce performs a single refresh tick: resolve coordinates,
// fetch, store. Errors are logged but never propagated; the caller
// (Run) will retry on the next tick.
func (s *Service) refreshOnce(ctx context.Context) {
	lat, lon, src, ok := s.resolve()
	if !ok {
		// No coordinates yet (no GPS, no home, no configured site).
		// Quiet skip: the daemon may be in pre-arm with a cold GPS.
		return
	}

	// Fetch with a per-call timeout independent of the parent ctx,
	// so a slow source can't block the next refresh tick beyond its
	// own bound.
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fresh, err := s.src.Fetch(fetchCtx, lat, lon)
	if err != nil {
		log.Printf("weather: refresh failed (%s, lat=%.4f lon=%.4f): %v",
			src, lat, lon, err)
		return
	}
	s.cache.PutAt(lat, lon, fresh)
	log.Printf("weather: refreshed from %s for %s lat=%.4f lon=%.4f temp=%.1fC wind=%.1fkmh",
		fresh.Source, src, lat, lon, fresh.Current.TempC, fresh.Current.WindSpeedKmh)
}
