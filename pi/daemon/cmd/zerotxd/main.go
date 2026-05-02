// Command zerotxd is the ZeroTX ground-station daemon.
//
// M2.1: opens a USB joystick, opens the USB-CDC link to the RP2040, runs the
// passthrough mapper at 50Hz, sends CHANNEL_INTENT frames. No mixer logic,
// no logic switches, no HTTP+WS API yet (those land in M2.2 / M2.3).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/api"
	"github.com/agoliveira/zerotx/pi/daemon/internal/arm"
	"github.com/agoliveira/zerotx/pi/daemon/internal/audio"
	"github.com/agoliveira/zerotx/pi/daemon/internal/devices/display"
	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/joystick"
	"github.com/agoliveira/zerotx/pi/daemon/internal/logbuf"
	"github.com/agoliveira/zerotx/pi/daemon/internal/logic"
	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/narrator"
	"github.com/agoliveira/zerotx/pi/daemon/internal/panel"
	"github.com/agoliveira/zerotx/pi/daemon/internal/recorder"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
	"github.com/agoliveira/zerotx/pi/daemon/internal/telemetry"

	"go.bug.st/serial"
)

const version = "0.22.3-display-protocol"

func main() {
	// SDL2 wants the event pump on the main OS thread. Lock it now so any
	// goroutine that touches sdl.PollEvent runs here.
	runtime.LockOSThread()

	portFlag := flag.String("port", "", "RP2040 USB-CDC device (auto-detect if empty)")
	baudFlag := flag.Int("baud", 115200, "serial baud (USB-CDC ignores)")
	modelFlag := flag.String("model", "", "ZeroTX model file")
	jsIndex := flag.Int("joystick", -1, "SDL joystick index (-1 = first match by -joystick-name)")
	jsName := flag.String("joystick-name", "", "case-insensitive substring of joystick name to open")
	listJS := flag.Bool("list-joysticks", false, "list joysticks and exit")
	verbose := flag.Bool("v", false, "verbose: print channel values periodically")
	verboseLogic := flag.Bool("v-logic", false, "verbose: also log logic switch state changes")
	rate := flag.Int("rate-hz", 1000/ipc.CRSFPeriodMs, "channel intent send rate (Hz)")
	apiAddr := flag.String("api", "", "API server bind address (e.g. 127.0.0.1:8080); empty disables")
	webDir := flag.String("web-dir", "", "serve web GUI from this filesystem path (default: embedded). Useful for fast iteration during development.")
	modelImage := flag.String("model-image", "", "path to model bitmap file (BMP/PNG/JPG); shown in Model tab if set")
	panelFile := flag.String("panel-file", "", "GCS panel state YAML; live-reloaded on edit")
	panelStdin := flag.Bool("panel-stdin", false, "read panel commands from stdin (mutually exclusive with -panel-file)")
	soundsDir := flag.String("sounds-dir", os.ExpandEnv("$HOME/fpv/Edgetx/sd/SOUNDS"), "audio sample directory (EdgeTX-compatible layout)")
	soundsLang := flag.String("sounds-lang", "en", "language subdirectory under -sounds-dir (e.g. en, pt)")
	noAudio := flag.Bool("no-audio", false, "disable audio playback (PLAY_TRACK CFs are silent)")
	audioThreshold := flag.String("audio-threshold", "notice", "audio threshold: info / notice / warning / critical (critical events ignore threshold)")
	recordingsDir := flag.String("recordings-dir", defaultRecordingsDir(), "directory for saved flight recordings")
	noRecordings := flag.Bool("no-recordings", false, "disable flight recording entirely")
	keepRecordings := flag.Int("keep-recordings", 10, "number of most-recent recordings to retain (older deleted on save)")
	displayPort := flag.String("display-port", "", "HUB75 display device serial port (e.g. /dev/ttyACM1); empty disables display output")
	displayBrightness := flag.Int("display-brightness", 80, "HUB75 display brightness 0-100")
	flag.Parse()

	if *panelFile != "" && *panelStdin {
		log.Fatal("cannot use both -panel-file and -panel-stdin")
	}

	// Capture log output into a ring buffer so the API can serve recent
	// lines back to the GUI. Stderr still gets the same lines, so the
	// terminal experience is unchanged.
	logBuf := logbuf.New(2000)
	log.SetOutput(logBuf.TeeWriter(os.Stderr))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("zerotxd %s starting", version)

	// Joystick subsystem.
	if err := joystick.Init(); err != nil {
		log.Fatalf("sdl init: %v", err)
	}
	defer joystick.Quit()

	if *listJS {
		listAndExit()
	}

	// Open joystick.
	var jsReader *joystick.Reader
	if *jsName != "" {
		var err error
		var idx int
		jsReader, idx, err = joystick.OpenByName(*jsName)
		if err != nil {
			log.Fatalf("open joystick by name %q: %v", *jsName, err)
		}
		log.Printf("opened joystick #%d: %q (%d axes, %d buttons, %d hats)",
			idx, jsReader.Name(), jsReader.NumAxes(), jsReader.NumButtons(), jsReader.NumHats())
	} else if *jsIndex >= 0 {
		var err error
		jsReader, err = joystick.Open(*jsIndex)
		if err != nil {
			log.Fatalf("open joystick #%d: %v", *jsIndex, err)
		}
		log.Printf("opened joystick #%d: %q (%d axes, %d buttons, %d hats)",
			*jsIndex, jsReader.Name(), jsReader.NumAxes(), jsReader.NumButtons(), jsReader.NumHats())
	} else {
		// Default: first device if any.
		devs := joystick.List()
		if len(devs) == 0 {
			log.Println("no joysticks attached. starting without one (channels will use safe defaults)")
		} else {
			var err error
			jsReader, err = joystick.Open(0)
			if err != nil {
				log.Fatalf("open default joystick: %v", err)
			}
			log.Printf("opened joystick #0: %q (%d axes, %d buttons)", jsReader.Name(), jsReader.NumAxes(), jsReader.NumButtons())
		}
	}
	if jsReader != nil {
		defer jsReader.Close()
	}

	// Model (optional).
	var ztxModel *model.ZeroTXModel
	if *modelFlag != "" {
		var err error
		ztxModel, err = model.LoadZeroTX(*modelFlag)
		if err != nil {
			log.Fatalf("load model %s: %v", *modelFlag, err)
		}
		log.Printf("loaded model: %s (%d mixes, %d sensors)",
			ztxModel.EdgeTX.Header.Name, len(ztxModel.EdgeTX.MixData),
			len(ztxModel.EdgeTX.TelemetrySensors))
	} else {
		log.Println("no model loaded; channels will use safe defaults")
	}

	// RP2040 USB-CDC link.
	port := *portFlag
	if port == "" {
		var err error
		port, err = ipc.AutoDetectPort()
		if err != nil {
			log.Fatalf("autodetect port: %v", err)
		}
		if port == "" {
			log.Fatal("no serial port found; plug RP2040 or pass -port")
		}
		log.Printf("auto-detected port: %s", port)
	}
	link, err := ipc.Open(port, *baudFlag)
	if err != nil {
		log.Fatalf("open link: %v", err)
	}
	defer link.Close()
	link.LocalVersion = "zerotxd " + version
	link.OnLog = func(s string) { log.Printf("[mcu] %s", s) }
	link.OnFrame = func(f ipc.Frame) {
		// Heartbeats are routine and noisy under -v; counted in MCU logs anyway.
		if f.Type == ipc.MsgHeartbeat {
			return
		}
		if *verbose {
			log.Printf("[mcu] frame type=%#02x seq=%d payload=%d bytes", f.Type, f.Seq, len(f.Payload))
		}
	}

	// Telemetry: decode CRSF frames forwarded by the MCU into a typed
	// snapshot. The state lives for the lifetime of the daemon; on
	// model unload it's reset so cell-count detection re-runs against
	// the next pack.
	telemetryState := telemetry.New(log.Printf)
	link.OnTelemetry = func(payload []byte) {
		telemetryState.Feed(payload)
	}

	// Arm state machine. Tracks the GCS-side arming sequence (panel
	// arm key + confirm + throttle 0 + FC ready). Inputs are fed by
	// link.OnInputEvent (arm key from MCU), the channel intent loop
	// (throttle), the telemetry snapshot (FC ready), and the API
	// (Confirm). Events are drained in goroutines launched after
	// the context is established (below).
	//
	// The first MsgInputEvent for the arm key after boot is treated
	// as Init (so the state machine sees boot-time ground truth and
	// emits the "key up at boot" warning if applicable). Subsequent
	// events are normal KeyChanged transitions.
	armMachine := arm.New()
	var armInitOnce sync.Once
	link.OnInputEvent = func(inputID, state byte) {
		switch inputID {
		case ipc.InputArmKey:
			keyUp := state != 0
			fired := false
			armInitOnce.Do(func() {
				armMachine.Init(keyUp)
				fired = true
			})
			if !fired {
				armMachine.KeyChanged(keyUp)
			}
		default:
			log.Printf("ipc: unknown input id %#02x state=%d", inputID, state)
		}
	}
	log.Printf("link open on %s", port)

	// Panel (optional). NullPanel by default; replaced if a flag is set.
	var pnl panel.Panel = panel.NullPanel{}
	switch {
	case *panelFile != "":
		fp, err := panel.NewFilePanel(*panelFile)
		if err != nil {
			log.Fatalf("panel-file %s: %v", *panelFile, err)
		}
		pnl = fp
		log.Printf("panel: file backend, polling %s", *panelFile)
	case *panelStdin:
		sp := panel.NewStdinPanel(os.Stdin, os.Stderr)
		pnl = sp
		log.Println("panel: stdin REPL backend")
	default:
		log.Println("panel: null (no GCS panel state)")
	}

	// Build the joystick holder. The Stack captures its source.JoystickState
	// adapter once; we can then swap which device is active at runtime
	// (via API) without rebuilding the stack.
	jsHolder := newJoystickHolder()
	if jsReader != nil {
		jsHolder.Set(jsReader)
	}
	jsState := jsHolder.JoystickState()

	// Audio player. Created before any Stack is built so model load
	// goes straight into a working drain. Null player when -no-audio
	// or when no playback backend is found on the system.
	var player audio.Player
	if *noAudio {
		log.Println("audio: disabled (-no-audio)")
		player = &audio.NullPlayer{}
	} else {
		thr, ok := audio.ParseLevel(*audioThreshold)
		if !ok {
			log.Printf("audio: -audio-threshold %q invalid, defaulting to notice", *audioThreshold)
		}
		player = audio.New(audio.Config{
			SoundsDir: *soundsDir,
			Lang:      *soundsLang,
			Threshold: thr,
		})
	}
	defer player.Close()

	// Narrator builds and emits structured narrative announcements
	// (boot greeting, post-flight summary). Pure transformation;
	// owns no goroutines. Backed by the audio Player above.
	narr := narrator.New(player)

	// Recorder: SQLite-backed flight recording. In-memory while flying,
	// saved to <recordings-dir> on disarm. Failures here must not
	// affect flight; we fall back to a no-op recorder on construction
	// error or when explicitly disabled.
	var rec recorder.Interface = recorder.NoOpRecorder{}
	if !*noRecordings {
		r, err := recorder.New(recorder.Config{
			RecordingsDir: *recordingsDir,
			Keep:          *keepRecordings,
		})
		if err != nil {
			log.Printf("recorder: disabled (%v)", err)
		} else {
			rec = r
			log.Printf("recorder: dir=%s keep=%d", *recordingsDir, *keepRecordings)
		}
	} else {
		log.Println("recorder: disabled (-no-recordings)")
	}
	defer rec.Close()

	// Telemetry sampler: poll the telemetry snapshot at 5Hz and forward
	// to the recorder. The recorder throttles internally to avoid
	// duplicate rows when nothing has changed; this goroutine just has
	// to wake often enough to catch every meaningful update. Stops on
	// daemon shutdown via the same context as the rest of the daemon.
	telemSamplerStop := make(chan struct{})
	// dispMgr is initialized after the API setup below; declared here
	// so the sampler closure can capture it. nil until the display
	// section runs; sampler guards on nil.
	var dispMgr *display.Manager
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-telemSamplerStop:
				return
			case <-t.C:
				snap := telemetryState.Snapshot()
				if !snap.HaveAny {
					continue
				}
				rec.LogTelemetry(buildTelemetrySample(snap))
				if dispMgr != nil {
					dispMgr.SetState(telemetryToDisplayState(snap))
				}
			}
		}
	}()
	defer close(telemSamplerStop)

	// stack holder. nil => IDLE (no CRSF emission). The tick loop reads
	// this atomically each tick; the API server writes it via load/unload.
	holder := &stackHolder{}

	// If a model was provided at startup, build its Stack and go READY.
	if ztxModel != nil {
		s, err := BuildStack(ztxModel, jsState, pnl, player, rec)
		if err != nil {
			log.Fatalf("build stack: %v", err)
		}
		holder.Store(s)
		log.Printf("logic: %d switches, %d custom functions",
			len(ztxModel.EdgeTX.LogicalSw), len(ztxModel.EdgeTX.CustomFn))
		log.Println("daemon: READY")
	} else {
		log.Println("daemon: IDLE (no model loaded)")
	}

	// chHolder provides the API with the latest channel array. The tick
	// loop updates it after every Resolve(); the API reads it via the
	// closure passed to providers.
	chHolder := &channelHolder{}

	// Lifecycle.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigC
		log.Println("shutting down (SIGINT)")
		cancel()
	}()

	// Display device (HUB75 LED panel), optional. When -display-port
	// is set, run a Manager that opens the serial port, talks the
	// DISP protocol, and reconnects on failure. When unset, dispMgr
	// stays nil and all integration call sites guard on it.
	if *displayPort != "" {
		dispMgr = display.NewManager(display.ManagerConfig{
			Open: func() (display.Transport, error) {
				return serial.Open(*displayPort, &serial.Mode{
					BaudRate: 115200,
					Parity:   serial.NoParity,
					DataBits: 8,
					StopBits: serial.OneStopBit,
					InitialStatusBits: &serial.ModemOutputBits{
						DTR: false, RTS: false, // ESP32 reset workaround
					},
				})
			},
			OnEvent: func(ev display.Event) {
				if ev.Kind == "READY" {
					log.Printf("display: device ready: %v", ev.Args)
				} else if ev.Kind == "ERROR" {
					log.Printf("display: device error: %v", ev.Args)
				}
			},
		})
		go func() { _ = dispMgr.Run(ctx) }()
		dispMgr.SetBrightness(*displayBrightness)
		// Initial mode: IDLE if no model, PREFLIGHT once one is loaded.
		if ztxModel == nil {
			dispMgr.SetMode(display.ModeIdle)
		} else {
			dispMgr.SetMode(display.ModePreflight)
			dispMgr.SetThresholds(modelThresholdsToDisplay(ztxModel.ZeroTX.Thresholds))
		}
	}

	// Arm state machine: launch the event drain (logs transitions
	// for now; audio/AUX wiring follow in subsequent steps) and a
	// 1Hz Tick driver for the 60s arming-request timeout.
	go drainArmEvents(ctx, armMachine)
	go tickArmMachine(ctx, armMachine)

	// Start the API server if requested.
	if *apiAddr != "" {
		providers := buildAPIProviders(chHolder, holder, pnl, jsHolder, player, narr, telemetryState, rec, port, *modelImage, *modelFlag, *recordingsDir, logBuf, version, time.Now(), dispMgr, armMachine, ctx)
		apiSrv := api.NewServer(*apiAddr, providers)
		apiSrv.SetWebDir(*webDir)
		go func() {
			if err := apiSrv.Run(ctx); err != nil {
				log.Printf("api: %v", err)
			}
		}()
	}

	// Boot greeting. Narrative announcement that the daemon is ready.
	// Played once at the configured threshold; if -no-audio or audio
	// threshold filters it out, this is silent. Either way the daemon
	// keeps booting; the greeting is presentational, never a gate.
	narr.PlayBootGreeting()

	// Goroutines.
	go func() {
		if err := link.Run(ctx); err != nil {
			log.Printf("link run: %v", err)
			cancel()
		}
	}()
	// Register the hot-plug callback BEFORE starting the pump. When SDL
	// reports a new joystick has appeared, we check whether its GUID
	// matches the currently-installed-but-disconnected reader. If so,
	// reattach transparently and resume emission. Different GUID = a
	// different controller plugged in; that's a swap and goes through
	// the API like any other selection.
	joystick.SetOnDeviceAdded(func(deviceIndex int) {
		newGUID := joystick.GUIDForIndex(deviceIndex)
		if newGUID == "" {
			return
		}
		curr := jsHolder.Reader()
		if curr == nil {
			// No previous reader to match against. Operator has to
			// pick from /api/v1/joysticks via the GUI.
			return
		}
		if curr.Connected() {
			// We already have a working device; nothing to do. (This
			// fires when a *second* joystick is plugged in alongside
			// the current one.)
			return
		}
		if curr.GUID() != newGUID {
			// Different physical device. Don't auto-attach; the
			// operator picks it explicitly via the API/GUI.
			log.Printf("joystick: new device appeared (GUID %s) but does not match disconnected reader (GUID %s); ignoring", newGUID, curr.GUID())
			return
		}
		// Matching GUID: reattach. Bypass the armed-flight lock by
		// passing emergency=true; this is reattachment, not a swap.
		next, err := joystick.Open(deviceIndex)
		if err != nil {
			log.Printf("joystick: reattach failed: %v", err)
			return
		}
		prev, swapErr := jsHolder.Swap(next, true)
		if swapErr != nil {
			next.Close()
			log.Printf("joystick: reattach swap failed: %v", swapErr)
			return
		}
		if prev != nil {
			prev.Close()
		}
		log.Printf("joystick: reattached %q (GUID %s)", next.Name(), newGUID)
	})

	// Run the SDL event pump as a daemon-level goroutine. It dispatches
	// to all registered readers, so opening/closing readers at runtime
	// (joystick swap on the Pre-flight tab) just works.
	go joystick.PumpEvents(ctx)

	// Run panel backend in its own goroutine if it has a Run method.
	type runnable interface {
		Run(context.Context) error
	}
	if r, ok := pnl.(runnable); ok {
		go func() {
			if err := r.Run(ctx); err != nil {
				log.Printf("panel run: %v", err)
			}
		}()
	}

	// 50Hz mapper -> CHANNEL_INTENT.
	period := time.Second / time.Duration(*rate)
	if period <= 0 {
		period = 20 * time.Millisecond
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	// Joystick-loss hold window: when the active joystick is disconnected,
	// keep emitting last-known channel intents for this many milliseconds,
	// then go silent. RP2040+FC failsafe handles the rest.
	const joystickHoldMs = 500
	var jsLossLogged bool

	var lastCh [ipc.Channels]uint16
	var lastLogic []bool
	first := true
	for {
		select {
		case <-ctx.Done():
			log.Println("daemon stopped.")
			return
		case <-ticker.C:
			s := holder.Load()
			if s == nil {
				// IDLE: no model loaded. Don't emit CRSF; RP2040's own
				// failsafe takes over (no input -> hold-then-failsafe).
				continue
			}

			// Joystick disconnect handling. Three states:
			//   1. Joystick installed and connected   -> normal emission
			//   2. Joystick was lost <500ms ago        -> hold last-known (still emit)
			//   3. Joystick was lost >=500ms ago       -> stop emission entirely
			// State 2 gives the operator a brief window where transient USB
			// glitches don't immediately throw the airplane into failsafe.
			// State 3 lets the RP2040 watchdog time out, which lets the FC
			// failsafe (RTH if configured) take over.
			if r := jsHolder.Reader(); r != nil && !r.Connected() {
				lostMs := time.Since(r.LostAt()).Milliseconds()
				if !jsLossLogged {
					log.Printf("joystick lost: holding last-known for %dms then failsafe", joystickHoldMs)
					jsLossLogged = true
				}
				if lostMs >= joystickHoldMs {
					// Stop emission entirely. Skip this tick.
					continue
				}
				// else: fall through and emit last-known values.
			} else if jsLossLogged && jsHolder.Connected() {
				log.Printf("joystick: link recovered")
				jsLossLogged = false
			}

			ch := s.Mapper.Resolve()
			chHolder.Set(ch)
			// Feed throttle (CH3 by EdgeTX convention) into the arm
			// state machine. CRSF range minimum is ~172; use a
			// generous 200 cutoff so floating-point jitter near idle
			// doesn't false-positive as "throttle non-zero".
			armMachine.ThrottleChanged(ch[2] <= 200)
			// FC ready-to-arm: derive from flight mode telemetry.
			// Permissive heuristic for now — fresh, non-empty mode
			// string counts as ready. Real FCs (INAV/ArduPilot)
			// signal pre-arm errors via decorations in the mode
			// string (asterisks, '!' prefixes); refinement to check
			// those will come once we have real FC telemetry to
			// pattern against.
			tsnap := telemetryState.Snapshot()
			fcReady := tsnap.FlightMode != nil && !tsnap.FlightMode.Stale && tsnap.FlightMode.Data.Mode != ""
			armMachine.FCReadyChanged(fcReady)
			if err := link.SendChannelIntent(ch); err != nil {
				log.Printf("send intent: %v", err)
				continue
			}
			if *verbose && (first || ch != lastCh) {
				log.Printf("channels: %v", trimZeros(ch))
				lastCh = ch
				first = false
			}
			if *verboseLogic {
				snap := s.Engine.Snapshot()
				if !boolSliceEq(lastLogic, snap) {
					log.Printf("logic: %s", formatLogic(snap))
					lastLogic = append(lastLogic[:0], snap...)
				}
			}
		}
	}
}

