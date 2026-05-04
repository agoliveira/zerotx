package tilewarm

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// FSStore is a filesystem-backed Store. Tiles live at
// {Root}/{Tileset}/{z}/{x}/{y}.{Ext} and the on-disk mtime is the
// freshness timestamp.
//
// Atomic writes: each Put writes to a sibling .tmp file and renames.
// Crashes leave a .tmp behind; readers ignore them by extension.
//
// Directory layout matches the existing /tiles/ URL scheme so the
// HTTP layer can map a tile request directly to a file path with
// no in-memory index.
type FSStore struct {
	Root    string
	Tileset string
	Ext     string // e.g. "jpg" for satellite, "png" for OSM raster
}

// NewFSStore constructs an FSStore. Root is created if missing.
func NewFSStore(root, tileset, ext string) (*FSStore, error) {
	if root == "" {
		return nil, fmt.Errorf("tilewarm: store root is required")
	}
	if tileset == "" {
		return nil, fmt.Errorf("tilewarm: store tileset is required")
	}
	if ext == "" {
		ext = "jpg"
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("tilewarm: create root %q: %w", root, err)
	}
	return &FSStore{Root: root, Tileset: tileset, Ext: ext}, nil
}

// Path returns the absolute filesystem path of a tile, regardless of
// whether the file exists.
func (s *FSStore) Path(t Tile) string {
	return filepath.Join(
		s.Root, s.Tileset,
		fmt.Sprintf("%d", t.Z),
		fmt.Sprintf("%d", t.X),
		fmt.Sprintf("%d.%s", t.Y, s.Ext),
	)
}

// Age returns the file's age via mtime. ok=false when the file
// doesn't exist.
func (s *FSStore) Age(t Tile, now time.Time) (time.Duration, bool) {
	st, err := os.Stat(s.Path(t))
	if err != nil {
		return 0, false
	}
	return now.Sub(st.ModTime()), true
}

// Put atomically writes the tile bytes. Creates parent directories
// as needed.
func (s *FSStore) Put(t Tile, data []byte) error {
	final := s.Path(t)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
