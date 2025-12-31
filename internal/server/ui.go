package server

import (
	"net/http"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (s *Server) handleAppJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(appJS))
}

const indexHTML = `<!doctype html>
<html>
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width,initial-scale=1"/>
  <title>Stock Radar (Audible)</title>
  <style>
    body { font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Arial; margin: 18px; }
    .row { display:flex; gap:12px; flex-wrap:wrap; align-items:center; }
    .pill { padding: 6px 10px; border: 1px solid #ddd; border-radius: 999px; font-size: 12px; background:#fff; }
    button { padding: 8px 12px; border-radius: 10px; border: 1px solid #111; background:#111; color:#fff; cursor:pointer;}
    button.secondary { background:#fff; color:#111; }
    input { padding:8px; border-radius:10px; border:1px solid #ddd; min-width: 320px; }
    #events { margin-top: 14px; border-top:1px solid #eee; padding-top: 12px; }
    .event { padding: 10px 10px; border-bottom: 1px solid #f1f1f1; border-left: 6px solid #ddd; border-radius: 8px; margin-bottom: 8px; }
    .event .meta { color:#666; font-size: 12px; }
    .event .msg { font-size: 14px; margin-top: 2px; }
    .event.up { border-left-color: #18a558; background: rgba(24,165,88,0.08); }
    .event.down { border-left-color: #d64545; background: rgba(214,69,69,0.08); }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }

    /* Cloud box with FULL FRAME */
    .cloudBox { margin-top: 14px; padding: 12px; border-radius: 14px; background:#fafafa; border: 5px solid #111; }
    .cloudBox.up { border-color: #18a558; }
    .cloudBox.down { border-color: #d64545; }
    .cloudBox.flat { border-color: #111; }

    .cloudTitle { display:flex; justify-content:space-between; gap:12px; flex-wrap:wrap; align-items:baseline; }
    .cloudBig { font-size: 18px; font-weight: 800; }
    .cloudSmall { color:#666; font-size: 12px; }
    .barWrap { width: 360px; max-width: 90vw; height: 10px; background:#e9e9e9; border-radius: 999px; overflow:hidden; }
    .bar { height: 10px; width: 0%; background:#111; }
    .bar.up { background:#18a558; }
    .bar.down { background:#d64545; }
  </style>
</head>
<body>
  <h2>Stock Radar (Option B: Browser Audio)</h2>

  <div class="row">
    <span class="pill">SSE: <span id="sseStatus" class="mono">connecting…</span></span>
    <span class="pill">Voice alerts: <span id="audioStatus" class="mono">disabled</span></span>
    <button id="enableAudio">Enable Audio</button>
    <button id="muteAudio" class="secondary">Mute</button>

    <span class="pill">Cloud sound: <span id="cloudSoundStatus" class="mono">on</span></span>
    <button id="toggleCloud" class="secondary">Cloud: on</button>

    <span class="pill">Cloud voice: <span id="cloudVoiceStatus" class="mono">on</span></span>
    <button id="toggleCloudVoice" class="secondary">Cloud voice: on</button>
  </div>

  <div class="cloudBox flat" id="cloudBox">
    <div class="cloudTitle">
      <div>
        <div class="cloudBig" id="cloudHeadline">Cloud: —</div>
        <div class="cloudSmall mono" id="cloudDetails">waiting for ticks…</div>
      </div>
      <div class="barWrap" aria-label="cloud strength">
        <div class="bar" id="cloudBar"></div>
      </div>
    </div>
  </div>

  <div style="margin-top:12px" class="row">
    <input id="testText" placeholder="Test TTS text (e.g. 'hello radar')"/>
    <button id="testSpeak" class="secondary">Generate + Play</button>
    <span class="pill">Cache dir served at <span class="mono">/audio/…</span></span>
  </div>

  <div id="events"></div>

  <script src="/app.js"></script>
</body>
</html>
`