// boolSliceEq returns true when two bool slices have identical contents.
func boolSliceEq(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// formatLogic renders a logic switch bitmap as "L1=1 L2=0 L3=1 L4=0".
func formatLogic(snap []bool) string {
	parts := make([]string, 0, len(snap))
	for i, v := range snap {
		bit := 0
		if v {
			bit = 1
		}
		parts = append(parts, fmt.Sprintf("L%d=%d", i+1, bit))
	}
	return strings.Join(parts, " ")
}

func listAndExit() {
	devs := joystick.List()
	if len(devs) == 0 {
		fmt.Println("no joysticks attached")
		os.Exit(0)
	}
	for _, d := range devs {
		fmt.Printf("  #%d  %s  (axes=%d buttons=%d hats=%d) GUID=%s\n",
			d.Index, d.Name, d.NumAxes, d.NumBtns, d.NumHats, d.GUID)
	}
	os.Exit(0)
}

// trimZeros shows just enough of the channel array to be readable in logs.
func trimZeros(ch [ipc.Channels]uint16) []uint16 {
	last := 0
	for i, v := range ch {
		if v != ipc.CrsfChMid && v != ipc.CrsfChMin {
			last = i + 1
		}
	}
	if last < 6 {
		last = 6
	}
	return ch[:last]
}

// channelHolder is a thread-safe latest-value holder for the channel
// array. Updated each tick by the main loop, read by the API.
type channelHolder struct {
	mu sync.RWMutex
	ch [ipc.Channels]uint16
}

func (h *channelHolder) Set(ch [ipc.Channels]uint16) {
	h.mu.Lock()
	h.ch = ch
	h.mu.Unlock()
}

func (h *channelHolder) Get() [ipc.Channels]uint16 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ch
}

