package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// GetFeeTiers handles GET /fee-tiers (business group 5: Incentives & Fees).
func (h *Handler) GetFeeTiers(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	tier, err := h.Promotion.GetFeeTiers(r.Context(), acct)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "promotion service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, tier)
}

// GetLeaderboard handles GET /leaderboard.
func (h *Handler) GetLeaderboard(w http.ResponseWriter, r *http.Request) {
	entries, err := h.Promotion.GetLeaderboard(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "promotion service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}

// RedeemVoucher handles POST /redeem-voucher. A cash voucher credits the
// account's CASH balance in both the risk engine and the settlement ledger so
// the claimed credited_amount is real, not just cosmetic.
func (h *Handler) RedeemVoucher(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	var req struct {
		VoucherCode string `json:"voucher_code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	resp, err := h.Promotion.RedeemVoucher(r.Context(), acct, req.VoucherCode)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "promotion service unavailable")
		return
	}
	if resp.Success {
		if amount, ok := parseMoney(resp.CreditedAmount); ok && amount > 0 {
			if derr := h.Risk.Deposit(r.Context(), acct, "CASH", resp.CreditedAmount); derr != nil {
				slog.ErrorContext(r.Context(), "voucher risk deposit failed", "account_id", acct, "err", derr)
			}
			_ = h.Settlement.Deposit(r.Context(), acct, "CASH", resp.CreditedAmount)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
