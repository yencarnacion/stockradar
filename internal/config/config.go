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
	if cfg.OpenAI.MaxTextChars <= 0 {
		cfg.OpenAI.MaxTextChars = 500
	}
	if cfg.Cache.AudioDir == "" {
		cfg.Cache.AudioDir = "./cache/audio"
	}
	if cfg.Radar.AlertWorkers <= 0 {
		cfg.Radar.AlertWorkers = 2
	}
	return cfg, nil
}


