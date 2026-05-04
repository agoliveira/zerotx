package weather

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Cache stores recent Weather results keyed by rounded coordinates
// (~1 km granularity). Survives daemon restarts via DiskDir; safe
// for concurrent use.
//
// The disk layout is one JSON file per coordinate key, named
// "<latRounded>_<lonRounded>.json", with both values formatted to two
// decimal places. Stale files are not garbage-collected automatically
// (a flight site visited once a year shouldn't lose its history).
type Cache struct {
	diskDir string

	mu      sync.RWMutex
	entries map[string]Weather
}

// NewCache constructs a Cache. If diskDir is non-empty, existing JSON
// files in the directory are loaded into memory at construction time.
// A non-empty diskDir that doesn't exist yet is created (Run-time
// failures to write back are logged, not returned, so the live data
// path is unaffected by disk problems).
func NewCache(diskDir string) (*Cache, error) {
	c := &Cache{
		diskDir: diskDir,
		entries: make(map[string]Weather),
	}
	if diskDir == "" {
		return c, nil
	}
	if err := os.MkdirAll(diskDir, 0o755); err != nil {
		return nil, fmt.Errorf("weather cache: mkdir: %w", err)
	}
	entries, err := os.ReadDir(diskDir)
	if err != nil {
		return nil, fmt.Errorf("weather cache: readdir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(diskDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var w Weather
		if err := json.Unmarshal(raw, &w); err != nil {
			continue
		}
		key := keyFor(w.LatDeg, w.LonDeg)
		c.entries[key] = w
	}
	return c, nil
}

// Get returns the cached weather for the given coordinates and a flag
// indicating whether an entry exists. The lookup uses the same
// rounding as Put, so callers can pass raw GPS values without
// pre-rounding.
func (c *Cache) Get(latDeg, lonDeg float64) (Weather, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	w, ok := c.entries[keyFor(latDeg, lonDeg)]
	return w, ok
}

// Put stores a weather entry. The entry's lat/lon fields are normalized
// to the rounded form before storage so disk filenames stay clean.
// Disk write errors are silently dropped; the in-memory entry is
// always kept (callers should not block on cache writes).
func (c *Cache) Put(w Weather) {
	c.put(w, w.LatDeg, w.LonDeg)
}

// PutAt stores a weather entry under the rounded form of (latDeg, lonDeg),
// independent of the entry's own LatDeg/LonDeg fields. Used when the
// source returns nudged coordinates (e.g. Open-Meteo snapping to its
// nearest grid point) but the daemon should be able to find the entry
// later via the originally-requested coordinates.
//
// If (latDeg, lonDeg) round to a different key than the entry's own
// fields, both keys point at the same value so a future lookup by
// either coordinate hits.
func (c *Cache) PutAt(latDeg, lonDeg float64, w Weather) {
	c.put(w, latDeg, lonDeg)
	if keyFor(latDeg, lonDeg) != keyFor(w.LatDeg, w.LonDeg) {
		c.put(w, w.LatDeg, w.LonDeg)
	}
}

func (c *Cache) put(w Weather, keyLat, keyLon float64) {
	rLat, rLon := round2(keyLat), round2(keyLon)
	// The entry's own fields always reflect the source-reported coords
	// (so consumers see the actual grid point); only the cache key uses
	// the alias coordinates.
	key := keyFor(rLat, rLon)

	c.mu.Lock()
	c.entries[key] = w
	c.mu.Unlock()

	if c.diskDir == "" {
		return
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return
	}
	path := filepath.Join(c.diskDir, key+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// Age returns the time elapsed since FetchedAt for the cached entry,
// or (0, false) if no entry exists. Callers use this to decide whether
// a refresh is due.
func (c *Cache) Age(latDeg, lonDeg float64, now time.Time) (time.Duration, bool) {
	w, ok := c.Get(latDeg, lonDeg)
	if !ok {
		return 0, false
	}
	return now.Sub(w.FetchedAt), true
}

// keyFor produces the disk-and-map key for a coordinate pair.
// Format is "<lat>_<lon>" with each component formatted to two
// decimal places, signed (so negatives are preserved).
func keyFor(latDeg, lonDeg float64) string {
	return fmt.Sprintf("%+07.2f_%+08.2f", round2(latDeg), round2(lonDeg))
}

// round2 rounds a coordinate to two decimal places (~1 km granularity
// at typical latitudes), so a moving GPS fix doesn't churn cache keys.
func round2(x float64) float64 {
	return math.Round(x*100) / 100
}
