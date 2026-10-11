package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/nguyen-anh-hao/api-gateway/internal/client"
)

type placeOrderReq struct {
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Price         string `json:"price"`
	Quantity      string `json:"quantity"`
	TimeInForce   string `json:"time_in_force"`
	ClientOrderID string `json:"client_order_id"`
}

// PlaceOrder handles POST /orders (business group 2: Order Execution).
func (h *Handler) PlaceOrder(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	var req placeOrderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Side == "" {
		req.Side = "BUY"
	}
	if req.Type == "" {
		req.Type = "LIMIT"
	}
	if req.TimeInForce == "" {
		req.TimeInForce = "GTC"
	}

	// Resolve a reference price for risk. LIMIT orders use their own price;
	// MARKET orders have no price, so use the current best bid/ask as the
	// worst-case reference (this also rejects market orders with no liquidity).
	refPrice := req.Price
	if strings.EqualFold(req.Type, "MARKET") {
		t, err := h.Engine.Ticker(r.Context(), req.Symbol)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "matching engine unavailable")
			return
		}
		if strings.EqualFold(req.Side, "BUY") {
			refPrice = t.BestAsk
		} else {
			refPrice = t.BestBid
		}
		if refPrice == "" || refPrice == "0" {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"accepted": false, "reject_reason": "market order: no liquidity",
			})
			return
		}
	}

	refP, refOK := parseMoney(refPrice)
	qtyF, qtyOK := parseMoney(req.Quantity)
	if !qtyOK || qtyF <= 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"accepted": false, "reject_reason": "quantity must be positive",
		})
		return
	}
	if !refOK || refP <= 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"accepted": false, "reject_reason": "order requires a valid price",
		})
		return
	}

	// The incoming order is always the taker; reserve worst-case cash including
	// the taker fee so the account can never go negative after settlement.
	rates := h.feeRatesFor(r.Context(), acct)
	feeEstimate := refP * qtyF * rates.taker
	ckReq := client.CheckRequest{
		AccountID: acct, Symbol: req.Symbol, Side: req.Side,
		Type: req.Type, Price: refPrice, Quantity: req.Quantity,
		Fee: fmtMoney(feeEstimate),
	}

	// 1. Pre-trade risk check (margin/buying power).
	chk, err := h.Risk.Check(r.Context(), ckReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "risk engine unavailable")
		return
	}
	if !chk.Approved {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"accepted": false, "reject_reason": chk.RejectReason,
		})
		return
	}

	// 2. Freeze the worst-case cost (notional + fee).
	if err := h.Risk.Reserve(r.Context(), ckReq); err != nil {
		writeErr(w, http.StatusBadGateway, "risk reserve failed")
		return
	}

	// 3. Submit to the matching engine (raw price, empty for MARKET).
	resp, err := h.Engine.PlaceOrder(r.Context(), client.OrderRequest{
		Symbol: req.Symbol, Side: req.Side, Type: req.Type,
		Price: req.Price, Quantity: req.Quantity, TimeInForce: req.TimeInForce,
		AccountID: acct, ClientOrderID: req.ClientOrderID,
	})
	if err != nil {
		_ = h.Risk.Release(r.Context(), ckReq)
		slog.ErrorContext(r.Context(), "matching engine error", "err", err)
		writeErr(w, http.StatusBadGateway, "matching engine unavailable")
		return
	}
	if !resp.Accepted {
		_ = h.Risk.Release(r.Context(), ckReq)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 4. Settle each fill: consume frozen funds (risk) + double-entry (settlement).
	if err := h.settleTrades(r.Context(), resp.Trades, req.Side, refPrice, rates.taker); err != nil {
		slog.ErrorContext(r.Context(), "settlement failed", "order_id", resp.OrderID, "err", err)
	}

	// 5. Track remaining frozen for a resting order so cancel can release it.
	if resp.Status == "NEW" || resp.Status == "PARTIALLY_FILLED" {
		remaining := qtyF
		for _, t := range resp.Trades {
			if q, ok := parseMoney(t.Quantity); ok {
				remaining -= q
			}
		}
		if remaining > 0 {
			h.mu.Lock()
			h.reserved[resp.OrderID] = reservedAmt{side: req.Side, price: refP, rate: rates.taker, qty: remaining, symbol: req.Symbol}
			h.mu.Unlock()
		}
	}

	// 6. Record order + fills in the in-memory read model.
	h.recordOrder(acct, &OrderView{
		OrderID: resp.OrderID, Symbol: req.Symbol, Side: req.Side, Type: req.Type,
		Price: req.Price, Quantity: req.Quantity, ExecutedQuantity: executedQty(resp.Trades),
		Status: resp.Status, Timestamp: nowNs(),
	})
	for _, t := range resp.Trades {
		h.recordTrade(TradeView{
			TradeID: t.TradeID, Symbol: t.Symbol, Price: t.Price,
			Quantity: t.Quantity, Timestamp: nowNs(),
		}, t.MakerAccountID, t.TakerAccountID, t.MakerOrderID, t.TakerOrderID)
	}

	// Record slippage (basis points) for limit orders.
	if req.Type == "LIMIT" && req.Price != "" && len(resp.Trades) > 0 {
		if limit, err := strconv.ParseFloat(req.Price, 64); err == nil && limit > 0 {
			var notional, qty float64
			for _, t := range resp.Trades {
				p, _ := strconv.ParseFloat(t.Price, 64)
				q, _ := strconv.ParseFloat(t.Quantity, 64)
				notional += p * q
				qty += q
			}
			if qty > 0 {
				avg := notional / qty
				bps := (avg - limit) / limit * 10000
				if req.Side == "SELL" {
					bps = (limit - avg) / limit * 10000
				}
				slippageBps.Set(bps, req.Symbol)
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// CancelOrder handles DELETE /orders?id=&symbol= (business group 2).
func (h *Handler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	id, _ := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
	symbol := r.URL.Query().Get("symbol")

	// Ownership check: the order must belong to the authenticated account.
	if !h.ownsOrder(acct, id) {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"success": false, "reason": "order not found"})
		return
	}

	ok, err := h.Engine.CancelOrder(r.Context(), id, symbol)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "matching engine unavailable")
		return
	}
	if ok {
		h.releaseReserved(r.Context(), acct, id)
		h.markCancelled(acct, id)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": ok})
}

// BulkCancel handles POST /orders/bulk-cancel (business group 2).
func (h *Handler) BulkCancel(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	var req struct {
		Symbol string `json:"symbol"`
		Side   string `json:"side"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	h.mu.RLock()
	orders := h.orders[acct]
	var targets []struct {
		id     uint64
		symbol string
	}
	for id, o := range orders {
		if o.Status != "NEW" && o.Status != "PARTIALLY_FILLED" {
			continue
		}
		if req.Symbol != "" && o.Symbol != req.Symbol {
			continue
		}
		if req.Side != "" && !strings.EqualFold(o.Side, req.Side) {
			continue
		}
		targets = append(targets, struct {
			id     uint64
			symbol string
		}{id, o.Symbol})
	}
	h.mu.RUnlock()

	cancelled := 0
	for _, t := range targets {
		if ok, err := h.Engine.CancelOrder(r.Context(), t.id, t.symbol); err == nil && ok {
			h.releaseReserved(r.Context(), acct, t.id)
			h.markCancelled(acct, t.id)
			cancelled++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"cancelled_count": cancelled})
}

// ModifyOrder handles POST /orders/modify as an atomic cancel-and-replace: the
// old order is cancelled and its funds released, then the replacement order goes
// through the full check/reserve/settle flow so any immediate fills are settled.
func (h *Handler) ModifyOrder(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	var req struct {
		OrderID     uint64 `json:"order_id"`
		NewPrice    string `json:"new_price"`
		NewQuantity string `json:"new_quantity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	h.mu.RLock()
	old := h.orders[acct][req.OrderID]
	h.mu.RUnlock()
	if old == nil {
		writeErr(w, http.StatusNotFound, "order not found")
		return
	}

	// Cancel and release the old order (ignore failure to cancel an already-done order).
	if ok, err := h.Engine.CancelOrder(r.Context(), req.OrderID, old.Symbol); err == nil && ok {
		h.releaseReserved(r.Context(), acct, req.OrderID)
	}
	h.markCancelled(acct, req.OrderID)

	// Validate the replacement parameters.
	price := req.NewPrice
	if price == "" {
		price = old.Price
	}
	qty := req.NewQuantity
	if qty == "" {
		qty = old.Quantity
	}
	newP, newPOK := parseMoney(price)
	newQ, newQOK := parseMoney(qty)
	if !newPOK || newP <= 0 || !newQOK || newQ <= 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "reason": "invalid price or quantity"})
		return
	}

	rates := h.feeRatesFor(r.Context(), acct)
	feeEstimate := newP * newQ * rates.taker
	ckReq := client.CheckRequest{
		AccountID: acct, Symbol: old.Symbol, Side: old.Side,
		Type: old.Type, Price: price, Quantity: qty, Fee: fmtMoney(feeEstimate),
	}

	chk, err := h.Risk.Check(r.Context(), ckReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "risk engine unavailable")
		return
	}
	if !chk.Approved {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "reason": chk.RejectReason})
		return
	}
	if err := h.Risk.Reserve(r.Context(), ckReq); err != nil {
		writeErr(w, http.StatusBadGateway, "risk reserve failed")
		return
	}

	resp, err := h.Engine.PlaceOrder(r.Context(), client.OrderRequest{
		Symbol: old.Symbol, Side: old.Side, Type: old.Type,
		Price: price, Quantity: qty, TimeInForce: "GTC",
		AccountID: acct,
	})
	if err != nil {
		_ = h.Risk.Release(r.Context(), ckReq)
		writeErr(w, http.StatusBadGateway, "matching engine unavailable")
		return
	}
	if !resp.Accepted {
		_ = h.Risk.Release(r.Context(), ckReq)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "reason": resp.RejectReason})
		return
	}

	if err := h.settleTrades(r.Context(), resp.Trades, old.Side, price, rates.taker); err != nil {
		slog.ErrorContext(r.Context(), "modify settlement failed", "order_id", resp.OrderID, "err", err)
	}

	if resp.Status == "NEW" || resp.Status == "PARTIALLY_FILLED" {
		remaining := newQ
		for _, t := range resp.Trades {
			if q, ok := parseMoney(t.Quantity); ok {
				remaining -= q
			}
		}
		if remaining > 0 {
			h.mu.Lock()
			h.reserved[resp.OrderID] = reservedAmt{side: old.Side, price: newP, rate: rates.taker, qty: remaining, symbol: old.Symbol}
			h.mu.Unlock()
		}
	}

	h.recordOrder(acct, &OrderView{
		OrderID: resp.OrderID, Symbol: old.Symbol, Side: old.Side, Type: old.Type,
		Price: price, Quantity: qty, ExecutedQuantity: executedQty(resp.Trades),
		Status: resp.Status, Timestamp: nowNs(),
	})
	for _, t := range resp.Trades {
		h.recordTrade(TradeView{
			TradeID: t.TradeID, Symbol: t.Symbol, Price: t.Price,
			Quantity: t.Quantity, Timestamp: nowNs(),
		}, t.MakerAccountID, t.TakerAccountID, t.MakerOrderID, t.TakerOrderID)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": resp.Accepted, "new_order_id": resp.OrderID,
	})
}

