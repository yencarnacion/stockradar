package radar

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"stockradar/internal/watchlist"
)

type AlertType string

const (
	AlertBaseUp      AlertType = "base_up"
	AlertBaseDown    AlertType = "base_down"
	AlertMomentumUp  AlertType = "momentum_up"
	AlertMomentumDown AlertType = "momentum_down"
	AlertCrossAbove  AlertType = "cross_above"
	AlertCrossBelow  AlertType = "cross_below"
)

type Alert struct {
	Type      AlertType
	Symbol    string
	Price     float64
	Message   string
	SpeakText string
}

type Config struct {
	GlobalCooldown time.Duration
	HistoryWindow  time.Duration
}

type Engine struct {
	cfg   Config
	wl    *watchlist.Watchlist
	log   zerolog.Logger

	mu     sync.Mutex
	state  map[string]*symbolState
}

type point struct {
	t time.Time
	p float64
	v float64
}

type symbolState struct {
	basePrice float64
	lastPrice float64
	lastTime  time.Time

	hist []point

	// for edge detection (avoid repeating while condition stays true)
	active map[string]bool

	// cooldown by key
	lastAlert map[string]time.Time
}

func NewEngine(cfg Config, wl *watchlist.Watchlist, log zerolog.Logger) *Engine {
	if cfg.GlobalCooldown <= 0 {
		cfg.GlobalCooldown = 25 * time.Second
	}
	if cfg.HistoryWindow <= 0 {
		cfg.HistoryWindow = 5 * time.Minute
	}
	return &Engine{
		cfg:  cfg,
		wl:   wl,
		log:  log,
		state: make(map[string]*symbolState),
	}
}

func (e *Engine) Update(symbol string, price float64, volume float64, ts time.Time) []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()

	ws := e.wl.Find(symbol)
	if ws == nil {
		return nil
	}
	if ws.Enabled != nil && !*ws.Enabled {
		return nil
	}

	st := e.state[symbol]
	if st == nil {
		st = &symbolState{
			active:    map[string]bool{},
			lastAlert: map[string]time.Time{},
		}
		e.state[symbol] = st
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	if price <= 0 {
		return nil
	}

	// base set on first tick
	if st.basePrice == 0 {
		st.basePrice = price
	}

	st.lastPrice = price
	st.lastTime = ts

	// update history
	st.hist = append(st.hist, point{t: ts, p: price, v: volume})
	st.hist = pruneByAge(st.hist, ts.Add(-e.cfg.HistoryWindow))

	var alerts []Alert

	// --- Base change rule (relative to basePrice from first seen) ---
	if ws.BaseChange != nil && st.basePrice > 0 {
		upKey := "base_up"
		downKey := "base_down"
		pct := ((price - st.basePrice) / st.basePrice) * 100.0

		if ws.BaseChange.UpPct > 0 {
			isUp := pct >= ws.BaseChange.UpPct
			alerts = append(alerts, e.edgeAlert(ws, st, upKey, isUp, ws.BaseChange.Cooldown.ToDuration(),
				AlertBaseUp, symbol, price,
				fmt.Sprintf("%s up %.2f%% vs baseline", symbol, pct),
				fmt.Sprintf("Alert. %s up %.1f percent.", symbol, pct),
			)...)
		}
		if ws.BaseChange.DownPct > 0 {
			isDown := pct <= -math.Abs(ws.BaseChange.DownPct)
			alerts = append(alerts, e.edgeAlert(ws, st, downKey, isDown, ws.BaseChange.Cooldown.ToDuration(),
				AlertBaseDown, symbol, price,
				fmt.Sprintf("%s down %.2f%% vs baseline", symbol, math.Abs(pct)),
				fmt.Sprintf("Alert. %s down %.1f percent.", symbol, math.Abs(pct)),
			)...)
		}
	}

	// --- Momentum rule (relative to price N seconds ago) ---
	if ws.Momentum != nil {
		win := ws.Momentum.Window.ToDuration()
		if win <= 0 {
			win = 60 * time.Second
		}
		oldPrice, ok := priceAtOrBefore(st.hist, ts.Add(-win))
		if ok && oldPrice > 0 {
			pct := ((price - oldPrice) / oldPrice) * 100.0
			upKey := "mom_up_" + win.String()
			downKey := "mom_down_" + win.String()

			if ws.Momentum.UpPct > 0 {
				isUp := pct >= ws.Momentum.UpPct
				alerts = append(alerts, e.edgeAlert(ws, st, upKey, isUp, ws.Momentum.Cooldown.ToDuration(),
					AlertMomentumUp, symbol, price,
					fmt.Sprintf("%s momentum up %.2f%% in %s", symbol, pct, win),
					fmt.Sprintf("Momentum. %s up %.1f percent in the last %d seconds.", symbol, pct, int(win.Seconds())),
				)...)
			}
			if ws.Momentum.DownPct > 0 {
				isDown := pct <= -math.Abs(ws.Momentum.DownPct)
				alerts = append(alerts, e.edgeAlert(ws, st, downKey, isDown, ws.Momentum.Cooldown.ToDuration(),
					AlertMomentumDown, symbol, price,
					fmt.Sprintf("%s momentum down %.2f%% in %s", symbol, math.Abs(pct), win),
					fmt.Sprintf("Momentum. %s down %.1f percent in the last %d seconds.", symbol, math.Abs(pct), int(win.Seconds())),
				)...)
			}
		}
	}

	// --- Price cross rule (absolute levels) ---
	if ws.PriceCross != nil {
		if ws.PriceCross.Above > 0 {
			key := fmt.Sprintf("cross_above_%.4f", ws.PriceCross.Above)
			isAbove := price >= ws.PriceCross.Above
			alerts = append(alerts, e.edgeAlert(ws, st, key, isAbove, ws.PriceCross.Cooldown.ToDuration(),
				AlertCrossAbove, symbol, price,
				fmt.Sprintf("%s crossed above %.2f", symbol, ws.PriceCross.Above),
				fmt.Sprintf("Price level. %s crossed above %.2f.", symbol, ws.PriceCross.Above),
			)...)
		}
		if ws.PriceCross.Below > 0 {
			key := fmt.Sprintf("cross_below_%.4f", ws.PriceCross.Below)
			isBelow := price <= ws.PriceCross.Below
			alerts = append(alerts, e.edgeAlert(ws, st, key, isBelow, ws.PriceCross.Cooldown.ToDuration(),
				AlertCrossBelow, symbol, price,
				fmt.Sprintf("%s crossed below %.2f", symbol, ws.PriceCross.Below),
				fmt.Sprintf("Price level. %s crossed below %.2f.", symbol, ws.PriceCross.Below),
			)...)
		}
	}

	return alerts
}

