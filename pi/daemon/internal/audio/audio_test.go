package audio

import (
	"os"
	"path/filepath"
	"testing"
)

// substituteFileExists swaps the package-level fileExists for a mock
// over the duration of the test, restoring it on cleanup.
func substituteFileExists(t *testing.T, mock func(path string) bool) {
	t.Helper()
	prev := fileExists
	fileExists = mock
	t.Cleanup(func() { fileExists = prev })
}

func TestResolve_LanguageSubdirPreferred(t *testing.T) {
	// Both /sounds/en/armed.wav and /sounds/armed.wav exist; lang
	// preference wins.
	have := map[string]bool{
		"/sounds/en/armed.wav": true,
		"/sounds/armed.wav":    true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}
	got, ok := p.resolve("armed")
	if !ok || got != "/sounds/en/armed.wav" {
		t.Errorf("got %q (ok=%v), want /sounds/en/armed.wav", got, ok)
	}
}

func TestResolve_FallsBackToRoot(t *testing.T) {
	// Lang dir has nothing; the name is found at the root.
	have := map[string]bool{
		"/sounds/siren.wav": true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}
	got, ok := p.resolve("siren")
	if !ok || got != "/sounds/siren.wav" {
		t.Errorf("got %q (ok=%v), want /sounds/siren.wav", got, ok)
	}
}

func TestResolve_TriesExtensionsInOrder(t *testing.T) {
	// Only .ogg exists; .wav is the first candidate but missing.
	have := map[string]bool{
		"/sounds/en/armed.ogg": true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav", ".ogg", ".mp3"},
	}}
	got, ok := p.resolve("armed")
	if !ok || got != "/sounds/en/armed.ogg" {
		t.Errorf("got %q (ok=%v), want /sounds/en/armed.ogg", got, ok)
	}
}

func TestResolve_HandlesEdgeTXSuffixedNames(t *testing.T) {
	// EdgeTX writes "armed.1x" as the def field; the file is
	// "armed.1x.wav" — the ".1x" is part of the stem, not a wrapper.
	have := map[string]bool{
		"/sounds/en/armed.1x.wav": true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}
	got, ok := p.resolve("armed.1x")
	if !ok || got != "/sounds/en/armed.1x.wav" {
		t.Errorf("got %q (ok=%v), want /sounds/en/armed.1x.wav", got, ok)
	}
}

func TestResolve_NotFound(t *testing.T) {
	substituteFileExists(t, func(p string) bool { return false })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Lang:       "en",
		Extensions: []string{".wav"},
	}}
	_, ok := p.resolve("ghost")
	if ok {
		t.Errorf("expected resolve to fail when no file exists")
	}
}

func TestResolve_NoLangOnlyRoot(t *testing.T) {
	// When Lang is empty, only root-level files are considered.
	have := map[string]bool{
		"/sounds/en/armed.wav": true, // not searched
		"/sounds/armed.wav":    true,
	}
	substituteFileExists(t, func(p string) bool { return have[p] })

	p := &shellPlayer{cfg: Config{
		SoundsDir:  "/sounds",
		Extensions: []string{".wav"},
	}}
	got, ok := p.resolve("armed")
	if !ok || got != "/sounds/armed.wav" {
		t.Errorf("got %q (ok=%v), want /sounds/armed.wav", got, ok)
	}
}

func TestNullPlayer_DoesNothing(t *testing.T) {
	// NullPlayer must be safe to call at any time and never block.
	p := &NullPlayer{}
	p.Play("track", "anything")
	p.Close()
	p.Play("track", "again-after-close") // also fine
}

func TestShellPlayer_QueueDropsWhenFull(t *testing.T) {
	// We use a real shellPlayer with a stub command. Set QueueDepth=1
	// and don't start the worker, so the channel buffer fills and the
	// next Play drops. Verifies the drop path doesn't deadlock.
	p := &shellPlayer{
		cfg:    Config{QueueDepth: 1},
		events: make(chan event, 1),
		done:   make(chan struct{}),
	}
	// Don't start p.run(); the channel will fill on the second Play.
	p.Play("track", "first")
	p.Play("track", "second") // dropped, must not block

	// Drain
	if got := <-p.events; got.name != "first" {
		t.Errorf("first event lost: %+v", got)
	}
}

func TestFileExists_RealFilesystem(t *testing.T) {
	// Sanity-check the production fileExists against a real tempfile.
	dir := t.TempDir()
	path := filepath.Join(dir, "real.wav")
	if err := os.WriteFile(path, []byte("not really wav"), 0644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(path) {
		t.Errorf("fileExists should return true for a real file")
	}
	if fileExists(filepath.Join(dir, "ghost.wav")) {
		t.Errorf("fileExists should return false for a missing file")
	}
	// Directories don't count as regular files.
	if fileExists(dir) {
		t.Errorf("fileExists should return false for a directory")
	}
}