// buildAPIProviders wires the daemon's components into the API package's
// Providers struct. Most closures read through the stackHolder so they
// observe live model swaps.
func buildAPIProviders(
	chH *channelHolder,
	holder *stackHolder,
	pnl panel.Panel,
	jsHolder *joystickHolder,
	player audio.Player,
	narr *narrator.Narrator,
	telemState *telemetry.State,
	rec recorder.Interface,
	port string,
	modelImagePath string,
	modelDefaultPath string,
	recordingsDir string,
	logBuf *logbuf.Buffer,
	version string,
	startedAt time.Time,
	dispMgr *display.Manager,
	armMachine *arm.Machine,
	ctx context.Context,
) *api.Providers {
	return &api.Providers{
		Channels: chH.Get,
		Logic: func() map[string]bool {
			s := holder.Load()
			if s == nil {
				return nil
			}
			snap := s.Engine.Snapshot()
			out := make(map[string]bool, len(snap))
			for i, v := range snap {
				out[fmt.Sprintf("L%d", i+1)] = v
			}
			return out
		},
		Panel: pnl.Snapshot,
		Joystick: func() *api.JoystickSnapshot {
			r := jsHolder.Reader()
			if r == nil {
				return nil
			}
			snap := r.Snapshot()
			axes := make([]float64, len(snap.Axes))
			for i, v := range snap.Axes {
				axes[i] = float64(v) / 32767.0
			}
			return &api.JoystickSnapshot{
				Name:    r.Name(),
				Axes:    axes,
				Buttons: append([]bool(nil), snap.Buttons...),
			}
		},
		Link: func() api.LinkSnapshot {
			return api.LinkSnapshot{State: "active", Port: port}
		},
		Model: func() api.ModelSummary {
			s := holder.Load()
			if s == nil {
				return api.ModelSummary{Available: false}
			}
			return api.ModelSummary{
				Name:      s.Model.EdgeTX.Header.Name,
				Mixes:     len(s.Model.EdgeTX.MixData),
				Logic:     len(s.Model.EdgeTX.LogicalSw),
				CustomFn:  len(s.Model.EdgeTX.CustomFn),
				Sensors:   len(s.Model.EdgeTX.TelemetrySensors),
				Available: true,
			}
		},
		ModelDetails: func() api.ModelDetails {
			s := holder.Load()
			if s == nil {
				return api.ModelDetails{Available: false}
			}
			return buildModelDetails(s.Model, s.Engine, modelImagePath)
		},
		ModelImagePath: func() string { return modelImagePath },
		Logs: func(since time.Time) []api.LogEntry {
			snap := logBuf.Snapshot(since)
			out := make([]api.LogEntry, len(snap))
			for i, e := range snap {
				out[i] = api.LogEntry{
					Time: e.Time.UTC().Format(time.RFC3339Nano),
					Msg:  e.Msg,
				}
			}
			return out
		},
		Preflight: func() api.Preflight {
			return buildPreflight(holder, jsHolder, port, modelDefaultPath)
		},
		LoadModel: func(path string) error {
			if err := loadModel(holder, jsHolder.JoystickState(), pnl, player, rec, path); err != nil {
				return err
			}
			if dispMgr != nil {
				if s := holder.Load(); s != nil && s.Model != nil {
					dispMgr.SetThresholds(modelThresholdsToDisplay(s.Model.ZeroTX.Thresholds))
				}
				dispMgr.SetMode(display.ModePreflight)
			}
			return nil
		},
		UnloadModel: func() {
			if s := holder.Load(); s != nil {
				log.Printf("model unloaded: %s", s.Model.EdgeTX.Header.Name)
			}
			holder.Store(nil)
			telemState.ClearHome()
			if dispMgr != nil {
				dispMgr.SetThresholds(nil)
				dispMgr.SetMode(display.ModeIdle)
			}
		},
		Joysticks: func() []api.JoystickDevice {
			devs := joystick.List()
			out := make([]api.JoystickDevice, len(devs))
			for i, d := range devs {
				out[i] = api.JoystickDevice{
					Index:   d.Index,
					Name:    d.Name,
					Axes:    d.NumAxes,
					Buttons: d.NumBtns,
					Hats:    d.NumHats,
					GUID:    d.GUID,
				}
			}
			return out
		},
		SelectJoystick: func(index int, emergency bool) error {
			next, err := joystick.Open(index)
			if err != nil {
				return err
			}
			prev, swapErr := jsHolder.Swap(next, emergency)
			if swapErr != nil {
				next.Close()
				return swapErr
			}
			if prev != nil {
				prev.Close()
				log.Printf("joystick: swapped from %q to %q", prev.Name(), next.Name())
			} else {
				log.Printf("joystick: opened %q (%d axes, %d buttons)", next.Name(), next.NumAxes(), next.NumButtons())
			}
			return nil
		},
		ReleaseJoystick: func() error {
			prev, err := jsHolder.Swap(nil, false)
			if err != nil {
				return err
			}
			if prev != nil {
				prev.Close()
				log.Printf("joystick: released %q", prev.Name())
			}
			return nil
		},
		ListModels: listModels,
		SetFlightArmed: func(armed bool) {
			// Arming starts a recording; disarming closes it (which
			// auto-saves and rotates) and auto-acknowledges any active
			// audio alarms so the post-flight environment isn't still
			// beeping. Recorder failures are logged inside the package
			// and never block this code path.
			if armed {
				name := ""
				if s := holder.Load(); s != nil && s.Model != nil {
					name = s.Model.EdgeTX.Header.Name
				}
				rec.OnArm(name, "")
				// Record home position if GPS has a fix. Idempotent
				// across re-arms (force=false) so multiple takeoffs
				// in one session keep the original home.
				if telemState.SetHome(false) {
					log.Printf("home position set on arm")
				}
				if dispMgr != nil {
					dispMgr.SetMode(display.ModeFlight)
				}
			} else {
				path := rec.OnDisarm()
				player.AcknowledgeAll()
				if dispMgr != nil {
					dispMgr.SetMode(display.ModePostflight)
					// Postflight banner sticks for 30s on the device,
					// then we drop back to PREFLIGHT (model still
					// loaded) or IDLE.
					go func() {
						select {
						case <-time.After(30 * time.Second):
						case <-ctx.Done():
							return
						}
						if dispMgr == nil {
							return
						}
						if s := holder.Load(); s != nil && s.Model != nil {
							dispMgr.SetMode(display.ModePreflight)
						} else {
							dispMgr.SetMode(display.ModeIdle)
						}
					}()
				}

				// Post-flight narration. If recording produced a
				// saved file, summarize it and emit the narrative
				// announcement. Failures here (file missing,
				// summary error, etc.) are logged but do not
				// affect any other disarm-side cleanup. We launch
				// in a goroutine so the disarm path stays snappy
				// — summary computation is cheap but the audio
				// queue is async anyway, so launching async
				// matches the rest of the audio path's contract.
				if path != "" {
					go func(p string) {
						summary, err := recorder.Summarize(p)
						if err != nil {
							log.Printf("post-flight: summarize %s: %v", p, err)
							return
						}
						narr.PlayPostFlight(summary)
					}(path)
				}
			}
			jsHolder.SetFlightArmed(armed)
		},
		Telemetry: func() interface{} {
			return telemState.Snapshot()
		},
		Arm: func() interface{} {
			return armMachine.Snapshot()
		},
		ArmConfirm: armMachine.Confirm,
		Audio: func() api.AudioInfo {
			return api.AudioInfo{
				Threshold:    player.Threshold().String(),
				ActiveAlarms: player.ActiveAlarms(),
			}
		},
		SetAudioThreshold: func(level string) error {
			l, ok := audio.ParseLevel(level)
			if !ok {
				return fmt.Errorf("invalid level %q (want info|notice|warning|critical)", level)
			}
			player.SetThreshold(l)
			return nil
		},
		Acknowledge:    player.Acknowledge,
		AcknowledgeAll: player.AcknowledgeAll,

		Recordings: func() ([]api.Recording, error) {
			recs, err := rec.Recordings()
			if err != nil {
				return nil, err
			}
			out := make([]api.Recording, 0, len(recs))
			for _, r := range recs {
				out = append(out, api.Recording{
					Name:    r.Name,
					Path:    r.Path,
					Size:    r.Size,
					ModTime: r.ModTime,
				})
			}
			return out, nil
		},
		Summarize: func(name string) (interface{}, error) {
			// Reject anything that looks like a path traversal. The
			// name parameter is just a filename basename; if the
			// caller is trying to walk the filesystem, refuse.
			if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
				return nil, fmt.Errorf("invalid recording name")
			}
			path := filepath.Join(recordingsDir, name)
			return recorder.Summarize(path)
		},

		Version: version,
		Uptime:  func() time.Duration { return time.Since(startedAt) },
	}
}

