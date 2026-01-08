package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"stockradar/internal/tts"
)

type Config struct {
	Bind              string
	Port              int
	AudioDir          string
	ReadHeaderTimeout time.Duration

	// Net-voice bucket settings (sent to the UI via /api/cues)
	NetBucketStep int
	NetBucketFlat int
}

type Event struct {
	Time     time.Time `json:"time"`
	Symbol   string    `json:"symbol"`
	Price    float64   `json:"price"`
	Volume   float64   `json:"volume,omitempty"`
	Type     string    `json:"type"`
	Message  string    `json:"message"`
	AudioURL string    `json:"audio_url,omitempty"`
	CacheHit bool      `json:"cache_hit,omitempty"`

	// Optional direction/intensity metadata (cloud + alert coloring)
	Direction string  `json:"direction,omitempty"` // up | down | flat
	Strength  float64 `json:"strength,omitempty"`  // 0..1 (cloud)
	Score     float64 `json:"score,omitempty"`     // % (cloud)
	Adv       int     `json:"adv,omitempty"`
	Dec       int     `json:"dec,omitempty"`
	Flat      int     `json:"flat,omitempty"`
	Active    int     `json:"active,omitempty"`
	Total     int     `json:"total,omitempty"`
	RateHz    float64 `json:"rate_hz,omitempty"` // cloud suggested tick rate

	// For pulse/debug
	DeltaPct float64 `json:"delta_pct,omitempty"`
}

type Server struct {
	cfg Config
	tts *tts.Client
	log zerolog.Logger

	mu      sync.Mutex
	clients map[chan []byte]struct{}
	history []Event

	// High-frequency “cloud” events are not stored in history; only keep latest.
	hasCloud bool
	cloud    Event

	// Precomputed short cue audio URLs (up/down/flat/etc)
	cues map[string]string
}

func New(cfg Config, ttsClient *tts.Client, log zerolog.Logger) *Server {
	if cfg.Bind == "" {
		cfg.Bind = "0.0.0.0"
	}
	if cfg.Port == 0 {
		cfg.Port = 8091
	}
	if cfg.AudioDir == "" {
		cfg.AudioDir = "./cache/audio"
	}
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = 5 * time.Second
	}

	// Net bucket defaults
	if cfg.NetBucketStep < 0 {
		cfg.NetBucketStep = -cfg.NetBucketStep
	}
	if cfg.NetBucketFlat < 0 {
		cfg.NetBucketFlat = -cfg.NetBucketFlat
	}
	if cfg.NetBucketStep == 0 {
		cfg.NetBucketStep = 20
	}
	if cfg.NetBucketFlat == 0 {
		cfg.NetBucketFlat = 20
	}
	_ = os.MkdirAll(cfg.AudioDir, 0o755)

	return &Server{
		cfg:     cfg,
		tts:     ttsClient,
		log:     log,
		clients: make(map[chan []byte]struct{}),
		history: make([]Event, 0, 200),
		cues:    make(map[string]string),
	}
}

func (s *Server) Addr() string {
	return fmt.Sprintf("http://%s:%d", s.cfg.Bind, s.cfg.Port)
}

