// Command sat-download fetches Esri World Imagery raster tiles for a
// given bbox and zoom range, storing them in an MBTiles file. Resumes
// on interrupt by skipping tiles already present in the database.
//
// Usage:
//
//	sat-download \
//	    -bbox "-53.20,-25.40,-44.10,-19.70" \
//	    -zoom 5-17 \
//	    -out ~/zerotx/maptiles/sp-state-sat.mbtiles \
//	    -rate 8
//
// After running, convert to PMTiles:
//
//	pmtiles convert sp-state-sat.mbtiles sp-state-sat.pmtiles
//
// Conservative rate limiting: default 8 req/sec sustained with random
// jitter. Tile servers throttle aggressive scrapers; staying under
// 10 req/sec is generally safe.
//
// Source URL pattern (Esri World Imagery, AGS REST API):
//
//	https://server.arcgisonline.com/ArcGIS/rest/services/
//	    World_Imagery/MapServer/tile/{z}/{y}/{x}
//
// Note y/x order is reversed from OSM's z/x/y.
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
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	esriURLTemplate = "https://server.arcgisonline.com/ArcGIS/rest/services/" +
		"World_Imagery/MapServer/tile/%d/%d/%d"
	userAgent = "ZeroTX-MapBuild/1.0 (https://github.com/agoliveira/zerotx)"
)

type config struct {
	bbox     bbox
	minZoom  int
	maxZoom  int
	outPath  string
	rate     float64
	maxRetry int
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
	min, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("min zoom: %w", err)
	}
	max, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("max zoom: %w", err)
	}
	if min < 0 || max > 22 || min > max {
		return 0, 0, fmt.Errorf("zoom out of range or inverted")
	}
	return min, max, nil
}

