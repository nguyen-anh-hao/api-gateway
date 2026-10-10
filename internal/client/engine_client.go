// Package client contains HTTP clients to the downstream services.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/exchange/trading-system/services/api-gateway/internal/metrics"
)

var matchingLatencyNs = metrics.Default.Gauge(
	"matching_latency_ns", "Matching engine round-trip latency in nanoseconds.", "op")

// EngineClient talks to the C++ matching engine over HTTP/JSON.
type EngineClient struct {
	baseURL string
	http    *http.Client
}

func NewEngineClient(addr string) *EngineClient {
	return &EngineClient{
		baseURL: "http://" + addr,
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

type OrderRequest struct {
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Price         string `json:"price"`
	Quantity      string `json:"quantity"`
	TimeInForce   string `json:"time_in_force"`
	AccountID     uint64 `json:"account_id"`
	ClientOrderID string `json:"client_order_id"`
}

type Trade struct {
	TradeID        uint64 `json:"trade_id"`
	Symbol         string `json:"symbol"`
	Price          string `json:"price"`
	Quantity       string `json:"quantity"`
	MakerOrderID   uint64 `json:"maker_order_id"`
	TakerOrderID   uint64 `json:"taker_order_id"`
	MakerAccountID uint64 `json:"maker_account_id"`
	TakerAccountID uint64 `json:"taker_account_id"`
}

type OrderResponse struct {
	Accepted     bool    `json:"accepted"`
	OrderID      uint64  `json:"order_id"`
	Status       string  `json:"status"`
	RejectReason string  `json:"reject_reason"`
	Trades       []Trade `json:"trades"`
}

func (c *EngineClient) PlaceOrder(ctx context.Context, req OrderRequest) (OrderResponse, error) {
	start := time.Now()
	defer func() {
		matchingLatencyNs.Set(float64(time.Since(start).Nanoseconds()), "place")
	}()
	var out OrderResponse
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/orders", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

// CancelOrder cancels a resting order. It returns (true, nil) when the engine
// confirmed the cancellation, (false, nil) when the engine could not cancel it
// (unknown/already-terminal order), and (false, err) on transport failure.
func (c *EngineClient) CancelOrder(ctx context.Context, id uint64, symbol string) (bool, error) {
	start := time.Now()
	defer func() {
		matchingLatencyNs.Set(float64(time.Since(start).Nanoseconds()), "cancel")
	}()
	u := fmt.Sprintf("%s/orders?id=%d&symbol=%s", c.baseURL, id, url.QueryEscape(symbol))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Success, nil
}

type BookSnapshot struct {
	Symbol string     `json:"symbol"`
	Bids   [][]string `json:"bids"`
	Asks   [][]string `json:"asks"`
}

func (c *EngineClient) OrderBook(ctx context.Context, symbol string, depth uint32) (BookSnapshot, error) {
	start := time.Now()
	defer func() {
		matchingLatencyNs.Set(float64(time.Since(start).Nanoseconds()), "book")
	}()
	var out BookSnapshot
	u := fmt.Sprintf("%s/book?symbol=%s&depth=%d", c.baseURL, url.QueryEscape(symbol), depth)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return out, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

type Ticker struct {
	Symbol    string `json:"symbol"`
	LastPrice string `json:"last_price"`
	BestBid   string `json:"best_bid"`
	BestAsk   string `json:"best_ask"`
}

func (c *EngineClient) Ticker(ctx context.Context, symbol string) (Ticker, error) {
	start := time.Now()
	defer func() {
		matchingLatencyNs.Set(float64(time.Since(start).Nanoseconds()), "ticker")
	}()
	var out Ticker
	u := fmt.Sprintf("%s/ticker?symbol=%s", c.baseURL, url.QueryEscape(symbol))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return out, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}
