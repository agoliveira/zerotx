// Command sat-download fetches Esri World Imagery raster tiles for a
// given bbox and zoom range, storing them in an MBTiles file. Resumes
// on interrupt by skipping tiles already present in the database.
//
// Usage:
//
//	sat-download \
//	    -bbox "-53.20,-25.40,-44.10,-19.70" \
//	    -zoom 5-14 \
//	    -out ~/zerotx/maptiles/sp-state-sat.mbtiles \
//	    -workers 4 \
//	    -rate 12 \
//	    -pmtiles-out ~/zerotx/maptiles/sp-state-sat.pmtiles
//
// Concurrency: a configurable worker pool fetches tiles in parallel,
// sharing a single token-bucket rate limiter. Default is 4 workers at
// 12 req/s sustained. Workers are HTTP-bound; the rate limiter governs
// total polite-request rate against Esri regardless of worker count.
//
// Adaptive rate: on HTTP 429/503 from Esri, the limiter halves its rate
// and slowly recovers toward the configured target over ~5 minutes of
// successful traffic. This protects against IP bans without aborting.
//
// Source URL pattern (Esri World Imagery, AGS REST API):
//
//	https://server.arcgisonline.com/ArcGIS/rest/services/
//	    World_Imagery/MapServer/tile/{z}/{y}/{x}
//
// Note y/x order is reversed from OSM's z/x/y.
//
// After download, if -pmtiles-out is set, runs `pmtiles convert` to
// produce a serving-ready archive. The pmtiles binary must be in PATH.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/time/rate"
	_ "modernc.org/sqlite"
)

const (
	esriURLTemplate = "https://server.arcgisonline.com/ArcGIS/rest/services/" +
		"World_Imagery/MapServer/tile/%d/%d/%d"
	userAgent = "ZeroTX-MapBuild/1.0 (https://github.com/agoliveira/zerotx)"
)

type config struct {
	bbox        bbox
	minZoom     int
	maxZoom     int
	outPath     string
	pmtilesOut  string
	rate        float64
	workers     int
	maxRetry    int
	progressInt time.Duration
}

type bbox struct {
	west, south, east, north float64
}

func parseBBox(s string) (bbox, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return bbox{}, fmt.Errorf("expected W,S,E,N got %d parts", len(parts))
	}
	vals := make([]float64, 4)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return bbox{}, fmt.Errorf("part %d (%q): %w", i, p, err)
		}
		vals[i] = v
	}
	b := bbox{west: vals[0], south: vals[1], east: vals[2], north: vals[3]}
	if b.west >= b.east || b.south >= b.north {
		return bbox{}, fmt.Errorf("bbox invalid: W>=E or S>=N")
	}
	return b, nil
}

func parseZoomRange(s string) (int, int, error) {
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected MIN-MAX, got %q", s)
	}
	mn, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("min zoom: %w", err)
	}
	mx, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("max zoom: %w", err)
	}
	if mn < 0 || mx > 22 || mn > mx {
		return 0, 0, fmt.Errorf("zoom out of range or inverted")
	}
	return mn, mx, nil
}

// tilesForZoom returns the inclusive XYZ tile-coordinate range covering
// the bbox at the given zoom level.
func tilesForZoom(b bbox, z int) (xMin, xMax, yMin, yMax int) {
	n := math.Pow(2, float64(z))
	xMin = int(math.Floor((b.west + 180) / 360 * n))
	xMax = int(math.Floor((b.east + 180) / 360 * n))
	yMin = int(math.Floor((1 - math.Log(math.Tan(b.north*math.Pi/180)+1/math.Cos(b.north*math.Pi/180))/math.Pi) / 2 * n))
	yMax = int(math.Floor((1 - math.Log(math.Tan(b.south*math.Pi/180)+1/math.Cos(b.south*math.Pi/180))/math.Pi) / 2 * n))
	if xMin < 0 {
		xMin = 0
	}
	if yMin < 0 {
		yMin = 0
	}
	return
}

// totalTiles counts tiles across all zoom levels in the bbox.
func totalTiles(b bbox, minZoom, maxZoom int) int64 {
	var total int64
	for z := minZoom; z <= maxZoom; z++ {
		xMin, xMax, yMin, yMax := tilesForZoom(b, z)
		total += int64(xMax-xMin+1) * int64(yMax-yMin+1)
	}
	return total
}