const appJS = `
(function(){
  const sseStatus = document.getElementById('sseStatus');
  const audioStatus = document.getElementById('audioStatus');
  const cloudSoundStatus = document.getElementById('cloudSoundStatus');
  const cloudVoiceStatus = document.getElementById('cloudVoiceStatus');

  const cloudBox = document.getElementById('cloudBox');
  const cloudHeadline = document.getElementById('cloudHeadline');
  const cloudDetails = document.getElementById('cloudDetails');
  const cloudBar = document.getElementById('cloudBar');

  const eventsEl = document.getElementById('events');

  let audioEnabled = false;
  let muted = false;

  function setSSE(text){ sseStatus.textContent = text; }
  function setAudio(text){ audioStatus.textContent = text; }

  // Main voice-alert queue (WebAudio mixer + decoded buffer cache)
  let queue = [];
  let playing = false;
  let currentVoiceSrc = null;

  // WebAudio graph for voice alerts:
  //   (sources) -> voiceBus -> compressor -> voiceMaster -> destination
  let voiceBus = null;
  let voiceComp = null;
  let voiceMaster = null;

  // Decoded buffer cache: url -> { buf: AudioBuffer, last: epochMs }
  const voiceCache = new Map();
  const voiceInflight = new Map();
  const MAX_VOICE_BUFFERS = 80;   // keep memory bounded
  const MAX_VOICE_QUEUE = 200;    // preserve your existing behavior

  function applyVoiceMute() {
    if (voiceMaster) voiceMaster.gain.value = muted ? 0.0 : 1.0;
  }

  function ensureVoiceGraph(){
    // Reuse your existing AudioContext (same one used by cloud clicks)
    ensureAudioCtx();
    if (!audioCtx) return false;

    if (!voiceBus) {
      voiceBus = audioCtx.createGain();
      voiceBus.gain.value = 1.0;

      // A little compression so stacked/fast alerts don’t clip badly
      voiceComp = audioCtx.createDynamicsCompressor();
      voiceComp.threshold.value = -18;
      voiceComp.knee.value = 6;
      voiceComp.ratio.value = 6;
      voiceComp.attack.value = 0.003;
      voiceComp.release.value = 0.15;

      voiceMaster = audioCtx.createGain();
      voiceMaster.gain.value = muted ? 0.0 : 1.0;

      voiceBus.connect(voiceComp);
      voiceComp.connect(voiceMaster);
      voiceMaster.connect(audioCtx.destination);
    }

    applyVoiceMute();
    return true;
  }

  function decodeAudioCompat(arrayBuffer) {
    // Works with both Promise-based and callback-based decodeAudioData
    return new Promise((resolve, reject) => {
      try {
        const maybePromise = audioCtx.decodeAudioData(arrayBuffer, resolve, reject);
        if (maybePromise && typeof maybePromise.then === 'function') {
          maybePromise.then(resolve).catch(reject);
        }
      } catch (e) {
        reject(e);
      }
    });
  }

  function evictVoiceCacheIfNeeded(){
    if (voiceCache.size <= MAX_VOICE_BUFFERS) return;

    // Evict least-recently-used
    let oldestKey = null;
    let oldestTs = Infinity;
    for (const [k, v] of voiceCache.entries()) {
      if (v.last < oldestTs) {
        oldestTs = v.last;
        oldestKey = k;
      }
    }
    if (oldestKey !== null) voiceCache.delete(oldestKey);
  }

  function warmVoiceBuffer(url){
    if (!url) return;
    if (!audioEnabled || muted) return;
    if (!ensureVoiceGraph()) return;

    if (voiceCache.has(url) || voiceInflight.has(url)) return;

    const p = (async () => {
      const res = await fetch(url);
      if (!res.ok) throw new Error('fetch failed: ' + res.status);
      const ab = await res.arrayBuffer();
      const buf = await decodeAudioCompat(ab);
      voiceCache.set(url, { buf: buf, last: Date.now() });
      evictVoiceCacheIfNeeded();
      return buf;
    })();

    voiceInflight.set(url, p);
    p.catch(() => {}).finally(() => voiceInflight.delete(url));
  }

  async function getVoiceBuffer(url){
    if (voiceCache.has(url)) {
      const v = voiceCache.get(url);
      v.last = Date.now();
      return v.buf;
    }
    if (voiceInflight.has(url)) {
      const buf = await voiceInflight.get(url);
      if (buf && !voiceCache.has(url)) {
        voiceCache.set(url, { buf: buf, last: Date.now() });
        evictVoiceCacheIfNeeded();
      }
      return buf;
    }

    const p = (async () => {
      const res = await fetch(url);
      if (!res.ok) throw new Error('fetch failed: ' + res.status);
      const ab = await res.arrayBuffer();
      const buf = await decodeAudioCompat(ab);
      voiceCache.set(url, { buf: buf, last: Date.now() });
      evictVoiceCacheIfNeeded();
      return buf;
    })();

    voiceInflight.set(url, p);
    try {
      return await p;
    } finally {
      voiceInflight.delete(url);
    }
  }

  function stopCurrentVoice(){
    if (currentVoiceSrc) {
      try {
        currentVoiceSrc.onended = null;
        currentVoiceSrc.stop();
      } catch(e) {}
      currentVoiceSrc = null;
    }
    playing = false;
  }

  async function pump(){
    if (!audioEnabled || muted) return;
    if (playing) return;
    if (!ensureVoiceGraph()) return;

    const next = queue.shift();
    if (!next) return;

    playing = true;

    try {
      // Load/decode (often already warmed)
      const buf = await getVoiceBuffer(next);

      // User might have muted while we were fetching/decoding
      if (!audioEnabled || muted) {
        playing = false;
        return;
      }

      const src = audioCtx.createBufferSource();
      src.buffer = buf;
      src.connect(voiceBus);
      currentVoiceSrc = src;

      src.onended = () => {
        if (currentVoiceSrc === src) currentVoiceSrc = null;
        playing = false;
        pump();
      };

      src.start();
    } catch (e) {
      console.warn('voice alert playback failed:', e);
      currentVoiceSrc = null;
      playing = false;
      pump();
    }
  }

  function enqueue(url){
    if (!url) return;

    queue.push(url);
    if (queue.length > MAX_VOICE_QUEUE) queue = queue.slice(queue.length - MAX_VOICE_QUEUE);

    // Start fetch/decode ASAP to reduce latency later
    warmVoiceBuffer(url);

    pump();
  }

  // Cloud voice has its own tiny queue to ensure you actually hear it
  let cloudVoiceQueue = [];
  let cloudVoicePlaying = false;
  const cloudVoiceAudio = new Audio();
  cloudVoiceAudio.volume = 1.0;
  cloudVoiceAudio.addEventListener('ended', () => { cloudVoicePlaying = false; cloudVoicePump(); });
  cloudVoiceAudio.addEventListener('error', () => { cloudVoicePlaying = false; cloudVoicePump(); });

  function cloudVoicePump(){
    if (!audioEnabled || muted) return;
    if (!cloudVoiceEnabled) return;
    if (cloudVoicePlaying) return;
    const next = cloudVoiceQueue.shift();
    if (!next) return;
    cloudVoicePlaying = true;
    cloudVoiceAudio.src = next;
    cloudVoiceAudio.play().catch(() => { cloudVoicePlaying = false; });
  }

  function cloudVoiceEnqueue(url, priority){
    if (!url) return;
    if (priority) cloudVoiceQueue.unshift(url);
    else cloudVoiceQueue.push(url);
    if (cloudVoiceQueue.length > 20) cloudVoiceQueue = cloudVoiceQueue.slice(0, 20);
    cloudVoicePump();
  }

  // --- Cloud “geiger” sound ---
  let cloudEnabled = true;
  let cloudVoiceEnabled = true;

  let audioCtx = null;

  let cloudDir = 'flat';
  let cloudStrength = 0.0;
  let cloudRateHz = 0.0; // kept for display only (no longer drives audio timing)

  // cue URLs fetched from server (/api/cues)
  let cues = { up:null, upStrong:null, down:null, downStrong:null, flat:null };

  // speech timing
  let lastSpokenDir = null;
  let lastVoiceAt = 0;
  let lastStrong = false;

  function ensureAudioCtx(){
    if (!audioCtx) {
      const AC = window.AudioContext || window.webkitAudioContext;
      audioCtx = new AC();
    }
    if (audioCtx && audioCtx.state === 'suspended') {
      audioCtx.resume().catch(()=>{});
    }
  }

  // Cloud click mixer (so bursts don’t clip your speakers)
  let cloudBus = null;
  let cloudComp = null;
  let cloudMaster = null;

  function applyCloudMute(){
    if (cloudMaster) cloudMaster.gain.value = (muted || !cloudEnabled) ? 0.0 : 1.0;
  }

  function ensureCloudGraph(){
    ensureAudioCtx();
    if (!audioCtx) return false;

    if (!cloudBus) {
      cloudBus = audioCtx.createGain();
      cloudBus.gain.value = 1.0;

      cloudComp = audioCtx.createDynamicsCompressor();
      cloudComp.threshold.value = -16;
      cloudComp.knee.value = 6;
      cloudComp.ratio.value = 8;
      cloudComp.attack.value = 0.002;
      cloudComp.release.value = 0.08;

      cloudMaster = audioCtx.createGain();
      cloudMaster.gain.value = (muted || !cloudEnabled) ? 0.0 : 1.0;

      cloudBus.connect(cloudComp);
      cloudComp.connect(cloudMaster);
      cloudMaster.connect(audioCtx.destination);
    }

    applyCloudMute();
    return true;
  }

  function playCloudClick(dir, strength, volNorm){
    if (!audioCtx) return;
    if (!cloudEnabled) return;
    if (!audioEnabled || muted) return;
    if (!ensureCloudGraph()) return;

    const now = audioCtx.currentTime;

    const osc = audioCtx.createOscillator();
    const gain = audioCtx.createGain();

    // Direction => pitch band (flat gets its own band)
    const s = Math.max(0, Math.min(1, strength || 0));
    const v = Math.max(0, Math.min(1, (typeof volNorm === 'number') ? volNorm : 0.5));

    const base = (dir === 'up') ? 800 : (dir === 'down') ? 240 : 520;
    const span = (dir === 'up') ? 1500 : (dir === 'down') ? 650 : 220;
    const freq = base + (span * s);

    osc.type = 'square';
    osc.frequency.setValueAtTime(freq, now);

    // Loudness: strength + volume (volume has the bigger say)
    const baseVol = (dir === 'flat') ? 0.025 : 0.045;
    const vol = (baseVol + 0.14 * s) * (0.25 + 0.75 * v);

    gain.gain.setValueAtTime(0.0001, now);
    gain.gain.exponentialRampToValueAtTime(vol, now + 0.002);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.035);

    osc.connect(gain);
    gain.connect(cloudBus);

    osc.start(now);
    osc.stop(now + 0.045);
  }

  // Pulse handling (event-driven timing)
  let lastPulseAtMs = 0;
  const MIN_PULSE_SPACING_MS = 0; // ~125 Hz max “machine gun” cap (adjust if you want)

  let volLogEwma = 0.0;
  let volHas = false;
  const VOL_ALPHA = 0.08;

  function onCloudPulse(ev){
    if (!cloudEnabled) return;
    if (!audioEnabled || muted) return;
    ensureAudioCtx();
    if (!audioCtx) return;

    const nowMs = Date.now();
    if (nowMs - lastPulseAtMs < MIN_PULSE_SPACING_MS) return;
    lastPulseAtMs = nowMs;

    const dir = ev.direction || 'flat';
    const strength = (typeof ev.strength === 'number') ? ev.strength : 0;
    const volRaw = (typeof ev.volume === 'number') ? ev.volume : 0;

    // Normalize volume using log + EWMA, so “loud” adapts to the symbol mix
    const lv = Math.log(1 + Math.max(0, volRaw));
    if (!volHas) { volLogEwma = lv; volHas = true; }
    else { volLogEwma = (1 - VOL_ALPHA) * volLogEwma + VOL_ALPHA * lv; }

    const denom = Math.max(0.0001, volLogEwma * 2.0);
    let volNorm = lv / denom;
    volNorm = Math.max(0, Math.min(1, volNorm));

    playCloudClick(dir, strength, volNorm);
  }

  async function loadCues(){
    try {
      const res = await fetch('/api/cues');
      if (!res.ok) return false;
      const j = await res.json();
      const m = (j && j.cues) ? j.cues : {};
      cues.up = m.up || cues.up;
      cues.upStrong = m.upStrong || cues.upStrong;
      cues.down = m.down || cues.down;
      cues.downStrong = m.downStrong || cues.downStrong;
      cues.flat = m.flat || cues.flat;
      return true;
    } catch(e) {
      return false;
    }
  }

  async function fallbackSpeak(text){
    // fallback if /api/cues not available for some reason
    try {
      const res = await fetch('/api/speak?text=' + encodeURIComponent(text));
      if (!res.ok) return null;
      const j = await res.json();
      return j.audio_url || null;
    } catch(e) {
      return null;
    }
  }

  function cueFor(dir, strength){
    const strong = strength >= 0.70;

    if (dir === 'up') return strong ? cues.upStrong : cues.up;
    if (dir === 'down') return strong ? cues.downStrong : cues.down;
    if (dir === 'flat') return cues.flat;
    return null;
  }

  async function speakDirOnceNow(){
    // Called right after Enable Audio so you ALWAYS hear current state
    let url = cueFor(cloudDir, cloudStrength);
    if (!url) {
      // fallback create
      const txt = (cloudDir === 'up') ? 'up' : (cloudDir === 'down') ? 'down' : 'flat';
      url = await fallbackSpeak(txt);
    }
    if (url) cloudVoiceEnqueue(url, true);
  }

  function maybeSpeakCloud(dir, strength){
    if (!cloudVoiceEnabled) return;
    if (!audioEnabled || muted) return;

    const strong = (dir === 'up' || dir === 'down') ? (strength >= 0.70) : false;
    const now = Date.now();

    const dirChanged = (dir !== lastSpokenDir);
    const strongBecameTrue = (!lastStrong && strong);

    // Speak on direction change (including FLAT) or when we newly become "strong"
    // and a small debounce so it doesn't chatter.
    if ((dirChanged || strongBecameTrue) && (now - lastVoiceAt >= 900)) {
      lastVoiceAt = now;
      lastSpokenDir = dir;
      lastStrong = strong;

      const url = cueFor(dir, strength);
      if (url) cloudVoiceEnqueue(url, true);
      return;
    }

    // Heartbeat: if we haven't said anything in a while, remind the current direction.
    // This ensures you don’t end up in “I see it but never hear it” situations.
    const heartbeatMs = (dir === 'flat') ? 12000 : 6000;
    if (now - lastVoiceAt >= heartbeatMs) {
      lastVoiceAt = now;
      lastSpokenDir = dir;
      lastStrong = strong;

      const url = cueFor(dir, strength);
      if (url) cloudVoiceEnqueue(url, false);
    }
  }

  function setCloudUI(ev){
    const dir = ev.direction || 'flat';
    const strength = (typeof ev.strength === 'number') ? ev.strength : 0;
    const score = (typeof ev.score === 'number') ? ev.score : 0;
    const rate = (typeof ev.rate_hz === 'number') ? ev.rate_hz : 0;
    const adv = ev.adv || 0;
    const dec = ev.dec || 0;
    const flat = ev.flat || 0;
    const active = ev.active || 0;
    const total = ev.total || 0;

    let label = 'FLAT';
    if (dir === 'up') label = 'UP';
    if (dir === 'down') label = 'DOWN';

    cloudHeadline.textContent = 'Cloud: ' + label + '  (strength ' + strength.toFixed(2) + ')';
    cloudDetails.textContent = 'score ' + (score >= 0 ? '+' : '') + score.toFixed(4) + '% • rate ' + rate.toFixed(1) + ' Hz • adv ' + adv + ' / dec ' + dec + ' / flat ' + flat + ' • active ' + active + '/' + total;

    // FULL FRAME color
    cloudBox.className = 'cloudBox ' + (dir === 'up' ? 'up' : (dir === 'down' ? 'down' : 'flat'));

    const pct = Math.max(0, Math.min(1, strength)) * 100;
    cloudBar.style.width = pct.toFixed(0) + '%';
    cloudBar.className = 'bar ' + (dir === 'up' ? 'up' : (dir === 'down' ? 'down' : ''));

    // Update state for audio loop
    cloudDir = dir;
    cloudStrength = strength;
    cloudRateHz = rate;

    // Speak "up / down / flat" when appropriate
    maybeSpeakCloud(dir, strength);
  }

  function addEvent(ev){
    const d = document.createElement('div');

    let dir = ev.direction || '';
    if (!dir && ev.type) {
      const t = ('' + ev.type).toLowerCase();
      if (t.indexOf('down') >= 0 || t.indexOf('below') >= 0) dir = 'down';
      if (t.indexOf('up') >= 0 || t.indexOf('above') >= 0) dir = 'up';
    }

    d.className = 'event' + (dir === 'up' ? ' up' : (dir === 'down' ? ' down' : ''));

    const ts = ev.time ? new Date(ev.time).toLocaleTimeString() : '';
    const cache = ev.cache_hit ? 'cache' : 'new';
    d.innerHTML = '<div class="meta"><span class="mono">' + ts + '</span> • <span class="mono">' + (ev.symbol||'') + '</span> • <span class="mono">' + (ev.type||'') + '</span> • <span class="mono">$' + (ev.price||0).toFixed(2) + '</span> • <span class="mono">' + cache + '</span></div>'
               + '<div class="msg">' + (ev.message || '') + '</div>';
    eventsEl.prepend(d);

    if (eventsEl.childNodes.length > 200) {
      while (eventsEl.childNodes.length > 200) eventsEl.removeChild(eventsEl.lastChild);
    }
  }

  // --- UI Controls ---
  document.getElementById('enableAudio').addEventListener('click', async () => {
    audioEnabled = true;
    muted = false;
    setAudio('enabled');

    ensureAudioCtx();

    // Load pre-generated cue URLs (fast, no OpenAI calls in the browser)
    await loadCues();

    // Speak current state immediately so you definitely hear "up/down/flat"
    await speakDirOnceNow();
    pump();
    cloudVoicePump();
  });

  document.getElementById('muteAudio').addEventListener('click', () => {
    muted = !muted;
    setAudio(muted ? 'muted' : (audioEnabled ? 'enabled' : 'disabled'));
    applyVoiceMute();
    applyCloudMute();
    if (muted) stopCurrentVoice();
    if (!muted) {
      ensureAudioCtx();
      pump();
      cloudVoicePump();
    }
  });

  document.getElementById('toggleCloud').addEventListener('click', () => {
    cloudEnabled = !cloudEnabled;
    cloudSoundStatus.textContent = cloudEnabled ? 'on' : 'off';
    document.getElementById('toggleCloud').textContent = cloudEnabled ? 'Cloud: on' : 'Cloud: off';
    applyCloudMute();
  });

  document.getElementById('toggleCloudVoice').addEventListener('click', () => {
    cloudVoiceEnabled = !cloudVoiceEnabled;
    cloudVoiceStatus.textContent = cloudVoiceEnabled ? 'on' : 'off';
    document.getElementById('toggleCloudVoice').textContent = cloudVoiceEnabled ? 'Cloud voice: on' : 'Cloud voice: off';
    if (cloudVoiceEnabled) cloudVoicePump();
  });

  // Test speak (existing)
  document.getElementById('testSpeak').addEventListener('click', async () => {
    const text = document.getElementById('testText').value || '';
    const q = encodeURIComponent(text.trim());
    if (!q) return;

    const res = await fetch('/api/speak?text=' + q);
    if (!res.ok) {
      alert('TTS failed: ' + (await res.text()));
      return;
    }
    const j = await res.json();
    addEvent({ time: new Date().toISOString(), symbol:'TEST', type:'speak', price:0, message:text, audio_url:j.audio_url, cache_hit:j.cache_hit });
    enqueue(j.audio_url);
  });

  // --- SSE ---
  const es = new EventSource('/events');
  es.onopen = () => setSSE('connected');
  es.onerror = () => setSSE('error / reconnecting…');
  es.onmessage = (msg) => {
    try {
      const ev = JSON.parse(msg.data);

      if (ev.type === 'cloud') {
        setCloudUI(ev);
        return;
      }
      if (ev.type === 'cloud_pulse') {
        onCloudPulse(ev);
        return;
      }

      addEvent(ev);
      if (ev.audio_url) enqueue(ev.audio_url);
    } catch(e) {}
  };

  // initial status
  setAudio('disabled');
  cloudSoundStatus.textContent = 'on';
  cloudVoiceStatus.textContent = 'on';
})();
`
