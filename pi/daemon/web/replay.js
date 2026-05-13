// Replay module shared by /hud and /map.
//
// Both kiosk pages have a `?replay=<recording-name>` URL param that
// switches them out of live-WS mode and into playback. The same
// synthesizer drives both pages so a single recording produces
// consistent state for HUD widgets and the map track.
//
// Public surface (window.Replay):
//
//   Replay.start(name, { handleStateMessage })
//     Fetches the named recording via /api/v1/recordings/detail,
//     starts a 10 Hz playback loop, installs the keyboard handler,
//     and feeds synthesized state messages into the caller-supplied
//     handler. The state shape matches what the live /api/v1/stream
//     WS payload sends, so the caller's existing renderer (also
//     fed by the WS path) works unchanged.
//
// Cross-tab sync: BroadcastChannel('zerotx-replay-' + name). Any
// control change (pause/seek/speed) broadcasts the full playback
// state to peers, which apply it without showing a toast (only the
// initiating tab gets feedback).
//
// Keyboard shortcuts (in this commit):
//
//   Space            play / pause
//   Left / Right     seek -10s / +10s
//   Shift+Left/Right jump to previous / next event marker
//   1 / 2 / 3 / 4    speed 0.5x / 1x / 2x / 4x
//   Home / End       rewind / jump to end
//   Q                close replay (reload without ?replay param)
//   ?                show shortcut help overlay
//
// Esc deliberately not used (browsers and window managers intercept
// it for fullscreen exit and other things).

