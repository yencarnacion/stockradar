# Audible Stock Radar (Massive + OpenAI Voice TTS)

This document describes the **audible stock signals “radar” service** implemented in Go. It is intended to be used as a handoff/continuation prompt for another LLM or a competent developer—together with the source code and the `config.yaml`, `watchlist.yaml`, and `.env` files.

The service:

* Connects to the **Massive** real-time market data feed using the **official Massive Go client** (`github.com/massive-com/client-go/v2/websocket`). ([GitHub][1])
* Subscribes to real-time stock topics (notably **second aggregates** and/or **trades** depending on configuration). ([GitHub][1])
* Evaluates simple, configurable “signal rules” for a watchlist of tickers.
* Generates **spoken audio alerts** using **OpenAI Voice TTS** (MP3).
* **Caches generated MP3 files on disk** so repeated alerts do not regenerate speech.
* Runs a small HTTP server (configurable port, e.g. **8091**) providing health/status endpoints and typically a lightweight way to fetch/stream alerts and audio files.
* **Does not use Twilio** (no calls/SMS).

---

## 1) What the program accomplishes

### Primary outcome

When a watched stock exhibits a configured “signal” (for example: **price move over N% within a short window**, **volume spike**, or **large trade prints**—depending on what is enabled), the radar will:

1. Construct a short alert phrase (example: “**AGQ up one point two percent in ten seconds**”).
2. Look up the phrase in the **audio cache**:

   * If already cached, reuse the MP3 immediately.
   * If not cached, call OpenAI TTS to generate an MP3, write it to disk, then reuse it.
3. Publish the alert in real time (logs + HTTP API + whatever client/UI you run to play the MP3).

### Why it’s useful

* You can keep a browser tab or local client pointed at the radar and **hear market movement** without watching charts continuously.
* Caching ensures repeated/overlapping alert phrases do not hammer the TTS API.

---

## 2) High-level architecture

### Components

1. **Config loader**

   * Loads `config.yaml`.
   * Loads `watchlist.yaml`.
   * Loads secrets from `.env` into environment variables.
   * Applies defaults (port, thresholds, cache directory, etc.).

2. **Massive WebSocket ingestion**

   * Creates a Massive websocket client using `massivews.New(massivews.Config{ ... })`.
   * Connects via `c.Connect()`.
   * Subscribes to stock topics:

     * Example from Massive docs: `StocksSecAggs` and `StocksTrades`. ([GitHub][1])
   * Reads messages from `c.Output()` and errors from `c.Error()`. ([GitHub][1])

3. **Symbol state + signal engine**

   * Maintains per-symbol rolling state (last price, rolling window, rolling volume, last alert time).
   * Evaluates configured triggers.
   * Implements **dedup/cooldown** logic to avoid spam.

4. **Alert bus**

   * A channel or pub/sub fanout to:

     * Log alerts
     * Store recent alerts in memory (for HTTP endpoints)
     * Notify any connected HTTP streaming clients (SSE / WebSocket / polling depending on implementation)

5. **TTS service (OpenAI)**

   * Produces MP3 bytes for a given alert phrase (voice/model are configurable).
   * Uses `.env` secrets for authentication.

6. **Sound cache**

   * Maps `(voice, model, normalized_text)` → stable filename (usually via hash).
   * Stores MP3 files under a configured directory (example: `./sounds-cache/`).
   * Uses concurrency control so **only one** generation occurs per phrase even if multiple alerts request it at once.

7. **HTTP server**

   * Runs on configurable port (example `:8091`).
   * Exposes basic operational endpoints and access to cached MP3s.
   * Often also exposes a minimal dashboard or streaming endpoint so a browser can play audio.

---

## 3) Key assumptions

### Market feed assumptions

* The service uses the Massive websocket client described in the Massive `client-go` README:

  * Import paths:

    * `github.com/massive-com/client-go/v2/websocket`
    * `github.com/massive-com/client-go/v2/websocket/models` ([GitHub][1])
  * Create client with config including `APIKey`, `Feed: massivews.RealTime`, `Market: massivews.Stocks`. ([GitHub][1])
  * Subscribe with:

    * `c.Subscribe(massivews.StocksSecAggs)` (optionally with tickers)
    * `c.Subscribe(massivews.StocksTrades, "TSLA", "GME")` style usage ([GitHub][1])
  * Receive messages via a single output channel and type-switch them (e.g., `models.EquityAgg`, `models.EquityTrade`). ([GitHub][1])

### Audio playback assumptions

* The radar **generates and serves** MP3 audio.
* Actual “playing” can be done by:

  * A browser UI that fetches the MP3 and plays it (typical, cross-platform), or
  * A local player command (optional) if your code includes that mode.
* If a browser is used, you may need a user gesture to allow autoplay (browser security model).

### Secrets and configuration assumptions

