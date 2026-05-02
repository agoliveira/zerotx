package audio

// tts.go: Piper TTS subprocess management for the audio Player.
//
// The synthesizer is an external Piper binary (rhasspy/piper or
// OHF-Voice/piper1-gpl), launched once per voice at daemon start and
// kept alive. We feed it phrases via stdin in JSON-input mode; it
// writes a WAV file to a path we choose, and we then play that WAV
// through the existing playback path. Synthesised WAVs are cached on
// disk by hash(text + voice); subsequent identical phrases skip
// synthesis entirely.
//
// Two voices are supported by default: en (en_US-amy-medium) and
// pt (pt_BR-faber-medium). The voice used per Speak() call is the
// daemon's current Lang (Config.Lang); the bank-to-voice mapping
// lives in TTSConfig.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TTSConfig configures the Piper-based TTS synthesizer. Disabled when
// PiperBinary is empty (typical in CI / non-audio environments).
type TTSConfig struct {
	// PiperBinary is the absolute path to the piper executable. Empty
	// disables TTS entirely; Speak() becomes a no-op + log warning.
	PiperBinary string

	// VoicesDir holds the .onnx + .onnx.json voice model files. Each
	// voice is referenced by its basename (without extension).
	VoicesDir string

	// CacheDir is where synthesized WAVs are stored. Created if
	// missing. Cache key is hash(text + voice).
	CacheDir string

	// Voices maps a language bank name ("en", "pt", ...) to a voice
	// model basename ("en_US-amy-medium", "pt_BR-faber-medium").
	Voices map[string]string
}

// pipeProc owns one running Piper subprocess pinned to one voice.
// Speak() requests are serialised through reqMu; Piper handles them
// strictly in order anyway (single stdin/stdout stream), so callers
// don't get parallelism within one voice. Different voices run in
// parallel because each has its own pipeProc.
type pipeProc struct {
	voice string // voice basename, e.g. "en_US-amy-medium"
	bin   string
	model string

	mu    sync.Mutex // guards cmd/stdin/stdout and lifecycle
	cmd   *exec.Cmd
	stdin io.WriteCloser
	stdout *bufio.Reader

	reqMu sync.Mutex // serialises Synth requests on this voice
}

// piperRequest is the JSON line we feed Piper on stdin. We always
// supply output_file so Piper writes the WAV to a known path; we
// never read audio from stdout.
type piperRequest struct {
	Text       string `json:"text"`
	OutputFile string `json:"output_file"`
}

// piperResponse is what Piper emits on stdout per request: a JSON
// line echoing the request with the file path actually written.
// We don't strictly need to parse it; we just consume the line to
// keep stdout drained.
type piperResponse struct {
	Text       string `json:"text"`
	OutputFile string `json:"output_file"`
}

// newPipeProc constructs a pipeProc for a given voice. Does NOT start
// the process; Synth() does that lazily on first call (and on respawn
// after death). Returns nil + error if the model file is missing —
// no point creating a pipe for a voice the operator never installed.
func newPipeProc(bin, voicesDir, voice string) (*pipeProc, error) {
	model := filepath.Join(voicesDir, voice+".onnx")
	cfg := filepath.Join(voicesDir, voice+".onnx.json")
	if !fileExists(model) {
		return nil, fmt.Errorf("voice model not found: %s", model)
	}
	if !fileExists(cfg) {
		return nil, fmt.Errorf("voice config not found: %s", cfg)
	}
	return &pipeProc{
		voice: voice,
		bin:   bin,
		model: model,
	}, nil
}

