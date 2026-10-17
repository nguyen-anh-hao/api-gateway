package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/nguyen-anh-hao/api-gateway/internal/client"
	"github.com/nguyen-anh-hao/api-gateway/internal/handler"
	"github.com/nguyen-anh-hao/api-gateway/internal/metrics"
	"github.com/nguyen-anh-hao/api-gateway/internal/middleware"
	"github.com/nguyen-anh-hao/api-gateway/internal/proxy"
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
	risk := client.NewRiskClient(env("RISK_ENGINE_ADDR", "localhost:9002"))
	settlement := client.NewSettlementClient(env("SETTLEMENT_ADDR", "localhost:8083"))
	promotion := client.NewPromotionClient(env("PROMOTION_ADDR", "localhost:8084"))
	marketdata := client.NewMarketDataClient(env("MARKET_DATA_ADDR", "localhost:8085"))

	rdb := storage.NewRedisClient()
	if rdb == nil {
		slog.Error("REDIS_ADDR is required for stateless api-gateway operation")
		os.Exit(1)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	pingErr := rdb.Ping(pingCtx).Err()
	cancel()
	if pingErr != nil {
		slog.Error("redis unavailable; api-gateway requires shared state", "err", pingErr)
		os.Exit(1)
	}
	auth := middleware.NewAuthManager(rdb)
	h := handler.New(risk, settlement, promotion, marketdata, auth)

	// Thin upstream proxies: order lifecycle/Oms lives in order-service; the
	// live market-data stream (WebSocket) lives in market-data-service.
	internalToken := env("INTERNAL_API_TOKEN", "")
	orderProxy := proxy.New(env("ORDER_SERVICE_ADDR", "localhost:8087"), internalToken)
	marketStreamProxy := proxy.New(env("MARKET_DATA_ADDR", "localhost:8085"), internalToken)

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

	// 2. Order Execution & Lifecycle (order-service owns the domain logic).
	mux.Handle("POST /orders", middleware.RequirePermission("trade", orderProxy))
	mux.Handle("DELETE /orders", middleware.RequirePermission("trade", orderProxy))
	mux.Handle("POST /orders/bulk-cancel", middleware.RequirePermission("trade", orderProxy))
	mux.Handle("POST /orders/modify", middleware.RequirePermission("trade", orderProxy))
	mux.Handle("GET /orders/open", orderProxy)
	mux.Handle("GET /orders/history", orderProxy)
	mux.Handle("GET /trades", orderProxy)

	// 3. Market Data (HTTP read-through; live stream is /ws below).
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

	// /health, /metrics, /swagger, account registration and the public
	// market-data WebSocket stream are outside the authenticated API surface.
	root := http.NewServeMux()
	swagger.Register(root)
	root.HandleFunc("POST /accounts", h.CreateAccount)
	root.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	root.Handle("GET /metrics", metrics.Default.Handler())
	root.Handle("/ws", marketStreamProxy)
	root.Handle("/", protected)

	addr := ":" + env("API_GATEWAY_PORT", "8080")
	slog.Info("api-gateway listening", "addr", addr)
	if err := http.ListenAndServe(addr, root); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