// openMBTiles creates or opens an MBTiles file. Existing archives are
// extended: bounds and zoom-range metadata are widened to the union of
// existing values and the requested run's parameters.
func openMBTiles(path string, b bbox, minZoom, maxZoom int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	// WAL + larger cache: long-running writes plus concurrent reads.
	// busy_timeout=5000 lets SQLite block up to 5 s on a lock instead
	// of returning SQLITE_BUSY immediately, which a long download
	// otherwise hits when the WAL checkpointer and the INSERT loop
	// briefly collide; 5 s is well below the rate-limiter's per-tile
	// budget so a stalled write doesn't slow throughput perceptibly.
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA cache_size=-65536`, // ~64MB
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}

	const schema = `
		CREATE TABLE IF NOT EXISTS metadata (
			name TEXT PRIMARY KEY,
			value TEXT
		);
		CREATE TABLE IF NOT EXISTS tiles (
			zoom_level INTEGER,
			tile_column INTEGER,
			tile_row INTEGER,
			tile_data BLOB,
			PRIMARY KEY (zoom_level, tile_column, tile_row)
		);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}

	// Read existing metadata to widen union of bounds + zoom-range
	// when extending an archive across multiple runs.
	existing := map[string]string{}
	rows, err := db.Query(`SELECT name, value FROM metadata`)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			existing[k] = v
		}
	}
	rows.Close()

	bounds := fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", b.west, b.south, b.east, b.north)
	if existing["bounds"] != "" {
		bounds = unionBounds(existing["bounds"], b)
	}
	mn, mx := minZoom, maxZoom
	if v, err := strconv.Atoi(existing["minzoom"]); err == nil && v < mn {
		mn = v
	}
	if v, err := strconv.Atoi(existing["maxzoom"]); err == nil && v > mx {
		mx = v
	}

	meta := map[string]string{
		"name":        firstNonEmpty(existing["name"], "ZeroTX Satellite Tiles"),
		"description": firstNonEmpty(existing["description"], "Esri World Imagery, downloaded for offline FPV use"),
		"format":      "jpg",
		"version":     "1.3",
		"type":        "baselayer",
		"minzoom":     strconv.Itoa(mn),
		"maxzoom":     strconv.Itoa(mx),
		"bounds":      bounds,
		"center":      computeCenter(bounds, mn, mx),
	}
	for k, v := range meta {
		if _, err := db.Exec(
			`INSERT INTO metadata(name, value) VALUES(?, ?)
			 ON CONFLICT(name) DO UPDATE SET value=excluded.value`,
			k, v); err != nil {
			return nil, fmt.Errorf("metadata %s: %w", k, err)
		}
	}
	return db, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func unionBounds(existing string, b bbox) string {
	parts := strings.Split(existing, ",")
	if len(parts) != 4 {
		return fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", b.west, b.south, b.east, b.north)
	}
	w, _ := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	s, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	e, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	n, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
	if b.west < w {
		w = b.west
	}
	if b.south < s {
		s = b.south
	}
	if b.east > e {
		e = b.east
	}
	if b.north > n {
		n = b.north
	}
	return fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", w, s, e, n)
}

func computeCenter(bounds string, mn, mx int) string {
	parts := strings.Split(bounds, ",")
	if len(parts) != 4 {
		return ""
	}
	w, _ := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	s, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	e, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	n, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
	return fmt.Sprintf("%.6f,%.6f,%d", (w+e)/2, (s+n)/2, (mn+mx)/2)
}

// loadExistingTiles returns the set of (x, tmsY) keys already in the
// archive for the given zoom level and tile rectangle.
func loadExistingTiles(db *sql.DB, z, xMin, xMax, yMinTms, yMaxTms int) (map[uint64]struct{}, error) {
	set := make(map[uint64]struct{})
	rows, err := db.Query(
		`SELECT tile_column, tile_row FROM tiles
		 WHERE zoom_level=? AND tile_column BETWEEN ? AND ? AND tile_row BETWEEN ? AND ?`,
		z, xMin, xMax, yMinTms, yMaxTms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x, tmsY int
		if err := rows.Scan(&x, &tmsY); err != nil {
			return nil, err
		}
		set[encodeKey(x, tmsY)] = struct{}{}
	}
	return set, rows.Err()
}

