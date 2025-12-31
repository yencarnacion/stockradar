package radar

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"stockradar/internal/watchlist"
)

type CloudConfig struct {
	Enabled       bool
	EmitEvery     time.Duration
	StaleAfter    time.Duration
	DeadbandPct   float64
	CapMovePct    float64
	StrengthPct   float64
	Smoothing     float64
	MinRateHz     float64
	MaxRateHz     float64
	BreadthWeight float64
}

type CloudSnapshot struct {
	Time      time.Time `json:"time"`
	Direction string    `json:"direction"` // up | down | flat

	// strength is 0..1, computed from smoothed composite score
	Strength float64 `json:"strength"`

	// rate_hz is the “geiger tick rate suggestion” for the browser
	RateHz float64 `json:"rate_hz"`

	// Composite score in percent units (smoothed)
	ScorePct float64 `json:"score"`

	// Debug/supporting metrics
	RawPct   float64 `json:"raw_score"`
	Breadth  float64 `json:"breadth"` // (adv-dec)/active
	Adv      int     `json:"adv"`
	Dec      int     `json:"dec"`
	Flat     int     `json:"flat"`
	Active   int     `json:"active"`
	Total    int     `json:"total"`
	Message  string  `json:"message"`
}

// CloudPulse is a per-market-update “click” signal.
// It is intentionally lightweight and meant to be emitted at the pace of the feed.
type CloudPulse struct {
	Time      time.Time `json:"time"`
	Symbol    string    `json:"symbol"`
	Price     float64   `json:"price"`
	Volume    float64   `json:"volume"`
	DeltaPct  float64   `json:"delta_pct"`
	Direction string    `json:"direction"` // up | down | flat
	Strength  float64   `json:"strength"`  // 0..1
}

type CloudEngine struct {
	cfg CloudConfig
	wl  *watchlist.Watchlist
	log zerolog.Logger

	mu      sync.Mutex
	syms    map[string]*cloudSym
	ewma    float64
	hasEwma bool
}

type cloudSym struct {
	lastPrice    float64
	lastDeltaPct float64
	lastVol      float64
	lastTs       time.Time
	ready        bool
}

func NewCloudEngine(cfg CloudConfig, wl *watchlist.Watchlist, log zerolog.Logger) *CloudEngine {
	// Defaults (kept here so CloudEngine also works without config.yaml fields present)
	if cfg.EmitEvery <= 0 {
		cfg.EmitEvery = 200 * time.Millisecond
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = 3 * time.Second
	}
	if cfg.DeadbandPct < 0 {
		cfg.DeadbandPct = 0
	}
	if cfg.DeadbandPct == 0 {
		// Default deadband: 0.003% composite score before we “wake up” the cloud
		cfg.DeadbandPct = 0.003
	}
	if cfg.StrengthPct == 0 {
		// Default “full scale”: 0.03% composite score -> strength ~ 1.0
		cfg.StrengthPct = 0.03
	}
	if cfg.StrengthPct < 0 {
		cfg.StrengthPct = -cfg.StrengthPct
	}
	if cfg.Smoothing <= 0 || cfg.Smoothing > 1 {
		cfg.Smoothing = 0.25
	}
	if cfg.MinRateHz < 0 {
		cfg.MinRateHz = 0
	}
	if cfg.MaxRateHz <= 0 {
		cfg.MaxRateHz = 12.0
	}
	if cfg.MaxRateHz < cfg.MinRateHz {
		cfg.MaxRateHz = cfg.MinRateHz
	}
	if cfg.BreadthWeight < 0 {
		cfg.BreadthWeight = 0
	}
	if cfg.BreadthWeight > 1 {
		cfg.BreadthWeight = 1
	}
	if cfg.BreadthWeight == 0 {
		cfg.BreadthWeight = 0.45
	}

	ce := &CloudEngine{
		cfg:  cfg,
		wl:   wl,
		log:  log,
		syms: make(map[string]*cloudSym),
	}

	// Pre-seed symbol map (stable Total + fewer allocations)
	if wl != nil {
		for _, t := range wl.Tickers() {
			ce.syms[t] = &cloudSym{}
		}
	}

	return ce
}

