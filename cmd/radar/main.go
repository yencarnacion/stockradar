package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	massivews "github.com/massive-com/client-go/v2/websocket"
	wsmodels "github.com/massive-com/client-go/v2/websocket/models"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"stockradar/internal/config"
	"stockradar/internal/radar"
	"stockradar/internal/server"
	"stockradar/internal/tts"
	"stockradar/internal/watchlist"
)

type Tick struct {
	Symbol string
	Price  float64
	Volume float64
	Time   time.Time
}

func main() {
	var cfgPath string
	var wlPath string

	flag.StringVar(&cfgPath, "config", "config.yaml", "Path to config YAML")
	flag.StringVar(&wlPath, "watchlist", "watchlist.yaml", "Path to watchlist YAML")
	flag.Parse()

	_ = godotenv.Load()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Logging
	zerolog.TimeFieldFormat = time.RFC3339Nano
	level, err := zerolog.ParseLevel(cfg.Radar.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	log.Logger = logger

	wl, err := watchlist.Load(wlPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load watchlist")
	}
	tickers := wl.Tickers()
	if len(tickers) == 0 {
		log.Fatal().Msg("watchlist has zero symbols; add symbols to watchlist.yaml")
	}

	// Secrets from env
	massiveKey := strings.TrimSpace(os.Getenv(cfg.Massive.APIKeyEnv))
	if massiveKey == "" {
		log.Fatal().Str("env", cfg.Massive.APIKeyEnv).Msg("missing Massive API key env var")
	}
	openAIKey := strings.TrimSpace(os.Getenv(cfg.OpenAI.APIKeyEnv))
	if openAIKey == "" {
		log.Fatal().Str("env", cfg.OpenAI.APIKeyEnv).Msg("missing OpenAI API key env var")
	}

	// Context / shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// TTS client (with persistent cache)
	ttsClient, err := tts.NewClient(tts.Config{
		APIKey:         openAIKey,
		BaseURL:        cfg.OpenAI.BaseURL,
		Model:          cfg.OpenAI.Model,
		Voice:          cfg.OpenAI.Voice,
		ResponseFormat: cfg.OpenAI.ResponseFormat,
		Speed:          cfg.OpenAI.Speed,
		Timeout:        cfg.OpenAI.Timeout.ToDuration(),
		CacheDir:       cfg.Cache.AudioDir,
		MaxTextChars:   cfg.OpenAI.MaxTextChars,
	}, log.Logger)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init TTS client")
	}

	// Web server (Option B)
	srv := server.New(server.Config{
		Bind:              cfg.Server.Bind,
		Port:              cfg.Server.Port,
		AudioDir:          cfg.Cache.AudioDir,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.ToDuration(),
	}, ttsClient, log.Logger)

	// ---- NEW: Pre-generate the cloud cue audio ON STARTUP (only if missing) ----
	// This ensures "up / down / flat" (and strong variants) are already in ./cache/audio.
	{
		cueTexts := map[string]string{
			"up":         "up",
			"upStrong":   "UP!",
			"down":       "down",
			"downStrong": "DOWN!",
			"flat":       "flat",
		}

		cues := make(map[string]string, len(cueTexts))
		timeout := cfg.OpenAI.Timeout.ToDuration()
		if timeout <= 0 {
			timeout = 30 * time.Second
		}

		for key, phrase := range cueTexts {
			cctx, ccancel := context.WithTimeout(ctx, timeout)
			res, err := ttsClient.SpeakToFile(cctx, phrase)
			ccancel()

			if err != nil {
				log.Error().Err(err).Str("cue", key).Str("text", phrase).Msg("failed to pre-generate cue")
				continue
			}
			cues[key] = "/audio/" + filepath.Base(res.Path)
		}

		srv.SetCues(cues)
	}

	go func() {
		if err := srv.Start(ctx); err != nil {
			log.Error().Err(err).Msg("http server stopped with error")
			cancel()
		}
	}()

	// Radar engine (per-symbol alerts)
	engine := radar.NewEngine(radar.Config{
		GlobalCooldown: cfg.Radar.GlobalCooldown.ToDuration(),
		HistoryWindow:  cfg.Radar.HistoryWindow.ToDuration(),
	}, wl, log.Logger)

	// Cloud engine (watchlist-wide “geiger” signal)
	cloud := radar.NewCloudEngine(radar.CloudConfig{
		Enabled:       cfg.Cloud.Enabled,
		EmitEvery:     cfg.Cloud.EmitEvery.ToDuration(),
		StaleAfter:    cfg.Cloud.StaleAfter.ToDuration(),
		DeadbandPct:   cfg.Cloud.DeadbandPct,
		CapMovePct:    cfg.Cloud.CapMovePct,
		StrengthPct:   cfg.Cloud.StrengthPct,
		Smoothing:     cfg.Cloud.Smoothing,
		MinRateHz:     cfg.Cloud.MinRateHz,
		MaxRateHz:     cfg.Cloud.MaxRateHz,
		BreadthWeight: cfg.Cloud.BreadthWeight,
	}, wl, log.Logger)

	// Periodically publish cloud state (UI drives continuous sound based on latest state)
	if cfg.Cloud.Enabled {
		emitEvery := cfg.Cloud.EmitEvery.ToDuration()
		if emitEvery <= 0 {
			emitEvery = 200 * time.Millisecond
		}
		go func() {
			tk := time.NewTicker(emitEvery)
			defer tk.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
					snap := cloud.Snapshot(time.Now())
					srv.Broadcast(server.Event{
						Time:      snap.Time,
						Symbol:    "CLOUD",
						Price:     0,
						Type:      "cloud",
						Message:   snap.Message,
						Direction: snap.Direction,
						Strength:  snap.Strength,
						Score:     snap.ScorePct,
						Adv:       snap.Adv,
						Dec:       snap.Dec,
						Flat:      snap.Flat,
						Active:    snap.Active,
						Total:     snap.Total,
						RateHz:    snap.RateHz,
					})
				}
			}
		}()
	}

	alertCh := make(chan radar.Alert, 1024)

	// Alert workers: generate / cache audio then broadcast to UI
	for i := 0; i < cfg.Radar.AlertWorkers; i++ {
		go func(workerID int) {
			for {
				select {
				case <-ctx.Done():
					return
				case a := <-alertCh:
					ev := server.Event{
						Time:      time.Now(),
						Symbol:    a.Symbol,
						Price:     a.Price,
						Type:      string(a.Type),
						Message:   a.Message,
						Direction: directionFromAlertType(a.Type),
					}

					// Generate (or reuse cached) MP3
					res, err := ttsClient.SpeakToFile(ctx, a.SpeakText)
					if err != nil {
						log.Error().
							Err(err).
							Str("symbol", a.Symbol).
							Str("type", string(a.Type)).
							Msg("tts failed; broadcasting alert without audio")
					} else {
						ev.AudioURL = "/audio/" + filepath.Base(res.Path)
						ev.CacheHit = res.CacheHit
					}

					srv.Broadcast(ev)
				}
			}
		}(i)
	}

	// Massive WS client
	feedConst := parseMassiveFeed(cfg.Massive.Feed)
	marketConst := parseMassiveMarket(cfg.Massive.Market)

	ws, err := massivews.New(massivews.Config{
		APIKey: massiveKey,
		Feed:   feedConst,
		Market: marketConst,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Massive websocket client")
	}
	defer ws.Close()

	if err := ws.Connect(); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to Massive websocket")
	}

	// Subscribe to 1-second aggregates for watchlist tickers
	if err := ws.Subscribe(massivews.StocksSecAggs, tickers...); err != nil {
		log.Fatal().Err(err).Msg("failed to subscribe to Massive topic stocks sec aggs")
	}

	log.Info().
		Int("symbols", len(tickers)).
		Str("addr", srv.Addr()).
		Msg("running. Open the UI in your browser and click Enable Audio")

	// Read stream
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("shutting down")
			return

		case err := <-ws.Error():
			// Fatal errors (auth, etc.)
			log.Error().Err(err).Msg("Massive websocket fatal error")
			cancel()

		case out, more := <-ws.Output():
			if !more {
				log.Warn().Msg("Massive websocket output channel closed")
				cancel()
				continue
			}

			switch msg := out.(type) {
			case wsmodels.EquityAgg:
				t, ok := tickFromAny(msg)
				if !ok {
					continue
				}

				// Update cloud (watchlist-wide signal)
				cloud.Update(t.Symbol, t.Price, t.Time)

				// Per-symbol alert engine
				alerts := engine.Update(t.Symbol, t.Price, t.Volume, t.Time)
				for _, a := range alerts {
					select {
					case alertCh <- a:
					default:
						log.Warn().Msg("alert channel full; dropping alert")
					}
				}

			case *wsmodels.EquityAgg:
				t, ok := tickFromAny(msg)
				if !ok {
					continue
				}

				cloud.Update(t.Symbol, t.Price, t.Time)

				alerts := engine.Update(t.Symbol, t.Price, t.Volume, t.Time)
				for _, a := range alerts {
					select {
					case alertCh <- a:
					default:
						log.Warn().Msg("alert channel full; dropping alert")
					}
				}

			default:
				// ignore other message types
			}
		}
	}
}