* `.env` exists and contains at least:

  * `MASSIVE_API_KEY=...`
  * `OPENAI_API_KEY=...`
* YAML files do **not** contain secrets.

---

## 4) Files and configuration

### 4.1 `.env`

Purpose: store secrets outside source control.

Expected variables (typical):

* `MASSIVE_API_KEY` — used for Massive websocket authentication
* `OPENAI_API_KEY` — used for OpenAI TTS

Optional variables commonly supported in similar setups:

* `OPENAI_BASE_URL` — if using a proxy/gateway
* `LOG_LEVEL` — if your logger supports it

**Do not commit `.env`.**

---

### 4.2 `config.yaml`

Purpose: runtime behavior and thresholds.

Typical fields (names depend on the code you have, but conceptually):

* `server.port`: port number (default 8091)
* `server.bind`: bind address (default `0.0.0.0` or `127.0.0.1`)
* `massive.feed`: `realtime` (maps to `massivews.RealTime`)
* `massive.market`: `stocks` (maps to `massivews.Stocks`)
* `massive.topics`: which topics to subscribe to (sec aggs, trades)
* `signals.*`: thresholds and windows

  * example conceptual knobs:

    * `move_window_seconds`
    * `move_pct_threshold`
    * `volume_spike_threshold`
    * `cooldown_seconds`
* `tts.model`: e.g. `tts-1-hd` (or whatever your code uses)
* `tts.voice`: e.g. `nova`
* `audio.cache_dir`: e.g. `./sounds-cache`
* `audio.public_path`: URL path prefix for serving cached audio (e.g. `/sounds/`)
* `alerts.max_recent`: number of recent alerts to keep in memory (for API)

Even if your current code doesn’t implement all of the above, those are the standard growth points for a continuation LLM/dev.

---

### 4.3 `watchlist.yaml`

Purpose: define the symbols to track, and (optionally) symbol-specific overrides.

Your service should accept a minimal entry like:

```yaml
watchlist:
  - symbol: "AGQ"
  - symbol: "SLV"
```

Optionally, a symbol entry may include per-symbol overrides (if supported), such as:

* `enabled: true/false`
* `cooldown_seconds`
* `move_pct_threshold`
* `min_volume`
* `speak_name` / `alias`

**Design intent:** global defaults come from `config.yaml`, while `watchlist.yaml` can override per symbol.

---

## 5) Data flow in detail

### 5.1 Startup sequence

1. Load `.env` → environment variables.
2. Load `config.yaml` → config struct.
3. Load `watchlist.yaml` → list of symbols + any overrides.
4. Ensure audio cache directory exists (create if missing).
5. Start HTTP server (port from config).
6. Create Massive websocket client:

   * `massivews.New(massivews.Config{ APIKey: os.Getenv("MASSIVE_API_KEY"), Feed: massivews.RealTime, Market: massivews.Stocks })` ([GitHub][1])
7. Connect and subscribe to topics for the watchlist.
8. Enter the main event loop:

   * Receive market data
   * Update per-symbol state
   * Evaluate signals
   * Emit alerts
   * Generate/serve cached audio

### 5.2 Handling a market message

When a message arrives from `c.Output()`:

* Type-switch to identify:

  * `models.EquityAgg` (second aggregates)
  * `models.EquityTrade` (trade prints) ([GitHub][1])
* Extract:

  * `symbol`
  * price (last/close/whatever field your code uses)
  * volume (if present)
  * timestamp
* Update per-symbol rolling state.

### 5.3 Signal evaluation (conceptual)

Common real-time “radar” signals:

* **Fast move**: percent change over a short window exceeds threshold
* **Volume spike**: window volume exceeds multiplier over baseline
* **Large prints**: a trade with size/value above threshold

Typical implementation pattern:

* Maintain a rolling buffer of recent points (ring buffer) per symbol:

  * store `(timestamp, price, volume_increment)`
* Compute:

  * oldest point within window → calculate percent move
  * sum volume within window
* Trigger if thresholds met AND cooldown not active.

### 5.4 Alert deduplication / cooldown

To avoid repeating the same message constantly:

* Keep `lastAlertAt` per symbol (and sometimes per-signal-type).
* Only allow a new alert if:

  * `now - lastAlertAt >= cooldown`
* Optionally, allow an “escalation” rule: if move grows significantly (e.g. doubles), allow a new alert sooner.

---

## 6) Audio generation and caching

### 6.1 Cache key

The cache key should uniquely represent the audio content:

* `tts_model + tts_voice + normalized_text`

**Normalization** usually includes:

* trim spaces
* collapse multiple whitespace
* optionally lowercasing (but be careful with tickers like “SLV”)

The file name is typically:

* `sha256(key).mp3` (stable, filesystem-safe)

### 6.2 Cache behavior

On alert:

1. Compute cache key.
2. If `cache_dir/<hash>.mp3` exists:

   * return it immediately
3. Else:

   * call OpenAI TTS
   * write bytes to `cache_dir/<hash>.mp3`
   * return it

### 6.3 Concurrency control

If alerts come quickly, multiple goroutines can request the same phrase.
Use one of:

* `singleflight.Group`
* a per-key mutex map
* an in-flight map with channels

Goal: at most **one** TTS API call per unique phrase at a time.

### 6.4 Persistence across sessions

Because files are written to disk, restarting the service still has the cached MP3s available immediately.

---

## 7) HTTP server responsibilities

Even if your first version is “minimal,” the HTTP server exists to make the system observable and usable.

Common endpoints:

### 7.1 Health and readiness

* `GET /healthz`

  * returns `200 OK` if process is alive
* `GET /readyz`

  * returns `200 OK` only if:

    * Massive websocket is connected/authenticated
    * watchlist loaded

### 7.2 Watchlist and config introspection

* `GET /api/watchlist`

  * returns the currently loaded symbols
* `GET /api/config`

  * returns safe config (no secrets)

### 7.3 Alerts feed

* `GET /api/alerts`

  * returns recent alerts (in-memory ring)
* `GET /api/alerts/stream`

  * server-sent events (SSE) stream or websocket stream:

    * sends JSON describing alerts as they occur:

      * symbol
      * signal type
      * message text
      * timestamp
      * audio URL/path

### 7.4 Serving cached audio

* `GET /sounds/<filename>.mp3`

  * serves the cached MP3 from disk
  * sets `Content-Type: audio/mpeg`
  * enables caching headers if desired

### 7.5 Optional “dashboard”

* `GET /`

  * a tiny HTML page that:

    * connects to `/api/alerts/stream`
    * plays audio when alerts arrive

This browser approach avoids OS-specific audio playback code and is the simplest way to “hear” alerts immediately.

---

## 8) Operational notes

### Market hours / no data

If the market is closed or the feed is quiet, you may see:

* successful connection but no messages
* very low alert frequency

### Autoplay restrictions

If you use a browser UI:

* Most browsers require a click/gesture to enable sound.
* Provide a button like “Enable Audio” that the user must click once.

### Logging

Recommended logs:

* startup config summary (safe fields only)
* massive connect/subscribe events
* signal triggers (include thresholds and computed values)
* cache hits/misses
* TTS generation latency/errors

---

## 9) Development and extension guide

### Where to add new signals

Add a new rule in the signal engine:

* define inputs needed (trade size, rolling vwap, etc.)
* add state to per-symbol context if needed
* implement trigger + cooldown semantics
* create alert message text
* emit alert to bus

### Per-symbol overrides

If you want per-symbol thresholds:

* extend watchlist entry struct:

  * `MovePctThreshold *float64`
  * `CooldownSeconds *int`
* in evaluation, use:

  * per-symbol override if not nil else global config default

### Persisting alerts

To persist alerts across restarts:

* write alert events to a file (JSONL)
* or store to sqlite/postgres
* keep HTTP API returning recent alerts from storage + memory

### Reducing TTS cost

* Keep messages short and “templated” so cache hits are high.
* Example: standardized phrase formats:

  * “{SYMBOL} up {PCT} percent”
  * “{SYMBOL} down {PCT} percent”
* Avoid embedding rapidly changing numbers unless you really need them (numbers reduce cache reuse).

---

## 10) Reference: Massive websocket client behavior (important)

Massive’s Go websocket client (as documented in the repository README) provides:

* `New(Config{APIKey, Feed, Market})`
* `Connect()`
* `Subscribe(topic, tickers...)`
* `Output()` channel for data events
* `Error()` channel for fatal errors
* automatic reconnect/resubscribe behavior ([GitHub][1])

These behaviors strongly influence how the radar should be structured: a single read loop over `Output()` plus a separate `Error()` select-case is the stable pattern. ([GitHub][1])

---

## 11) What to provide to another LLM for continuation

When handing off, include:

1. The full Go source tree.
2. `config.yaml` (with safe values).
3. `watchlist.yaml`.
4. This documentation.
5. A short “goal list,” e.g.:

   * add new signals
   * add per-symbol overrides
   * add a prettier dashboard
   * add backtesting mode from historical data

A good prompt to another LLM is:

> “Here is the code and README. Extend the signal engine to add `X`, ensure audio caching remains stable, add an endpoint `/api/stats`, and keep the Massive websocket client usage pattern intact.”

---

If you paste your current `config.yaml` and the Go file layout you’re using (even just the filenames), I can tailor this documentation to match your exact struct field names and endpoints precisely—without changing your design goals.

[1]: https://github.com/massive-com/client-go "GitHub - massive-com/client-go: The official Go client library for the Massive REST and WebSocket API."
