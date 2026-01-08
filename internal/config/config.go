package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration time.Duration

func (d Duration) ToDuration() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		*d = 0
		return nil
	}
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar")
	}

	// allow: "5s", "2m", or integer seconds
	switch value.Tag {
	case "!!int":
		i, err := strconv.ParseInt(value.Value, 10, 64)
		if err != nil {
			return err
		}
		*d = Duration(time.Duration(i) * time.Second)
		return nil
	case "!!str":
		if value.Value == "" {
			*d = 0
			return nil
		}
		if dur, err := time.ParseDuration(value.Value); err == nil {
			*d = Duration(dur)
			return nil
		}
		// also allow string that is numeric = seconds
		if i, err := strconv.ParseInt(value.Value, 10, 64); err == nil {
			*d = Duration(time.Duration(i) * time.Second)
			return nil
		}
		return fmt.Errorf("invalid duration: %q", value.Value)
	default:
		// try parse anyway
		if dur, err := time.ParseDuration(value.Value); err == nil {
			*d = Duration(dur)
			return nil
		}
		return fmt.Errorf("invalid duration: %q", value.Value)
	}
}

type Config struct {
	Server ServerConfig `yaml:"server"`
	Massive MassiveConfig `yaml:"massive"`
	OpenAI OpenAIConfig `yaml:"openai"`
	Cache  CacheConfig  `yaml:"cache"`
	Radar  RadarConfig  `yaml:"radar"`
	Cloud  CloudConfig  `yaml:"cloud"`
}

