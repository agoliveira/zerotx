#include "spectator.h"

#include <WiFi.h>
#include <WebServer.h>
#include <WebSocketsServer.h>

namespace {

WebServer        http(80);
WebSocketsServer ws(81);
unsigned long    last_tick_ms = 0;
bool             ap_up        = false;

// Cached snapshot from the most recent push_state() call. Held by
// value so we don't depend on caller lifetime for primitives;
// strings are pointer-copied (see header note).
spectator::Snapshot snap = {};
char snap_mode_buf[24]   = {0};   // own copy of mode/alarm so the
char snap_alarm_buf[16]  = {0};   // pointers in snap stay valid
                                  // across ticks even if the
                                  // caller's strings change.

// Single static HTML page, served from PROGMEM. Mobile-first dark
// theme: hero status block + 2-column tile grid. WebSocket reconnect
// is built in so phones that screen-lock and wake recover cleanly.
const char DASHBOARD_HTML[] PROGMEM = R"HTML(
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,user-scalable=no">
<title>ZeroTX Spectator</title>
<style>
:root{
  --bg:#0a0d10;--surface:#141a1f;--border:#252b32;
  --text:#e0e6ec;--dim:#8a9099;--accent:#00d4aa;
  --warn:#ffb820;--err:#ff4848;--ok:#5fdc54;
}
*{box-sizing:border-box;margin:0;padding:0}
html,body{background:var(--bg);color:var(--text);
  font-family:ui-monospace,Menlo,Consolas,monospace;
  -webkit-font-smoothing:antialiased}
body{min-height:100vh;padding:16px;max-width:560px;margin:0 auto}
header{display:flex;justify-content:space-between;align-items:baseline;
  margin-bottom:12px;color:var(--dim);font-size:12px}
header b{color:var(--accent);letter-spacing:.1em}
.hero{background:var(--surface);border:1px solid var(--border);
  border-radius:8px;padding:24px 16px;text-align:center;
  margin-bottom:12px;transition:background .2s,border-color .2s}
.hero .label{color:var(--dim);font-size:12px;letter-spacing:.15em}
.hero .value{font-size:42px;font-weight:600;letter-spacing:.05em;
  margin-top:6px;line-height:1}
.hero[data-state="armed"] .value{color:var(--err)}
.hero[data-state="armed"]{background:#2a1010;border-color:var(--err)}
.hero[data-state="rth"] .value{color:var(--warn)}
.hero[data-state="rth"]{background:#2a2010;border-color:var(--warn)}
.hero[data-state="failsafe"] .value{color:var(--err)}
.hero[data-state="failsafe"]{background:#3a0a0a;border-color:var(--err);
  animation:pulse 1.2s infinite}
@keyframes pulse{50%{filter:brightness(1.4)}}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:8px}
.tile{background:var(--surface);border:1px solid var(--border);
  border-radius:6px;padding:12px}
.tile .label{color:var(--dim);font-size:11px;letter-spacing:.1em}
.tile .value{font-size:24px;font-weight:600;margin-top:2px}
.tile .unit{color:var(--dim);font-size:14px;font-weight:400;
  margin-left:4px}
.tile.full{grid-column:1/-1}
.foot{margin-top:16px;color:var(--dim);font-size:11px;text-align:center}
.foot.stale{color:var(--err)}
</style>
</head>
<body>
<header>
  <b>ZEROTX</b>
  <span id="conn">connecting&hellip;</span>
</header>
<div class="hero" id="hero" data-state="disarmed">
  <div class="label">STATUS</div>
  <div class="value" id="status">DISARMED</div>
</div>
<div class="grid">
  <div class="tile">
    <div class="label">MODE</div>
    <div class="value" id="mode">&mdash;</div>
  </div>
  <div class="tile">
    <div class="label">TIME ALOFT</div>
    <div class="value" id="time">&mdash;</div>
  </div>
  <div class="tile">
    <div class="label">ALTITUDE</div>
    <div class="value"><span id="alt">&mdash;</span><span class="unit">m</span></div>
  </div>
  <div class="tile">
    <div class="label">DISTANCE</div>
    <div class="value"><span id="dist">&mdash;</span><span class="unit">m</span></div>
  </div>
  <div class="tile">
    <div class="label">SPEED</div>
    <div class="value"><span id="spd">&mdash;</span><span class="unit">km/h</span></div>
  </div>
  <div class="tile">
    <div class="label">BATTERY</div>
    <div class="value"><span id="bat">&mdash;</span><span class="unit">V</span></div>
  </div>
  <div class="tile full">
    <div class="label">LINK QUALITY</div>
    <div class="value"><span id="lq">&mdash;</span><span class="unit">%</span></div>
  </div>
</div>
<div class="foot" id="foot">No data yet.</div>

<script>
(function(){
  var $=function(id){return document.getElementById(id)};
  var ws,reconnectAt=0,lastRx=0;

  function fmtTime(s){
    if(s==null||s<0)return '\u2014';
    var m=Math.floor(s/60),r=s%60;
    return m+':'+(r<10?'0':'')+r;
  }
  function setText(id,v,fb){
    $(id).textContent=(v==null||v===''?(fb||'\u2014'):v);
  }
  function apply(d){
    lastRx=Date.now();
    var st=(d.status||'DISARMED').toLowerCase();
    $('hero').setAttribute('data-state',st);
    $('status').textContent=(d.status||'DISARMED').toUpperCase();
    setText('mode',d.mode);
    setText('time',fmtTime(d.time_s));
    setText('alt',d.alt_m);
    setText('dist',d.dist_m);
    setText('spd',d.spd_kmh);
    setText('bat',d.bat_v!=null?d.bat_v.toFixed(2):null);
    setText('lq',d.link_pct);
    $('foot').className='foot';
    $('foot').textContent='Live.';
  }
  function markStale(){
    $('conn').textContent='reconnecting\u2026';
    $('foot').className='foot stale';
    $('foot').textContent='Connection lost.';
  }
  function connect(){
    try{
      ws=new WebSocket('ws://'+location.hostname+':81/');
      ws.onopen=function(){$('conn').textContent='connected'};
      ws.onmessage=function(e){
        try{apply(JSON.parse(e.data))}catch(err){}
      };
      ws.onclose=function(){markStale();reconnectAt=Date.now()+1500};
      ws.onerror=function(){};
    }catch(err){
      reconnectAt=Date.now()+1500;
    }
  }
  setInterval(function(){
    if(reconnectAt&&Date.now()>=reconnectAt){
      reconnectAt=0;connect();
    }
    if(lastRx&&Date.now()-lastRx>3000)markStale();
  },500);
  connect();
})();
</script>
</body>
</html>
)HTML";

// Build the JSON payload. Keep it small: ~200 bytes typical.
void build_state_json(char *out, size_t cap) {
  // Status string. Failsafe and RTH override the base armed state.
  const char *status = "DISARMED";
  if (snap.armed_known && snap.armed) {
    status = "ARMED";
    if (snap.flight_mode &&
        (strstr(snap.flight_mode, "RTH") ||
         strstr(snap.flight_mode, "rth"))) {
      status = "RTH";
    }
    if (snap.alarm_active && snap.alarm_level &&
        (strcmp(snap.alarm_level, "critical") == 0 ||
         strcmp(snap.alarm_level, "failsafe") == 0)) {
      status = "FAILSAFE";
    }
  }

  int n = snprintf(out, cap,
    "{\"status\":\"%s\",\"mode\":\"%s\"",
    status, snap.flight_mode ? snap.flight_mode : "");

  if (snap.alt_known)   n += snprintf(out+n, cap-n, ",\"alt_m\":%d",   snap.alt_m);
  if (snap.dist_known)  n += snprintf(out+n, cap-n, ",\"dist_m\":%d",  snap.dist_m);
  if (snap.spd_known)   n += snprintf(out+n, cap-n, ",\"spd_kmh\":%d", snap.spd_kmh);
  if (snap.link_known)  n += snprintf(out+n, cap-n, ",\"link_pct\":%d", snap.link_pct);
  if (snap.bat_known)   n += snprintf(out+n, cap-n, ",\"bat_v\":%.2f", snap.bat_v);
  if (snap.time_known)  n += snprintf(out+n, cap-n, ",\"time_s\":%d",  snap.time_s);

  snprintf(out+n, cap-n, "}");
}

void on_ws_event(uint8_t /*num*/, WStype_t type,
                 uint8_t * /*payload*/, size_t /*len*/) {
  // We don't accept any client-to-server messages.
  (void)type;
}

}  // namespace

