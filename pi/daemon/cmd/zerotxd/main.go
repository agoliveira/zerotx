// Command zerotxd is the ZeroTX ground-station daemon.
//
// M2.1: opens a USB joystick, opens the USB-CDC link to the RP2040, runs the
// passthrough mapper at 50Hz, sends CHANNEL_INTENT frames. No mixer logic,
// no logic switches, no HTTP+WS API yet (those land in M2.2 / M2.3).
package main

import (
	"context"
	"errors"
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/api"
	"github.com/agoliveira/zerotx/pi/daemon/internal/arm"
	"github.com/agoliveira/zerotx/pi/daemon/internal/audio"
	"github.com/agoliveira/zerotx/pi/daemon/internal/crsftee"
	"github.com/agoliveira/zerotx/pi/daemon/internal/devices/display"
	"github.com/agoliveira/zerotx/pi/daemon/internal/geo"
	"github.com/agoliveira/zerotx/pi/daemon/internal/gps"
	"github.com/agoliveira/zerotx/pi/daemon/internal/heartbeat"
	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/sitl"
	"github.com/agoliveira/zerotx/pi/daemon/internal/joystick"
	"github.com/agoliveira/zerotx/pi/daemon/internal/logbuf"
	"github.com/agoliveira/zerotx/pi/daemon/internal/logic"
	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/narrator"
	"github.com/agoliveira/zerotx/pi/daemon/internal/phrasebook"
	"github.com/agoliveira/zerotx/pi/daemon/internal/panel"
	"github.com/agoliveira/zerotx/pi/daemon/internal/recorder"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
	"github.com/agoliveira/zerotx/pi/daemon/internal/telemetry"
	"github.com/agoliveira/zerotx/pi/daemon/internal/trackballled"
	"github.com/agoliveira/zerotx/pi/daemon/internal/vfd"
	"github.com/agoliveira/zerotx/pi/daemon/internal/netclass"
	"github.com/agoliveira/zerotx/pi/daemon/internal/tilewarm"
	"github.com/agoliveira/zerotx/pi/daemon/internal/weather"
	"github.com/agoliveira/zerotx/pi/daemon/internal/wxalert"

	"go.bug.st/serial"
)

const version = "0.32.0-metrics"

