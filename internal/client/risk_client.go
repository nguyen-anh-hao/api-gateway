package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// RiskClient talks to the Go risk engine over HTTP/JSON.
type RiskClient struct {
	baseURL string
	http    *http.Client
}

func NewRiskClient(addr string) *RiskClient {
	return &RiskClient{
		baseURL: "http://" + addr,
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

type CheckRequest struct {
	AccountID uint64 `json:"account_id"`
	Symbol    string `json:"symbol"`
	Side      string `json:"side"`
	Type      string `json:"type"`
	Price     string `json:"price"`
	Quantity  string `json:"quantity"`
	Fee       string `json:"fee"`
}

type CheckResponse struct {
	Approved     bool   `json:"approved"`
	RejectReason string `json:"reject_reason"`
	BuyingPower  string `json:"buying_power"`
}

type AssetBalance struct {
	Asset     string `json:"asset"`
	Total     string `json:"total"`
	Available string `json:"available"`
	Frozen    string `json:"frozen"`
}

// do builds the request from ctx, injects W3C trace context and executes it.
func (c *RiskClient) do(ctx context.Context, method, path string, payload interface{}, out interface{}) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("risk engine: %s: %s", resp.Status, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *RiskClient) Check(ctx context.Context, req CheckRequest) (CheckResponse, error) {
	var out CheckResponse
	err := c.do(ctx, http.MethodPost, "/check", req, &out)
	return out, err
}

func (c *RiskClient) Reserve(ctx context.Context, req CheckRequest) error {
	return c.do(ctx, http.MethodPost, "/reserve", req, nil)
}

func (c *RiskClient) Release(ctx context.Context, req CheckRequest) error {
	return c.do(ctx, http.MethodPost, "/release", req, nil)
}

// ApplyRequest settles a filled trade onto the shadow balances.
type ApplyRequest struct {
	AccountID uint64 `json:"account_id"`
	Symbol    string `json:"symbol"`
	Side      string `json:"side"`
	Price     string `json:"price"`
	Quantity  string `json:"quantity"`
	Fee       string `json:"fee"`
	Reserved  string `json:"reserved"`
}

// Apply settles a single side of a trade (BUY: cash out / stock in; SELL: cash in / stock out).
func (c *RiskClient) Apply(ctx context.Context, req ApplyRequest) error {
	return c.do(ctx, http.MethodPost, "/apply", req, nil)
}

func (c *RiskClient) GetBalances(ctx context.Context, accountID uint64) ([]AssetBalance, error) {
	var out []AssetBalance
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/balances?account_id=%d", accountID), nil, &out)
	return out, err
}

func (c *RiskClient) Deposit(ctx context.Context, accountID uint64, asset, amount string) error {
	return c.do(ctx, http.MethodPost, "/deposit", map[string]interface{}{
		"account_id": accountID,
		"asset":      asset,
		"amount":     amount,
	}, nil)
}

func (c *RiskClient) Withdraw(ctx context.Context, accountID uint64, asset, amount string) error {
	return c.do(ctx, http.MethodPost, "/withdraw", map[string]interface{}{
		"account_id": accountID,
		"asset":      asset,
		"amount":     amount,
	}, nil)
}