func (e *Engine) edgeAlert(
	ws *watchlist.Symbol,
	st *symbolState,
	key string,
	condition bool,
	cooldown time.Duration,
	atype AlertType,
	symbol string,
	price float64,
	message string,
	speak string,
) []Alert {
	if cooldown <= 0 {
		if ws.Cooldown.ToDuration() > 0 {
			cooldown = ws.Cooldown.ToDuration()
		} else {
			cooldown = e.cfg.GlobalCooldown
		}
	}

	now := time.Now()

	// edge detection: only fire when condition becomes true
	prev := st.active[key]
	st.active[key] = condition

	if !condition || prev {
		return nil
	}

	// cooldown
	if last, ok := st.lastAlert[key]; ok {
		if now.Sub(last) < cooldown {
			return nil
		}
	}
	st.lastAlert[key] = now

	return []Alert{{
		Type:      atype,
		Symbol:    symbol,
		Price:     price,
		Message:   message,
		SpeakText: speak,
	}}
}

func pruneByAge(h []point, min time.Time) []point {
	if len(h) == 0 {
		return h
	}
	// find first index >= min
	i := 0
	for i < len(h) && h[i].t.Before(min) {
		i++
	}
	if i == 0 {
		return h
	}
	// copy to avoid holding old backing array
	out := make([]point, 0, len(h)-i)
	out = append(out, h[i:]...)
	return out
}

// priceAtOrBefore finds a price at or before target time.
// hist is assumed time-ordered.
func priceAtOrBefore(hist []point, target time.Time) (float64, bool) {
	if len(hist) == 0 {
		return 0, false
	}
	// linear scan is fine because hist is small (history window)
	var best point
	found := false
	for _, p := range hist {
		if p.t.After(target) {
			break
		}
		best = p
		found = true
	}
	if !found {
		return 0, false
	}
	return best.p, true
}