func encodeKey(x, tmsY int) uint64 {
	return uint64(uint32(x))<<32 | uint64(uint32(tmsY))
}

// writeTile inserts an XYZ tile, converting y to TMS for MBTiles.
func writeTile(db *sql.DB, z, x, y int, data []byte) error {
	tmsY := (1 << z) - 1 - y
	_, err := db.Exec(
		`INSERT OR REPLACE INTO tiles(zoom_level, tile_column, tile_row, tile_data)
		 VALUES(?, ?, ?, ?)`,
		z, x, tmsY, data)
	return err
}

// fetchTile downloads a single tile with retries. The throttled return
// flag is true if any attempt got 429/503; the rate controller uses it
// to back off.
func fetchTile(ctx context.Context, client *http.Client, z, x, y, maxRetry int) (data []byte, throttled bool, err error) {
	url := fmt.Sprintf(esriURLTemplate, z, y, x)
	var lastErr error
	for attempt := 0; attempt < maxRetry; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, throttled, ctx.Err()
			case <-time.After(backoff):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, throttled, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		switch resp.StatusCode {
		case http.StatusOK:
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = err
				continue
			}
			return body, throttled, nil
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			resp.Body.Close()
			throttled = true
			lastErr = fmt.Errorf("status %d (throttle)", resp.StatusCode)
			continue
		case http.StatusNotFound:
			resp.Body.Close()
			return nil, throttled, errors.New("tile not found")
		default:
			resp.Body.Close()
			return nil, throttled, fmt.Errorf("status %d", resp.StatusCode)
		}
	}
	return nil, throttled, fmt.Errorf("after %d retries: %w", maxRetry, lastErr)
}

// rateController wraps a token-bucket limiter with adaptive behavior.
// On throttle (429/503) halves the rate and pauses for 30s before
// recovering linearly toward target.
type rateController struct {
	mu          sync.Mutex
	limiter     *rate.Limiter
	target      rate.Limit
	current     rate.Limit
	lastBackoff time.Time
}

func newRateController(target float64) *rateController {
	t := rate.Limit(target)
	return &rateController{
		limiter: rate.NewLimiter(t, 1),
		target:  t,
		current: t,
	}
}

func (rc *rateController) onThrottle() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := time.Now()
	if now.Sub(rc.lastBackoff) < 10*time.Second {
		return // already in a fresh backoff
	}
	rc.current = rc.current / 2
	if rc.current < 1 {
		rc.current = 1
	}
	rc.limiter.SetLimit(rc.current)
	rc.lastBackoff = now
	log.Printf("rate: throttle detected, reducing to %.1f/s", float64(rc.current))
}

func (rc *rateController) onSuccess() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.current >= rc.target {
		return
	}
	if time.Since(rc.lastBackoff) < 30*time.Second {
		return
	}
	step := rc.target / 300
	if step < 0.01 {
		step = 0.01
	}
	rc.current += step
	if rc.current > rc.target {
		rc.current = rc.target
	}
	rc.limiter.SetLimit(rc.current)
}

func (rc *rateController) currentRate() float64 {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return float64(rc.current)
}

type counters struct {
	done       int64
	downloaded int64
	skipped    int64
	notFound   int64
	errs       int64
}

type tileJob struct {
	z, x, y int
}

type tileResult struct {
	job       tileJob
	data      []byte
	err       error
	notFound  bool
	throttled bool
}

func worker(
	ctx context.Context,
	jobs <-chan tileJob,
	results chan<- tileResult,
	rc *rateController,
	client *http.Client,
	maxRetry int,
) {
	for job := range jobs {
		if err := rc.limiter.Wait(ctx); err != nil {
			return
		}
		data, throttled, err := fetchTile(ctx, client, job.z, job.x, job.y, maxRetry)
		notFound := err != nil && err.Error() == "tile not found"
		select {
		case <-ctx.Done():
			return
		case results <- tileResult{
			job:       job,
			data:      data,
			err:       err,
			notFound:  notFound,
			throttled: throttled,
		}:
		}
	}
}