// GetOpenOrders handles GET /orders/open.
func (h *Handler) GetOpenOrders(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	symbol := r.URL.Query().Get("symbol")
	out := []OrderView{}
	h.mu.RLock()
	for _, o := range h.orders[acct] {
		if o.Status != "NEW" && o.Status != "PARTIALLY_FILLED" {
			continue
		}
		if symbol != "" && o.Symbol != symbol {
			continue
		}
		out = append(out, *o)
	}
	h.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"orders": out})
}

// GetOrderHistory handles GET /orders/history.
func (h *Handler) GetOrderHistory(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	symbol := r.URL.Query().Get("symbol")
	status := r.URL.Query().Get("status")
	out := []OrderView{}
	h.mu.RLock()
	for _, o := range h.orders[acct] {
		if o.Status == "NEW" || o.Status == "PARTIALLY_FILLED" {
			continue
		}
		if symbol != "" && o.Symbol != symbol {
			continue
		}
		if status != "" && o.Status != status {
			continue
		}
		out = append(out, *o)
	}
	h.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"orders": out})
}

// GetTrades handles GET /trades.
func (h *Handler) GetTrades(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	symbol := r.URL.Query().Get("symbol")
	orderID, _ := strconv.ParseUint(r.URL.Query().Get("order_id"), 10, 64)
	out := []TradeView{}
	h.mu.RLock()
	for _, t := range h.trades[acct] {
		if symbol != "" && t.Symbol != symbol {
			continue
		}
		if orderID != 0 && t.OrderID != orderID {
			continue
		}
		out = append(out, t)
	}
	h.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"trades": out})
}