// loadModel parses a YAML at the given path and atomically swaps in
// the new Stack. Returns an error without touching the active stack if
// parsing or building fails.
func loadModel(holder *stackHolder, jsState source.JoystickState, pnl panel.Panel, player audio.Player, rec recorder.Interface, path string) error {
	m, err := model.LoadZeroTX(path)
	if err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	s, err := BuildStack(m, jsState, pnl, player, rec)
	if err != nil {
		return fmt.Errorf("build stack for %s: %w", path, err)
	}
	holder.Store(s)
	log.Printf("model loaded: %s (%d mixes, %d logic, %d CFs, %d sensors) from %s",
		m.EdgeTX.Header.Name,
		len(m.EdgeTX.MixData), len(m.EdgeTX.LogicalSw),
		len(m.EdgeTX.CustomFn), len(m.EdgeTX.TelemetrySensors), path)
	return nil
}

// buildPreflight returns the aggregate readiness snapshot the GUI's
// pre-flight tab consumes via /api/v1/preflight.
func buildPreflight(holder *stackHolder, jsHolder *joystickHolder, port, modelDefaultPath string) api.Preflight {
	out := api.Preflight{
		GroundStation: api.PreflightGS{
			LinkPort:  port,
			LinkState: "active",
		},
	}

	// Joystick.
	if r := jsHolder.Reader(); r != nil {
		snap := r.Snapshot()
		out.Joystick.Selected = &api.PreflightJoystick{
			Name:      r.Name(),
			Axes:      len(snap.Axes),
			Buttons:   len(snap.Buttons),
			Connected: r.Connected(),
		}
	}

	// Model + state.
	s := holder.Load()
	if s != nil {
		out.Model.Loaded = &api.PreflightModel{
			Name:     s.Model.EdgeTX.Header.Name,
			Mixes:    len(s.Model.EdgeTX.MixData),
			Logic:    len(s.Model.EdgeTX.LogicalSw),
			CustomFn: len(s.Model.EdgeTX.CustomFn),
			Sensors:  len(s.Model.EdgeTX.TelemetrySensors),
			Path:     modelDefaultPath, // best-effort; runtime swap may not match
		}
		out.State = "ready"
	} else {
		out.State = "idle"
	}

	// Blockers: the things stopping us from being ready to fly.
	if out.Model.Loaded == nil {
		out.Blockers = append(out.Blockers, "no model loaded")
	}
	// Joystick is currently optional in the daemon; the operator's
	// "I confirm" step in the GUI checklist enforces the real-world
	// requirement. We don't list its absence as a blocker here.
	out.Ready = len(out.Blockers) == 0

	return out
}
func buildModelDetails(m *model.ZeroTXModel, eng *logic.Engine, imgPath string) api.ModelDetails {
	if m == nil {
		return api.ModelDetails{Available: false}
	}
	et := m.EdgeTX

	hasBitmap := false
	if imgPath != "" {
		if _, err := os.Stat(imgPath); err == nil {
			hasBitmap = true
		}
	}

	out := api.ModelDetails{
		Available: true,
		Name:      et.Header.Name,
		Bitmap:    et.Header.Bitmap,
		HasBitmap: hasBitmap,
		Airframe:  m.ZeroTX.Airframe,
	}

	if t := m.ZeroTX.Thresholds; t != nil {
		td := &api.ThresholdDetails{}
		if b := t.Battery; b != nil {
			cells := float64(b.Cells)
			td.Battery = &api.BatteryThresholdDetails{
				Cells:     b.Cells,
				CellWarnV: b.CellWarnV,
				CellCritV: b.CellCritV,
				CellMinV:  b.CellMinV,
				CellFullV: b.CellFullV,
				PackWarnV: cells * b.CellWarnV,
				PackCritV: cells * b.CellCritV,
				PackMinV:  cells * b.CellMinV,
				PackFullV: cells * b.CellFullV,
			}
		}
		if a := t.Altitude; a != nil {
			td.Altitude = &api.AltitudeThresholdDetails{WarnM: a.WarnM, CritM: a.CritM}
		}
		if d := t.Distance; d != nil {
			td.Distance = &api.DistanceThresholdDetails{WarnM: d.WarnM, CritM: d.CritM}
		}
		if l := t.Link; l != nil {
			td.Link = &api.LinkThresholdDetails{
				RSSIWarnDBM: l.RSSIWarnDBM, RSSICritDBM: l.RSSICritDBM,
				LQWarnPct: l.LQWarnPct, LQCritPct: l.LQCritPct,
			}
		}
		if ft := t.FlightTime; ft != nil {
			td.FlightTime = &api.FlightTimeThresholdDetails{WarnS: ft.WarnS, CritS: ft.CritS}
		}
		out.Thresholds = td
	}

	// Mixes: stored as a slice in YAML order, just translate field names.
	for i, mix := range et.MixData {
		out.Mixes = append(out.Mixes, api.MixDetail{
			Index:     i,
			Ch:        mix.DestCh + 1,
			Name:      mix.Name,
			Source:    mix.SrcRaw,
			Weight:    mix.Weight,
			Offset:    mix.Offset,
			Switch:    mix.Swtch,
			Mltpx:     mix.Mltpx,
			DelayUp:   mix.DelayUp,
			DelayDown: mix.DelayDown,
		})
	}

	// Logic switches: keyed by int. Sort keys so L1 comes before L2 etc.
	logicSnap := eng.Snapshot()
	logicKeys := sortedIntKeys(et.LogicalSw)
	for _, k := range logicKeys {
		ls := et.LogicalSw[k]
		active := false
		if k < len(logicSnap) {
			active = logicSnap[k]
		}
		out.LogicSwitches = append(out.LogicSwitches, api.LogicSwitchDetail{
			Name:     fmt.Sprintf("L%d", k+1),
			Func:     stripFuncPrefix(ls.Func),
			Def:      ls.Def,
			Andsw:    ls.Andsw,
			Delay:    float64(ls.Delay) / 10.0,
			Duration: float64(ls.Duration) / 10.0,
			Active:   active,
		})
	}

	// Custom functions.
	for _, k := range sortedIntKeys(et.CustomFn) {
		cf := et.CustomFn[k]
		out.CustomFns = append(out.CustomFns, api.CustomFnDetail{
			ID:     k + 1,
			Switch: cf.Swtch,
			Func:   cf.Func,
			Def:    trimNulls(cf.Def),
		})
	}

	// Sensors.
	for _, k := range sortedIntKeys(et.TelemetrySensors) {
		s := et.TelemetrySensors[k]
		out.Sensors = append(out.Sensors, api.SensorDetail{
			Index: k,
			Name:  trimNulls(s.Label),
			Unit:  sensorUnitName(s.Unit),
		})
	}

	return out
}

