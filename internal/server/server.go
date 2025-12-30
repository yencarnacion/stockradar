package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"stockradar/internal/tts"
)

type Config struct {
	Bind              string
	Port              int
	AudioDir          string
	ReadHeaderTimeout time.Duration
}

type Event struct {
	Time     time.Time `json:"time"`
	Symbol   string    `json:"symbol"`
	Price    float64   `json:"price"`
	Type     string    `json:"type"`
	Message  string    `json:"message"`
	AudioURL string    `json:"audio_url,omitempty"`
	CacheHit bool      `json:"cache_hit,omitempty"`
}

type Server struct {
	cfg Config
	tts *tts.Client
	log zerolog.Logger

	mu      sync.Mutex
	clients map[chan []byte]struct{}
	history []Event
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
	_ = os.MkdirAll(cfg.AudioDir, 0o755)

	return &Server{
		cfg:     cfg,
		tts:     ttsClient,
		log:     log,
		clients: make(map[chan []byte]struct{}),
		history: make([]Event, 0, 200),
	}
}

func (s *Server) Addr() string {
	return fmt.Sprintf("http://%s:%d", s.cfg.Bind, s.cfg.Port)
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
	// send recent history on connect
	hist := append([]Event(nil), s.history...)
	s.mu.Unlock()

	// initial: history events
	for _, ev := range hist {
		b, _ := json.Marshal(ev)
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
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"events": h,
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


