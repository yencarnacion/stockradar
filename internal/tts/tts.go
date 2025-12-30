package tts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"
)

type Config struct {
	APIKey         string
	BaseURL        string
	Model          string
	Voice          string
	ResponseFormat string // mp3, wav, etc
	Speed          float64
	Timeout        time.Duration
	CacheDir       string
	MaxTextChars   int
}

type Client struct {
	cfg  Config
	http *http.Client
	log  zerolog.Logger

	sf singleflight.Group
}

type SpeakResult struct {
	Path     string
	CacheHit bool
}

func NewClient(cfg Config, log zerolog.Logger) (*Client, error) {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		return nil, errors.New("missing OpenAI API key")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Model == "" {
		cfg.Model = "tts-1-hd"
	}
	if cfg.Voice == "" {
		cfg.Voice = "nova"
	}
	if cfg.ResponseFormat == "" {
		cfg.ResponseFormat = "mp3"
	}
	if cfg.Speed <= 0 {
		cfg.Speed = 1.0
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = "./cache/audio"
	}
	if cfg.MaxTextChars <= 0 {
		cfg.MaxTextChars = 500
	}

	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return nil, err
	}

	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: cfg.Timeout,
		},
		log: log,
	}, nil
}

func (c *Client) SpeakToFile(ctx context.Context, text string) (SpeakResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return SpeakResult{}, errors.New("empty tts text")
	}
	if len([]rune(text)) > c.cfg.MaxTextChars {
		// hard truncate (safe + predictable)
		r := []rune(text)
		text = string(r[:c.cfg.MaxTextChars])
	}

	key := c.cacheKey(text)
	ext := extensionFromFormat(c.cfg.ResponseFormat)
	if ext == "" {
		ext = "mp3"
	}
	finalPath := filepath.Join(c.cfg.CacheDir, key+"."+ext)

	// fast path
	if fileExists(finalPath) {
		return SpeakResult{Path: finalPath, CacheHit: true}, nil
	}

	v, err, _ := c.sf.Do(key, func() (any, error) {
		// double-check after singleflight
		if fileExists(finalPath) {
			return SpeakResult{Path: finalPath, CacheHit: true}, nil
		}

		audioBytes, err := c.synthesize(ctx, text)
		if err != nil {
			return SpeakResult{}, err
		}

		tmp := fmt.Sprintf("%s.tmp-%d-%d", finalPath, time.Now().UnixNano(), rand.Intn(999999))
		if err := os.WriteFile(tmp, audioBytes, 0o644); err != nil {
			return SpeakResult{}, err
		}
		// atomic replace
		if err := os.Rename(tmp, finalPath); err != nil {
			_ = os.Remove(tmp)
			return SpeakResult{}, err
		}

		return SpeakResult{Path: finalPath, CacheHit: false}, nil
	})
	if err != nil {
		return SpeakResult{}, err
	}
	return v.(SpeakResult), nil
}

func (c *Client) synthesize(ctx context.Context, text string) ([]byte, error) {
	endpoint := c.cfg.BaseURL + "/audio/speech"

	// Try with response_format first (most common)
	payload := map[string]any{
		"model": c.cfg.Model,
		"voice": c.cfg.Voice,
		"input": text,
	}
	if c.cfg.ResponseFormat != "" {
		payload["response_format"] = c.cfg.ResponseFormat
	}
	if c.cfg.Speed > 0 {
		payload["speed"] = c.cfg.Speed
	}

	b, code, errMsg, err := c.postAudio(ctx, endpoint, payload)
	if err == nil {
		return b, nil
	}

	// Fallback: if API complains about response_format, try format instead
	if code == 400 && strings.Contains(strings.ToLower(errMsg), "response_format") {
		delete(payload, "response_format")
		payload["format"] = c.cfg.ResponseFormat
		b2, _, _, err2 := c.postAudio(ctx, endpoint, payload)
		if err2 == nil {
			return b2, nil
		}
	}

	return nil, err
}

func (c *Client) postAudio(ctx context.Context, url string, payload map[string]any) ([]byte, int, string, error) {
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if len(data) == 0 {
			return nil, resp.StatusCode, "", errors.New("empty audio response")
		}
		return data, resp.StatusCode, "", nil
	}

	// parse OpenAI-style error json if present
	errMsg := strings.TrimSpace(string(data))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &parsed) == nil {
		if parsed.Error.Message != "" {
			errMsg = parsed.Error.Message
		}
	}

	return nil, resp.StatusCode, errMsg, fmt.Errorf("openai tts failed: status=%d msg=%s", resp.StatusCode, errMsg)
}

func (c *Client) cacheKey(text string) string {
	raw := c.cfg.Model + "|" + c.cfg.Voice + "|" + c.cfg.ResponseFormat + "|" + fmt.Sprintf("%.3f", c.cfg.Speed) + "|" + text
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func extensionFromFormat(f string) string {
	f = strings.ToLower(strings.TrimSpace(f))
	switch f {
	case "mp3":
		return "mp3"
	case "wav":
		return "wav"
	case "aac":
		return "aac"
	case "opus":
		return "opus"
	case "flac":
		return "flac"
	case "pcm":
		return "pcm"
	default:
		// default to mp3
		return "mp3"
	}
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !st.IsDir() && st.Size() > 0
}