// sortedIntKeys returns the keys of an int-keyed map in ascending order.
func sortedIntKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// trimNulls strips trailing null bytes (and any trailing whitespace) from
// EdgeTX strings. EdgeTX stores fixed-width fields with null padding.
func trimNulls(s string) string {
	return strings.TrimRight(s, "\x00 \t")
}

// stripFuncPrefix removes the EdgeTX "FUNC_" prefix from logic switch
// func names ("FUNC_VNEG" -> "VNEG"). Display polish; the underlying
// YAML still contains the prefixed form.
func stripFuncPrefix(s string) string {
	return strings.TrimPrefix(s, "FUNC_")
}

// sensorUnitName converts EdgeTX UNIT_* enum values to display strings.
// Source: EdgeTX firmware src/dataconstants.h, recent versions.
//
// Indices 0-30 are scalar units. Indices 31+ are "virtual" or formatter
// types (cells, datetime, GPS, bitfield, text, flight mode) that don't
// have a single character unit string — the sensor name itself
// describes the value. We return "" for these so the GUI hides the
// "(u<N>)" suffix that previously appeared.
var sensorUnitNames = []string{
	"",      // 0  UNIT_RAW
	"V",     // 1  UNIT_VOLTS
	"A",     // 2  UNIT_AMPS
	"mA",    // 3  UNIT_MILLIAMPS
	"kts",   // 4  UNIT_KTS
	"m/s",   // 5  UNIT_METERS_PER_SECOND
	"ft/s",  // 6  UNIT_FEET_PER_SECOND
	"km/h",  // 7  UNIT_KMH
	"mph",   // 8  UNIT_MPH
	"m",     // 9  UNIT_METERS
	"ft",    // 10 UNIT_FEET
	"°C",    // 11 UNIT_CELSIUS
	"°F",    // 12 UNIT_FAHRENHEIT
	"%",     // 13 UNIT_PERCENT
	"mAh",   // 14 UNIT_MAH
	"W",     // 15 UNIT_WATTS
	"mW",    // 16 UNIT_MILLIWATTS
	"dBm",   // 17 UNIT_DB (display as dBm; ELRS RSSI uses this)
	"rpm",   // 18 UNIT_RPMS
	"g",     // 19 UNIT_G
	"°",     // 20 UNIT_DEGREE
	"rad",   // 21 UNIT_RADIANS
	"ml",    // 22 UNIT_MILLILITERS
	"fl oz", // 23 UNIT_FLOZ
	"hPa",   // 24 (hPa in some EdgeTX builds)
	"min",   // 25 UNIT_MINUTES
	"s",     // 26 UNIT_SECONDS
	"#",     // 27 cells / count
	"us",    // 28 UNIT_US
	"ms",    // 29 UNIT_MS
	"Hz",    // 30 UNIT_HZ

	// 31+: virtual / formatter types with no single-character unit.
	// Empty string → GUI omits the "(u<N>)" suffix entirely.
	"", // 31 UNIT_CELLS
	"", // 32 UNIT_DATETIME
	"", // 33 UNIT_GPS
	"", // 34 UNIT_BITFIELD
	"", // 35 UNIT_TEXT
	"", // 36 UNIT_GPS_LONGITUDE
	"", // 37 UNIT_GPS_LATITUDE
	"", // 38 UNIT_DATETIME_YEAR
	"", // 39 UNIT_DATETIME_DAY_MONTH
	"", // 40 UNIT_DATETIME_HOUR_MIN  (or GPS in some versions)
	"", // 41 UNIT_DATETIME_SEC
	"", // 42 UNIT_FLIGHT_MODE
	"", // 43 reserved
	"", // 44 reserved
	"", // 45 reserved
	"", // 46 reserved
	"", // 47 reserved
	"", // 48 reserved
	"", // 49 reserved
	"", // 50 reserved
}