func (h *Handler) markCancelled(acct uint64, id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if o := h.orders[acct][id]; o != nil {
		o.Status = "CANCELLED"
	}
}

func executedQty(trades []client.Trade) string {
	sum := 0.0
	for _, t := range trades {
		q, _ := strconv.ParseFloat(t.Quantity, 64)
		sum += q
	}
	return strconv.FormatFloat(sum, 'f', -1, 64)
}

// settleTrades settles every fill of an order, returning the first error (if any).
func (h *Handler) settleTrades(ctx context.Context, trades []client.Trade, takerSide, takerRefPrice string, takerRate float64) error {
	var firstErr error
	for _, t := range trades {
		if err := h.settleTrade(ctx, t, takerSide, takerRefPrice, takerRate); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// settleTrade applies a filled trade to both counterparties. Each side is
// charged its own role-based fee (maker vs taker), and each BUY side releases
// the exact cash amount it had frozen at order time, so price improvement and
// the maker/taker fee difference flow back to the available balance.
func (h *Handler) settleTrade(ctx context.Context, t client.Trade, takerSide, takerRefPrice string, takerRate float64) error {
	var buyer, seller uint64
	buyerIsMaker := false
	if strings.EqualFold(takerSide, "BUY") {
		buyer, seller = t.TakerAccountID, t.MakerAccountID
		buyerIsMaker = false
	} else {
		buyer, seller = t.MakerAccountID, t.TakerAccountID
		buyerIsMaker = true
	}
	sellerIsMaker := !buyerIsMaker

	priceF, _ := parseMoney(t.Price)
	qtyF, _ := parseMoney(t.Quantity)

	// Per-side fee rate by liquidity role.
	buyerRates := h.feeRatesFor(ctx, buyer)
	sellerRates := h.feeRatesFor(ctx, seller)
	buyerRate := buyerRates.taker
	if buyerIsMaker {
		buyerRate = buyerRates.maker
	}
	sellerRate := sellerRates.taker
	if sellerIsMaker {
		sellerRate = sellerRates.maker
	}
	buyerFee := computeFee(t.Price, t.Quantity, buyerRate)
	sellerFee := computeFee(t.Price, t.Quantity, sellerRate)

	// Reserved cash for the BUY side (the SELL side's reserved stock is handled
	// inside Apply and needs no cash "reserved" value).
	var buyerReserved float64
	if buyerIsMaker {
		buyerReserved = h.makerReservedFor(t.MakerOrderID, qtyF, priceF, buyerRates.taker)
	} else {
		takerRefP, _ := parseMoney(takerRefPrice)
		buyerReserved = takerRefP * qtyF * (1 + takerRate)
	}

	if err := h.Risk.Apply(ctx, client.ApplyRequest{
		AccountID: buyer, Symbol: t.Symbol, Side: "BUY",
		Price: t.Price, Quantity: t.Quantity, Fee: buyerFee, Reserved: fmtMoney(buyerReserved),
	}); err != nil {
		return err
	}
	if err := h.Risk.Apply(ctx, client.ApplyRequest{
		AccountID: seller, Symbol: t.Symbol, Side: "SELL",
		Price: t.Price, Quantity: t.Quantity, Fee: sellerFee,
	}); err != nil {
		return err
	}

	// Consume the maker's reserved quantity for this fill (the maker is always
	// the resting order, t.MakerOrderID; the taker's reservation is reconciled
	// separately when PlaceOrder records the remaining resting quantity).
	h.consumeReserved(t.MakerOrderID, qtyF)

	if err := h.Settlement.Settle(ctx, client.SettleRequest{
		TradeID: t.TradeID, Symbol: t.Symbol, Price: t.Price, Quantity: t.Quantity,
		BuyerFee: buyerFee, SellerFee: sellerFee,
		MakerOrderID: t.MakerOrderID, TakerOrderID: t.TakerOrderID,
		MakerAccountID: t.MakerAccountID, TakerAccountID: t.TakerAccountID,
		BuyerAccountID: buyer, SellerAccountID: seller,
	}); err != nil {
		return err
	}
	return nil
}

// makerReservedFor returns the exact cash the maker had frozen for `qty` at its
// order time. Falls back to the fill price when the entry is unavailable (e.g.
// a pre-restart order), which avoids under-releasing frozen funds.
func (h *Handler) makerReservedFor(orderID uint64, qty, fallbackPrice, fallbackRate float64) float64 {
	h.mu.RLock()
	ra, ok := h.reserved[orderID]
	h.mu.RUnlock()
	if !ok {
		return fallbackPrice * qty * (1 + fallbackRate)
	}
	return ra.price * qty * (1 + ra.rate)
}

// consumeReserved reduces a resting maker order's reserved quantity by the fill
// amount, removing the entry once fully consumed.
func (h *Handler) consumeReserved(orderID uint64, qty float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ra, ok := h.reserved[orderID]; ok {
		ra.qty -= qty
		if ra.qty <= 0 {
			delete(h.reserved, orderID)
		} else {
			h.reserved[orderID] = ra
		}
	}
}

// ownsOrder reports whether the order belongs to the account (ownership check).
func (h *Handler) ownsOrder(account uint64, orderID uint64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.orders[account] != nil && h.orders[account][orderID] != nil
}

// releaseReserved unfreezes the remaining reserved funds for a cancelled order.
func (h *Handler) releaseReserved(ctx context.Context, account uint64, orderID uint64) {
	h.mu.Lock()
	ra, ok := h.reserved[orderID]
	delete(h.reserved, orderID)
	h.mu.Unlock()
	if !ok {
		return
	}
	fee := "0"
	if strings.EqualFold(ra.side, "BUY") {
		fee = fmtMoney(ra.price * ra.qty * ra.rate)
	}
	_ = h.Risk.Release(ctx, client.CheckRequest{
		AccountID: account,
		Symbol:    ra.symbol,
		Side:      ra.side,
		Price:     fmtMoney(ra.price),
		Quantity:  fmtMoney(ra.qty),
		Fee:       fee,
	})
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

// fmtMoney formats a float64 as a fixed-point money string (8 decimals).
func fmtMoney(v float64) string {
	return strconv.FormatFloat(v, 'f', 8, 64)
}

