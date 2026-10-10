package handler

import (
	"log"
	"net/http"
	"sort"
	"strconv"
)

// GetTicker handles GET /ticker?symbol= (business group 3: Market Data).
func (h *Handler) GetTicker(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	t, err := h.Engine.Ticker(r.Context(), symbol)
	if err != nil {
		log.Printf("[market-data] ticker engine error: %v", err)
		writeErr(w, http.StatusBadGateway, "matching engine unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ticker": t})
}

// GetOrderBook handles GET /book?symbol=&depth= (L2 depth snapshot).
func (h *Handler) GetOrderBook(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	depth, _ := strconv.ParseUint(r.URL.Query().Get("depth"), 10, 32)
	book, err := h.Engine.OrderBook(r.Context(), symbol, uint32(depth))
	if err != nil {
		log.Printf("[market-data] book engine error: %v", err)
		writeErr(w, http.StatusBadGateway, "matching engine unavailable")
		return
	}
	writeJSON(w, http.StatusOK, book)
}

// GetTradesTape handles GET /trades-tape?symbol= (public anonymous tape).
func (h *Handler) GetTradesTape(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	out := []TradeView{}
	h.mu.RLock()
	for _, t := range h.tape {
		if symbol != "" && t.Symbol != symbol {
			continue
		}
		out = append(out, t)
	}
	h.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"trades": out})
}

type candle struct {
	Ts     int64   `json:"timestamp"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// GetOHLCV handles GET /ohlcv?symbol=&interval=&limit= (business group 3).
func (h *Handler) GetOHLCV(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	interval := r.URL.Query().Get("interval")
	if interval == "" {
		interval = "1m"
	}
	bucketSec := intervalSeconds(interval)

	h.mu.RLock()
	var trades []TradeView
	for _, t := range h.tape {
		if symbol != "" && t.Symbol != symbol {
			continue
		}
		trades = append(trades, t)
	}
	h.mu.RUnlock()

	type agg struct {
		open, high, low, close, volume float64
	}
	buckets := map[int64]*agg{}
	for _, t := range trades {
		price, _ := strconv.ParseFloat(t.Price, 64)
		qty, _ := strconv.ParseFloat(t.Quantity, 64)
		sec := t.Timestamp / 1e9
		bucket := sec - sec%bucketSec
		c, ok := buckets[bucket]
		if !ok {
			c = &agg{open: price, high: price, low: price, close: price}
			buckets[bucket] = c
		}
		if price > c.high {
			c.high = price
		}
		if price < c.low {
			c.low = price
		}
		c.close = price
		c.volume += qty
	}

	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	out := []candle{}
	for _, k := range keys {
		c := buckets[k]
		out = append(out, candle{Ts: k, Open: c.open, High: c.high, Low: c.low, Close: c.close, Volume: c.volume})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"candles": out})
}

func intervalSeconds(interval string) int64 {
	switch interval {
	case "1s":
		return 1
	case "5m":
		return 5 * 60
	case "1h":
		return 60 * 60
	case "1d":
		return 24 * 60 * 60
	default:
		return 60 // 1m
	}
}
