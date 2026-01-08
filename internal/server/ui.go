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

    <span class="pill">Net voice: <span id="cloudVoiceStatus" class="mono">on</span></span>
    <button id="toggleCloudVoice" class="secondary">Net voice: on</button>
  </div>

  <div class="cloudBox flat" id="cloudBox">
    <div class="cloudTitle">
      <div>
        <div class="cloudBig" id="cloudHeadline">Cloud: —</div>
        <div class="cloudSmall mono" id="cloudDetails">waiting for ticks…</div>
      </div>
      <div style="display:flex; flex-direction:column; align-items:flex-end; gap:6px;">
        <div class="cloudSmall mono" id="roll60Counts">60s ticks: up 0 • down 0 • net +0</div>
        <div class="barWrap" aria-label="cloud strength">
          <div class="bar" id="cloudBar"></div>
        </div>
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
  const roll60Counts = document.getElementById('roll60Counts');

  const eventsEl = document.getElementById('events');

  let audioEnabled = false;
  let muted = false;

  // --- MIX KNOBS ---
  // Geiger (cloud clicks) overall loudness (0..1 typical)
  const CLOUD_CLICK_MASTER_GAIN = 0.22;

  // Cloud up/down voice amplification (WebAudio gain; can be > 1.0)
  const CLOUD_VOICE_GAIN = 2.2;

  function setSSE(text){ sseStatus.textContent = text; }
  function setAudio(text){ audioStatus.textContent = text; }

  // --- Rolling 60s UP vs DOWN counts (based on cloud_pulse events) ---
  const ROLL60_MS = 60 * 1000;

  // Store last 60s of pulses without O(n) shift() costs
  const pulseRoll = []; // { t:number, dir:'up'|'down' }
  let pulseHead = 0;
  let rollUp = 0;
  let rollDown = 0;

  function prunePulseRoll(nowMs){
    const cutoff = nowMs - ROLL60_MS;

    while (pulseHead < pulseRoll.length && pulseRoll[pulseHead].t < cutoff) {
      const old = pulseRoll[pulseHead];
      if (old.dir === 'up') rollUp--;
      else if (old.dir === 'down') rollDown--;
      pulseHead++;
    }

    // occasional compaction
    if (pulseHead > 2000 && pulseHead > (pulseRoll.length >> 1)) {
      pulseRoll.splice(0, pulseHead);
      pulseHead = 0;
    }

    if (rollUp < 0) rollUp = 0;
    if (rollDown < 0) rollDown = 0;
  }

  function renderPulseRoll(){
    if (!roll60Counts) return;
    const now = Date.now();
    prunePulseRoll(now);
    const net = rollUp - rollDown;
    roll60Counts.textContent =
      '60s ticks: up ' + rollUp +
      ' • down ' + rollDown +
      ' • net ' + (net >= 0 ? '+' : '') + net;

    // Speak net bucket only when the 20-range changes
    const bucket = netBucket(net);
    requestSpeakNetBucket(bucket, false);
  }

  function recordPulseRoll(ev){
    if (!ev) return;
    const dir = ev.direction || 'flat';
    if (dir !== 'up' && dir !== 'down') return;

    const now = Date.now();
    pulseRoll.push({ t: now, dir: dir });
    if (dir === 'up') rollUp++;
    else rollDown++;

    // prune + update immediately so it feels "live"
    prunePulseRoll(now);
    renderPulseRoll();
  }

  // Keep it rolling even when no new pulses arrive (so old ones age out)
  setInterval(renderPulseRoll, 500);

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

  // --- Cloud voice: "tape metronome" mode ---
  // Goals:
  // 1) Continuous cadence while UP/DOWN (speed ~ strength/breadth)
  // 2) Never lag behind reality (queue <= 1)
  // 3) Interrupt immediately on direction flips

  let cloudVoiceQueue = [];
  let cloudVoicePlaying = false;

  const cloudVoiceAudio = new Audio();
  cloudVoiceAudio.preload = 'auto';
  cloudVoiceAudio.volume = 1.0;

  cloudVoiceAudio.addEventListener('ended', () => {
    cloudVoicePlaying = false;
    cloudVoicePump();
  });

  cloudVoiceAudio.addEventListener('error', () => {
    cloudVoicePlaying = false;
    cloudVoicePump();
  });

  // Route cloudVoiceAudio into WebAudio so we can boost volume above 1.0
  let cloudVoiceMediaNode = null;
  let cloudVoiceGainNode = null;

  function ensureCloudVoiceRouting(){
    // Must have audioCtx + voice graph
    if (!ensureVoiceGraph()) return false;

    // Only create ONE MediaElementSource per HTMLMediaElement (browser rule)
    if (!cloudVoiceMediaNode) {
      cloudVoiceMediaNode = audioCtx.createMediaElementSource(cloudVoiceAudio);

      cloudVoiceGainNode = audioCtx.createGain();
      cloudVoiceGainNode.gain.value = CLOUD_VOICE_GAIN;

      // Cloud voice -> gain -> voiceBus (then compressor + master)
      cloudVoiceMediaNode.connect(cloudVoiceGainNode);
      cloudVoiceGainNode.connect(voiceBus);
    }
    return true;
  }

  function stopCloudVoiceNow(){
    try {
      cloudVoiceAudio.pause();
      cloudVoiceAudio.currentTime = 0;
    } catch(e) {}
    cloudVoicePlaying = false;
    cloudVoiceQueue = [];
  }

  function cloudVoicePump(){
    if (!audioEnabled || muted) return;
    if (!cloudVoiceEnabled) return;

    ensureAudioCtx();
    if (!ensureCloudVoiceRouting()) return;

    if (cloudVoicePlaying) return;

    const next = cloudVoiceQueue.shift();
    if (!next) return;

    cloudVoicePlaying = true;

    // Let WebAudio do the amplification; keep element volume at max
    cloudVoiceAudio.volume = 1.0;

    cloudVoiceAudio.playbackRate = 1.0;

    cloudVoiceAudio.src = next;
    cloudVoiceAudio.play().catch(() => {
      cloudVoicePlaying = false;
      cloudVoicePump();
    });
  }

  function cloudVoiceEnqueue(url, priority){
    if (!url) return;
    if (!audioEnabled || muted) return;
    if (!cloudVoiceEnabled) return;

    if (priority) {
      // Direction flips should be felt NOW
      stopCloudVoiceNow();
      cloudVoiceQueue = [url];
    } else {
      // Never build latency. Keep only ONE pending item (the freshest).
      if (cloudVoiceQueue.length === 0) cloudVoiceQueue.push(url);
      else cloudVoiceQueue[0] = url;
    }

    cloudVoicePump();
  }

  // --- Cloud “geiger” sound ---
  let cloudEnabled = true;
  let cloudVoiceEnabled = true;

  let audioCtx = null;

  let cloudDir = 'flat';
  let cloudStrength = 0.0;
  let cloudRateHz = 0.0; // kept for display only (no longer drives audio timing)

  // cue URLs fetched from server (/api/cues) as a key->url map
  // Keys used: flat, plus_<N>, minus_<N>
  let cues = {};

  // Net voice state (only speak when bucket changes)
  let lastNetBucketSpoken = null;
  let wantedNetBucket = null;
  let netSpeakInflight = false;

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
    if (cloudMaster) cloudMaster.gain.value = (muted || !cloudEnabled) ? 0.0 : CLOUD_CLICK_MASTER_GAIN;
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
      cloudMaster.gain.value = (muted || !cloudEnabled) ? 0.0 : CLOUD_CLICK_MASTER_GAIN;

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
      cues = m || {};

      // Pull bucket settings from server config
      const step = (j && j.net_bucket_step) ? parseInt(j.net_bucket_step, 10) : 0;
      const flat = (j && j.net_bucket_flat) ? parseInt(j.net_bucket_flat, 10) : 0;
      if (step > 0) NET_BUCKET_STEP = step;
      if (flat > 0) NET_BUCKET_FLAT = flat;

      // If config changed, force next net announcement to re-speak
      lastNetBucketSpoken = null;
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

  // --- Net bucket speech (configurable) ---
  // Defaults; overridden by /api/cues:
  //   step=20, flat=20
  //
  // Example: step=30, flat=40
  //   +40..+69 => +40
  //   +70..+99 => +70
  //   +100..+129 => +100
  //
  let NET_BUCKET_STEP = 20;
  let NET_BUCKET_FLAT = 20;
  const NET_BUCKET_MAX = 1000;
  const NET_BUCKET_ABOVE_MAX = NET_BUCKET_MAX + 1;     // sentinel
  const NET_BUCKET_BELOW_MAX = -(NET_BUCKET_MAX + 1);  // sentinel

  function netBucket(net){
    // flat between (-flat, +flat)
    if (net > -NET_BUCKET_FLAT && net < NET_BUCKET_FLAT) return 0;

    // cap beyond +/-1000
    if (net > NET_BUCKET_MAX) return NET_BUCKET_ABOVE_MAX;
    if (net < -NET_BUCKET_MAX) return NET_BUCKET_BELOW_MAX;

    const step = Math.max(1, Math.abs((NET_BUCKET_STEP|0) || 20));
    const base = Math.max(1, Math.abs((NET_BUCKET_FLAT|0) || 20));

    // Buckets anchored at +/-base:
    //   base..(base+step-1) => base
    //   (base+step)..(base+2*step-1) => base+step
    if (net > 0) return base + Math.floor((net - base) / step) * step;
    return -(base + Math.floor((Math.abs(net) - base) / step) * step);
  }

  function netBucketKey(bucket){
    if (bucket === 0) return 'flat';
    if (bucket === NET_BUCKET_ABOVE_MAX) return 'above_1000';
    if (bucket === NET_BUCKET_BELOW_MAX) return 'below_1000';
    const mag = Math.abs(bucket);
    return (bucket > 0 ? 'plus_' : 'minus_') + mag;
  }

  function numberToWords(n){
    n = Math.floor(Math.abs(n));
    if (n === 0) return 'zero';

    const ones = ['zero','one','two','three','four','five','six','seven','eight','nine'];
    const teens = ['ten','eleven','twelve','thirteen','fourteen','fifteen','sixteen','seventeen','eighteen','nineteen'];
    const tens = ['zero','ten','twenty','thirty','forty','fifty','sixty','seventy','eighty','ninety'];

    function under100(x){
      if (x < 10) return ones[x];
      if (x < 20) return teens[x - 10];
      const t = Math.floor(x / 10);
      const r = x % 10;
      if (r === 0) return tens[t];
      return tens[t] + ' ' + ones[r];
    }

    function under1000(x){
      if (x < 100) return under100(x);
      const h = Math.floor(x / 100);
      const r = x % 100;
      if (r === 0) return ones[h] + ' hundred';
      return ones[h] + ' hundred ' + under100(r);
    }

    if (n < 1000) return under1000(n);

    const th = Math.floor(n / 1000);
    const r = n % 1000;
    const head = (th < 1000) ? under1000(th) : ('' + th);
    if (r === 0) return head + ' thousand';
    return head + ' thousand ' + under1000(r);
  }

  function netBucketPhrase(bucket){
    if (bucket === 0) return 'flat';
    if (bucket === NET_BUCKET_ABOVE_MAX) return 'above one thousand';
    if (bucket === NET_BUCKET_BELOW_MAX) return 'below one thousand';
    const mag = Math.abs(bucket);
    const w = numberToWords(mag);
    return (bucket > 0 ? 'plus ' : 'minus ') + w;
  }

  async function urlForNetBucket(bucket){
    const k = netBucketKey(bucket);
    if (cues && cues[k]) return cues[k];
    return await fallbackSpeak(netBucketPhrase(bucket));
  }

  function requestSpeakNetBucket(bucket, force){
    if (!cloudVoiceEnabled) return;
    if (!audioEnabled || muted) return;

    if (!force && bucket === lastNetBucketSpoken) return;

    wantedNetBucket = bucket;
    if (netSpeakInflight) return;

    netSpeakInflight = true;
    (async () => {
      while (wantedNetBucket !== null && wantedNetBucket !== undefined) {
        const b = wantedNetBucket;
        wantedNetBucket = null;

        const url = await urlForNetBucket(b);
        if (url) {
          cloudVoiceEnqueue(url, true);
          lastNetBucketSpoken = b;
        }
      }
    })().catch(()=>{}).finally(() => {
      netSpeakInflight = false;
    });
  }

  function currentNet(){
    const now = Date.now();
    prunePulseRoll(now);
    return rollUp - rollDown;
  }

  async function speakNetOnceNow(){
    const net = currentNet();
    const bucket = netBucket(net);
    requestSpeakNetBucket(bucket, true);
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

    // Speak current 60s net bucket immediately (flat / plus twenty / minus twenty / ...)
    await speakNetOnceNow();
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
    document.getElementById('toggleCloudVoice').textContent = cloudVoiceEnabled ? 'Net voice: on' : 'Net voice: off';
    if (!cloudVoiceEnabled) {
      stopCloudVoiceNow();
      lastNetBucketSpoken = null;
      wantedNetBucket = null;
    } else {
      speakNetOnceNow();
    }
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
        recordPulseRoll(ev);
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
