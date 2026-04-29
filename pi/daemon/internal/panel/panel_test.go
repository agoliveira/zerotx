package panel

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNullPanel(t *testing.T) {
	var p Panel = NullPanel{}
	if _, ok := p.Switch("SA"); ok {
		t.Errorf("NullPanel.Switch should return ok=false")
	}
	if _, ok := p.Selector("6POS"); ok {
		t.Errorf("NullPanel.Selector should return ok=false")
	}
	if _, ok := p.Button("rth"); ok {
		t.Errorf("NullPanel.Button should return ok=false")
	}
}

func TestFilePanel_LoadAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.yml")

	// Write initial state.
	if err := os.WriteFile(path, []byte(`
switches:
  SA: 1
  SE: 2
selectors:
  6POS: 3
buttons:
  rth: true
`), 0644); err != nil {
		t.Fatal(err)
	}

	p, err := NewFilePanel(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Initial load happened in NewFilePanel.
	if v, ok := p.Switch("SA"); !ok || v != 1 {
		t.Errorf("SA: got (%d, %v), want (1, true)", v, ok)
	}
	if v, ok := p.Switch("SE"); !ok || v != 2 {
		t.Errorf("SE: got (%d, %v), want (2, true)", v, ok)
	}
	if v, ok := p.Selector("6POS"); !ok || v != 3 {
		t.Errorf("6POS: got (%d, %v), want (3, true)", v, ok)
	}
	if v, ok := p.Button("rth"); !ok || !v {
		t.Errorf("rth: got (%v, %v), want (true, true)", v, ok)
	}

	// Switch not in file -> ok=false.
	if _, ok := p.Switch("SB"); ok {
		t.Errorf("SB should be absent")
	}

	// Modify file. Wait at least 1 ms so mtime resolution catches it,
	// then poll once explicitly via reload.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`
switches:
  SA: 0
  SE: 0
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := p.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if v, ok := p.Switch("SA"); !ok || v != 0 {
		t.Errorf("after reload SA: got (%d, %v), want (0, true)", v, ok)
	}
	// 6POS was removed -> should be absent.
	if _, ok := p.Selector("6POS"); ok {
		t.Errorf("6POS should be absent after reload")
	}
}

func TestFilePanel_MissingFile(t *testing.T) {
	// Missing file at startup: should not error, should report no state.
	p, err := NewFilePanel("/nonexistent/zerotx-panel-test.yml")
	if err != nil {
		t.Fatalf("new with missing file should not error, got: %v", err)
	}
	if _, ok := p.Switch("SA"); ok {
		t.Errorf("missing file: SA should be absent")
	}
}

func TestFilePanel_RunCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.yml")
	os.WriteFile(path, []byte("switches: {SA: 0}"), 0644)
	p, _ := NewFilePanel(path)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

func TestStdinPanel_BasicCommands(t *testing.T) {
	in := strings.NewReader("SA 1\nSE 2\nselector 6POS 4\nbutton rth on\nbutton rth off\n")
	out := &bytes.Buffer{}
	p := NewStdinPanel(in, out)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	// Run exits when stdin reaches EOF (which is immediate for strings.Reader).
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Run didn't return on EOF")
	}

	if v, ok := p.Switch("SA"); !ok || v != 1 {
		t.Errorf("SA: got (%d, %v), want (1, true)", v, ok)
	}
	if v, ok := p.Switch("SE"); !ok || v != 2 {
		t.Errorf("SE: got (%d, %v), want (2, true)", v, ok)
	}
	if v, ok := p.Selector("6POS"); !ok || v != 4 {
		t.Errorf("6POS: got (%d, %v), want (4, true)", v, ok)
	}
	if v, ok := p.Button("rth"); !ok || v != false {
		t.Errorf("rth: got (%v, %v), want (false, true)", v, ok)
	}
}

func TestStdinPanel_GuessesKind(t *testing.T) {
	// Bare "NAME VAL": numeric -> switch, bool-y -> button.
	in := strings.NewReader("SA 2\nrth on\n")
	out := &bytes.Buffer{}
	p := NewStdinPanel(in, out)
	ctx := context.Background()
	go p.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	if v, ok := p.Switch("SA"); !ok || v != 2 {
		t.Errorf("guess switch: SA = (%d, %v)", v, ok)
	}
	if v, ok := p.Button("rth"); !ok || !v {
		t.Errorf("guess button: rth = (%v, %v)", v, ok)
	}
}

func TestStdinPanel_Comments(t *testing.T) {
	in := strings.NewReader("# this is a comment\nSA 1\n")
	out := &bytes.Buffer{}
	p := NewStdinPanel(in, out)
	go p.Run(context.Background())
	time.Sleep(50 * time.Millisecond)

	if v, ok := p.Switch("SA"); !ok || v != 1 {
		t.Errorf("after comment: SA = (%d, %v)", v, ok)
	}
}

func TestPanel_CaseInsensitive(t *testing.T) {
	// Stdin: lowercase typed, uppercase looked up.
	in := strings.NewReader("se 2\nsa 1\n")
	out := &bytes.Buffer{}
	p := NewStdinPanel(in, out)
	go p.Run(context.Background())
	time.Sleep(50 * time.Millisecond)

	if v, ok := p.Switch("SE"); !ok || v != 2 {
		t.Errorf("lowercase typed, uppercase queried: SE = (%d, %v)", v, ok)
	}
	if v, ok := p.Switch("Sa"); !ok || v != 1 {
		t.Errorf("mixed case query: Sa = (%d, %v)", v, ok)
	}
}

func TestStdinPanel_6POSGuessesSelector(t *testing.T) {
	in := strings.NewReader("6POS 5\n6P15 3\n")
	out := &bytes.Buffer{}
	p := NewStdinPanel(in, out)
	go p.Run(context.Background())
	time.Sleep(50 * time.Millisecond)

	if v, ok := p.Selector("6POS"); !ok || v != 5 {
		t.Errorf("6POS should be a selector: got (%d, %v)", v, ok)
	}
	if v, ok := p.Selector("6P15"); !ok || v != 3 {
		t.Errorf("6P15 should be a selector: got (%d, %v)", v, ok)
	}
	// Should NOT be in switches.
	if _, ok := p.Switch("6POS"); ok {
		t.Errorf("6POS should not be a switch")
	}
}

func TestFilePanel_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.yml")
	if err := os.WriteFile(path, []byte("switches: {se: 2, sa: 1}"), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := NewFilePanel(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := p.Switch("SE"); !ok || v != 2 {
		t.Errorf("file lowercase, uppercase query: SE = (%d, %v)", v, ok)
	}
	if v, ok := p.Switch("SA"); !ok || v != 1 {
		t.Errorf("file lowercase, uppercase query: SA = (%d, %v)", v, ok)
	}
}
