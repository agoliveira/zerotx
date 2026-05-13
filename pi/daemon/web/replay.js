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
//     starts a 10 Hz playback loop, and feeds synthesized state
//     messages into the caller-supplied handler. The handler shape
//     matches what the live /api/v1/stream WS payload sends, so the
//     caller's existing renderer (also fed by the WS path) works
//     unchanged.
//
//     Returns a small object {detail, totalMs} on success, null on
//     failure (e.g. recording not found). Errors are logged via
//     console.error -- the caller need not catch.
//
// What's NOT in this module (handled by callers or later commits):
//   * URL-param detection -- callers decide whether to call start()
//   * Playback controls (pause/scrub/speed/keyboard) -- 2b-4
//   * Cross-tab sync -- 2b-3 will add this here as it's shared
//   * kiosk.replay auto-nav from the WS state -- 2b-5, lives in
//     the caller's connectWS path since it needs the live socket

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

    // Wall-clock anchored playback. Pause/resume in later commits
    // is just rebasing anchorWall; scrub is rebasing anchorClock.
    let anchorWall = Date.now();
    let anchorClock = 0;
    const speed = 1; // fixed at 1x until 2b-4

    // Helper to read the current playback clock at this instant.
    // Used by the tick loop AND by the BroadcastChannel handler
    // when a freshly-loaded peer tab asks "where are you?".
    function currentClockMs() {
      return anchorClock + (Date.now() - anchorWall) * speed;
    }

    // ---- Cross-tab sync via BroadcastChannel ----
    //
    // Same-origin tabs (e.g. /hud and /map on the same Pi) keep
    // their playback clocks aligned via a per-recording channel.
    // The handshake is symmetric: when a tab joins it broadcasts
    // 'hello' and any tab that's already playing responds with
    // 'anchor' carrying its current clock. The joining tab
    // rebases its own anchor to match, so they all run at the
    // same playback time within ~1 frame.
    //
    // Channel name is scoped to the recording so loading a
    // different recording in a new tab doesn't accidentally sync
    // with an unrelated replay session.
    let synced = false;
    let bc = null;
    try {
      bc = new BroadcastChannel('zerotx-replay-' + name);
    } catch (e) {
      // BroadcastChannel is widely available but a degraded
      // browser shouldn't break replay. Log and continue without
      // cross-tab sync.
      console.warn('Replay: BroadcastChannel unavailable, running unsynced:', e);
    }

    if (bc) {
      bc.onmessage = (ev) => {
        const msg = ev.data;
        if (!msg || typeof msg !== 'object') return;
        switch (msg.type) {
          case 'hello':
            // A peer just joined and is asking where we are.
            // Respond with our current playback clock. If we
            // haven't started ticking yet (still in the join
            // grace period below) currentClockMs() returns 0,
            // which is the correct answer in that case.
            bc.postMessage({ type: 'anchor', clockMs: currentClockMs() });
            break;
          case 'anchor':
            // A peer told us where playback is. Only act on the
            // first such message we see (so a chain of replies
            // doesn't keep nudging us). Sanity-check: ignore
            // values that would imply playback is past the end
            // of the recording.
            if (synced) break;
            if (typeof msg.clockMs !== 'number' || msg.clockMs < 0) break;
            if (msg.clockMs > totalMs + 1000) break;
            anchorWall = Date.now();
            anchorClock = msg.clockMs;
            synced = true;
            console.log(`[replay] synced to peer at t=${Math.round(msg.clockMs/1000)}s`);
            break;
        }
      };
      bc.postMessage({ type: 'hello' });
    }

    // Brief grace period before starting the tick loop so any peer
    // that's going to respond with an 'anchor' has time to do so.
    // ~150 ms is generous for an in-browser BroadcastChannel
    // round-trip (real RTT is sub-millisecond), and the visual
    // cost of starting 150 ms late is invisible.
    await new Promise((resolve) => setTimeout(resolve, 150));

    const tickMs = 100; // 10 Hz, matches live stream cadence
    setInterval(() => {
      const clockMs = currentClockMs();
      if (clockMs > totalMs) return; // end-of-recording: stop emitting
      const state = synthesize(detail, clockMs);
      options.handleStateMessage(state);
    }, tickMs);

    return { detail, totalMs };
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

    // Flight mode: prefer the canonical mode-change event sequence,
    // fall back to the telemetry row's fmMode column for rows
    // recorded before mode-change events were a thing.
    const modeStr = derived.currentMode || tel?.flightMode;
    if (modeStr) {
      out.telemetry.flightMode = { data: { mode: modeStr } };
    }

    if (tel?.gpsLat != null && tel?.gpsLon != null) {
      // gps.fix isn't in the recording schema; synthesize from sats.
      // The renderers check fix===3 / fix===2 specifically, so the
      // mapping needs to land in those buckets when the recording
      // implies good GPS.
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

  // latestTelemetryAt returns the row whose tsMs is the largest
  // value still <= clockMs, or undefined if no row qualifies (clock
  // is before the first row). Linear scan from the end; O(n) per
  // call, but n is small (~3000 for a 10-min flight) and clockMs
  // is monotonic in normal playback. Future optimization is a
  // cached "last index" hint -- defer until profile says we need it.
  function latestTelemetryAt(rows, clockMs) {
    for (let i = rows.length - 1; i >= 0; i--) {
      if (rows[i].tsMs <= clockMs) return rows[i];
    }
    return undefined;
  }

  // deriveFromEvents walks the event stream up to clockMs and
  // computes derived state -- arm, home position, current flight
  // mode. Walked linearly because events are O(tens), not O(thousands).
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
