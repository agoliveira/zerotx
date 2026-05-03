// Package api: map tile serving.
//
// Stage 1: online proxy mode for development. Map sources:
//   - "osm": OpenStreetMap raster tiles
//   - "satellite": Esri World Imagery raster tiles
//
// Stage 2 (later): local PMTiles files in mapTilesDir.
//
// Route: /tiles/{tileset}/{z}/{x}/{y}.{ext}
//
// The frontend chooses the tileset at runtime, allowing UI toggles
// between OSM and satellite views.
package api

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SetMapTilesDir configures a directory containing PMTiles files for
// offline tile serving. Empty string disables offline mode and forces
// online proxy fallback (stage 1 default).
func (s *Server) SetMapTilesDir(dir string) {
	s.mapTilesDir = dir
}

// SetOnlineTileFallback controls whether to proxy to public tile servers
// when a tile isn't found locally. Default true. Set false in production
// when offline operation is required.
func (s *Server) SetOnlineTileFallback(enable bool) {
	s.onlineFallback = enable
}

// onlineTileURL builds the upstream URL for a public tile server.
// Returns an error for unknown tilesets.
func onlineTileURL(tileset string, z, x, y int) (string, error) {
	switch tileset {
	case "osm":
		return fmt.Sprintf("https://tile.openstreetmap.org/%d/%d/%d.png", z, x, y), nil
	case "satellite":
		// Esri World Imagery uses y/x order swapped vs OSM convention.
		return fmt.Sprintf(
			"https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/%d/%d/%d",
			z, y, x,
		), nil
	default:
		return "", fmt.Errorf("unknown tileset %q", tileset)
	}
}

// httpTileClient is a shared HTTP client for upstream tile fetches.
// Modest timeout: tile servers are usually fast; if not, fail fast and
// let the frontend retry.
var httpTileClient = &http.Client{
	Timeout: 10 * time.Second,
}

// handleTile serves a map tile.
//
// Path format: /tiles/{tileset}/{z}/{x}/{y}.{ext}
//
// Stage 1: proxies to public tile servers based on tileset name.
// Stage 2: will check mapTilesDir first, fall back to online if
// onlineFallback is true.
func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Strip the /tiles/ prefix and parse {tileset}/{z}/{x}/{y}.{ext}
	rest := strings.TrimPrefix(r.URL.Path, "/tiles/")
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		http.Error(w, "bad tile path", http.StatusBadRequest)
		return
	}
	tileset := parts[0]
	z, errZ := strconv.Atoi(parts[1])
	x, errX := strconv.Atoi(parts[2])
	// {y}.{ext} -- split off extension if present
	yPart := parts[3]
	if dot := strings.LastIndex(yPart, "."); dot >= 0 {
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

	// Stage 2 hook: if mapTilesDir is set, look there first. Not
	// implemented yet; fall through to online for now.
	// TODO: open PMTiles for tileset, range-read tile, return bytes.

	if !s.onlineFallback && s.mapTilesDir == "" {
		http.Error(w, "no tile source available", http.StatusServiceUnavailable)
		return
	}

	url, err := onlineTileURL(tileset, z, x, y)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Proxy the upstream tile. Stream bytes through; do not buffer.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, "tile request build failed", http.StatusInternalServerError)
		return
	}
	// Some tile servers require a User-Agent identifying the client.
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

	// Pass through Content-Type and basic cache headers from upstream.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Cache aggressively client-side: tiles are immutable for our
	// purposes. 7 days is fine for development; in prod the local
	// PMTiles is the canonical source anyway.
	w.Header().Set("Cache-Control", "public, max-age=604800")

	if _, err := io.Copy(w, resp.Body); err != nil {
		// Connection probably closed by client mid-stream. Common and
		// not worth logging at INFO level. If debugging tile issues,
		// add a verbose log here.
		_ = err
	}
}