// SetCues lets main() install pre-generated cue URLs ("/audio/<hash>.mp3").
func (s *Server) SetCues(c map[string]string) {
	s.mu.Lock()
	s.cues = make(map[string]string, len(c))
	for k, v := range c {
		s.cues[k] = v
	}
	s.mu.Unlock()
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// UI
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/app.js", s.handleAppJS)

	// SSE stream
	mux.HandleFunc("/events", s.handleSSE)

	// History API
	mux.HandleFunc("/api/events", s.handleEventsJSON)
	mux.HandleFunc("/api/cloud", s.handleCloudJSON)

	// New: cue map (up/down/flat) so UI can play instantly without generating on-demand
	mux.HandleFunc("/api/cues", s.handleCuesJSON)

	// Quick TTS test endpoint:
	//   GET /api/speak?text=hello
	mux.HandleFunc("/api/speak", s.handleSpeak)

	// Health
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Serve cached audio files
	audioFS := http.FileServer(http.Dir(s.cfg.AudioDir))
	mux.Handle("/audio/", http.StripPrefix("/audio/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// prevent directory listing patterns
		if r.URL.Path == "" || r.URL.Path == "/" {
			http.NotFound(w, r)
			return
		}
		// basic traversal protection
		clean := filepath.Clean(r.URL.Path)
		if clean == "." || clean == ".." || clean[0] == '/' || clean == `\` {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		audioFS.ServeHTTP(w, r)
	})))

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", s.cfg.Bind, s.cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
	}

	// shutdown
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()

	s.log.Info().Str("addr", srv.Addr).Msg("http server listening")
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) Broadcast(ev Event) {
	s.mu.Lock()

	// Cloud events: keep only latest; do not pollute history.
	if ev.Type == "cloud" {
		s.cloud = ev
		s.hasCloud = true

		b, _ := json.Marshal(ev)
		for ch := range s.clients {
			select {
			case ch <- b:
			default:
			}
		}
		s.mu.Unlock()
		return
	}

	// Cloud pulse events: DO NOT store in history; just stream to clients.
	if ev.Type == "cloud_pulse" {
		b, _ := json.Marshal(ev)
		for ch := range s.clients {
			select {
			case ch <- b:
			default:
			}
		}
		s.mu.Unlock()
		return
	}

	// history
	if len(s.history) >= 500 {
		s.history = s.history[len(s.history)-400:]
	}
	s.history = append(s.history, ev)

	// push to clients
	b, _ := json.Marshal(ev)
	for ch := range s.clients {
		select {
		case ch <- b:
		default:
			// slow client: drop
		}
	}
	s.mu.Unlock()
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	clientCh := make(chan []byte, 64)

	s.mu.Lock()
	s.clients[clientCh] = struct{}{}
	// copy history + latest cloud
	hist := append([]Event(nil), s.history...)
	cloud := s.cloud
	hasCloud := s.hasCloud
	s.mu.Unlock()

	// initial: history events
	for _, ev := range hist {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}

	// initial: last cloud (single)
	if hasCloud {
		b, _ := json.Marshal(cloud)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}

	flusher.Flush()

	notify := r.Context().Done()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	defer func() {
		s.mu.Lock()
		delete(s.clients, clientCh)
		close(clientCh)
		s.mu.Unlock()
	}()

	for {
		select {
		case <-notify:
			return
		case <-keepAlive.C:
			// comment line keeps connection alive
			fmt.Fprintf(w, ": ping %d\n\n", time.Now().Unix())
			flusher.Flush()
		case msg := <-clientCh:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (s *Server) handleEventsJSON(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := append([]Event(nil), s.history...)
	cloud := s.cloud
	hasCloud := s.hasCloud
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"events": h,
	}
	if hasCloud {
		resp["cloud"] = cloud
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleCloudJSON(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cloud := s.cloud
	hasCloud := s.hasCloud
	s.mu.Unlock()

	if !hasCloud {
		http.Error(w, "cloud not ready yet", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cloud)
}

func (s *Server) handleCuesJSON(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := make(map[string]string, len(s.cues))
	for k, v := range s.cues {
		out[k] = v
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"cues":            out,
		"net_bucket_step": s.cfg.NetBucketStep,
		"net_bucket_flat": s.cfg.NetBucketFlat,
	})
}

func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if strings.TrimSpace(text) == "" {
		http.Error(w, "missing text", http.StatusBadRequest)
		return
	}

	res, err := s.tts.SpeakToFile(r.Context(), text)
	if err != nil {
		http.Error(w, "tts error: "+err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"audio_url": "/audio/" + filepath.Base(res.Path),
		"cache_hit": res.CacheHit,
	})
}