func main() {
	// SDL2 wants the event pump on the main OS thread. Lock it now so any
	// goroutine that touches sdl.PollEvent runs here.
	runtime.LockOSThread()

	portFlag := flag.String("port", "", "RP2040 USB-CDC device (auto-detect if empty)")
	baudFlag := flag.Int("baud", 115200, "serial baud (USB-CDC ignores)")
	fcTCPAddr := flag.String("fc-tcp-addr", "", "INAV SITL CRSF endpoint as host:port (e.g. 127.0.0.1:5762). When set, the daemon talks raw CRSF over TCP instead of opening the RP2040 USB-CDC link.")
	modelFlag := flag.String("model", "", "ZeroTX model file")
	jsIndex := flag.Int("joystick", -1, "SDL joystick index (-1 = first match by -joystick-name)")
	jsName := flag.String("joystick-name", "", "case-insensitive substring of joystick name to open")
	listJS := flag.Bool("list-joysticks", false, "list joysticks and exit")
	verbose := flag.Bool("v", false, "verbose: print channel values periodically")
	verboseLogic := flag.Bool("v-logic", false, "verbose: also log logic switch state changes")
	rate := flag.Int("rate-hz", 1000/ipc.CRSFPeriodMs, "channel intent send rate (Hz)")
	apiAddr := flag.String("api", "", "API server bind address (e.g. 127.0.0.1:8080); empty disables")
	webDir := flag.String("web-dir", "", "serve web GUI from this filesystem path (default: embedded). Useful for fast iteration during development.")
	mapTilesDir := flag.String("maptiles-dir", "", "directory containing PMTiles files for offline map tile serving. Empty = online proxy mode (development).")
	noOnlineTiles := flag.Bool("no-online-tiles", false, "disable online tile proxy fallback. With -maptiles-dir empty, this disables tile serving entirely.")
	tilesetOsmFile := flag.String("tileset-osm-file", "", "PMTiles file basename for the 'osm' tileset (e.g. 'sp-state-osm'). Empty = uses 'osm.pmtiles' if present.")
	tilesetSatFile := flag.String("tileset-sat-file", "", "PMTiles file basename for the 'satellite' tileset (e.g. 'campinas-sat'). Empty = uses 'satellite.pmtiles' if present.")
	warmTilesDir := flag.String("warm-tiles-dir", os.ExpandEnv("$HOME/zerotx/maptiles/warm"), "directory of recently-fetched tiles served in front of the PMTiles archive. Populated by the tile-warm subsystem. Empty disables.")
	noTileWarm := flag.Bool("no-tilewarm", false, "disable opportunistic satellite tile pre-warm")
	tileWarmRadiusKm := flag.Float64("tilewarm-radius-km", 5.0, "radius around -site-lat/-site-lon to keep warm, km")
	tileWarmMaxAgeDays := flag.Int("tilewarm-max-age-days", 30, "warm tile staleness threshold; older tiles get refetched")
	tileWarmRate := flag.Float64("tilewarm-rate", 2.0, "warm tile fetch rate, requests per second (gentle background)")
	netClassFile := flag.String("netclass-file", os.ExpandEnv("$HOME/.config/zerotx/netclass.json"), "file storing operator-declared network class. Empty = disable netclass subsystem (treat as Home).")
	modelImage := flag.String("model-image", "", "path to model bitmap file (BMP/PNG/JPG); shown in Model tab if set")
	panelFile := flag.String("panel-file", "", "GCS panel state YAML; live-reloaded on edit")
	panelStdin := flag.Bool("panel-stdin", false, "read panel commands from stdin (mutually exclusive with -panel-file)")
	soundsDir := flag.String("sounds-dir", os.ExpandEnv("$HOME/zerotx/sounds"), "audio sample directory (EdgeTX-compatible layout)")
	soundsLang := flag.String("sounds-lang", "en", "language subdirectory under -sounds-dir (e.g. en, pt)")
	noAudio := flag.Bool("no-audio", false, "disable audio playback (PLAY_TRACK CFs are silent)")
	audioThreshold := flag.String("audio-threshold", "notice", "audio threshold: info / notice / warning / critical (critical events ignore threshold)")
	piperBin := flag.String("piper-binary", "", "path to piper TTS binary (empty disables on-demand TTS)")
	voicesDir := flag.String("voices-dir", os.ExpandEnv("$HOME/zerotx/voices"), "directory containing piper .onnx + .onnx.json voice files")
	ttsCacheDir := flag.String("tts-cache-dir", os.ExpandEnv("$HOME/.cache/zerotx/tts"), "where synthesized TTS WAVs are cached on disk")
	ttsVoiceEN := flag.String("tts-voice-en", "en_US-amy-medium", "voice basename used for the en bank (must exist under -voices-dir)")
	ttsVoicePT := flag.String("tts-voice-pt", "pt_BR-faber-medium", "voice basename used for the pt bank (must exist under -voices-dir)")
	narrateInterval := flag.Duration("narrate-interval", 60*time.Second, "in-flight periodic narration interval (e.g. 60s, 2m)")
	narrateContent := flag.String("narrate-content", "", "comma-separated periodic narration fields (battery,distance,altitude,speed,link,mode,time-aloft); empty disables")
	narratePreset := flag.String("narrate-preset", "", "narration preset shortcut: compact (battery+distance+altitude) or full (all). Overridden by -narrate-content if both set")
	recordingsDir := flag.String("recordings-dir", defaultRecordingsDir(), "directory for saved flight recordings")
	noRecordings := flag.Bool("no-recordings", false, "disable flight recording entirely")
	keepRecordings := flag.Int("keep-recordings", 10, "number of most-recent recordings to retain (older deleted on save)")
	displayPort := flag.String("display-port", "", "HUB75 display device serial port (e.g. /dev/ttyACM1); empty disables display output")
	displayBrightness := flag.Int("display-brightness", 80, "HUB75 display brightness 0-100")
	mwpTeeAddr := flag.String("mwp-tee-addr", "127.0.0.1:5761", "TCP listen addr for CRSF telemetry tee to mwp; empty disables")
	iohubPort := flag.String("iohub-port", "", "Mega IO board USB-CDC device (serial path, e.g. /dev/ttyACM2; \"log\" to scaffold via daemon log; empty disables)")
	geoDB := flag.String("geo-db", "", "Offline place-name database for post-flight narration (built by tools/build-geo.sh). Empty disables location enrichment.")
	weatherCacheDir := flag.String("weather-cache-dir", os.ExpandEnv("$HOME/zerotx/cache/weather"), "directory for cached weather JSON. Empty disables persistence (cache held in memory only).")
	siteLat := flag.Float64("site-lat", 0, "configured flight site latitude (decimal degrees). Used as fallback when no GPS lock and no home position. 0 = unset.")
	siteLon := flag.Float64("site-lon", 0, "configured flight site longitude (decimal degrees). Used as fallback when no GPS lock and no home position. 0 = unset.")
	noWeather := flag.Bool("no-weather", false, "disable weather subsystem entirely (no fetches, no API)")
	wxMaxGustKmh := flag.Float64("wx-max-gust-kmh", 30, "weather alert: surface gust limit, km/h. Above this fires wind_gust_high.")
	wxMaxWindKmh := flag.Float64("wx-max-wind-kmh", 20, "weather alert: surface sustained wind limit, km/h. Above this fires wind_speed_high.")
	wxPrecipProbPct := flag.Float64("wx-precip-pct", 60, "weather alert: precipitation probability threshold (0-100) for next 3 hours.")
	wxNearSunsetMin := flag.Int("wx-near-sunset-min", 30, "weather alert: minutes before sunset that fires near_sunset.")
	wxShearDirDeg := flag.Float64("wx-shear-dir-deg", 45, "weather alert: surface-vs-80m wind direction delta threshold, degrees.")
	wxShearSpeedRatio := flag.Float64("wx-shear-speed-ratio", 2.0, "weather alert: 80m/surface wind speed ratio threshold.")
	wxGoldenElevDeg := flag.Float64("wx-golden-elev-deg", 6, "weather alert: sun elevation threshold (degrees) for golden_hour_active.")
	heartbeatGPIO := flag.Int("heartbeat-gpio", -1, "Pi GPIO line number for the daemon heartbeat LED (BCM numbering). -1 disables. The pin blinks at 1Hz while the main loop is healthy and goes dark on hang.")
	heartbeatChip := flag.String("heartbeat-chip", "gpiochip0", "GPIO character device for the heartbeat LED. Pi 4 / Pi 400 use gpiochip0.")
	gpsPort := flag.String("gps-port", "", "serial device for an optional Pi-attached GPS module (e.g. /dev/ttyAMA0, /dev/serial0). Empty disables. Failure to open is non-fatal: the daemon logs and continues.")
	gpsBaud := flag.Int("gps-baud", 9600, "baud rate for the GPS serial port. Most consumer u-blox modules ship at 9600.")
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

	// FC endpoint setup. Default path: open the RP2040 USB-CDC link
	// and use the IPC framed protocol. Bench-test path (-fc-tcp-addr
	// set): connect to INAV SITL on TCP and speak raw CRSF instead;
	// the RP2040 sits idle. The two backends share the same telemetry
	// callback wiring so everything downstream of the FC endpoint
	// (telemetry decode, mwp tee, audio, HUD, recorder) runs the
	// same code in either mode.
	var (
		link    *ipc.Link
		sitlCon *sitl.Conn
		port    string // RP2040 path, empty in SITL mode
	)
	if *fcTCPAddr != "" {
		log.Printf("fc endpoint: SITL TCP %s (RP2040 link disabled)", *fcTCPAddr)
		c, err := sitl.Dial(*fcTCPAddr)
		if err != nil {
			log.Fatalf("sitl: %v", err)
		}
		c.LocalVersion = "zerotxd " + version
		sitlCon = c
		defer sitlCon.Close()
		port = "sitl://" + *fcTCPAddr
	} else {
		port = *portFlag
		if port == "" {
			var err error
			port, err = ipc.AutoDetectPort()
			if err != nil {
				log.Fatalf("autodetect port: %v", err)
			}
			if port == "" {
				log.Fatal("no serial port found; plug RP2040 or pass -port (or use -fc-tcp-addr for SITL)")
			}
			log.Printf("auto-detected port: %s", port)
		}
		l, err := ipc.Open(port, *baudFlag)
		if err != nil {
			log.Fatalf("open link: %v", err)
		}
		defer l.Close()
		l.LocalVersion = "zerotxd " + version
		l.OnLog = func(s string) { log.Printf("[mcu] %s", s) }
		l.OnFrame = func(f ipc.Frame) {
			// Heartbeats are routine and noisy under -v; counted in MCU logs anyway.
			if f.Type == ipc.MsgHeartbeat {
				return
			}
			if *verbose {
				log.Printf("[mcu] frame type=%#02x seq=%d payload=%d bytes", f.Type, f.Seq, len(f.Payload))
			}
		}
		link = l
	}

	// Telemetry: decode CRSF frames forwarded by the MCU into a typed
	// snapshot. The state lives for the lifetime of the daemon; on
	// model unload it's reset so cell-count detection re-runs against
	// the next pack.
	telemetryState := telemetry.New(log.Printf)

	// CRSF tee: optional TCP listener that forwards reconstructed
	// CRSF frames to mwptools (left-LCD map). Read-only; mission
	// upload and other bidirectional features are deferred (would
	// conflict with the channel intent loop). Empty addr disables.
	mwpTee := crsftee.New(*mwpTeeAddr, log.Printf)

	// vfdEvt is wired below once the VFD driver is constructed; the
	// hot-path telemHandler captures the pointer so that telemetry
	// frames can be counted as they arrive without a back-edge from
	// the VFD setup. CountFrame is nil-safe.
	var vfdEvt *vfdEventEmitter

	telemHandler := func(payload []byte) {
		telemetryState.Feed(payload)
		mwpTee.Forward(payload)
		vfdEvt.CountFrame()
	}
	if link != nil {
		link.OnTelemetry = telemHandler
	} else {
		sitlCon.OnTelemetry = telemHandler
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
	if link != nil {
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
		log.Printf("link: rp2040 ipc")
	} else {
		// SITL mode has no panel arm key. Initialize the arm machine
		// as if the key were UP (default safe state). Operator triggers
		// arming via the GUI / API path during bench tests.
		armMachine.Init(true)
		log.Printf("link: sitl tcp %s", *fcTCPAddr)
	}

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

		// Optional TTS bring-up: only attempts if -piper-binary is set.
		// Failures (missing voices, missing binary) log a warning and
		// leave TTS disabled; pre-baked playback continues to work.
		var tts *audio.TTSEngine
		if *piperBin != "" {
			t, err := audio.NewTTSEngine(audio.TTSConfig{
				PiperBinary: *piperBin,
				VoicesDir:   *voicesDir,
				CacheDir:    *ttsCacheDir,
				Voices: map[string]string{
					"en": *ttsVoiceEN,
					"pt": *ttsVoicePT,
				},
			})
			if err != nil {
				log.Printf("audio: tts disabled: %v", err)
			} else {
				tts = t
			}
		}

		player = audio.NewWithTTS(audio.Config{
			SoundsDir: *soundsDir,
			Lang:      *soundsLang,
			Threshold: thr,
		}, tts)
	}
	defer player.Close()

	// Geo: optional offline place-name lookup for post-flight
	// narration. Soft dependency; if -geo-db is empty or the file
	// is missing, narration falls back to no-location phrasing.
	var geoLookup narrator.GeoLookup
	if *geoDB != "" {
		gl, err := geo.Open(*geoDB)
		if err != nil {
			log.Printf("geo: open %s: %v (continuing without location enrichment)", *geoDB, err)
		} else {
			defer gl.Close()
			geoLookup = geoAdapter{gl}
			log.Printf("geo: loaded %s", *geoDB)
		}
	}

	// Narrator builds and emits structured narrative announcements
	// (boot greeting, post-flight summary). Pure transformation;
	// owns no goroutines. Backed by the audio Player above.
	narr := narrator.New(player, *soundsLang, geoLookup)

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

	// Flight event detector: edge-triggered classifier that watches
	// telemetry snapshots while armed and emits discrete events to
	// the recorder. The recorder is the source of truth; the detector
	// is purely a producer. Post-flight narration consumes the events
	// table, but the events are also useful for replay and analysis
	// without TTS being on.
	flightEvents := newFlightEventDetector(rec)

	// Periodic in-flight narration: while armed, every -narrate-interval,
	// speak a status report assembled from telemetry. Disabled by
	// default (empty content); operator opts in via -narrate-content
	// or -narrate-preset.
	//
	// armedFlag is read by the periodic narrator to gate its tick.
	// armedPings notifies it of arm-state transitions so the timer
	// resets cleanly (first announcement is one full interval after
	// arm, never overlapping the pre-flight summary).
	var armedFlag atomic.Bool
	armedPings := make(chan struct{}, 4)
	narrateConfigPath := defaultNarrateConfigPath()
	initialCfg := initialNarrateConfig(narrateConfigPath, "", *narrateContent, *narratePreset, *narrateInterval)
	narrateStore := newNarrateConfigStore(narrateConfigPath, initialCfg)
	if len(initialCfg.Fields) > 0 {
		log.Printf("narrate: periodic interval=%s fields=%v (config=%s)",
			initialCfg.Interval, initialCfg.Fields, narrateConfigPath)
	} else {
		log.Printf("narrate: periodic narration disabled (no fields configured); GUI/API can enable at runtime")
	}
	// Telemetry sampler: poll the telemetry snapshot at 5Hz and forward
	// to the recorder. The recorder throttles internally to avoid
	// duplicate rows when nothing has changed; this goroutine just has
	// to wake often enough to catch every meaningful update. Stops on
	// daemon shutdown via the same context as the rest of the daemon.
	//
	// Also detects flight-mode edges and narrates them via TTS. Edge
	// detection is intentionally simple: compare last seen mode string
	// to current; speak when they differ. The first non-empty mode
	// after boot also narrates (so the operator hears what mode the
	// FC is in when telemetry first comes online).
	telemSamplerStop := make(chan struct{})
	// dispMgr is initialized after the API setup below; declared here
	// so the sampler closure can capture it. nil until the display
	// section runs; sampler guards on nil.
	var dispMgr *display.Manager
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		var lastMode string
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
				flightEvents.Tick(snap)
				if dispMgr != nil {
					dispMgr.SetState(telemetryToDisplayState(snap))
				}
				// Flight-mode edge → TTS. Lowercase the mode string
				// for natural narration (FC sends "ANGL", we say
				// "angle"). The mode names are FC-defined so this
				// is best-effort: unknown modes get spoken as-is.
				if snap.FlightMode != nil {
					m := snap.FlightMode.Data.Mode
					if m != "" && m != lastMode {
						lastMode = m
						player.Speak(phrasebook.FlightMode(*soundsLang, m), audio.LevelInfo)
					}
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

	// GPS: optional Pi-attached serial GPS. Failure to open is
	// non-fatal; the daemon proceeds without it. Constructed early
	// so the weather coordinate resolver and the API state provider
	// can capture the reader without taking on nil-handling boilerplate
	// at every call site (Get returns a zero State on a nil reader is
	// a no-go because reader is *gps.Reader, not an interface).
	var gpsRdr *gps.Reader
	if *gpsPort != "" {
		r, err := gps.Open(*gpsPort, *gpsBaud)
		if err != nil {
			log.Printf("GPS: %v (continuing without)", err)
		} else if err := r.Start(ctx); err != nil {
			log.Printf("GPS start: %v (continuing without)", err)
			_ = r.Close()
		} else {
			gpsRdr = r
			log.Printf("GPS: %s @%d", *gpsPort, *gpsBaud)
			defer gpsRdr.Close()
		}
	}

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

	// flightArmedHandler runs the daemon-side side effects of an arm
	// transition: starting/stopping recording, locking joystick swaps,
	// switching the HUB75 display mode, acknowledging alarms,
	// triggering post-flight narration. Closure over all the
	// subsystems it touches; called from drainArmEvents on EventArmed
	// and EventDisarmed.
	flightArmedHandler := func(armed bool) {
		armedFlag.Store(armed)
		vfdEvt.SetArmed(armed)
		select {
		case armedPings <- struct{}{}:
		default: // channel full; periodic narrator already has a pending wakeup
		}
		if armed {
			name := ""
			if s := holder.Load(); s != nil && s.Model != nil {
				name = s.Model.EdgeTX.Header.Name
			}
			rec.OnArm(name, "")
			// Detector's SetArmed must come AFTER OnArm so the
			// "armed" event is stamped to the new session_id, not
			// the pre-arm session 0 (which gets wiped on rotation).
			flightEvents.SetArmed(true)
			// Record home position if GPS has a fix. Idempotent
			// across re-arms (force=false) so multiple takeoffs
			// in one session keep the original home.
			if telemetryState.SetHome(false) {
				log.Printf("home position set on arm")
			}
			// Record operator station position at this moment, if
			// station GPS is configured and locked. One-shot event
			// per flight: 'where the operator was standing when
			// they armed this aircraft'. The post-flight summary
			// can pair it with the home position to reason about
			// where takeoff happened. Most other domain events live
			// in flight_events.go; this one is here because it
			// reads gpsRdr (constructed in main alongside other I/O)
			// and only fires once at arm.
			if gpsRdr != nil {
				if s := gpsRdr.Get(); s.Fix >= gps.Fix2D {
					rec.LogEvent("flight", "station-position", "info", map[string]interface{}{
						"lat":  s.LatDeg,
						"lon":  s.LonDeg,
						"alt":  s.AltMeters,
						"fix":  s.Fix.String(),
						"sats": s.Sats,
						"hdop": s.HDOP,
					})
				}
			}
			if dispMgr != nil {
				dispMgr.SetMode(display.ModeFlight)
			}
		} else {
			// Detector's SetArmed BEFORE event capture so the
			// "disarmed" event is included in the narration.
			flightEvents.SetArmed(false)
			// Capture flight events while the working DB still
			// holds them (OnDisarm rotates the session). Errors
			// here are logged; narration falls through to the
			// pre-baked stitched path on failure.
			flightEventsLog, eventsErr := rec.CurrentSessionEvents()
			if eventsErr != nil {
				log.Printf("post-flight: read events: %v", eventsErr)
			}

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

			// Post-flight narration. Tier 1 is TTS built from the
			// in-flight event log: duration + peaks + noteworthy
			// events. Falls back to the pre-baked stitched
			// summary (read from the saved file) when there are
			// no events to narrate, which preserves something
			// audible even when the detector is disabled or the
			// flight produced no detectable signal.
			spoken := narr.SpeakPostFlight(flightEventsLog)
			if spoken == "" && path != "" {
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
	}

	// Arm state machine: launch the event drain (logs transitions,
	// fires audio cues, runs flight-armed side effects on
	// EventArmed/EventDisarmed) and a 1Hz Tick driver for the 60s
	// arming-request timeout.
	go drainArmEvents(ctx, armMachine, player, holder, telemetryState, *soundsLang, flightArmedHandler)
	go tickArmMachine(ctx, armMachine)
	go func() {
		if err := mwpTee.Run(ctx); err != nil {
			log.Printf("crsftee: %v (continuing without tee)", err)
		}
	}()
	go runPeriodicNarrator(ctx, player, telemetryState, armedFlag.Load, armedPings, narrateStore, *soundsLang)

	// VFD: two parallel feeds.
	//
	//   1. Firehose taps the daemon log buffer and pushes new lines
	//      as L0/L1 text overlays.
	//   2. Event emitter translates state changes into "E ..." commands
	//      that the firmware uses to drive its animation state machine
	//      (arm transitions, mode changes, telemetry tick rate, LQ,
	//      battery, alarm flashes).
	//
	// Mega IO board connection. One iohub.Client serves multiple
	// subsystems on the device: VFD, trackball LEDs, indicator LEDs,
	// buttons, WS2813 strip, future LDR/buzzer. The iohub-port flag
	// names the Mega's USB-CDC device.
	//
	// With -iohub-port empty the client is a no-op; with -iohub-port=log
	// commands echo to the daemon log so the wire format can be
	// validated without hardware. Anything else is treated as a
	// serial device path.
	hub := iohub.New(*iohubPort)
	defer hub.Close()
	// Default event handler: log unsolicited EVENT lines so button
	// presses, boot events, and ready signals from the Mega are
	// visible in the daemon log. Subsystem-specific consumers
	// (e.g. semantic button dispatch) can register additional
	// handlers - all subscribers receive every event.
	hub.OnEvent(func(target, payload string) {
		if payload == "" {
			log.Printf("iohub: EVENT %s", target)
		} else {
			log.Printf("iohub: EVENT %s %s", target, payload)
		}
	})
	go func() {
		if err := hub.Run(ctx); err != nil {
			log.Printf("iohub: run: %v", err)
		}
	}()

	// VFD driver shares the hub instead of owning a private serial
	// port. Close on a hub-shared driver is a no-op so other
	// subsystems on the same hub keep working at shutdown.
	vfdDriver := vfd.NewWithHub(hub)
	defer vfdDriver.Close()
	if *iohubPort != "" {
		log.Printf("vfd: firehose enabled (driver=%s)", *iohubPort)
		vfdHose := vfd.NewFirehose(vfdDriver, logBuf)
		go func() {
			if err := vfdHose.Run(ctx); err != nil {
				log.Printf("vfd: %v", err)
			}
		}()
		vfdEvt = newVFDEventEmitter(vfdDriver, telemetryState)
		go vfdEvt.Run(ctx)
	}

	// Construct the weather service if enabled. The resolver chain
	// is: station GPS (when available with at least a 2D fix) ->
	// telemetry home (set on arm) -> -site-lat/-site-lon flag.
	// Returns ok=false when none of the tiers has coordinates yet.
	var weatherSvc *weather.Service
	if !*noWeather {
		cache, err := weather.NewCache(*weatherCacheDir)
		if err != nil {
			log.Printf("weather: cache init failed (%v); disabling", err)
		} else {
			lat := *siteLat
			lon := *siteLon
			resolver := func() (float64, float64, string, bool) {
				if gpsRdr != nil {
					st := gpsRdr.Get()
					if st.Fix >= gps.Fix2D {
						return st.LatDeg, st.LonDeg, "gps", true
					}
				}
				if hLat, hLon, ok := telemetryState.HomePosition(); ok {
					return hLat, hLon, "home", true
				}
				if lat != 0 || lon != 0 {
					return lat, lon, "site", true
				}
				return 0, 0, "", false
			}
			src := weather.NewOpenMeteoSource(weather.OpenMeteoConfig{
				UserAgent: "zerotx/" + version,
			})
			svc, err := weather.New(weather.Options{
				Source:   src,
				Cache:    cache,
				Resolver: resolver,
			})
			if err != nil {
				log.Printf("weather: service init failed (%v); disabling", err)
			} else {
				weatherSvc = svc
				go func() {
					if err := svc.Run(ctx); err != nil {
						log.Printf("weather: %v", err)
					}
				}()
				tiers := []string{}
				if gpsRdr != nil {
					tiers = append(tiers, "station-gps")
				}
				tiers = append(tiers, "telemetry-home")
				if lat != 0 || lon != 0 {
					tiers = append(tiers, fmt.Sprintf("site=%.4f,%.4f", lat, lon))
				}
				log.Printf("weather: started, resolver chain=%v cache=%s", tiers, *weatherCacheDir)
			}
		}
	}

	// Construct the weather-alert subsystem if weather is enabled.
	// Limits come from -wx-* flags; defaults match wxalert.Defaults().
	// Holder owns the hysteresis tracker and exposes the active list
	// to the API. Goroutine polls the weather cache once a minute,
	// evaluates rules, fires TTS on transitions.
	var wxAlerts *wxAlertHolder
	if weatherSvc != nil {
		wxAlerts = newWxAlertHolder(wxalert.Limits{
			MaxWindKmh:           *wxMaxWindKmh,
			MaxGustKmh:           *wxMaxGustKmh,
			PrecipProbabilityPct: *wxPrecipProbPct,
			NearSunsetMinutes:    *wxNearSunsetMin,
			ShearDirDeg:          *wxShearDirDeg,
			ShearSpeedRatio:      *wxShearSpeedRatio,
			GoldenHourElevDeg:    *wxGoldenElevDeg,
		})
		go runWxAlerts(ctx, wxAlerts, weatherSvc, player, dispMgr, armMachine)
		log.Printf("wxalert: started (gust>%.0f wind>%.0f precip>%.0f%% sunset<%dm shear>%.0f° ratio>%.1fx)",
			*wxMaxGustKmh, *wxMaxWindKmh, *wxPrecipProbPct,
			*wxNearSunsetMin, *wxShearDirDeg, *wxShearSpeedRatio)
	}

	// Trackball status LED. Pure operator-feedback indicator: green
	// when system is healthy, red when an alert needs attention. The
	// driver shares the Mega iohub.Client; if -iohub-port is empty/log
	// the LED simply doesn't drive a real device but the goroutine
	// runs harmlessly.
	{
		alertProvider := wxAlertProviderAdapter{wxAlerts}
		tbDrv := trackballled.New(hub, armMachine, alertProvider)
		go tbDrv.Run(ctx)
		log.Printf("trackballled: started")
	}

	// Construct the netclass holder. Operator-declared network class
	// drives downstream policy in tilewarm and (later) stan-sync.
	// File-backed for persistence across restarts.
	var netClassHolder *netclass.Holder
	{
		var err error
		netClassHolder, err = netclass.New(*netClassFile)
		if err != nil {
			log.Printf("netclass: load failed (%v); falling back to in-memory Offline", err)
			netClassHolder, _ = netclass.New("")
		}
		log.Printf("netclass: %s (file=%s)", netClassHolder.Current(), *netClassFile)
	}

	// Tile-warm stats holder. Constructed unconditionally so the
	// metrics endpoint can read it even when tilewarm is disabled
	// (it will report zero runs). The goroutine that updates it
	// is started later after the API server is up.
	tileWarmStatsHolder := &tileWarmStats{}

	// Start the API server if requested.
	if *apiAddr != "" {
		providers := buildAPIProviders(chHolder, holder, pnl, jsHolder, player, narr, telemetryState, rec, port, *modelImage, *modelFlag, *recordingsDir, *soundsLang, narrateStore, logBuf, version, time.Now(), dispMgr, armMachine, weatherSvc, wxAlerts, netClassHolder, tileWarmStatsHolder, gpsRdr, ctx)
		apiSrv := api.NewServer(*apiAddr, providers)
		apiSrv.SetWebDir(*webDir)
		apiSrv.SetMapTilesDir(*mapTilesDir)
		apiSrv.SetWarmTilesDir(*warmTilesDir)
		apiSrv.SetOnlineTileFallback(!*noOnlineTiles)
		if *tilesetOsmFile != "" {
			apiSrv.SetTilesetFile("osm", *tilesetOsmFile)
		}
		if *tilesetSatFile != "" {
			apiSrv.SetTilesetFile("satellite", *tilesetSatFile)
		}
		go func() {
			if err := apiSrv.Run(ctx); err != nil {
				log.Printf("api: %v", err)
			}
		}()
	}

	// Tile-warm subsystem: opportunistic re-fetch of recently-used
	// satellite tiles around the configured site. Skipped if disabled,
	// no warm dir configured, or no site coordinates set. The stats
	// holder was constructed earlier so the API metrics endpoint can
	// always read it.
	if !*noTileWarm && *warmTilesDir != "" && (*siteLat != 0 || *siteLon != 0) {
		store, err := tilewarm.NewFSStore(*warmTilesDir, "satellite", "jpg")
		if err != nil {
			log.Printf("tilewarm: store init failed (%v); disabling", err)
		} else {
			cfg := tilewarm.DefaultConfig(*siteLat, *siteLon)
			cfg.RadiusKm = *tileWarmRadiusKm
			cfg.MaxAge = time.Duration(*tileWarmMaxAgeDays) * 24 * time.Hour
			cfg.RatePerSec = *tileWarmRate
			go runTileWarm(ctx, store, cfg, netClassHolder, tileWarmStatsHolder)
			log.Printf("tilewarm: scheduled (warm dir=%s)", *warmTilesDir)
		}
	}

	// Boot greeting. Narrative announcement that the daemon is ready.
	// Played once at the configured threshold; if -no-audio or audio
	// threshold filters it out, this is silent. Either way the daemon
	// keeps booting; the greeting is presentational, never a gate.
	{
		bootModel := ""
		if s := holder.Load(); s != nil && s.Model != nil {
			bootModel = s.Model.EdgeTX.Header.Name
		}
		narr.SpeakBootGreeting(bootModel)
	}

	// Once-only "Station GPS lock acquired" announcement. No-op when
	// gpsRdr is nil. Exits after the first 2D+ fix.
	go runStationGPSWatcher(ctx, gpsRdr, narr, 1*time.Second)

	// Goroutines.
	go func() {
		var err error
		if link != nil {
			err = link.Run(ctx)
		} else {
			err = sitlCon.Run(ctx)
		}
		if err != nil {
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

	// Heartbeat LED: drives a Pi GPIO at 1Hz while the 50Hz mapper
	// loop is healthy; goes dark on hang. Disabled by default
	// (-heartbeat-gpio < 0) so the daemon runs identically without
	// a breakout board attached.
	var hbDrv heartbeat.Driver
	if *heartbeatGPIO >= 0 {
		d, err := heartbeat.NewReal(*heartbeatChip, *heartbeatGPIO)
		if err != nil {
			log.Printf("heartbeat: %v (running without LED)", err)
			hbDrv = heartbeat.NewNull()
		} else {
			hbDrv = d
			log.Printf("heartbeat: %s line %d", *heartbeatChip, *heartbeatGPIO)
		}
	} else {
		hbDrv = heartbeat.NewNull()
	}
	hb := heartbeat.New(hbDrv, heartbeat.Config{})
	if err := hb.Start(); err != nil {
		log.Printf("heartbeat start: %v", err)
	}
	defer hb.Close()

	// RTC presence: the daemon does not read or write the RTC, but
	// announcing it at startup confirms the kernel detected the
	// DS3231 (or any other dtoverlay-loaded RTC). Absence is also
	// fine; the system clock is set from the network or the previous
	// systohc on shutdown.
	if data, err := os.ReadFile("/sys/class/rtc/rtc0/name"); err == nil {
		log.Printf("RTC: %s", strings.TrimSpace(string(data)))
	} else {
		log.Printf("RTC: not detected (relying on system clock)")
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
	// lastFCModeStr is the most recently logged CRSF flight-mode
	// string. The mode is sampled every channel-intent tick (50Hz);
	// we only log on transitions so the operator sees pre-arm state
	// changes without log spam.
	var lastFCModeStr string
	first := true
	for {
		select {
		case <-ctx.Done():
			log.Println("daemon stopped.")
			return
		case <-ticker.C:
			hb.Tick()
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
					player.Speak(phrasebook.JoystickDisconnected(*soundsLang), audio.LevelWarning)
					jsLossLogged = true
				}
				if lostMs >= joystickHoldMs {
					// Stop emission entirely. Skip this tick.
					continue
				}
				// else: fall through and emit last-known values.
			} else if jsLossLogged && jsHolder.Connected() {
				log.Printf("joystick: link recovered")
				player.Speak(phrasebook.JoystickReconnected(*soundsLang), audio.LevelNotice)
				jsLossLogged = false
			}

			ch := s.Mapper.Resolve()
			chHolder.Set(ch)
			// Feed throttle into the arm state machine, reading the
			// channel the model's mix table actually routes the Thr
			// source to (TAER -> CH1 / index 0; AETR -> CH3 / index 2;
			// other layouts -> whatever the model says). Pre-fix this
			// hardcoded ch[2] which silently read elevator on any TAER
			// model. -1 means the model has no throttle source mixed
			// to any channel (rare; gliders without a powered brake
			// mix); treat that as "not low" so confirm is refused
			// rather than green-lit on a model that never had a
			// throttle in the first place. CRSF range minimum is
			// ~172; the 200 cutoff absorbs floating-point jitter near
			// idle.
			thrIdx := s.ThrottleChannelIdx
			if thrIdx >= 0 && thrIdx < len(ch) {
				armMachine.ThrottleChanged(ch[thrIdx] <= 200)
			} else {
				armMachine.ThrottleChanged(false)
			}
			// FC ready-to-arm: derive from CRSF flight-mode string
			// using the dedicated decoder (see fc_ready.go).
			tsnap := telemetryState.Snapshot()
			modeStr := ""
			modeFresh := false
			if tsnap.FlightMode != nil && !tsnap.FlightMode.Stale {
				modeStr = tsnap.FlightMode.Data.Mode
				modeFresh = true
			}
			fcReady := modeFresh && fcReadyFromMode(modeStr)
			armMachine.FCReadyChanged(fcReady)
			if modeStr != lastFCModeStr {
				log.Printf("fc-ready: mode=%q ready=%v", modeStr, fcReady)
				lastFCModeStr = modeStr
			}
			var sendErr error
			if link != nil {
				sendErr = link.SendChannelIntent(ch)
			} else {
				sendErr = sitlCon.SendChannelIntent(ch)
			}
			if sendErr != nil {
				log.Printf("send intent: %v", sendErr)
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
	lang string,
	narrateStore *narrateConfigStore,
	logBuf *logbuf.Buffer,
	version string,
	startedAt time.Time,
	dispMgr *display.Manager,
	armMachine *arm.Machine,
	weatherSvc *weather.Service,
	wxAlerts *wxAlertHolder,
	netClassHolder *netclass.Holder,
	tileWarmStatsHolder *tileWarmStats,
	gpsRdr *gps.Reader,
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
			if err := loadModel(holder, jsHolder.JoystickState(), pnl, player, rec, lang, path); err != nil {
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
			name := ""
			if s := holder.Load(); s != nil {
				name = s.Model.EdgeTX.Header.Name
				log.Printf("model unloaded: %s", name)
			}
			holder.Store(nil)
			telemState.ClearHome()
			if dispMgr != nil {
				dispMgr.SetThresholds(nil)
				dispMgr.SetMode(display.ModeIdle)
			}
			player.Speak(phrasebook.ModelUnloaded(lang, name), audio.LevelInfo)
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
		Telemetry: func() interface{} {
			return telemState.Snapshot()
		},
		Arm: func() interface{} {
			return armMachine.Snapshot()
		},
		ArmConfirm:   armMachine.Confirm,
		ArmChecklist: armMachine.ChecklistOkChanged,
		WeatherCurrent: func() (interface{}, float64, float64, string, bool) {
			if weatherSvc == nil {
				return nil, 0, 0, "", false
			}
			w, lat, lon, src, ok := weatherSvc.GetCurrent()
			if !ok {
				return nil, lat, lon, src, false
			}
			return w, lat, lon, src, true
		},
		WeatherFetch: func(ctx context.Context, lat, lon float64) (interface{}, error) {
			if weatherSvc == nil {
				return nil, errors.New("weather not configured")
			}
			return weatherSvc.Get(ctx, lat, lon)
		},
		WeatherAlerts: func() []api.WeatherAlert {
			if wxAlerts == nil {
				return nil
			}
			snap := wxAlerts.snapshot()
			out := make([]api.WeatherAlert, len(snap))
			for i, a := range snap {
				out[i] = api.WeatherAlert{
					Name:     a.Name,
					Severity: a.Severity.String(),
					Message:  a.Message,
					Detail:   a.Detail,
				}
			}
			return out
		},
		NetClassGet: func() (string, time.Time) {
			if netClassHolder == nil {
				return "", time.Time{}
			}
			s := netClassHolder.Snapshot()
			return string(s.Class), s.UpdatedAt
		},
		NetClassSet: func(class string) error {
			if netClassHolder == nil {
				return errors.New("netclass disabled")
			}
			c := netclass.Class(class)
			if !netclass.Valid(c) {
				return netclass.ErrInvalidClass
			}
			return netClassHolder.Set(c)
		},
		TileWarmStats: func() *api.TileWarmStatsSnapshot {
			if tileWarmStatsHolder == nil {
				return nil
			}
			lastRun, reason, result, lastErr, totalRuns, totalErrors := tileWarmStatsHolder.snapshot()
			return &api.TileWarmStatsSnapshot{
				LastRunAt:      lastRun,
				LastReason:     reason,
				LastConsidered: result.Considered,
				LastSkipped:    result.Skipped,
				LastFetched:    result.Fetched,
				LastErrors:     result.Errors,
				LastError:      lastErr,
				TotalRuns:      totalRuns,
				TotalErrors:    totalErrors,
			}
		},
		Station: func() *api.StationSnapshot {
			if gpsRdr == nil {
				return nil
			}
			st := gpsRdr.Get()
			snap := &api.StationSnapshot{
				Available: st.Fix >= gps.Fix2D,
				Fix:       st.Fix.String(),
				Sats:      st.Sats,
				HDOP:      st.HDOP,
			}
			if st.Fix >= gps.Fix2D {
				snap.LatDeg = st.LatDeg
				snap.LonDeg = st.LonDeg
				snap.AltMeters = st.AltMeters
				snap.SpeedKmh = st.SpeedKmh
				snap.HeadingDeg = st.HeadingDeg
			}
			if !st.Time.IsZero() {
				snap.UTCTime = st.Time.UTC().Format(time.RFC3339Nano)
			}
			if !st.Updated.IsZero() {
				snap.Updated = st.Updated.Format(time.RFC3339Nano)
				snap.AgeMs = time.Since(st.Updated).Milliseconds()
			}
			return snap
		},
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
		Speak: func(text, level string) {
			lvl, ok := audio.ParseLevel(level)
			if !ok {
				lvl = audio.LevelNotice
			}
			player.Speak(text, lvl)
		},
		FlightEvents: func() (interface{}, error) {
			return rec.CurrentSessionEvents()
		},
		NarrateConfig: func() api.NarrateConfig {
			cfg := narrateStore.Load()
			return narrateConfigToAPI(cfg)
		},
		NarrateConfigSet: func(in api.NarrateConfig) error {
			cfg, err := narrateConfigFromAPI(in)
			if err != nil {
				return err
			}
			narrateStore.Set(cfg)
			if _, err := narrateStore.Save(); err != nil {
				log.Printf("narrate: persist failed: %v", err)
				return fmt.Errorf("persist failed: %w", err)
			}
			return nil
		},

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
func loadModel(holder *stackHolder, jsState source.JoystickState, pnl panel.Panel, player audio.Player, rec recorder.Interface, lang, path string) error {
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
	player.Speak(phrasebook.ModelLoaded(lang, m.EdgeTX.Header.Name), audio.LevelInfo)
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
// translates them into side effects: logs every transition, fires the
// matching audio cue, and runs flight-armed side effects (recorder,
// display mode, alarm cleanup, post-flight narration) on EventArmed
// and EventDisarmed.
func drainArmEvents(ctx context.Context, m *arm.Machine, player audio.Player, holder *stackHolder, tel *telemetry.State, lang string, flightArmedHandler func(bool)) {
	events := m.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-events:
			log.Printf("arm: %s (state=%s)", e, m.State())
			playArmCue(player, holder, tel, lang, e)
			if flightArmedHandler != nil {
				switch e {
				case arm.EventArmed:
					flightArmedHandler(true)
				case arm.EventDisarmed:
					flightArmedHandler(false)
				}
			}
		}
	}
}

// playArmCue maps an arm.Event to the audio side effect. Most events
// play a single pre-baked track. ArmingRequested is the exception:
// it speaks a TTS pre-flight summary built from current telemetry,
// so the operator hears the live status instead of a generic cue.
func playArmCue(player audio.Player, holder *stackHolder, tel *telemetry.State, lang string, e arm.Event) {
	if player == nil {
		return
	}
	if e == arm.EventArmingRequested {
		player.Speak(buildPreflightSummary(holder, tel, lang), audio.LevelNotice)
		return
	}
	stem := armEventStem(e)
	level := armEventLevel(e)
	player.Play("track", stem, level)
}

// buildPreflightSummary composes the pre-flight TTS announcement.
// Fragments are comma-separated to give natural TTS pauses. Missing
// telemetry is omitted; the announcement says only what's known. The
// final phrase is the localized "Ready to arm." so the operator hears
// a clear closing cue.
func buildPreflightSummary(holder *stackHolder, tel *telemetry.State, lang string) string {
	var parts []string

	if name := modelName(holder); name != "" {
		parts = append(parts, phrasebook.PreflightModelHeader(lang, name))
	}

	if tel != nil {
		snap := tel.Snapshot()

		if snap.Battery != nil && !snap.Battery.Stale {
			b := snap.Battery.Data
			if frag := phrasebook.PreflightBattery(lang, b.CellCount, b.Volts, b.Percent); frag != "" {
				parts = append(parts, frag)
			}
		}

		if snap.GPS != nil && !snap.GPS.Stale {
			g := snap.GPS.Data
			parts = append(parts, phrasebook.PreflightGPS(lang, int(g.Sats), g.Sats >= 6))
		}

		if snap.Link != nil && !snap.Link.Stale {
			l := snap.Link.Data
			parts = append(parts, phrasebook.PreflightLink(lang, int(l.UplinkLQ)))
		}
	}

	parts = append(parts, phrasebook.PreflightReady(lang))
	return strings.Join(parts, " ")
}

// modelName returns the loaded model's display name, or "" when no
// model is loaded.
func modelName(holder *stackHolder) string {
	s := holder.Load()
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.Model.EdgeTX.Header.Name)
}

// armEventStem maps an arm.Event to its dictionary stem. Most events
// match Event.String() one-to-one; EventDisarmed reuses the existing
// "disarm" track that CF logic already fires, so the GCS-side disarm
// announcement sounds identical to the model-side one.
func armEventStem(e arm.Event) string {
	if e == arm.EventDisarmed {
		return "disarm"
	}
	return e.String()
}

// armEventLevel classifies arm events for the audio threshold gate.
// Most are notice-level (fire once, don't nag). Two are warning-level
// because the operator likely needs to act: BootKeyUp blocks all
// future arming until cleared, and DisarmDeniedInFlight tells the
// operator the GCS can't help and they should reach for the radio
// or the mushroom.
func armEventLevel(e arm.Event) audio.Level {
	switch e {
	case arm.EventBootKeyUp, arm.EventDisarmDeniedInFlight:
		return audio.LevelWarning
	default:
		return audio.LevelNotice
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