func directionFromAlertType(t radar.AlertType) string {
	s := strings.ToLower(strings.TrimSpace(string(t)))
	switch {
	case strings.Contains(s, "down") || strings.Contains(s, "below"):
		return "down"
	case strings.Contains(s, "up") || strings.Contains(s, "above"):
		return "up"
	default:
		return ""
	}
}

func parseMassiveFeed(s string) massivews.Feed {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "realtime", "real_time", "real-time":
		return massivews.RealTime
	case "delayed":
		return massivews.Delayed
	default:
		return massivews.RealTime
	}
}

func parseMassiveMarket(s string) massivews.Market {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "stocks", "equities":
		return massivews.Stocks
	case "crypto":
		return massivews.Crypto
	case "forex":
		return massivews.Forex
	case "options":
		return massivews.Options
	default:
		return massivews.Stocks
	}
}

// tickFromAny intentionally avoids relying on specific struct fields.
// It marshals to JSON then pulls common keys (sym/ticker, close/c, volume/v, timestamp/t/e).
func tickFromAny(v any) (Tick, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return Tick{}, false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return Tick{}, false
	}

	sym := pickString(m, "sym", "Sym", "symbol", "Symbol", "ticker", "Ticker", "T")
	price := pickFloat(m, "c", "C", "close", "Close", "price", "Price", "p", "P")
	vol := pickFloat(m, "v", "V", "volume", "Volume")

	// timestamps often in ms
	tsms := pickInt64(m, "e", "E", "end", "End", "t", "T", "timestamp", "Timestamp")
	ts := time.Now()
	if tsms > 0 {
		// if it's seconds (10 digits) convert; if ms (13 digits) use milli
		if tsms < 1_000_000_000_000 {
			ts = time.Unix(tsms, 0)
		} else {
			ts = time.UnixMilli(tsms)
		}
	}

	sym = strings.ToUpper(strings.TrimSpace(sym))
	if sym == "" || price <= 0 {
		return Tick{}, false
	}
	return Tick{Symbol: sym, Price: price, Volume: vol, Time: ts}, true
}

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch vv := v.(type) {
			case string:
				return vv
			}
		}
	}
	return ""
}

func pickFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch vv := v.(type) {
			case float64:
				return vv
			case float32:
				return float64(vv)
			case int:
				return float64(vv)
			case int64:
				return float64(vv)
			case json.Number:
				f, _ := vv.Float64()
				return f
			}
		}
	}
	return 0
}

func pickInt64(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch vv := v.(type) {
			case int64:
				return vv
			case int:
				return int64(vv)
			case float64:
				return int64(vv)
			case json.Number:
				i, _ := vv.Int64()
				return i
			}
		}
	}
	return 0
}
