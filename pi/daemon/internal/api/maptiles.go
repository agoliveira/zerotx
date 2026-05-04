// Package api: map tile serving.
//
// Two-tier serving:
//   - Local PMTiles files in mapTilesDir (preferred when available).
//     Uses protomaps go-pmtiles lib for header/directory/range reads
//     with built-in LRU caching (default 64MB shared across archives).
//   - Online proxy fallback to public tile servers (development only).
//     Tilesets:
//       "osm"       -> OpenStreetMap raster
//       "satellite" -> Esri World Imagery raster
//
// Routes:
//   /tiles/{tileset}/{z}/{x}/{y}.{ext}
//
// PMTiles file lookup: the URL "tileset" name is mapped to a file
// basename via per-tileset configuration (SetTilesetFile). For example
// "osm" -> "sp-state-osm.pmtiles" and "satellite" -> "campinas-sat.pmtiles".
// If a tileset has no mapping configured, the URL "tileset" name is
// used directly as the basename ("osm" -> "osm.pmtiles").
package api

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/protomaps/go-pmtiles/pmtiles"
)

// SetMapTilesDir configures a directory containing PMTiles files for
// offline tile serving. Empty string disables offline mode (online
// fallback is used if onlineFallback is true). Calling this with a
// non-empty path initializes the embedded pmtiles.Server lazily on
// first tile request.
func (s *Server) SetMapTilesDir(dir string) {
	s.mapTilesDir = dir
}

// SetOnlineTileFallback controls whether to proxy to public tile servers
// when a tile isn't found locally. Default true. Set false in production
// when offline operation is required.
func (s *Server) SetOnlineTileFallback(enable bool) {
	s.onlineFallback = enable
}

// SetTilesetFile maps a URL tileset name (e.g. "osm") to a PMTiles file
// basename (e.g. "sp-state-osm.pmtiles" or "sp-state-osm" — the .pmtiles
// extension is added automatically by the pmtiles lib). This lets the
// frontend keep using stable names like "osm"/"satellite" while the
// underlying files can be regional or themed (e.g. "campinas-sat",
// "maringa-sat", "br-osm").
//
// Call this once per tileset before the first tile request.
func (s *Server) SetTilesetFile(tileset, basename string) {
	s.tilesetFilesMu.Lock()
	if s.tilesetFiles == nil {
		s.tilesetFiles = map[string]string{}
	}
	s.tilesetFiles[tileset] = strings.TrimSuffix(basename, ".pmtiles")
	s.tilesetFilesMu.Unlock()
}

// tilesetFile returns the PMTiles file basename (no extension) for a URL
// tileset name. Falls back to the tileset name itself when not mapped.
func (s *Server) tilesetFile(tileset string) string {
	s.tilesetFilesMu.RLock()
	defer s.tilesetFilesMu.RUnlock()
	if name, ok := s.tilesetFiles[tileset]; ok {
		return name
	}
	return tileset
}

// pmtilesServer lazily initializes (once) the embedded pmtiles.Server
// pointing at mapTilesDir as a file:// bucket. Returns nil if no tiles
// dir is configured.
func (s *Server) pmtilesServer() *pmtiles.Server {
	s.pmSrvOnce.Do(func() {
		if s.mapTilesDir == "" {
			return
		}
		bucketURL := "file://" + s.mapTilesDir
		logger := log.New(log.Writer(), "pmtiles: ", log.LstdFlags)
		// cacheSize is in MB. 64 is the lib default.
		// publicHostname is only used for TileJSON (we don't expose
		// /TILESET.json yet), so empty is fine.
		srv, err := pmtiles.NewServer(bucketURL, "", logger, 64, "")
		if err != nil {
			log.Printf("api: pmtiles server init failed: %v", err)
			return
		}
		srv.Start()
		s.pmSrv = srv
		log.Printf("api: pmtiles server ready (dir=%s, cache=64MB)", s.mapTilesDir)
	})
	return s.pmSrv
}

// onlineTileURL builds the upstream URL for a public tile server.
func onlineTileURL(tileset string, z, x, y int) (string, error) {
	switch tileset {
	case "osm":
		return fmt.Sprintf("https://tile.openstreetmap.org/%d/%d/%d.png", z, x, y), nil
	case "satellite":
		return fmt.Sprintf(
			"https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/%d/%d/%d",
			z, y, x,
		), nil
	default:
		return "", fmt.Errorf("unknown tileset %q", tileset)
	}
}

// httpTileClient is a shared HTTP client for upstream tile fetches.
var httpTileClient = &http.Client{
	Timeout: 10 * time.Second,
}

