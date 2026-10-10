package handler

import (
	"net/http"
	"strconv"
)

// GetPositions handles GET /positions (business group 4: Portfolio & PnL).
func (h *Handler) GetPositions(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	positions, err := h.Settlement.GetPositions(r.Context(), acct)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "settlement service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"positions": positions})
}

// GetPnL handles GET /pnl. Realized + unrealized PnL are computed
// authoritatively by the settlement service (cost basis + mark-to-market).
func (h *Handler) GetPnL(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	pnl, err := h.Settlement.GetPnL(r.Context(), acct)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "settlement service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, pnl)
}

// GetBuyingPower handles GET /buying-power. Buying power is approximated from
// the risk engine's available cash balance.
func (h *Handler) GetBuyingPower(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	balances, err := h.Risk.GetBalances(r.Context(), acct)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "risk engine unavailable")
		return
	}
	cash := 0.0
	for _, b := range balances {
		if b.Asset == "CASH" {
			cash, _ = strconv.ParseFloat(b.Available, 64)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cash_balance": cash,
		"margin":       0.0,
		"buying_power": cash,
		"margin_ratio": 0.0,
	})
}