func sensorUnitName(idx int) string {
	if idx >= 0 && idx < len(sensorUnitNames) {
		return sensorUnitNames[idx]
	}
	return fmt.Sprintf("u%d", idx)
}

// listModels returns the *.yml files in the given directory, parsed
// for their model names on a best-effort basis. Files that fail to
// parse are still returned with Name="" so the GUI can flag them.
// Subdirectories are not traversed.
func listModels(dir string) ([]api.ModelFile, error) {
	if dir == "" {
		return nil, fmt.Errorf("dir is required")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	out := make([]api.ModelFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		full := filepath.Join(dir, name)
		mf := api.ModelFile{
			Path: full,
			File: name,
		}
		// Best-effort parse for the model's display name. Don't fail
		// the whole listing if one file is broken.
		if m, err := model.LoadEdgeTX(full); err == nil {
			mf.Name = m.Header.Name
		}
		out = append(out, mf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, nil
}

// defaultRecordingsDir returns the default directory for saved
// recordings. Honours $XDG_DATA_HOME when set, otherwise falls back to
// ~/.local/share/zerotx/recordings or just "./recordings" if the home
// directory can't be resolved.
func defaultRecordingsDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "zerotx", "recordings")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "zerotx", "recordings")
	}
	return "recordings"
}

// buildTelemetrySample converts a telemetry.Snapshot into the
// recorder's TelemetrySample shape. Pointer-typed fields preserve
// "no data" vs "zero": battery 0V is a real reading.
func buildTelemetrySample(s telemetry.Snapshot) recorder.TelemetrySample {
	out := recorder.TelemetrySample{}
	if s.Battery != nil && !s.Battery.Stale {
		v := s.Battery.Data.Volts
		a := s.Battery.Data.Amps
		pct := int(s.Battery.Data.Percent)
		mah := int(s.Battery.Data.UsedMAh)
		out.BatVolts = &v
		out.BatAmps = &a
		out.BatPct = &pct
		out.BatMAh = &mah
	}
	if s.GPS != nil && !s.GPS.Stale {
		lat := s.GPS.Data.LatDeg
		lon := s.GPS.Data.LonDeg
		alt := int(s.GPS.Data.AltMeters)
		kmh := s.GPS.Data.GroundKmh
		hdg := s.GPS.Data.HeadingDeg
		sats := int(s.GPS.Data.Sats)
		out.GpsLat = &lat
		out.GpsLon = &lon
		out.GpsAlt = &alt
		out.GpsKmh = &kmh
		out.GpsHdg = &hdg
		out.GpsSats = &sats
	}
	if s.Link != nil && !s.Link.Stale {
		rssi := int(s.Link.Data.UplinkRSSIdBm)
		lq := int(s.Link.Data.UplinkLQ)
		snr := int(s.Link.Data.UplinkSNR)
		out.LinkRSSI = &rssi
		out.LinkLQ = &lq
		out.LinkSNR = &snr
	}
	if s.FlightMode != nil {
		mode := s.FlightMode.Data.Mode
		out.FlightMode = &mode
	}
	return out
}

