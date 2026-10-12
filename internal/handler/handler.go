// Package handler implements the gateway's HTTP handlers for all 5 business
// groups described in business.md.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/nguyen-anh-hao/api-gateway/internal/client"
	"github.com/nguyen-anh-hao/api-gateway/internal/eventbus"
	"github.com/nguyen-anh-hao/api-gateway/internal/metrics"
	"github.com/nguyen-anh-hao/api-gateway/internal/middleware"
)

// reservedAmt tracks funds frozen by Reserve for an open (resting) order, so
// they can be released precisely when the order is cancelled or consumed by a
// fill. `price` is the reference/limit price used at reserve time and `rate`
// the worst-case (taker) fee rate, so the exact frozen cash for any quantity
// is `price * qty * (1 + rate)`.
type reservedAmt struct {
	side   string
	price  float64
	rate   float64
	qty    float64
	symbol string
}

// feeRates holds an account's maker and taker fee rates.
type feeRates struct {
	maker float64
	taker float64
}

var (
	tradesTotal = metrics.Default.Counter("trades_total", "Total trades executed.", "symbol")
	slippageBps = metrics.Default.Gauge("slippage_bps", "Slippage in basis points.", "symbol")
)

// OrderView is the gateway-side projection of an order (kept in memory so the
// stateless matching engine can serve open-order / history queries).
type OrderView struct {
	OrderID          uint64 `json:"order_id"`
	Symbol           string `json:"symbol"`
	Side             string `json:"side"`
	Type             string `json:"type"`
	Price            string `json:"price"`
	Quantity         string `json:"quantity"`
	ExecutedQuantity string `json:"executed_quantity"`
	LeavesQuantity   string `json:"leaves_quantity"`
	Status           string `json:"status"`
	Timestamp        int64  `json:"timestamp_ns"`
}

// TradeView is a public projection of a fill.
type TradeView struct {
	TradeID   uint64 `json:"trade_id"`
	OrderID   uint64 `json:"order_id"`
	Symbol    string `json:"symbol"`
	Price     string `json:"price"`
	Quantity  string `json:"quantity"`
	Fee       string `json:"fee"`
	Role      string `json:"role"`
	Timestamp int64  `json:"timestamp_ns"`
}

// Handler holds the downstream clients and a lightweight in-memory store of
// orders/trades used to serve read-side queries.
type Handler struct {
	Engine     *client.EngineClient
	Risk       *client.RiskClient
	Settlement *client.SettlementClient
	Promotion  *client.PromotionClient
	MarketData *client.MarketDataClient
	Auth       *middleware.AuthManager
	Publisher  *eventbus.Publisher

	mu     sync.RWMutex
	orders map[uint64]map[uint64]*OrderView // accountID -> orderID -> order
	trades map[uint64][]TradeView           // accountID -> trades

	reserved map[uint64]reservedAmt  // orderID -> frozen amount (open resting orders)
	feeMu    sync.Mutex
	feeCache map[uint64]feeRates // accountID -> maker/taker fee rates
}

func New(e *client.EngineClient, r *client.RiskClient, s *client.SettlementClient, p *client.PromotionClient, md *client.MarketDataClient, pub *eventbus.Publisher, auth *middleware.AuthManager) *Handler {
	return &Handler{
		Engine:     e,
		Risk:       r,
		Settlement: s,
		Promotion:  p,
		MarketData: md,
		Auth:       auth,
		Publisher:  pub,
		orders:     make(map[uint64]map[uint64]*OrderView),
		trades:     make(map[uint64][]TradeView),
		reserved:   make(map[uint64]reservedAmt),
		feeCache:   make(map[uint64]feeRates),
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func accountID(r *http.Request) uint64 {
	return middleware.AccountIDFrom(r.Context())
}

func nowNs() int64 {
	return time.Now().UnixNano()
}

// recordOrder stores an order projection for the given account.
func (h *Handler) recordOrder(account uint64, o *OrderView) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.orders[account] == nil {
		h.orders[account] = make(map[uint64]*OrderView)
	}
	h.orders[account][o.OrderID] = o
}

// recordTrades stores trades for both counterparties.
func (h *Handler) recordTrade(t TradeView, makerAccount, takerAccount uint64, makerOrder, takerOrder uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tt := t
	tt.OrderID = takerOrder
	tt.Role = "TAKER"
	h.trades[takerAccount] = append(h.trades[takerAccount], tt)
	tm := t
	tm.OrderID = makerOrder
	tm.Role = "MAKER"
	h.trades[makerAccount] = append(h.trades[makerAccount], tm)

	tradesTotal.Inc(t.Symbol)

	h.publishTrade(t, makerAccount, takerAccount, makerOrder, takerOrder)
}

// publishTrade emits a market-data event for the fill. Best-effort: if the bus
// is unavailable or its buffer is full the event is dropped (the durable
// TimescaleDB path owned by market-data-service is the system of record).
func (h *Handler) publishTrade(t TradeView, makerAccount, takerAccount uint64, makerOrder, takerOrder uint64) {
	if h.Publisher == nil {
		return
	}
	h.Publisher.Publish(eventbus.TradeEvent{
		TradeID:        t.TradeID,
		Symbol:         t.Symbol,
		Price:          t.Price,
		Quantity:       t.Quantity,
		Fee:            t.Fee,
		Role:           t.Role,
		Timestamp:      t.Timestamp,
		MakerOrderID:   makerOrder,
		TakerOrderID:   takerOrder,
		MakerAccountID: makerAccount,
		TakerAccountID: takerAccount,
	})
}

// feeRatesFor returns the (cached) maker and taker fee rates for an account,
// sourced from the promotion service's fee tiers. Defaults match the promotion
// service's VIP0 tier when it is unreachable.
func (h *Handler) feeRatesFor(ctx context.Context, accountID uint64) feeRates {
	h.feeMu.Lock()
	if r, ok := h.feeCache[accountID]; ok {
		h.feeMu.Unlock()
		return r
	}
	h.feeMu.Unlock()

	r := feeRates{maker: 0.0008, taker: 0.001}
	if tier, err := h.Promotion.GetFeeTiers(ctx, accountID); err == nil {
		if m, e := strconv.ParseFloat(tier.MakerFee, 64); e == nil {
			r.maker = m
		}
		if t, e := strconv.ParseFloat(tier.TakerFee, 64); e == nil {
			r.taker = t
		}
	}
	h.feeMu.Lock()
	h.feeCache[accountID] = r
	h.feeMu.Unlock()
	return r
}

// computeFee returns a cash fee for a fill notional at the given rate.
func computeFee(price, qty string, rate float64) string {
	p, _ := strconv.ParseFloat(price, 64)
	q, _ := strconv.ParseFloat(qty, 64)
	return strconv.FormatFloat(p*q*rate, 'f', 8, 64)
}