// ensureRunning starts Piper if it isn't already alive. Caller must
// hold p.mu.
func (p *pipeProc) ensureRunning() error {
	if p.cmd != nil && p.cmd.Process != nil {
		// Already running. Verify it didn't quietly die: ProcessState
		// is nil while running, non-nil after exit.
		if p.cmd.ProcessState == nil {
			return nil
		}
		log.Printf("audio: piper voice=%s died (state=%s); respawning", p.voice, p.cmd.ProcessState)
		p.cmd = nil
		p.stdin = nil
		p.stdout = nil
	}
	// piper --model <path> --json-input
	// (no --output-raw: we use output_file in JSON, which produces WAV.)
	cmd := exec.Command(p.bin, "--model", p.model, "--json-input")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("piper stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("piper stdout: %w", err)
	}
	// Stderr goes to our stderr; piper logs progress / errors there.
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("piper start: %w", err)
	}
	p.cmd = cmd
	p.stdin = stdin
	p.stdout = bufio.NewReader(stdout)
	log.Printf("audio: piper voice=%s started (pid=%d)", p.voice, cmd.Process.Pid)
	return nil
}

// Synth renders `text` to the WAV file at `outPath` and returns when
// Piper has finished writing it. Bounded to 10s; a hung Piper at
// flight-time is unacceptable.
func (p *pipeProc) Synth(text, outPath string) error {
	p.reqMu.Lock()
	defer p.reqMu.Unlock()

	p.mu.Lock()
	if err := p.ensureRunning(); err != nil {
		p.mu.Unlock()
		return err
	}
	stdin := p.stdin
	stdout := p.stdout
	p.mu.Unlock()

	req := piperRequest{Text: text, OutputFile: outPath}
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal piper req: %w", err)
	}
	line = append(line, '\n')

	// Write under a deadline. Piper can hang if its model is borked.
	doneCh := make(chan error, 1)
	go func() {
		if _, err := stdin.Write(line); err != nil {
			doneCh <- fmt.Errorf("piper write: %w", err)
			return
		}
		// Piper emits one JSON line per request on stdout. Read until
		// newline to confirm completion. We don't need the contents;
		// the file is on disk by the time we see this.
		respLine, err := stdout.ReadString('\n')
		if err != nil {
			doneCh <- fmt.Errorf("piper read: %w", err)
			return
		}
		// Parse leniently. If it doesn't parse, that's fine; Piper
		// has produced the WAV and that's what we care about.
		var resp piperResponse
		_ = json.Unmarshal([]byte(strings.TrimSpace(respLine)), &resp)
		doneCh <- nil
	}()

	select {
	case err := <-doneCh:
		return err
	case <-time.After(10 * time.Second):
		// Best effort: kill the process so the next request gets a
		// fresh one. Synth() returns timeout regardless.
		p.mu.Lock()
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		p.cmd = nil
		p.stdin = nil
		p.stdout = nil
		p.mu.Unlock()
		return fmt.Errorf("piper timeout after 10s")
	}
}

// Close terminates the subprocess (if any). Safe to call multiple
// times.
func (p *pipeProc) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		// Give Piper a moment to exit on stdin EOF, then kill.
		done := make(chan struct{})
		go func() {
			_ = p.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			_ = p.cmd.Process.Kill()
			<-done
		}
	}
	p.cmd = nil
	p.stdin = nil
	p.stdout = nil
}

// ttsEngine is the per-Player TTS coordinator. Owns one pipeProc per
// configured voice, the cache directory, and the synth-to-cache logic.
type ttsEngine struct {
	cfg      TTSConfig
	procs    map[string]*pipeProc // keyed by voice basename
	mu       sync.Mutex           // guards procs map only
}

// TTSEngine is the exported handle audio.NewWithTTS accepts. The
// type itself is opaque to callers; construct via NewTTSEngine.
type TTSEngine = ttsEngine

// NewTTSEngine validates the config and constructs the engine. Returns
// nil + error if Piper binary isn't executable or no voices are usable.
// The caller (audio.NewWithTTS) treats nil as "TTS disabled" gracefully.
func NewTTSEngine(cfg TTSConfig) (*TTSEngine, error) {
	return newTTSEngine(cfg)
}