// === Display helpers ===

// modelThresholdsToDisplay converts model.Thresholds (per-cell battery,
// per-domain pointers) into display.Thresholds (pack-level battery,
// same per-domain shape). Returns nil if input is nil or has no
// populated domains; the display package treats nil as "clear all".
func modelThresholdsToDisplay(t *model.Thresholds) *display.Thresholds {
	if t == nil {
		return nil
	}
	out := &display.Thresholds{}
	if b := t.Battery; b != nil {
		cells := float64(b.Cells)
		out.Battery = &display.BatteryThresholds{
			WarnV: cells * b.CellWarnV,
			CritV: cells * b.CellCritV,
			MinV:  cells * b.CellMinV,
			FullV: cells * b.CellFullV,
		}
	}
	if a := t.Altitude; a != nil {
		out.Altitude = &display.AltitudeThresholds{WarnM: a.WarnM, CritM: a.CritM}
	}
	if d := t.Distance; d != nil {
		out.Distance = &display.DistanceThresholds{WarnM: d.WarnM, CritM: d.CritM}
	}
	if l := t.Link; l != nil {
		out.Link = &display.LinkThresholds{
			RSSIWarnDBM: l.RSSIWarnDBM, RSSICritDBM: l.RSSICritDBM,
			LQWarnPct: l.LQWarnPct, LQCritPct: l.LQCritPct,
		}
	}
	if ft := t.FlightTime; ft != nil {
		out.FlightTime = &display.FlightTimeThresholds{WarnS: ft.WarnS, CritS: ft.CritS}
	}
	// If nothing populated, return nil so the display clears.
	if out.Battery == nil && out.Altitude == nil && out.Distance == nil &&
		out.Link == nil && out.FlightTime == nil {
		return nil
	}
	return out
}

