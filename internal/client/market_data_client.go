package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// MarketDataClient proxies read-only market-data requests to market-data-service.
type MarketDataClient struct {
	baseURL string
	http    *http.Client
}

func NewMarketDataClient(addr string) *MarketDataClient {
	return &MarketDataClient{
		baseURL: "http://" + addr,
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// get performs a GET and returns the raw response body/status so the gateway
// proxies the downstream payload through unchanged (identical JSON contract).
func (c *MarketDataClient) get(ctx context.Context, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func (c *MarketDataClient) Ticker(ctx context.Context, symbol string) (int, []byte, error) {
	return c.get(ctx, "/ticker?symbol="+url.QueryEscape(symbol))
}

func (c *MarketDataClient) OrderBook(ctx context.Context, symbol string, depth uint32) (int, []byte, error) {
	return c.get(ctx, fmt.Sprintf("/book?symbol=%s&depth=%d", url.QueryEscape(symbol), depth))
}

func (c *MarketDataClient) TradesTape(ctx context.Context, symbol string) (int, []byte, error) {
	return c.get(ctx, "/trades-tape?symbol="+url.QueryEscape(symbol))
}

func (c *MarketDataClient) OHLCV(ctx context.Context, symbol, interval string, limit uint32) (int, []byte, error) {
	return c.get(ctx, fmt.Sprintf("/ohlcv?symbol=%s&interval=%s&limit=%d", url.QueryEscape(symbol), url.QueryEscape(interval), limit))
}
