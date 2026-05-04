package tilewarm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory Store. age() returns the elapsed time
// since the last Put for that tile, scaled by the test's clock.
type fakeStore struct {
	mu      sync.Mutex
	written map[Tile][]byte
	putAt   map[Tile]time.Time
	// putErr forces Put to fail for the given tile (for error-path tests).
	putErr map[Tile]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		written: map[Tile][]byte{},
		putAt:   map[Tile]time.Time{},
		putErr:  map[Tile]error{},
	}
}

func (f *fakeStore) Age(t Tile, now time.Time) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	at, ok := f.putAt[t]
	if !ok {
		return 0, false
	}
	return now.Sub(at), true
}

func (f *fakeStore) Put(t Tile, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.putErr[t]; ok {
		return err
	}
	f.written[t] = data
	f.putAt[t] = time.Now()
	return nil
}

// preload simulates a tile already present in store with a given age.
func (f *fakeStore) preload(t Tile, age time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putAt[t] = time.Now().Add(-age)
}

func (f *fakeStore) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.written)
}

// ----------------------------------------------------------------------------
// Geometry & enumeration
// ----------------------------------------------------------------------------

func TestLonLatToTile_KnownPoints(t *testing.T) {
	// Reference: standard slippy-map tilemath. Campinas (-22.91, -47.06)
	// at zoom 15 is around tile (12126, 18527). Bounds chosen wide
	// enough to absorb floating-point drift but tight enough to catch
	// scheme errors (sign flips, off-by-180 etc).
	x, y := lonLatToTile(-47.06, -22.91, 15)
	if x < 12100 || x > 12150 {
		t.Errorf("x out of expected range for Campinas z=15: %d", x)
	}
	if y < 18500 || y > 18550 {
		t.Errorf("y out of expected range for Campinas z=15: %d", y)
	}
}

func TestBboxAroundCenter_5km(t *testing.T) {
	b := bboxAroundCenter(-22.91, -47.06, 5)
	dLat := b.North - b.South
	if dLat < 0.08 || dLat > 0.10 {
		t.Errorf("5km radius should yield ~0.09° lat span; got %.4f", dLat)
	}
	dLon := b.East - b.West
	// At lat -22.91, cos ≈ 0.921, so dLon ≈ 0.0977
	if dLon < 0.09 || dLon > 0.11 {
		t.Errorf("5km radius at lat -22.91 should yield ~0.097° lon span; got %.4f", dLon)
	}
}

func TestEnumerateTiles_DeterministicOrder(t *testing.T) {
	cfg := DefaultConfig(-22.91, -47.06)
	cfg.RadiusKm = 1
	cfg.Zooms = []int{15}

	a := enumerateTiles(cfg)
	b := enumerateTiles(cfg)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("enumerateTiles is non-deterministic across calls")
	}
	// Verify sort: (z, x, y) ascending.
	if !sort.SliceIsSorted(a, func(i, j int) bool {
		if a[i].Z != a[j].Z {
			return a[i].Z < a[j].Z
		}
		if a[i].X != a[j].X {
			return a[i].X < a[j].X
		}
		return a[i].Y < a[j].Y
	}) {
		t.Errorf("enumerateTiles not sorted")
	}
}

func TestEnumerateTiles_DedupsAcrossZooms(t *testing.T) {
	cfg := DefaultConfig(-22.91, -47.06)
	cfg.RadiusKm = 0.5
	cfg.Zooms = []int{15, 15, 16, 16} // duplicates
	tiles := enumerateTiles(cfg)
	seen := map[Tile]int{}
	for _, t := range tiles {
		seen[t]++
	}
	for tile, count := range seen {
		if count > 1 {
			t.Errorf("tile %v appears %d times", tile, count)
		}
	}
}

// ----------------------------------------------------------------------------
// Run behaviour
// ----------------------------------------------------------------------------

func TestRun_FetchesMissingTiles(t *testing.T) {
	cfg := smallConfig()
	store := newFakeStore()
	fetched := 0
	fetcher := func(ctx context.Context, _ Tile) ([]byte, error) {
		fetched++
		return []byte("jpg"), nil
	}
	st, err := Run(context.Background(), cfg, store, fetcher)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Fetched == 0 {
		t.Errorf("expected some fetches; got %d", st.Fetched)
	}
	if st.Fetched != fetched {
		t.Errorf("Stats.Fetched=%d but fetcher called %d times", st.Fetched, fetched)
	}
	if st.Skipped != 0 {
		t.Errorf("expected no skips on empty store; got %d", st.Skipped)
	}
	if store.putCount() != st.Fetched {
		t.Errorf("store puts %d != Stats.Fetched %d", store.putCount(), st.Fetched)
	}
}

func TestRun_SkipsFreshTiles(t *testing.T) {
	cfg := smallConfig()
	store := newFakeStore()
	tiles := enumerateTiles(cfg)
	// Preload all tiles as fresh (age = 1 day, well below 30-day MaxAge).
	for _, t := range tiles {
		store.preload(t, 24*time.Hour)
	}
	fetcher := func(ctx context.Context, _ Tile) ([]byte, error) {
		return []byte("jpg"), nil
	}
	st, err := Run(context.Background(), cfg, store, fetcher)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Fetched != 0 {
		t.Errorf("fresh tiles should not be re-fetched; got %d", st.Fetched)
	}
	if st.Skipped != len(tiles) {
		t.Errorf("expected all %d tiles skipped; got %d", len(tiles), st.Skipped)
	}
}

