package watchlist

import (
	"errors"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"stockradar/internal/config"
)

type Watchlist struct {
	Symbols []Symbol `yaml:"symbols"`
}

type Symbol struct {
	Ticker  string `yaml:"ticker"`
	Name    string `yaml:"name,omitempty"`
	Enabled *bool  `yaml:"enabled,omitempty"`

	BaseChange *BaseChangeRule `yaml:"base_change,omitempty"`
	Momentum   *MomentumRule   `yaml:"momentum,omitempty"`
	PriceCross *PriceCrossRule `yaml:"price_cross,omitempty"`

	// fallback if rule cooldown omitted
	Cooldown config.Duration `yaml:"cooldown,omitempty"`
}

type BaseChangeRule struct {
	UpPct    float64        `yaml:"up_pct"`
	DownPct  float64        `yaml:"down_pct"`
	Cooldown config.Duration `yaml:"cooldown"`
}

type MomentumRule struct {
	Window   config.Duration `yaml:"window"`
	UpPct    float64         `yaml:"up_pct"`
	DownPct  float64         `yaml:"down_pct"`
	Cooldown config.Duration `yaml:"cooldown"`
}

type PriceCrossRule struct {
	Above    float64         `yaml:"above"`
	Below    float64         `yaml:"below"`
	Cooldown config.Duration `yaml:"cooldown"`
}

func Load(path string) (*Watchlist, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wl Watchlist
	if err := yaml.Unmarshal(b, &wl); err != nil {
		return nil, err
	}
	wl.Normalize()
	if len(wl.Symbols) == 0 {
		return nil, errors.New("watchlist empty")
	}
	return &wl, nil
}

func (w *Watchlist) Normalize() {
	seen := map[string]bool{}
	out := make([]Symbol, 0, len(w.Symbols))

	for _, s := range w.Symbols {
		s.Ticker = strings.ToUpper(strings.TrimSpace(s.Ticker))
		if s.Ticker == "" {
			continue
		}
		if seen[s.Ticker] {
			continue
		}
		seen[s.Ticker] = true

		// defaults if rule not provided
		if s.BaseChange == nil && s.Momentum == nil && s.PriceCross == nil {
			// sensible default: base-change + momentum
			s.BaseChange = &BaseChangeRule{UpPct: 1.0, DownPct: 1.0, Cooldown: config.Duration(90 * 1e9)}
			s.Momentum = &MomentumRule{Window: config.Duration(60 * 1e9), UpPct: 0.4, DownPct: 0.4, Cooldown: config.Duration(60 * 1e9)}
		}
		out = append(out, s)
	}

	// stable order
	sort.Slice(out, func(i, j int) bool { return out[i].Ticker < out[j].Ticker })
	w.Symbols = out
}

func (w *Watchlist) Tickers() []string {
	if w == nil {
		return nil
	}
	t := make([]string, 0, len(w.Symbols))
	for _, s := range w.Symbols {
		if s.Enabled != nil && !*s.Enabled {
			continue
		}
		if s.Ticker != "" {
			t = append(t, s.Ticker)
		}
	}
	sort.Strings(t)
	return t
}

func (w *Watchlist) Find(ticker string) *Symbol {
	if w == nil {
		return nil
	}
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	for i := range w.Symbols {
		if w.Symbols[i].Ticker == ticker {
			return &w.Symbols[i]
		}
	}
	return nil
}