// tilesForZoom returns the inclusive tile-coordinate range covering the
// bbox at the given zoom level.
func tilesForZoom(b bbox, z int) (xMin, xMax, yMin, yMax int) {
	n := math.Pow(2, float64(z))
	xMin = int(math.Floor((b.west + 180) / 360 * n))
	xMax = int(math.Floor((b.east + 180) / 360 * n))
	// Y is inverted: north corresponds to smaller y.
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

// totalTiles counts tiles across all zoom levels in the bbox. Used for
// progress reporting.
func totalTiles(b bbox, minZoom, maxZoom int) int {
	total := 0
	for z := minZoom; z <= maxZoom; z++ {
		xMin, xMax, yMin, yMax := tilesForZoom(b, z)
		total += (xMax - xMin + 1) * (yMax - yMin + 1)
	}
	return total
}

// openMBTiles creates or opens an MBTiles file. MBTiles is an SQLite
// database with a fixed schema; we use modernc.org/sqlite (pure Go,
// no CGO).
func openMBTiles(path string, b bbox, minZoom, maxZoom int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	// Enable WAL mode for better concurrent read/write while we're
	// busy writing tiles. Saves significant time on long runs.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("set wal: %w", err)
	}

	// Standard MBTiles schema. The metadata table is required by the
	// MBTiles 1.3 spec; the tiles table holds the actual data.
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

	// Populate metadata. Idempotent via upsert. MBTiles uses TMS y
	// (origin south) by convention; we'll convert XYZ y on insert.
	meta := map[string]string{
		"name":        "ZeroTX Satellite Tiles",
		"description": "Esri World Imagery, downloaded for offline FPV use",
		"format":      "jpg",
		"version":     "1.3",
		"type":        "baselayer",
		"minzoom":     strconv.Itoa(minZoom),
		"maxzoom":     strconv.Itoa(maxZoom),
		"bounds":      fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", b.west, b.south, b.east, b.north),
		"center": fmt.Sprintf("%.6f,%.6f,%d",
			(b.west+b.east)/2, (b.south+b.north)/2, (minZoom+maxZoom)/2),
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

// tileExists returns true if the given XYZ tile is already in the
// MBTiles file. Note MBTiles uses TMS y, so we convert.
func tileExists(db *sql.DB, z, x, y int) (bool, error) {
	tmsY := (1 << z) - 1 - y
	var exists int
	err := db.QueryRow(
		`SELECT 1 FROM tiles
		 WHERE zoom_level=? AND tile_column=? AND tile_row=?
		 LIMIT 1`,
		z, x, tmsY).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

// writeTile inserts an XYZ tile into the MBTiles, converting y to TMS.
func writeTile(db *sql.DB, z, x, y int, data []byte) error {
	tmsY := (1 << z) - 1 - y
	_, err := db.Exec(
		`INSERT OR REPLACE INTO tiles(zoom_level, tile_column, tile_row, tile_data)
		 VALUES(?, ?, ?, ?)`,
		z, x, tmsY, data)
	return err
}

// fetchTile downloads a single tile with retries and exponential
// backoff. Returns the tile bytes or an error after all retries
// exhausted.
func fetchTile(ctx context.Context, client *http.Client, z, x, y, maxRetry int) ([]byte, error) {
	url := fmt.Sprintf(esriURLTemplate, z, y, x) // y/x order for Esri
	var lastErr error
	for attempt := 0; attempt < maxRetry; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			// Cap at 30s.
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		switch resp.StatusCode {
		case http.StatusOK:
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = err
				continue
			}
			return data, nil
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			// Server is throttling. Backoff and try again.
			resp.Body.Close()
			lastErr = fmt.Errorf("status %d (throttle)", resp.StatusCode)
			continue
		case http.StatusNotFound:
			// Some tile coordinates have no imagery (e.g., poles, certain
			// remote areas). Treat as a permanent miss; don't retry.
			resp.Body.Close()
			return nil, errors.New("tile not found")
		default:
			resp.Body.Close()
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}
	}
	return nil, fmt.Errorf("after %d retries: %w", maxRetry, lastErr)
}

// rateLimit returns a channel that emits at the given rate (tokens per
// second) with random jitter to make traffic look more human-like.
func rateLimit(ctx context.Context, perSec float64) <-chan struct{} {
	ch := make(chan struct{})
	baseInterval := time.Duration(float64(time.Second) / perSec)
	go func() {
		defer close(ch)
		for {
			// Jitter: ±25% of base interval.
			jitter := baseInterval/2 - time.Duration(rand.Int64N(int64(baseInterval)))
			delay := baseInterval + jitter
			if delay < 0 {
				delay = baseInterval
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			select {
			case <-ctx.Done():
				return
			case ch <- struct{}{}:
			}
		}
	}()
	return ch
}

func run(cfg config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := openMBTiles(cfg.outPath, cfg.bbox, cfg.minZoom, cfg.maxZoom)
	if err != nil {
		return fmt.Errorf("open mbtiles: %w", err)
	}
	defer db.Close()

	client := &http.Client{Timeout: 30 * time.Second}
	tickets := rateLimit(ctx, cfg.rate)

	total := totalTiles(cfg.bbox, cfg.minZoom, cfg.maxZoom)
	log.Printf("planning %d tiles across zoom %d-%d", total, cfg.minZoom, cfg.maxZoom)

	var (
		done       int64
		downloaded int64
		skipped    int64
		notFound   int64
		errs       int64
	)

	startedAt := time.Now()

	// Progress logger goroutine.
	stopProgress := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				d := atomic.LoadInt64(&done)
				dl := atomic.LoadInt64(&downloaded)
				sk := atomic.LoadInt64(&skipped)
				nf := atomic.LoadInt64(&notFound)
				e := atomic.LoadInt64(&errs)
				elapsed := time.Since(startedAt)
				rate := float64(d) / elapsed.Seconds()
				remaining := time.Duration(0)
				if rate > 0 {
					remaining = time.Duration(float64(int64(total)-d)/rate) * time.Second
				}
				log.Printf("progress: %d/%d (%.1f%%) downloaded=%d skipped=%d 404=%d err=%d rate=%.1f/s ETA=%s",
					d, total, 100*float64(d)/float64(total),
					dl, sk, nf, e, rate, remaining.Round(time.Second))
			}
		}
	}()

	for z := cfg.minZoom; z <= cfg.maxZoom; z++ {
		xMin, xMax, yMin, yMax := tilesForZoom(cfg.bbox, z)
		log.Printf("zoom %d: x=[%d,%d] y=[%d,%d] (%d tiles)",
			z, xMin, xMax, yMin, yMax,
			(xMax-xMin+1)*(yMax-yMin+1))
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				if ctx.Err() != nil {
					close(stopProgress)
					return ctx.Err()
				}

				exists, err := tileExists(db, z, x, y)
				if err != nil {
					log.Printf("tileExists error z=%d x=%d y=%d: %v", z, x, y, err)
					atomic.AddInt64(&errs, 1)
					atomic.AddInt64(&done, 1)
					continue
				}
				if exists {
					atomic.AddInt64(&skipped, 1)
					atomic.AddInt64(&done, 1)
					continue
				}

				// Wait for rate limiter token.
				select {
				case <-ctx.Done():
					close(stopProgress)
					return ctx.Err()
				case <-tickets:
				}

				data, err := fetchTile(ctx, client, z, x, y, cfg.maxRetry)
				if err != nil {
					if err.Error() == "tile not found" {
						atomic.AddInt64(&notFound, 1)
					} else {
						atomic.AddInt64(&errs, 1)
						log.Printf("fetch z=%d x=%d y=%d: %v", z, x, y, err)
					}
					atomic.AddInt64(&done, 1)
					continue
				}

				if err := writeTile(db, z, x, y, data); err != nil {
					log.Printf("writeTile z=%d x=%d y=%d: %v", z, x, y, err)
					atomic.AddInt64(&errs, 1)
					atomic.AddInt64(&done, 1)
					continue
				}
				atomic.AddInt64(&downloaded, 1)
				atomic.AddInt64(&done, 1)
			}
		}
	}

	close(stopProgress)

	d := atomic.LoadInt64(&done)
	dl := atomic.LoadInt64(&downloaded)
	sk := atomic.LoadInt64(&skipped)
	nf := atomic.LoadInt64(&notFound)
	e := atomic.LoadInt64(&errs)
	log.Printf("complete: %d total, downloaded=%d skipped=%d 404=%d err=%d in %s",
		d, dl, sk, nf, e, time.Since(startedAt).Round(time.Second))

	return nil
}

func main() {
	bboxStr := flag.String("bbox", "", "bounding box W,S,E,N (decimal degrees)")
	zoomStr := flag.String("zoom", "5-17", "zoom range MIN-MAX")
	outPath := flag.String("out", "", "output MBTiles file path")
	rate := flag.Float64("rate", 8.0, "max requests per second (sustained)")
	maxRetry := flag.Int("max-retry", 5, "max retries per tile on transient errors")
	flag.Parse()

	if *bboxStr == "" || *outPath == "" {
		flag.Usage()
		os.Exit(2)
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
		bbox:     b,
		minZoom:  minZ,
		maxZoom:  maxZ,
		outPath:  *outPath,
		rate:     *rate,
		maxRetry: *maxRetry,
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("sat-download starting: bbox=%v zoom=%d-%d rate=%.1f/s out=%s",
		b, minZ, maxZ, *rate, *outPath)

	if err := run(cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("interrupted (output file is resumable, just re-run)")
			os.Exit(130)
		}
		log.Fatal(err)
	}
}
