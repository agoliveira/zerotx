package panel

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// FilePanel reads switch/selector/button state from a YAML file and
// polls the file's modification time on a fixed interval to pick up
// edits. The poll interval is 500 ms by default.
//
// Use this for development and CI tests. Edit the YAML in any editor
// and the daemon reflects the change within one poll cycle.
//
// File schema:
//
//	switches:
//	  SA: 0
//	  SB: 1
//	  SE: 2
//	selectors:
//	  6POS: 3
//	buttons:
//	  rth: false
//
// Missing keys are not errors; they just don't appear in the panel
// state. ok=false is returned for them, mapper falls back to defaults.
type FilePanel struct {
	state
	path     string
	interval time.Duration
	lastMod  atomic.Int64 // unix nano of last seen mtime
}

type fileSchema struct {
	Switches  map[string]int  `yaml:"switches"`
	Selectors map[string]int  `yaml:"selectors"`
	Buttons   map[string]bool `yaml:"buttons"`
}

// NewFilePanel constructs a FilePanel for the given path. Performs an
// initial load synchronously so the panel reports valid state by the
// time NewFilePanel returns. If the file doesn't exist, returns an
// empty panel and logs a warning; the file may appear later and the
// poll loop will pick it up.
func NewFilePanel(path string) (*FilePanel, error) {
	p := &FilePanel{
		state:    newState(),
		path:     path,
		interval: 500 * time.Millisecond,
	}
	if err := p.reload(); err != nil {
		// Soft fail: missing file at startup is OK, log and continue.
		// Hard parse errors of an existing file are also non-fatal but
		// we do log them.
		log.Printf("panel: initial load of %s: %v", path, err)
	}
	return p, nil
}

// Run starts the polling loop. Blocks until ctx is done. Run in a
// goroutine.
func (p *FilePanel) Run(ctx context.Context) error {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := p.reload(); err != nil {
				// Already-logged in reload, just keep polling.
			}
		}
	}
}

func (p *FilePanel) reload() error {
	info, err := os.Stat(p.path)
	if err != nil {
		// Don't spam the log every poll if the file is missing.
		return err
	}
	mod := info.ModTime().UnixNano()
	if mod == p.lastMod.Load() {
		return nil
	}
	data, err := os.ReadFile(p.path)
	if err != nil {
		return fmt.Errorf("panel: read %s: %w", p.path, err)
	}
	var s fileSchema
	if err := yaml.Unmarshal(data, &s); err != nil {
		log.Printf("panel: %s: yaml parse error: %v", p.path, err)
		return err
	}
	if s.Switches == nil {
		s.Switches = make(map[string]int)
	}
	if s.Selectors == nil {
		s.Selectors = make(map[string]int)
	}
	if s.Buttons == nil {
		s.Buttons = make(map[string]bool)
	}
	p.state.replace(s.Switches, s.Selectors, s.Buttons)
	p.lastMod.Store(mod)
	log.Printf("panel: reloaded %s (%d switches, %d selectors, %d buttons)",
		p.path, len(s.Switches), len(s.Selectors), len(s.Buttons))
	return nil
}

// Switch implements Panel.
func (p *FilePanel) Switch(name string) (int, bool) { return p.state.getSwitch(name) }

// Selector implements Panel.
func (p *FilePanel) Selector(name string) (int, bool) { return p.state.getSelector(name) }

// Button implements Panel.
func (p *FilePanel) Button(name string) (bool, bool) { return p.state.getButton(name) }

// LastUpdate implements Panel.
func (p *FilePanel) LastUpdate() time.Time { return p.state.lastUpdate() }

// Snapshot implements Panel.
func (p *FilePanel) Snapshot() Snapshot { return p.state.snapshot() }