func TestRun_FetchesStaleTiles(t *testing.T) {
	cfg := smallConfig()
	store := newFakeStore()
	tiles := enumerateTiles(cfg)
	// Preload all tiles as stale (age > MaxAge).
	for _, t := range tiles {
		store.preload(t, cfg.MaxAge+24*time.Hour)
	}
	fetcher := func(ctx context.Context, _ Tile) ([]byte, error) {
		return []byte("jpg"), nil
	}
	st, err := Run(context.Background(), cfg, store, fetcher)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Fetched != len(tiles) {
		t.Errorf("expected all %d stale tiles refetched; got %d", len(tiles), st.Fetched)
	}
}

func TestRun_FetcherError_CountsAndContinues(t *testing.T) {
	cfg := smallConfig()
	store := newFakeStore()
	calls := 0
	fetcher := func(ctx context.Context, _ Tile) ([]byte, error) {
		calls++
		if calls%2 == 0 {
			return nil, errors.New("upstream 503")
		}
		return []byte("jpg"), nil
	}
	st, err := Run(context.Background(), cfg, store, fetcher)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Errors == 0 {
		t.Errorf("expected some errors counted")
	}
	if st.Fetched+st.Errors != st.Considered {
		t.Errorf("fetched(%d) + errors(%d) != considered(%d)",
			st.Fetched, st.Errors, st.Considered)
	}
}

func TestRun_ContextCancel_StopsCleanly(t *testing.T) {
	cfg := smallConfig()
	store := newFakeStore()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	fetcher := func(ctx context.Context, _ Tile) ([]byte, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return []byte("jpg"), nil
	}
	st, err := Run(ctx, cfg, store, fetcher)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Fetched > 5 {
		t.Errorf("expected early cancel after a few fetches; got %d", st.Fetched)
	}
}

func TestRun_RejectsBadConfig(t *testing.T) {
	store := newFakeStore()
	fetcher := func(ctx context.Context, _ Tile) ([]byte, error) { return nil, nil }

	cases := []Config{
		{RadiusKm: 0, MaxAge: time.Hour},               // zero radius
		{RadiusKm: -1, MaxAge: time.Hour},              // negative radius
		{RadiusKm: 1, MaxAge: 0},                       // zero MaxAge
		{RadiusKm: 1, MaxAge: -1 * time.Hour},          // negative MaxAge
	}
	for i, cfg := range cases {
		if _, err := Run(context.Background(), cfg, store, fetcher); err == nil {
			t.Errorf("case %d: expected config error", i)
		}
	}
}

func TestRun_RejectsNilStoreOrFetcher(t *testing.T) {
	cfg := smallConfig()
	if _, err := Run(context.Background(), cfg, nil, func(context.Context, Tile) ([]byte, error) { return nil, nil }); err == nil {
		t.Errorf("nil store should error")
	}
	if _, err := Run(context.Background(), cfg, newFakeStore(), nil); err == nil {
		t.Errorf("nil fetcher should error")
	}
}

// ----------------------------------------------------------------------------
// FSStore integration
// ----------------------------------------------------------------------------

func TestFSStore_PutThenAge(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFSStore(dir, "satellite", "jpg")
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	tile := Tile{Z: 16, X: 12345, Y: 67890}
	if err := store.Put(tile, []byte("fakejpg")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	age, ok := store.Age(tile, time.Now())
	if !ok {
		t.Fatalf("expected tile present after Put")
	}
	if age > time.Second {
		t.Errorf("age too high right after Put: %v", age)
	}
	// File should exist at predictable path.
	want := filepath.Join(dir, "satellite", "16", "12345", "67890.jpg")
	if got := store.Path(tile); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestFSStore_AgeMissing(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFSStore(dir, "satellite", "jpg")
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	tile := Tile{Z: 16, X: 1, Y: 2}
	if _, ok := store.Age(tile, time.Now()); ok {
		t.Errorf("expected ok=false for missing tile")
	}
}

func TestFSStore_AtomicWrite_NoOrphanedTmp(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFSStore(dir, "satellite", "jpg")
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	tile := Tile{Z: 16, X: 5, Y: 5}
	if err := store.Put(tile, []byte("data")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// The .tmp sibling must not exist after a successful Put.
	tmp := store.Path(tile) + ".tmp"
	if _, err := os.Stat(tmp); err == nil {
		t.Errorf(".tmp file should be gone after successful Put: %s", tmp)
	}
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// smallConfig is a fast config for unit tests: tiny radius, single
// zoom, generous rate to avoid sleeping.
func smallConfig() Config {
	return Config{
		CenterLatDeg: -22.91,
		CenterLonDeg: -47.06,
		RadiusKm:     0.3,
		Zooms:        []int{15},
		MaxAge:       30 * 24 * time.Hour,
		RatePerSec:   1000,
	}
}