func writer(
	db *sql.DB,
	results <-chan tileResult,
	c *counters,
	rc *rateController,
	done chan<- struct{},
) {
	defer close(done)
	for r := range results {
		switch {
		case r.err == nil && r.data != nil:
			if err := writeTile(db, r.job.z, r.job.x, r.job.y, r.data); err != nil {
				log.Printf("writeTile z=%d x=%d y=%d: %v", r.job.z, r.job.x, r.job.y, err)
				atomic.AddInt64(&c.errs, 1)
			} else {
				atomic.AddInt64(&c.downloaded, 1)
			}
			rc.onSuccess()
		case r.notFound:
			atomic.AddInt64(&c.notFound, 1)
		default:
			atomic.AddInt64(&c.errs, 1)
			if r.err != nil && !errors.Is(r.err, context.Canceled) {
				log.Printf("fetch z=%d x=%d y=%d: %v", r.job.z, r.job.x, r.job.y, r.err)
			}
		}
		if r.throttled {
			rc.onThrottle()
		}
		atomic.AddInt64(&c.done, 1)
	}
}

func runZoom(
	ctx context.Context,
	z int,
	b bbox,
	db *sql.DB,
	rc *rateController,
	client *http.Client,
	c *counters,
	cfg config,
) error {
	xMin, xMax, yMin, yMax := tilesForZoom(b, z)
	yMinTms := (1 << z) - 1 - yMax
	yMaxTms := (1 << z) - 1 - yMin
	nTotal := int64(xMax-xMin+1) * int64(yMax-yMin+1)

	existing, err := loadExistingTiles(db, z, xMin, xMax, yMinTms, yMaxTms)
	if err != nil {
		return fmt.Errorf("loadExisting z=%d: %w", z, err)
	}
	nExisting := int64(len(existing))
	nMissing := nTotal - nExisting
	log.Printf("zoom %d: x=[%d,%d] y=[%d,%d] %d total, %d already present, %d to fetch",
		z, xMin, xMax, yMin, yMax, nTotal, nExisting, nMissing)

	atomic.AddInt64(&c.skipped, nExisting)
	atomic.AddInt64(&c.done, nExisting)

	if nMissing == 0 {
		return nil
	}

	jobs := make(chan tileJob, cfg.workers*4)
	results := make(chan tileResult, cfg.workers*4)
	writerDone := make(chan struct{})

	go writer(db, results, c, rc, writerDone)

	var wg sync.WaitGroup
	for i := 0; i < cfg.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, jobs, results, rc, client, cfg.maxRetry)
		}()
	}

producer:
	for y := yMin; y <= yMax; y++ {
		tmsY := (1 << z) - 1 - y
		for x := xMin; x <= xMax; x++ {
			if _, has := existing[encodeKey(x, tmsY)]; has {
				continue
			}
			select {
			case <-ctx.Done():
				break producer
			case jobs <- tileJob{z: z, x: x, y: y}:
			}
		}
	}
	close(jobs)
	wg.Wait()
	close(results)
	<-writerDone
	return ctx.Err()
}

func progressLogger(
	ctx context.Context,
	c *counters,
	rc *rateController,
	total int64,
	startedAt time.Time,
	interval time.Duration,
) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d := atomic.LoadInt64(&c.done)
			dl := atomic.LoadInt64(&c.downloaded)
			sk := atomic.LoadInt64(&c.skipped)
			nf := atomic.LoadInt64(&c.notFound)
			e := atomic.LoadInt64(&c.errs)
			elapsed := time.Since(startedAt)
			effRate := float64(dl) / elapsed.Seconds()
			eta := time.Duration(0)
			if effRate > 0 {
				eta = time.Duration(float64(total-d)/effRate) * time.Second
			}
			log.Printf("progress: %d/%d (%.1f%%) downloaded=%d skipped=%d 404=%d err=%d effRate=%.1f/s curRate=%.1f/s ETA=%s",
				d, total, 100*float64(d)/float64(total),
				dl, sk, nf, e, effRate, rc.currentRate(), eta.Round(time.Second))
		}
	}
}

