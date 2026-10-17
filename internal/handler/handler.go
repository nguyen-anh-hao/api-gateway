// Package handler contains the gateway's thin, non-domain HTTP handlers.
// Order lifecycle/Oms logic lives in order-service; the gateway only proxies to
// it. What remains here is account/treasury fan-out, incentives and market-data
// read-through.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/nguyen-anh-hao/api-gateway/internal/client"
	"github.com/nguyen-anh-hao/api-gateway/internal/middleware"
)

// Handler holds the downstream clients used by the gateway's thin BFF handlers.
type Handler struct {
	Risk       *client.RiskClient
	Settlement *client.SettlementClient
	Promotion  *client.PromotionClient
	MarketData *client.MarketDataClient
	Auth       *middleware.AuthManager
}

func New(r *client.RiskClient, s *client.SettlementClient, p *client.PromotionClient, md *client.MarketDataClient, auth *middleware.AuthManager) *Handler {
	return &Handler{
		Risk:       r,
		Settlement: s,
		Promotion:  p,
		MarketData: md,
		Auth:       auth,
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

// parseMoney parses a JSON money string to float64, reporting validity.
func parseMoney(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