(function () {
  'use strict';

  // ---- Public: start a replay session on this page ----

  async function start(name, options) {
    if (!name) {
      console.warn('Replay.start: empty name, ignoring');
      return null;
    }
    if (!options || typeof options.handleStateMessage !== 'function') {
      console.error('Replay.start: options.handleStateMessage is required');
      return null;
    }

    let detail;
    try {
      const r = await fetch('/api/v1/recordings/detail?name=' + encodeURIComponent(name));
      if (!r.ok) throw new Error('HTTP ' + r.status);
      detail = await r.json();
    } catch (e) {
      console.error('Replay: failed to load recording:', e);
      return null;
    }

    if (!detail.telemetry || detail.telemetry.length === 0) {
      console.warn('Replay: recording is empty');
      return null;
    }

    const totalMs = detail.telemetry[detail.telemetry.length - 1].tsMs;
    console.log(
      `[replay] loaded "${name}": ${detail.telemetry.length} telemetry rows, ` +
      `${(detail.events || []).length} events, duration ~${Math.round(totalMs / 1000)}s`
    );

    // Session state. All mutable fields live here so the keyboard
    // handler, BroadcastChannel handler, and tick loop can read and
    // mutate them without weaving them through closures.
    //
    // Time model: when not paused, currentClockMs() = anchorClock +
    // (Date.now() - anchorWall) * speed. Pause snapshots
    // currentClockMs() into anchorClock and rebases anchorWall on
    // resume.
    const s = {
      detail,
      totalMs,
      anchorWall: Date.now(),
      anchorClock: 0,
      speed: 1,
      paused: false,
      bc: null,
    };

    function currentClockMs() {
      if (s.paused) return s.anchorClock;
      return s.anchorClock + (Date.now() - s.anchorWall) * s.speed;
    }

    // Rebase the clock so currentClockMs() returns `to`. Used by
    // every control that mutates timing (seek, scrub, speed, pause).
    function rebaseTo(to) {
      s.anchorClock = to;
      s.anchorWall = Date.now();
    }

    // ---- Cross-tab sync via BroadcastChannel ----
    //
    // Same-origin tabs (e.g. /hud and /map on the same Pi) keep
    // their playback in sync via a per-recording channel. Messages:
    //
    //   {type: 'hello'}                          join announcement
    //   {type: 'state', clockMs, speed, paused}  full state sync
    //
    // The joining tab sends hello and any peer responds with state.
    // Every control change (pause, speed, seek) also broadcasts
    // state. Receivers apply state silently (no toast).
    try {
      s.bc = new BroadcastChannel('zerotx-replay-' + name);
    } catch (e) {
      console.warn('Replay: BroadcastChannel unavailable, running unsynced:', e);
    }

    function broadcastState() {
      if (!s.bc) return;
      s.bc.postMessage({
        type: 'state',
        clockMs: currentClockMs(),
        speed: s.speed,
        paused: s.paused,
      });
    }

    if (s.bc) {
      s.bc.onmessage = (ev) => {
        const msg = ev.data;
        if (!msg || typeof msg !== 'object') return;
        switch (msg.type) {
          case 'hello':
            // Peer joined. Send them our current state so they
            // can rebase. Acceptable to send even if we ourselves
            // haven't started ticking yet.
            broadcastState();
            break;
          case 'state':
            // Peer announced new state. Apply silently.
            if (typeof msg.clockMs !== 'number' || msg.clockMs < 0) break;
            if (msg.clockMs > s.totalMs + 1000) break;
            if (typeof msg.speed === 'number' && msg.speed > 0) s.speed = msg.speed;
            if (typeof msg.paused === 'boolean') s.paused = msg.paused;
            rebaseTo(msg.clockMs);
            break;
        }
      };
      s.bc.postMessage({ type: 'hello' });
    }

    // Brief grace period before starting the tick loop so any peer
    // that's going to respond has time to do so. ~150 ms is generous
    // for an in-browser BroadcastChannel round-trip.
    await new Promise((resolve) => setTimeout(resolve, 150));

    const tickMs = 100; // 10 Hz, matches live stream cadence
    setInterval(() => {
      const clockMs = currentClockMs();
      // End-of-recording: pause at the last sample so the kiosks
      // freeze on the final frame rather than fading to "no data"
      // via the absent-fields synthesis. Operator can scrub back
      // or hit Home to re-watch.
      if (clockMs >= s.totalMs) {
        if (!s.paused) {
          s.paused = true;
          rebaseTo(s.totalMs);
          broadcastState();
          showToast('⏭ end');
        }
        const state = synthesize(s.detail, s.totalMs);
        options.handleStateMessage(state);
        return;
      }
      const state = synthesize(s.detail, clockMs);
      options.handleStateMessage(state);
    }, tickMs);

    // ---- Controls ----

    function togglePause() {
      if (s.paused) {
        s.anchorWall = Date.now(); // rebase wall, anchorClock unchanged
        s.paused = false;
        showToast('▶ playing');
      } else {
        rebaseTo(currentClockMs());
        s.paused = true;
        showToast('⏸ paused');
      }
      broadcastState();
    }

    function setSpeed(newSpeed) {
      if (newSpeed === s.speed) return;
      rebaseTo(currentClockMs()); // preserve current clock across speed change
      s.speed = newSpeed;
      showToast('▶ ' + newSpeed + 'x');
      broadcastState();
    }

    function seek(deltaMs) {
      const t = Math.max(0, Math.min(s.totalMs, currentClockMs() + deltaMs));
      rebaseTo(t);
      if (t >= s.totalMs) s.paused = true;
      showToast((deltaMs >= 0 ? '⏩ +' : '⏪ ') + Math.round(deltaMs / 1000) + 's');
      broadcastState();
    }

    function seekToStart() {
      rebaseTo(0);
      showToast('⏮ start');
      broadcastState();
    }

    function seekToEnd() {
      rebaseTo(s.totalMs);
      s.paused = true;
      showToast('⏭ end');
      broadcastState();
    }

    // jumpToEventMarker(direction): ±1. 'event' here means a flight-
    // kind event whose name is in a small interesting set. Routine
    // telemetry rows are NOT marker-worthy -- the operator wants
    // to land on Arm, Disarm, mode changes, link drops, battery
    // thresholds, etc.
    function jumpToEventMarker(direction) {
      const interesting = new Set([
        'armed', 'disarmed',
        'mode-change',
        'rth-active', 'failsafe',
        'link-degraded', 'link-poor', 'link-recovered',
        'battery-low', 'battery-critical',
        'gps-lock-acquired', 'gps-lock-lost',
        'home-set',
      ]);
      const candidates = (s.detail.events || []).filter(
        (ev) => ev.kind === 'flight' && interesting.has(ev.name)
      );
      if (candidates.length === 0) {
        showToast('no event markers');
        return;
      }
      const here = currentClockMs();
      let target = null;
      if (direction > 0) {
        for (const ev of candidates) {
          if (ev.tsMs > here + 100) { target = ev; break; }
        }
      } else {
        for (let i = candidates.length - 1; i >= 0; i--) {
          if (candidates[i].tsMs < here - 100) { target = candidates[i]; break; }
        }
      }
      if (!target) {
        showToast(direction > 0 ? 'no later event' : 'no earlier event');
        return;
      }
      rebaseTo(target.tsMs);
      showToast(`⏵ ${target.name}`);
      broadcastState();
    }

    function closeReplay() {
      // 2b-4: strip the param locally and reload. 2b-5 will also
      // POST /api/v1/replay/stop on the daemon so the panel mode
      // flips back and the kiosk-replay state clears for both kiosks.
      const u = new URL(location.href);
      u.searchParams.delete('replay');
      location.href = u.toString();
    }

    // ---- Keyboard ----

    document.addEventListener('keydown', (ev) => {
      // Don't intercept if the user is typing in a form (defensive;
      // these pages don't have forms today, but if any get added
      // later we won't mysteriously break their text input).
      const tag = (ev.target && ev.target.tagName) || '';
      if (tag === 'INPUT' || tag === 'TEXTAREA') return;

      // Ignore modifiers we don't expect, except Shift which we use
      // for the event-marker shortcut.
      if (ev.ctrlKey || ev.altKey || ev.metaKey) return;

      switch (ev.key) {
        case ' ':
          ev.preventDefault();
          togglePause();
          break;
        case 'ArrowLeft':
          ev.preventDefault();
          ev.shiftKey ? jumpToEventMarker(-1) : seek(-10000);
          break;
        case 'ArrowRight':
          ev.preventDefault();
          ev.shiftKey ? jumpToEventMarker(+1) : seek(+10000);
          break;
        case '1': ev.preventDefault(); setSpeed(0.5); break;
        case '2': ev.preventDefault(); setSpeed(1);   break;
        case '3': ev.preventDefault(); setSpeed(2);   break;
        case '4': ev.preventDefault(); setSpeed(4);   break;
        case 'Home': ev.preventDefault(); seekToStart(); break;
        case 'End':  ev.preventDefault(); seekToEnd();   break;
        case 'q':
        case 'Q':
          ev.preventDefault();
          closeReplay();
          break;
        case '?':
          ev.preventDefault();
          showHelp();
          break;
      }
    });

    return { detail, totalMs };
  }

  // ---- Toast (keystroke feedback) ----
  //
  // Small auto-fading banner near the bottom of the page. Since the
  // operator chose 'no overlay chrome' for the replay UI, this is
  // the only visible signal that a keystroke registered. Visible
  // for 1.2 s then fades to nothing.

  let toastEl = null;
  let toastHideTimer = null;

  function showToast(text) {
    ensureToastDOM();
    toastEl.textContent = text;
    toastEl.classList.add('zerotx-toast-show');
    if (toastHideTimer) clearTimeout(toastHideTimer);
    toastHideTimer = setTimeout(() => {
      toastEl.classList.remove('zerotx-toast-show');
    }, 1200);
  }

  function ensureToastDOM() {
    if (toastEl) return;
    injectStyles();
    toastEl = document.createElement('div');
    toastEl.className = 'zerotx-toast';
    document.body.appendChild(toastEl);
  }

  // ---- Help overlay (?) ----
  //
  // Full-screen semi-transparent overlay with the shortcut list.
  // Dismissed by any subsequent key or by clicking the overlay.

  let helpEl = null;

  function showHelp() {
    ensureHelpDOM();
    helpEl.classList.add('zerotx-help-show');
  }
  function hideHelp() {
    if (helpEl) helpEl.classList.remove('zerotx-help-show');
  }

  function ensureHelpDOM() {
    if (helpEl) return;
    injectStyles();
    helpEl = document.createElement('div');
    helpEl.className = 'zerotx-help';
    helpEl.innerHTML = `
      <div class="zerotx-help-card">
        <h2>Replay controls</h2>
        <table>
          <tr><th>Space</th><td>play / pause</td></tr>
          <tr><th>← / →</th><td>seek −10s / +10s</td></tr>
          <tr><th>Shift+← / →</th><td>jump to previous / next event</td></tr>
          <tr><th>1 / 2 / 3 / 4</th><td>speed 0.5x / 1x / 2x / 4x</td></tr>
          <tr><th>Home / End</th><td>rewind / jump to end</td></tr>
          <tr><th>Q</th><td>close replay</td></tr>
          <tr><th>?</th><td>this help</td></tr>
        </table>
        <p class="zerotx-help-dismiss">press any key or click to dismiss</p>
      </div>`;
    helpEl.addEventListener('click', hideHelp);
    document.addEventListener('keydown', (ev) => {
      if (!helpEl.classList.contains('zerotx-help-show')) return;
      if (ev.key === '?') return; // ? itself toggles via the main handler
      hideHelp();
    });
    document.body.appendChild(helpEl);
  }

  // ---- Styles (injected lazily on first toast/help use) ----

  let stylesInjected = false;
  function injectStyles() {
    if (stylesInjected) return;
    stylesInjected = true;
    const style = document.createElement('style');
    style.textContent = `
      .zerotx-toast {
        position: fixed;
        bottom: 10%;
        left: 50%;
        transform: translateX(-50%);
        background: rgba(0, 0, 0, 0.78);
        color: #fff;
        font: 500 18px/1.2 system-ui, sans-serif;
        letter-spacing: 0.04em;
        padding: 10px 22px;
        border-radius: 6px;
        border: 1px solid rgba(255,255,255,0.15);
        z-index: 9998;
        opacity: 0;
        pointer-events: none;
        transition: opacity 200ms ease;
      }
      .zerotx-toast-show { opacity: 1; }

      .zerotx-help {
        position: fixed;
        inset: 0;
        background: rgba(0,0,0,0.75);
        display: none;
        align-items: center;
        justify-content: center;
        z-index: 9999;
      }
      .zerotx-help.zerotx-help-show { display: flex; }
      .zerotx-help-card {
        background: #1a1a1a;
        color: #fff;
        font: 14px/1.4 system-ui, sans-serif;
        padding: 24px 32px;
        border-radius: 8px;
        border: 1px solid rgba(255,255,255,0.2);
        max-width: 500px;
      }
      .zerotx-help-card h2 {
        margin: 0 0 16px 0;
        font-size: 18px;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        color: #d4af37;
      }
      .zerotx-help-card table {
        border-collapse: collapse;
        width: 100%;
      }
      .zerotx-help-card th, .zerotx-help-card td {
        text-align: left;
        padding: 6px 12px 6px 0;
        font-weight: normal;
      }
      .zerotx-help-card th {
        color: #a0a0a0;
        font-family: ui-monospace, monospace;
        white-space: nowrap;
        width: 40%;
      }
      .zerotx-help-dismiss {
        margin: 16px 0 0 0;
        font-size: 12px;
        color: #808080;
        text-align: center;
      }
    `;
    document.head.appendChild(style);
  }

  // ---- Synthesizer: recording at time T -> WS-shaped state ----
  //
  // The output shape mirrors what /api/v1/stream's WS endpoint
  // sends. Both /hud and /map's existing renderers consume this
  // shape directly. Fields are absent (not null) when the recording
  // has no data of that kind at the given time -- the renderers
  // already handle that gracefully via optional chaining.

  function synthesize(detail, clockMs) {
    const tel = latestTelemetryAt(detail.telemetry, clockMs);
    const derived = deriveFromEvents(detail.events, clockMs);

    const out = {
      ts: new Date(clockMs).toISOString(),
      arm: { state: derived.armState },
      telemetry: {},
    };

    const modeStr = derived.currentMode || tel?.flightMode;
    if (modeStr) {
      out.telemetry.flightMode = { data: { mode: modeStr } };
    }

    if (tel?.gpsLat != null && tel?.gpsLon != null) {
      let fix = 0;
      const sats = tel.gpsSats ?? 0;
      if (sats >= 6) fix = 3;
      else if (sats >= 4) fix = 2;
      out.telemetry.gps = {
        data: {
          latDeg: tel.gpsLat,
          lonDeg: tel.gpsLon,
          altMeters: tel.gpsAlt ?? 0,
          groundKmh: tel.gpsKmh ?? 0,
          headingDeg: tel.gpsHdg ?? 0,
          sats: sats,
          fix: fix,
        },
      };
    }

    if (derived.homePos) {
      out.telemetry.home = {
        data: { latDeg: derived.homePos.lat, lonDeg: derived.homePos.lon },
      };
    }

    if (tel?.linkRssi != null) {
      out.telemetry.link = {
        data: {
          uplinkRSSIdBm: tel.linkRssi,
          uplinkLQ: tel.linkLq ?? 0,
          uplinkSNR: tel.linkSnr ?? 0,
        },
      };
    }

    if (tel?.attitudeRoll != null) {
      out.telemetry.attitude = {
        data: {
          rollDeg: tel.attitudeRoll,
          pitchDeg: tel.attitudePitch ?? 0,
          yawDeg: tel.attitudeYaw ?? 0,
        },
      };
    }

    if (tel?.batVolts != null) {
      out.telemetry.battery = {
        data: {
          volts: tel.batVolts,
          amps: tel.batAmps ?? 0,
          percent: tel.batPct ?? 0,
          usedMAh: tel.batMah ?? 0,
        },
      };
    }

    return out;
  }

  function latestTelemetryAt(rows, clockMs) {
    for (let i = rows.length - 1; i >= 0; i--) {
      if (rows[i].tsMs <= clockMs) return rows[i];
    }
    return undefined;
  }

  function deriveFromEvents(events, clockMs) {
    let armState = 'DISARMED';
    let homePos = null;
    let currentMode = null;
    for (const ev of events || []) {
      if (ev.tsMs > clockMs) break;
      if (ev.kind !== 'flight') continue;
      if (ev.name === 'armed') armState = 'ARMED';
      else if (ev.name === 'disarmed') armState = 'DISARMED';
      else if (ev.name === 'home-set' && ev.detail) {
        try {
          const d = JSON.parse(ev.detail);
          if (Number.isFinite(d.lat) && Number.isFinite(d.lon)) {
            homePos = { lat: d.lat, lon: d.lon };
          }
        } catch (_) {}
      } else if (ev.name === 'mode-change' && ev.detail) {
        try {
          const d = JSON.parse(ev.detail);
          if (typeof d.mode === 'string') currentMode = d.mode;
        } catch (_) {}
      }
    }
    return { armState, homePos, currentMode };
  }

  // Export.
  window.Replay = { start };
})();