func run(cfg config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := openMBTiles(cfg.outPath, cfg.bbox, cfg.minZoom, cfg.maxZoom)
	if err != nil {
		return fmt.Errorf("open mbtiles: %w", err)
	}
	defer db.Close()

	transport := &http.Transport{
		MaxIdleConns:        cfg.workers * 2,
		MaxIdleConnsPerHost: cfg.workers * 2,
		MaxConnsPerHost:     cfg.workers * 2,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	rc := newRateController(cfg.rate)
	c := &counters{}
	total := totalTiles(cfg.bbox, cfg.minZoom, cfg.maxZoom)
	log.Printf("planning %d tiles across zoom %d-%d (workers=%d, target rate=%.1f/s)",
		total, cfg.minZoom, cfg.maxZoom, cfg.workers, cfg.rate)

	startedAt := time.Now()
	progCtx, progCancel := context.WithCancel(ctx)
	defer progCancel()
	go progressLogger(progCtx, c, rc, total, startedAt, cfg.progressInt)

	for z := cfg.minZoom; z <= cfg.maxZoom; z++ {
		if err := runZoom(ctx, z, cfg.bbox, db, rc, client, c, cfg); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			return fmt.Errorf("runZoom %d: %w", z, err)
		}
	}
	progCancel()

	d := atomic.LoadInt64(&c.done)
	dl := atomic.LoadInt64(&c.downloaded)
	sk := atomic.LoadInt64(&c.skipped)
	nf := atomic.LoadInt64(&c.notFound)
	e := atomic.LoadInt64(&c.errs)
	log.Printf("complete: %d total, downloaded=%d skipped=%d 404=%d err=%d in %s",
		d, dl, sk, nf, e, time.Since(startedAt).Round(time.Second))

	if cfg.pmtilesOut != "" {
		if err := convertToPMTiles(cfg.outPath, cfg.pmtilesOut); err != nil {
			return fmt.Errorf("pmtiles convert: %w", err)
		}
	}
	return nil
}

// convertToPMTiles shells out to `pmtiles convert` and atomically
// renames the result into place.
func convertToPMTiles(mbtilesPath, pmtilesOut string) error {
	pmtilesBin, err := exec.LookPath("pmtiles")
	if err != nil {
		return fmt.Errorf("pmtiles binary not found in PATH (install: go install github.com/protomaps/go-pmtiles/cmd/pmtiles@latest)")
	}
	tmpOut := pmtilesOut + ".tmp"
	_ = os.Remove(tmpOut)
	log.Printf("converting %s -> %s via %s", mbtilesPath, pmtilesOut, pmtilesBin)
	cmd := exec.Command(pmtilesBin, "convert", mbtilesPath, tmpOut)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpOut)
		return err
	}
	if err := os.Rename(tmpOut, pmtilesOut); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpOut, pmtilesOut, err)
	}
	log.Printf("pmtiles ready: %s", pmtilesOut)
	return nil
}

func main() {
	bboxStr := flag.String("bbox", "", "bounding box W,S,E,N (decimal degrees)")
	zoomStr := flag.String("zoom", "5-17", "zoom range MIN-MAX")
	outPath := flag.String("out", "", "output MBTiles file path (resumable)")
	pmtilesOut := flag.String("pmtiles-out", "", "if set, run `pmtiles convert` to this path after a clean download")
	rateF := flag.Float64("rate", 12.0, "target requests per second (sustained, shared across workers)")
	workers := flag.Int("workers", 4, "concurrent download workers")
	maxRetry := flag.Int("max-retry", 5, "max retries per tile on transient errors")
	progressInt := flag.Duration("progress-interval", 30*time.Second, "interval between progress log lines")
	flag.Parse()

	if *bboxStr == "" || *outPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *workers < 1 {
		log.Fatalf("workers must be >= 1")
	}
	if *rateF <= 0 {
		log.Fatalf("rate must be > 0")
	}

	b, err := parseBBox(*bboxStr)
	if err != nil {
		log.Fatalf("bbox: %v", err)
	}
	minZ, maxZ, err := parseZoomRange(*zoomStr)
	if err != nil {
		log.Fatalf("zoom: %v", err)
	}

	cfg := config{
		bbox:        b,
		minZoom:     minZ,
		maxZoom:     maxZ,
		outPath:     *outPath,
		pmtilesOut:  *pmtilesOut,
		rate:        *rateF,
		workers:     *workers,
		maxRetry:    *maxRetry,
		progressInt: *progressInt,
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("sat-download starting: bbox=%v zoom=%d-%d workers=%d rate=%.1f/s out=%s",
		b, minZ, maxZ, cfg.workers, cfg.rate, *outPath)

	if err := run(cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("interrupted (output file is resumable, just re-run)")
			os.Exit(130)
		}
		log.Fatal(err)
	}
}