type ServerConfig struct {
	Bind              string   `yaml:"bind"`
	Port              int      `yaml:"port"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
}

type MassiveConfig struct {
	APIKeyEnv string `yaml:"api_key_env"`
	Feed      string `yaml:"feed"`   // realtime, delayed
	Market    string `yaml:"market"` // stocks, crypto, forex, options
}

type OpenAIConfig struct {
	APIKeyEnv       string   `yaml:"api_key_env"`
	BaseURL         string   `yaml:"base_url"` // default https://api.openai.com/v1
	Model           string   `yaml:"model"`    // tts-1-hd, tts-1, gpt-4o-mini-tts, etc
	Voice           string   `yaml:"voice"`    // nova, alloy, etc
	ResponseFormat  string   `yaml:"response_format"` // mp3, wav, aac, opus, flac
	Speed           float64  `yaml:"speed"`
	Timeout         Duration `yaml:"timeout"`
	MaxTextChars    int      `yaml:"max_text_chars"`
}

type CacheConfig struct {
	AudioDir string `yaml:"audio_dir"`
}

type RadarConfig struct {
	LogLevel       string   `yaml:"log_level"`
	GlobalCooldown Duration `yaml:"global_cooldown"`
	HistoryWindow  Duration `yaml:"history_window"`
	AlertWorkers   int      `yaml:"alert_workers"`
}

type CloudConfig struct {
	Enabled bool `yaml:"enabled"`

	EmitEvery Duration `yaml:"emit_every"`
	StaleAfter Duration `yaml:"stale_after"`

	// Percent units, e.g. 0.003 == 0.003%
	DeadbandPct float64 `yaml:"deadband_pct"`

	// Clamp per-symbol delta % per update. 0 disables clamping.
	CapMovePct float64 `yaml:"cap_move_pct"`

	// Percent magnitude mapping to strength=1.0
	StrengthPct float64 `yaml:"strength_pct"`

	// EWMA alpha
	Smoothing float64 `yaml:"smoothing"`

	MinRateHz float64 `yaml:"min_rate_hz"`
	MaxRateHz float64 `yaml:"max_rate_hz"`

	// 0..1 blend: breadth vs avg move
	BreadthWeight float64 `yaml:"breadth_weight"`

	// Net “voice bucket” settings for the browser UI:
	// - net_bucket_flat: values in (-flat, +flat) => "flat"
	// - net_bucket_step: bucket step beyond flat
	// Defaults preserve existing behavior (flat=20, step=20).
	NetBucketStep int `yaml:"net_bucket_step"`
	NetBucketFlat int `yaml:"net_bucket_flat"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{
			Bind:              "0.0.0.0",
			Port:              8091,
			ReadHeaderTimeout: Duration(5 * time.Second),
		},
		Massive: MassiveConfig{
			APIKeyEnv: "MASSIVE_API_KEY",
			Feed:      "realtime",
			Market:    "stocks",
		},
		OpenAI: OpenAIConfig{
			APIKeyEnv:      "OPENAI_API_KEY",
			BaseURL:        "https://api.openai.com/v1",
			Model:          "tts-1-hd",
			Voice:          "nova",
			ResponseFormat: "mp3",
			Speed:          1.0,
			Timeout:        Duration(30 * time.Second),
			MaxTextChars:   500,
		},
		Cache: CacheConfig{
			AudioDir: "./cache/audio",
		},
		Radar: RadarConfig{
			LogLevel:       "info",
			GlobalCooldown: Duration(25 * time.Second),
			HistoryWindow:  Duration(5 * time.Minute),
			AlertWorkers:   2,
		},
		Cloud: CloudConfig{
			Enabled:       true,
			EmitEvery:     Duration(200 * time.Millisecond),
			StaleAfter:    Duration(3 * time.Second),
			DeadbandPct:   0.003,
			CapMovePct:    0.30,
			StrengthPct:   0.03,
			Smoothing:     0.25,
			MinRateHz:     1.0,
			MaxRateHz:     12.0,
			BreadthWeight: 0.45,
			NetBucketStep: 20,
			NetBucketFlat: 20,
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8091
	}
	if cfg.Server.Bind == "" {
		cfg.Server.Bind = "0.0.0.0"
	}
	if cfg.Server.ReadHeaderTimeout.ToDuration() <= 0 {
		cfg.Server.ReadHeaderTimeout = Duration(5 * time.Second)
	}

	if cfg.Massive.APIKeyEnv == "" {
		cfg.Massive.APIKeyEnv = "MASSIVE_API_KEY"
	}
	if cfg.OpenAI.APIKeyEnv == "" {
		cfg.OpenAI.APIKeyEnv = "OPENAI_API_KEY"
	}
	if cfg.OpenAI.BaseURL == "" {
		cfg.OpenAI.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.OpenAI.Model == "" {
		cfg.OpenAI.Model = "tts-1-hd"
	}
	if cfg.OpenAI.Voice == "" {
		cfg.OpenAI.Voice = "nova"
	}
	if cfg.OpenAI.ResponseFormat == "" {
		cfg.OpenAI.ResponseFormat = "mp3"
	}
	if cfg.OpenAI.Speed <= 0 {
		cfg.OpenAI.Speed = 1.0
	}
	if cfg.OpenAI.Timeout.ToDuration() <= 0 {
		cfg.OpenAI.Timeout = Duration(30 * time.Second)
	}
	if cfg.OpenAI.MaxTextChars <= 0 {
		cfg.OpenAI.MaxTextChars = 500
	}
	if cfg.Cache.AudioDir == "" {
		cfg.Cache.AudioDir = "./cache/audio"
	}
	if cfg.Radar.AlertWorkers <= 0 {
		cfg.Radar.AlertWorkers = 2
	}
	if cfg.Radar.GlobalCooldown.ToDuration() <= 0 {
		cfg.Radar.GlobalCooldown = Duration(25 * time.Second)
	}
	if cfg.Radar.HistoryWindow.ToDuration() <= 0 {
		cfg.Radar.HistoryWindow = Duration(5 * time.Minute)
	}

	// Cloud sanity (don’t override user values unless they are invalid)
	if cfg.Cloud.EmitEvery.ToDuration() <= 0 {
		cfg.Cloud.EmitEvery = Duration(200 * time.Millisecond)
	}
	if cfg.Cloud.StaleAfter.ToDuration() <= 0 {
		cfg.Cloud.StaleAfter = Duration(3 * time.Second)
	}
	if cfg.Cloud.DeadbandPct < 0 {
		cfg.Cloud.DeadbandPct = 0
	}
	if cfg.Cloud.CapMovePct < 0 {
		cfg.Cloud.CapMovePct = -cfg.Cloud.CapMovePct
	}
	if cfg.Cloud.StrengthPct < 0 {
		cfg.Cloud.StrengthPct = -cfg.Cloud.StrengthPct
	}
	if cfg.Cloud.Smoothing <= 0 || cfg.Cloud.Smoothing > 1 {
		cfg.Cloud.Smoothing = 0.25
	}
	if cfg.Cloud.MinRateHz < 0 {
		cfg.Cloud.MinRateHz = 0
	}
	if cfg.Cloud.MaxRateHz <= 0 {
		cfg.Cloud.MaxRateHz = 12.0
	}
	if cfg.Cloud.MaxRateHz < cfg.Cloud.MinRateHz {
		cfg.Cloud.MaxRateHz = cfg.Cloud.MinRateHz
	}
	if cfg.Cloud.BreadthWeight < 0 {
		cfg.Cloud.BreadthWeight = 0
	}
	if cfg.Cloud.BreadthWeight > 1 {
		cfg.Cloud.BreadthWeight = 1
	}

	// Net bucket defaults/sanity (UI net-voice feature)
	if cfg.Cloud.NetBucketStep < 0 {
		cfg.Cloud.NetBucketStep = -cfg.Cloud.NetBucketStep
	}
	if cfg.Cloud.NetBucketFlat < 0 {
		cfg.Cloud.NetBucketFlat = -cfg.Cloud.NetBucketFlat
	}
	if cfg.Cloud.NetBucketStep == 0 {
		cfg.Cloud.NetBucketStep = 20
	}
	if cfg.Cloud.NetBucketFlat == 0 {
		cfg.Cloud.NetBucketFlat = 20
	}

	return cfg, nil
}