func (c *CloudEngine) Update(symbol string, price float64, volume float64, ts time.Time) (CloudPulse, bool) {
	if !c.cfg.Enabled {
		return CloudPulse{}, false
	}
	if price <= 0 {
		return CloudPulse{}, false
	}
	if ts.IsZero() {
		ts = time.Now()
	}

	// Watchlist filter
	if c.wl != nil {
		ws := c.wl.Find(symbol)
		if ws == nil {
			return CloudPulse{}, false
		}
		if ws.Enabled != nil && !*ws.Enabled {
			return CloudPulse{}, false
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	st := c.syms[symbol]
	if st == nil {
		st = &cloudSym{}
		c.syms[symbol] = st
	}

	rawDelta := 0.0
	if st.ready && st.lastPrice > 0 {
		rawDelta = ((price - st.lastPrice) / st.lastPrice) * 100.0
	}

	// For pulse strength, clamp delta to CapMovePct (if configured) so outliers don’t blow out audio.
	d := rawDelta
	capMove := math.Abs(c.cfg.CapMovePct)
	if capMove > 0 {
		if d > capMove {
			d = capMove
		} else if d < -capMove {
			d = -capMove
		}
	}

	dir := "flat"
	if d > 0 {
		dir = "up"
	} else if d < 0 {
		dir = "down"
	}

	// Strength mapping (0..1)
	str := 0.0
	sp := c.cfg.StrengthPct
	if sp == 0 {
		sp = 0.03
	}
	if sp > 0 {
		str = math.Abs(d) / sp
		str = clamp(str, 0, 1)
	}

	// Persist state for Snapshot()
	st.lastDeltaPct = rawDelta
	st.lastPrice = price
	st.lastVol = volume
	st.lastTs = ts
	st.ready = true

	return CloudPulse{
		Time:      ts,
		Symbol:    symbol,
		Price:     price,
		Volume:    volume,
		DeltaPct:  d,
		Direction: dir,
		Strength:  str,
	}, true
}

// Snapshot computes a smoothed “market cloud” signal from latest per-symbol deltas.
func (c *CloudEngine) Snapshot(now time.Time) CloudSnapshot {
	if now.IsZero() {
		now = time.Now()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	total := len(c.syms)

	staleAfter := c.cfg.StaleAfter
	capMove := math.Abs(c.cfg.CapMovePct) // 0 => no clamp

	var sum float64
	n := 0
	adv, dec, flat := 0, 0, 0

	for _, st := range c.syms {
		if st == nil || !st.ready || st.lastPrice <= 0 {
			continue
		}
		if staleAfter > 0 && now.Sub(st.lastTs) > staleAfter {
			continue
		}

		d := st.lastDeltaPct

		if d > 0 {
			adv++
		} else if d < 0 {
			dec++
		} else {
			flat++
		}

		if capMove > 0 {
			if d > capMove {
				d = capMove
			} else if d < -capMove {
				d = -capMove
			}
		}

		sum += d
		n++
	}

	rawScore := 0.0
	breadth := 0.0
	if n > 0 {
		rawScore = sum / float64(n)
		breadth = float64(adv-dec) / float64(n) // -1..1
	}

	// Composite score (percent units):
	// - rawScore: avg % move per symbol per update
	// - breadth: adv/dec balance mapped into % space using StrengthPct
	bw := c.cfg.BreadthWeight
	composite := (1.0-bw)*rawScore + bw*(breadth*c.cfg.StrengthPct)

	// EWMA smoothing
	a := c.cfg.Smoothing
	if !c.hasEwma {
		c.ewma = composite
		c.hasEwma = true
	} else {
		c.ewma = (1.0-a)*c.ewma + a*composite
	}

	score := c.ewma

	direction := "flat"
	if math.Abs(score) >= c.cfg.DeadbandPct {
		if score > 0 {
			direction = "up"
		} else if score < 0 {
			direction = "down"
		}
	}

	strength := 0.0
	if direction != "flat" {
		sp := c.cfg.StrengthPct
		if sp == 0 {
			sp = 0.03
		}
		strength = math.Abs(score) / sp
		strength = clamp(strength, 0, 1)
	}

	rateHz := 0.0
	if direction != "flat" {
		rateHz = c.cfg.MinRateHz + strength*(c.cfg.MaxRateHz-c.cfg.MinRateHz)
		if rateHz < 0 {
			rateHz = 0
		}
	}

	label := "FLAT"
	if direction == "up" {
		label = "UP"
	} else if direction == "down" {
		label = "DOWN"
	}

	msg := fmt.Sprintf(
		"Cloud %s • strength %.2f • score %+0.4f%% • adv %d / dec %d / flat %d",
		label, strength, score, adv, dec, flat,
	)

	return CloudSnapshot{
		Time:      now,
		Direction: direction,
		Strength:  strength,
		RateHz:    rateHz,
		ScorePct:  score,
		RawPct:    rawScore,
		Breadth:   breadth,
		Adv:       adv,
		Dec:       dec,
		Flat:      flat,
		Active:    n,
		Total:     total,
		Message:   msg,
	}
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
