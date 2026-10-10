package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// PromotionClient talks to the Node.js promotion service over HTTP/JSON.
type PromotionClient struct {
	baseURL string
	http    *http.Client
}

func NewPromotionClient(addr string) *PromotionClient {
	return &PromotionClient{
		baseURL: "http://" + addr,
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

type FeeTier struct {
	Volume30d string `json:"volume_30d"`
	Tier      string `json:"current_tier"`
	MakerFee  string `json:"maker_fee"`
	TakerFee  string `json:"taker_fee"`
}

func (c *PromotionClient) GetFeeTiers(ctx context.Context, accountID uint64) (FeeTier, error) {
	var out FeeTier
	u := fmt.Sprintf("%s/fee-tiers?account_id=%d", c.baseURL, accountID)
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

type LeaderboardEntry struct {
	AccountID uint64 `json:"account_id"`
	Name      string `json:"name"`
	ROI       string `json:"roi"`
	Volume    string `json:"volume"`
	Rank      uint32 `json:"rank"`
}

func (c *PromotionClient) GetLeaderboard(ctx context.Context) ([]LeaderboardEntry, error) {
	var out []LeaderboardEntry
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/leaderboard", nil)
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

type RedeemVoucherResponse struct {
	Success        bool   `json:"success"`
	Reason         string `json:"reason"`
	CreditedAmount string `json:"credited_amount"`
}

func (c *PromotionClient) RedeemVoucher(ctx context.Context, accountID uint64, code string) (RedeemVoucherResponse, error) {
	var out RedeemVoucherResponse
	body, _ := json.Marshal(map[string]interface{}{
		"account_id":   accountID,
		"voucher_code": code,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/redeem-voucher", bytes.NewReader(body))
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
