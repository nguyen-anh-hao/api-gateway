package handler

import (
	"log/slog"
	"net/http"
	"strconv"
)

// GetTicker proxies GET /ticker?symbol= to market-data-service.
func (h *Handler) GetTicker(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	status, body, err := h.MarketData.Ticker(r.Context(), symbol)
	h.proxyMarketData(w, status, body, err)
}

// GetOrderBook proxies GET /book?symbol=&depth= to market-data-service.
func (h *Handler) GetOrderBook(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	depth, _ := strconv.ParseUint(r.URL.Query().Get("depth"), 10, 32)
	status, body, err := h.MarketData.OrderBook(r.Context(), symbol, uint32(depth))
	h.proxyMarketData(w, status, body, err)
}

// GetTradesTape proxies GET /trades-tape?symbol= to market-data-service.
func (h *Handler) GetTradesTape(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	status, body, err := h.MarketData.TradesTape(r.Context(), symbol)
	h.proxyMarketData(w, status, body, err)
}

// GetOHLCV proxies GET /ohlcv?symbol=&interval=&limit= to market-data-service.
func (h *Handler) GetOHLCV(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	interval := r.URL.Query().Get("interval")
	if interval == "" {
		interval = "1m"
	}
	limit, _ := strconv.ParseUint(r.URL.Query().Get("limit"), 10, 32)
	status, body, err := h.MarketData.OHLCV(r.Context(), symbol, interval, uint32(limit))
	h.proxyMarketData(w, status, body, err)
}

// proxyMarketData forwards the downstream response body/status unchanged so the
// gateway preserves the exact market-data JSON contract.
func (h *Handler) proxyMarketData(w http.ResponseWriter, status int, body []byte, err error) {
	if err != nil {
		slog.Error("market data proxy error", "err", err)
		writeErr(w, http.StatusBadGateway, "market data service unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