// telemetryToDisplayState maps a telemetry.Snapshot to a display.State.
// Only fields that are present (non-nil entries, non-stale data) are
// set on the output; the display preserves last-known values for
// absent fields.
func telemetryToDisplayState(s telemetry.Snapshot) display.State {
	var out display.State
	if s.Battery != nil {
		b := s.Battery.Data
		out.BatV = display.Float64Ptr(b.Volts)
		out.BatPct = display.IntPtr(int(b.Percent))
	}
	if s.GPS != nil {
		g := s.GPS.Data
		// CRSF altitude is meters + 1000 offset; subtract here.
		out.AltM = display.IntPtr(int(g.AltMeters) - 1000)
		out.SpdKmh = display.IntPtr(int(g.GroundKmh))
		out.Sats = display.IntPtr(int(g.Sats))
	}
	if s.Link != nil {
		l := s.Link.Data
		out.LinkPct = display.IntPtr(int(l.UplinkLQ))
	}
	if s.Home != nil {
		out.DistM = display.IntPtr(int(s.Home.Data.DistanceM))
	}
	if s.FlightMode != nil {
		out.FlightMode = s.FlightMode.Data.Mode
	}
	return out
}

// drainArmEvents consumes events from the arm state machine and
// translates them into side effects. For now (M3.1) it just logs;
// audio cues, AUX channel wiring, and GUI state updates land in
// later steps.
func drainArmEvents(ctx context.Context, m *arm.Machine) {
	events := m.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-events:
			log.Printf("arm: %s (state=%s)", e, m.State())
		}
	}
}

// tickArmMachine drives the 60s arming-request timeout. Calls Tick
// at 1Hz which is plenty of resolution for a 60s timeout. The
// state machine ignores Tick from any state other than
// ARMING_REQUESTED, so the cost of the call when nothing is
// pending is a single mutex acquire-release.
func tickArmMachine(ctx context.Context, m *arm.Machine) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.Tick(now)
		}
	}
}
