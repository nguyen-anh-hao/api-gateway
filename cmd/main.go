package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/nguyen-anh-hao/api-gateway/internal/client"
	"github.com/nguyen-anh-hao/api-gateway/internal/eventbus"
	"github.com/nguyen-anh-hao/api-gateway/internal/handler"
	"github.com/nguyen-anh-hao/api-gateway/internal/metrics"
	"github.com/nguyen-anh-hao/api-gateway/internal/middleware"
	"github.com/nguyen-anh-hao/api-gateway/internal/storage"
	"github.com/nguyen-anh-hao/api-gateway/internal/swagger"
	"github.com/nguyen-anh-hao/api-gateway/internal/telemetry"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// rateLimitPerSec reads RATE_LIMIT_PER_SEC (requests/second per client IP).
// Defaults to 10000; 0 or a negative value disables rate limiting entirely.
func rateLimitPerSec() float64 {
	v := os.Getenv("RATE_LIMIT_PER_SEC")
	if v == "" {
		return 10000
	}
	r, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 10000
	}
	return r
}

func main() {
	engine := client.NewEngineClient(env("MATCHING_ENGINE_ADDR", "localhost:9001"))
	risk := client.NewRiskClient(env("RISK_ENGINE_ADDR", "localhost:9002"))
	settlement := client.NewSettlementClient(env("SETTLEMENT_ADDR", "localhost:8083"))
	promotion := client.NewPromotionClient(env("PROMOTION_ADDR", "localhost:8084"))

	rdb := storage.NewRedisClient()
	auth := middleware.NewAuthManager(rdb)
	publisher := eventbus.NewPublisher(env("NATS_ADDR", ""))
	marketdata := client.NewMarketDataClient(env("MARKET_DATA_ADDR", "localhost:8085"))
	h := handler.New(engine, risk, settlement, promotion, marketdata, publisher, auth)

	// 1. Initialize structured logging first (slog -> OTLP gRPC -> Loki, plus JSON stdout).
	logShutdown, err := telemetry.InitLogging(env("OTEL_SERVICE_NAME", "api-gateway"), env("OTEL_EXPORTER_OTLP_ENDPOINT", ""))
	if err != nil {
		slog.Warn("OTLP logging disabled", "err", err)
		logShutdown = func(context.Context) error { return nil }
	}
	defer func() { _ = logShutdown(context.Background()) }()

	// 2. Initialize OpenTelemetry tracing (OTLP -> collector -> Jaeger).
	tpShutdown, err := telemetry.Init(env("OTEL_SERVICE_NAME", "api-gateway"), env("OTEL_EXPORTER_OTLP_ENDPOINT", ""))
	if err != nil {
		slog.Warn("tracing disabled", "err", err)
		tpShutdown = func(context.Context) error { return nil }
	}
	defer func() { _ = tpShutdown(context.Background()) }()

	mux := http.NewServeMux()

	// 1. Treasury & Account (writes are permission-gated).
	mux.HandleFunc("POST /accounts/api-keys", h.CreateApiKey)
	mux.Handle("POST /deposit", middleware.RequirePermission("trade", http.HandlerFunc(h.Deposit)))
	mux.Handle("POST /withdraw", middleware.RequirePermission("withdraw", http.HandlerFunc(h.Withdraw)))
	mux.HandleFunc("GET /balances", h.GetBalances)
	mux.HandleFunc("GET /ledger", h.GetLedger)

	// 2. Order Execution & Lifecycle (writes require the "trade" permission).
	mux.Handle("POST /orders", middleware.RequirePermission("trade", http.HandlerFunc(h.PlaceOrder)))
	mux.Handle("DELETE /orders", middleware.RequirePermission("trade", http.HandlerFunc(h.CancelOrder)))
	mux.Handle("POST /orders/bulk-cancel", middleware.RequirePermission("trade", http.HandlerFunc(h.BulkCancel)))
	mux.Handle("POST /orders/modify", middleware.RequirePermission("trade", http.HandlerFunc(h.ModifyOrder)))
	mux.HandleFunc("GET /orders/open", h.GetOpenOrders)
	mux.HandleFunc("GET /orders/history", h.GetOrderHistory)
	mux.HandleFunc("GET /trades", h.GetTrades)

	// 3. Market Data
	mux.HandleFunc("GET /ticker", h.GetTicker)
	mux.HandleFunc("GET /book", h.GetOrderBook)
	mux.HandleFunc("GET /trades-tape", h.GetTradesTape)
	mux.HandleFunc("GET /ohlcv", h.GetOHLCV)

	// 4. Portfolio & PnL
	mux.HandleFunc("GET /positions", h.GetPositions)
	mux.HandleFunc("GET /pnl", h.GetPnL)
	mux.HandleFunc("GET /buying-power", h.GetBuyingPower)

	// 5. Incentives & Fees
	mux.HandleFunc("GET /fee-tiers", h.GetFeeTiers)
	mux.HandleFunc("GET /leaderboard", h.GetLeaderboard)
	mux.HandleFunc("POST /redeem-voucher", h.RedeemVoucher)

	// Middleware chain (applied outermost-first): CORS -> tracing -> logging -> rate limit -> auth.
	rate := rateLimitPerSec()
	var protected http.Handler = mux
	protected = middleware.Logging(protected)
	if rate > 0 {
		protected = middleware.RateLimiter(rdb, rate, rate)(protected)
	}
	protected = auth.Authenticate(protected)
	protected = middleware.Tracing("api-gateway")(protected)
	protected = middleware.CORS(protected)

	// /health, /metrics, /swagger and account registration are public.
	root := http.NewServeMux()
	swagger.Register(root)
	root.HandleFunc("POST /accounts", h.CreateAccount)
	root.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	root.Handle("GET /metrics", metrics.Default.Handler())
	root.Handle("/", protected)

	addr := ":" + env("API_GATEWAY_PORT", "8080")
	slog.Info("api-gateway listening", "addr", addr)
	if err := http.ListenAndServe(addr, root); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
