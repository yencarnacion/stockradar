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
    .pill { padding: 6px 10px; border: 1px solid #ddd; border-radius: 999px; font-size: 12px; }
    button { padding: 8px 12px; border-radius: 10px; border: 1px solid #111; background:#111; color:#fff; cursor:pointer;}
    button.secondary { background:#fff; color:#111; }
    input { padding:8px; border-radius:10px; border:1px solid #ddd; min-width: 320px; }
    #events { margin-top: 14px; border-top:1px solid #eee; padding-top: 12px; }
    .event { padding: 10px 0; border-bottom: 1px solid #f1f1f1;}
    .event .meta { color:#666; font-size: 12px; }
    .event .msg { font-size: 14px; margin-top: 2px; }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
  </style>
</head>
<body>
  <h2>Stock Radar (Option B: Browser Audio)</h2>
  <div class="row">
    <span class="pill">SSE: <span id="sseStatus" class="mono">connecting…</span></span>
    <span class="pill">Audio: <span id="audioStatus" class="mono">disabled</span></span>
    <button id="enableAudio">Enable Audio</button>
    <button id="muteAudio" class="secondary">Mute</button>
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
  const eventsEl = document.getElementById('events');

  let audioEnabled = false;
  let muted = false;
  let queue = [];
  let playing = false;

  function setSSE(text){ sseStatus.textContent = text; }
  function setAudio(text){ audioStatus.textContent = text; }

  const audio = new Audio();
  audio.addEventListener('ended', () => { playing = false; pump(); });
  audio.addEventListener('error', () => { playing = false; pump(); });

  function pump(){
    if (!audioEnabled || muted) return;
    if (playing) return;
    const next = queue.shift();
    if (!next) return;
    playing = true;
    audio.src = next;
    audio.play().catch(() => { playing = false; });
  }

  function enqueue(url){
    if (!url) return;
    queue.push(url);
    pump();
  }

  function addEvent(ev){
    const d = document.createElement('div');
    d.className = 'event';
    const ts = ev.time ? new Date(ev.time).toLocaleTimeString() : '';
    const cache = ev.cache_hit ? 'cache' : 'new';
    d.innerHTML = '<div class="meta"><span class="mono">' + ts + '</span> • <span class="mono">' + ev.symbol + '</span> • <span class="mono">' + ev.type + '</span> • <span class="mono">$' + (ev.price||0).toFixed(2) + '</span> • <span class="mono">' + cache + '</span></div>'
               + '<div class="msg">' + (ev.message || '') + '</div>';
    eventsEl.prepend(d);
  }

  // Audio controls
  document.getElementById('enableAudio').addEventListener('click', async () => {
    audioEnabled = true;
    muted = false;
    setAudio('enabled');
    try {
      audio.src = 'data:audio/mp3;base64,//uQZAAAAAAAAAAAAAAAAAAAAAA...'; // tiny invalid placeholder; play() will fail but "gesture" is captured
      await audio.play().catch(()=>{});
    } catch(e){}
  });

  document.getElementById('muteAudio').addEventListener('click', () => {
    muted = !muted;
    setAudio(muted ? 'muted' : (audioEnabled ? 'enabled' : 'disabled'));
  });

  // Test speak
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

  // SSE
  const es = new EventSource('/events');
  es.onopen = () => setSSE('connected');
  es.onerror = () => setSSE('error / reconnecting…');
  es.onmessage = (msg) => {
    try {
      const ev = JSON.parse(msg.data);
      addEvent(ev);
      if (ev.audio_url) enqueue(ev.audio_url);
    } catch(e) {}
  };

  setAudio('disabled');
})();
`