// handleMapStyle serves a style JSON from mapTilesDir/styles/, rewriting
// relative URLs ("/tiles/...", "/fonts/...", "/sprites/...") into
// absolute URLs based on the request's host and scheme. MapLibre's
// vector tile fetcher constructs `new Request(url)` and rejects
// origin-relative paths with "Failed to parse URL" — so we must serve
// styles with absolute URLs at request time. Cannot bake them into the
// static file because the same JSON serves over different hostnames
// (127.0.0.1 from desktop preview, 127.0.0.1 from Pi kiosks too, but
// hypothetically remote clients on the LAN would also work).
func (s *Server) handleMapStyle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.mapTilesDir == "" {
		http.NotFound(w, r)
		return
	}
	// /styles/X.json -> mapTilesDir/styles/X.json
	rel := strings.TrimPrefix(r.URL.Path, "/styles/")
	if rel == "" || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	full := s.mapTilesDir + "/styles/" + rel
	raw, err := os.ReadFile(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Compute absolute base URL from the incoming request.
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	base := scheme + "://" + r.Host

	// Replace string occurrences of "/tiles/", "/fonts/", "/sprites/"
	// with absolute equivalents. The style JSON is text; no need to
	// parse+reserialize. Use targeted replacements that are safe even
	// if a layer happens to mention these strings in a filter (very
	// unlikely; values appear in url-typed fields only).
	body := string(raw)
	body = strings.ReplaceAll(body, `"/tiles/`, `"`+base+`/tiles/`)
	body = strings.ReplaceAll(body, `"/fonts/`, `"`+base+`/fonts/`)
	body = strings.ReplaceAll(body, `"/sprites/`, `"`+base+`/sprites/`)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}

// handleTile serves a map tile.
//
// Path format: /tiles/{tileset}/{z}/{x}/{y}.{ext}
//
// Order of attempts:
//  1. Local PMTiles (if mapTilesDir is set)
//  2. Online proxy (if onlineFallback is true)
//
// Returns 503 if neither source is available.
func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/tiles/")
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		http.Error(w, "bad tile path", http.StatusBadRequest)
		return
	}
	tileset := parts[0]
	z, errZ := strconv.Atoi(parts[1])
	x, errX := strconv.Atoi(parts[2])
	yPart := parts[3]
	ext := ""
	if dot := strings.LastIndex(yPart, "."); dot >= 0 {
		ext = yPart[dot+1:]
		yPart = yPart[:dot]
	}
	y, errY := strconv.Atoi(yPart)
	if errZ != nil || errX != nil || errY != nil {
		http.Error(w, "non-numeric z/x/y", http.StatusBadRequest)
		return
	}
	if z < 0 || z > 22 || x < 0 || y < 0 {
		http.Error(w, "z/x/y out of range", http.StatusBadRequest)
		return
	}

	// Try local PMTiles first.
	if s.mapTilesDir != "" {
		if served := s.servePMTile(w, r, tileset, z, x, y, ext); served {
			return
		}
		// Local PMTiles didn't have it. Online disabled = 404 here.
		if !s.onlineFallback {
			http.NotFound(w, r)
			return
		}
	}

	// Vector tilesets (osm) have no compatible online fallback: the
	// public OSM tile server returns PNG raster, not vector PBFs, so
	// blending it into a vector source breaks rendering. 404 here so
	// the frontend simply leaves the tile blank, instead of corrupting
	// the vector pipeline.
	if tileset == "osm" {
		http.NotFound(w, r)
		return
	}

	// Online fallback (raster only, development).
	if !s.onlineFallback {
		http.Error(w, "no tile source available", http.StatusServiceUnavailable)
		return
	}
	url, err := onlineTileURL(tileset, z, x, y)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.proxyOnlineTile(w, r, url)
}

// servePMTile attempts a local PMTiles read. Returns true if the response
// has been written (200 OK or hard error). Returns false on soft misses
// (file not found / tile out of archive range) so the caller can try
// online fallback.
func (s *Server) servePMTile(w http.ResponseWriter, r *http.Request, tileset string, z, x, y int, ext string) bool {
	srv := s.pmtilesServer()
	if srv == nil {
		return false
	}
	// The protomaps lib enforces extension-matches-archive. MapLibre
	// frontends ask for .pbf for vector tiles, but the archive's
	// recorded tile type is "mvt". Normalize so the frontend can use
	// either spelling.
	if ext == "" {
		switch tileset {
		case "osm":
			ext = "mvt"
		default:
			ext = "jpg"
		}
	} else if ext == "pbf" {
		ext = "mvt"
	}
	name := s.tilesetFile(tileset)
	path := fmt.Sprintf("/%s/%d/%d/%d.%s", name, z, x, y, ext)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	statusCode, headers, body := srv.Get(ctx, path)

	switch statusCode {
	case http.StatusOK:
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "public, max-age=604800")
		}
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return true
	case http.StatusNoContent, http.StatusNotFound:
		// Soft miss: tile not in this archive (out of zoom range or
		// out of bbox). 204 is what the protomaps lib returns for
		// "archive exists but tile is absent"; 404 is "archive doesn't
		// exist at all". Both should fall through to online fallback.
		return false
	default:
		// Hard error (corrupt archive, etc.). Return as-is.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(statusCode)
		if r.Method != http.MethodHead && len(body) > 0 {
			_, _ = w.Write(body)
		}
		return true
	}
}

func (s *Server) proxyOnlineTile(w http.ResponseWriter, r *http.Request, url string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, "tile request build failed", http.StatusInternalServerError)
		return
	}
	req.Header.Set("User-Agent", "ZeroTX/1.0 (https://github.com/agoliveira/zerotx)")

	resp, err := httpTileClient.Do(req)
	if err != nil {
		log.Printf("api: tile proxy error %s: %v", url, err)
		http.Error(w, "upstream tile fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("upstream returned %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")

	if _, err := io.Copy(w, resp.Body); err != nil {
		// Client closed connection mid-stream. Common; not noisy.
		_ = err
	}
}