namespace spectator {

void begin() {
  WiFi.mode(WIFI_AP);
  WiFi.softAP(SSID, PASSWORD, /*channel=*/1, /*hidden=*/0,
              /*max_connection=*/MAX_CLIENTS);

  http.on("/", HTTP_GET, []() {
    http.send_P(200, "text/html", DASHBOARD_HTML);
  });
  http.onNotFound([]() {
    http.send(404, "text/plain", "not found");
  });
  http.begin();

  ws.begin();
  ws.onEvent(on_ws_event);

  ap_up = true;
  Serial.print(F("DISP SPECTATOR ap_up ssid="));
  Serial.print(SSID);
  Serial.print(F(" ip="));
  Serial.println(WiFi.softAPIP());
}

void push_state(const Snapshot &s) {
  // Copy primitives directly; copy strings into our own buffers so
  // the cached pointers remain valid even if the caller's String
  // members get reassigned between push and broadcast.
  snap = s;
  if (s.flight_mode) {
    strncpy(snap_mode_buf, s.flight_mode, sizeof(snap_mode_buf) - 1);
    snap_mode_buf[sizeof(snap_mode_buf) - 1] = '\0';
    snap.flight_mode = snap_mode_buf;
  } else {
    snap_mode_buf[0] = '\0';
    snap.flight_mode = snap_mode_buf;
  }
  if (s.alarm_level) {
    strncpy(snap_alarm_buf, s.alarm_level, sizeof(snap_alarm_buf) - 1);
    snap_alarm_buf[sizeof(snap_alarm_buf) - 1] = '\0';
    snap.alarm_level = snap_alarm_buf;
  } else {
    snap_alarm_buf[0] = '\0';
    snap.alarm_level = snap_alarm_buf;
  }
}

void tick() {
  if (!ap_up) return;
  http.handleClient();
  ws.loop();

  unsigned long now = millis();
  if (now - last_tick_ms < TICK_INTERVAL_MS) return;
  last_tick_ms = now;

  if (ws.connectedClients() == 0) return;
  char buf[320];
  build_state_json(buf, sizeof(buf));
  ws.broadcastTXT(buf);
}

}  // namespace spectator