// newTTSEngine validates the config and constructs the engine. Returns
// nil + error if Piper binary isn't executable or no voices are usable.
// The caller (audio.New) treats nil as "TTS disabled" gracefully.
func newTTSEngine(cfg TTSConfig) (*ttsEngine, error) {
	if cfg.PiperBinary == "" {
		return nil, fmt.Errorf("PiperBinary not set")
	}
	if _, err := exec.LookPath(cfg.PiperBinary); err != nil {
		// Try absolute path resolution next.
		if _, statErr := os.Stat(cfg.PiperBinary); statErr != nil {
			return nil, fmt.Errorf("piper binary not found: %s", cfg.PiperBinary)
		}
	}
	if cfg.VoicesDir == "" {
		return nil, fmt.Errorf("VoicesDir not set")
	}
	if cfg.CacheDir == "" {
		return nil, fmt.Errorf("CacheDir not set")
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if len(cfg.Voices) == 0 {
		return nil, fmt.Errorf("no voices configured")
	}

	t := &ttsEngine{cfg: cfg, procs: make(map[string]*pipeProc)}
	// Pre-create pipeProc for each voice. Subprocesses don't actually
	// start until first Synth call.
	usable := 0
	for bank, voice := range cfg.Voices {
		p, err := newPipeProc(cfg.PiperBinary, cfg.VoicesDir, voice)
		if err != nil {
			log.Printf("audio: tts voice unavailable for bank=%s: %v", bank, err)
			continue
		}
		t.procs[voice] = p
		usable++
	}
	if usable == 0 {
		return nil, fmt.Errorf("no usable voices in %s", cfg.VoicesDir)
	}
	log.Printf("audio: tts ready: %d voice(s), cache=%s", usable, cfg.CacheDir)
	return t, nil
}

// voiceFor returns the voice basename mapped to the given lang bank.
// Falls back to any configured voice if the exact bank isn't mapped.
// Returns ("", false) if no voices at all.
func (t *ttsEngine) voiceFor(lang string) (string, bool) {
	if v, ok := t.cfg.Voices[lang]; ok {
		if _, ok := t.procs[v]; ok {
			return v, true
		}
	}
	// Fallback: any voice that has a running pipeProc.
	t.mu.Lock()
	defer t.mu.Unlock()
	for v := range t.procs {
		return v, true
	}
	return "", false
}

// cachePath returns the WAV file path for a given (text, voice) pair.
// Stable across runs (sha256 truncated to 16 hex chars).
func (t *ttsEngine) cachePath(text, voice string) string {
	sum := sha256.Sum256([]byte(voice + "\x00" + text))
	hash := hex.EncodeToString(sum[:8]) // 16 hex chars
	return filepath.Join(t.cfg.CacheDir, voice, hash+".wav")
}

// Synth returns a path to a WAV containing the synthesized text in
// the given voice. Cache hit: returns immediately. Cache miss:
// synthesizes via Piper, writes to cache, returns the path. Errors
// are returned to the caller; the caller decides whether to skip
// playback or fall back.
func (t *ttsEngine) Synth(text, voice string) (string, error) {
	if text = strings.TrimSpace(text); text == "" {
		return "", fmt.Errorf("empty text")
	}
	path := t.cachePath(text, voice)

	if fileExists(path) {
		return path, nil
	}

	// Cache miss. Ensure voice subdirectory exists, then synth.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("mkdir cache: %w", err)
	}

	t.mu.Lock()
	proc, ok := t.procs[voice]
	t.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("voice not loaded: %s", voice)
	}

	tmp := path + ".partial"
	defer os.Remove(tmp) // no-op if rename succeeded
	if err := proc.Synth(text, tmp); err != nil {
		return "", err
	}
	// Verify Piper actually wrote something.
	if !fileExists(tmp) {
		return "", fmt.Errorf("piper produced no output for %q", text)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("rename cache file: %w", err)
	}
	return path, nil
}

// Close terminates all Piper subprocesses.
func (t *ttsEngine) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.procs {
		p.Close()
	}
}
