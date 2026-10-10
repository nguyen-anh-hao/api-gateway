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

// SettlementClient talks to the Java clearing & settlement service over HTTP/JSON.
type SettlementClient struct {
	baseURL string
	http    *http.Client
}

func NewSettlementClient(addr string) *SettlementClient {
	return &SettlementClient{
		baseURL: "http://" + addr,
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

type LedgerEntry struct {
	EntryID   uint64 `json:"entry_id"`
	Asset     string `json:"asset"`
	Debit     string `json:"debit"`
	Credit    string `json:"credit"`
	Reference string `json:"reference"`
	Reason    string `json:"reason"`
	Timestamp int64  `json:"timestamp_ns"`
}

func (c *SettlementClient) GetLedger(ctx context.Context, accountID uint64) ([]LedgerEntry, error) {
	var out []LedgerEntry
	u := fmt.Sprintf("%s/ledger?account_id=%d", c.baseURL, accountID)
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

func (c *SettlementClient) Deposit(ctx context.Context, accountID uint64, asset, amount string) error {
	return c.mutation(ctx, "/deposit", accountID, asset, amount)
}

func (c *SettlementClient) Withdraw(ctx context.Context, accountID uint64, asset, amount string) error {
	return c.mutation(ctx, "/withdraw", accountID, asset, amount)
}

// SettleRequest is the trade to clear & settle. BuyerFee/SellerFee are the
// cash fees charged to each side (maker fee vs taker fee), so the settlement
// service can book a balanced double-entry journal plus a fee-revenue entry.
type SettleRequest struct {
	TradeID        uint64 `json:"trade_id"`
	Symbol         string `json:"symbol"`
	Price          string `json:"price"`
	Quantity       string `json:"quantity"`
	BuyerFee       string `json:"buyer_fee"`
	SellerFee      string `json:"seller_fee"`
	MakerOrderID   uint64 `json:"maker_order_id"`
	TakerOrderID   uint64 `json:"taker_order_id"`
	MakerAccountID uint64 `json:"maker_account_id"`
	TakerAccountID uint64 `json:"taker_account_id"`
	BuyerAccountID uint64 `json:"buyer_account_id"`
	SellerAccountID uint64 `json:"seller_account_id"`
}

// Settle posts a filled trade to the clearing & settlement service.
func (c *SettlementClient) Settle(ctx context.Context, req SettleRequest) error {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/settle", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *SettlementClient) mutation(ctx context.Context, path string, accountID uint64, asset, amount string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"account_id": accountID,
		"asset":      asset,
		"amount":     amount,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

type Position struct {
	Symbol         string `json:"symbol"`
	Available      string `json:"available_quantity"`
	Pending        string `json:"pending_quantity"`
	AverageCost    string `json:"average_cost"`
	MarketPrice    string `json:"market_price"`
	UnrealizedPnL  string `json:"unrealized_pnl"`
}

func (c *SettlementClient) GetPositions(ctx context.Context, accountID uint64) ([]Position, error) {
	var out []Position
	u := fmt.Sprintf("%s/portfolio?account_id=%d", c.baseURL, accountID)
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

// PnL is the realized + unrealized profit/loss for an account.
type PnL struct {
	RealizedPnl   string `json:"realized_pnl"`
	UnrealizedPnl string `json:"unrealized_pnl"`
}

func (c *SettlementClient) GetPnL(ctx context.Context, accountID uint64) (PnL, error) {
	var out PnL
	u := fmt.Sprintf("%s/pnl?account_id=%d", c.baseURL, accountID)
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
