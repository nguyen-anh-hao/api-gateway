# API Gateway (`api-gateway`)

> **Edge Proxy, Authentication, Rate-Limiting & BFF Orchestration** written in Go.

---

## 1. Primary Responsibility
The Gateway is the unified entry point for clients (Web UI, Mobile, Algorithmic Bots). It handles:
1. **Security & Ingress:** JWT authentication, API Key validation, and per-account Token Bucket rate limiting.
2. **Request Routing:** Validates requests and orchestrates downstream calls to `risk-engine`, `matching-engine`, `clearing-settlement`, and `promotion-service`.
3. **Market Data Proxy:** Forwards `/ticker`, `/book`, `/trades-tape` and `/ohlcv` reads to `market-data-service` (no market-data storage, tape or OHLCV aggregation lives here).
4. **Trade Event Producer:** Publishes each executed trade to NATS JetStream (`market.trades.executed`) for downstream consumers.

---

## 2. Boundaries & Downstream Services
- **Downstream RPCs:**
  - `matching-engine:9001` (Order placement & cancellation)
  - `risk-engine:9002` (Pre-trade checks & balance queries)
  - `clearing-settlement:8083` (Ledger, deposits, withdrawals)
  - `promotion-service:8084` (Vouchers & reward tracking)
  - `market-data-service:8085` (Ticker, order book, trade tape, OHLCV)
- **Event Bus (producer):** Publishes executed trades to NATS JetStream subject `market.trades.executed`.
- **Tracing:** OpenTelemetry trace propagation across all downstream HTTP/gRPC calls.

---

## 3. Quick Start (Run in 2 minutes)

```bash
# Set environment variables pointing to local or containerized services
export MATCHING_ENGINE_ADDR="localhost:9001"
export RISK_ENGINE_ADDR="localhost:9002"
export SETTLEMENT_ADDR="localhost:8083"
export PROMOTION_ADDR="localhost:8084"
export NATS_ADDR="localhost:4222"
export MARKET_DATA_ADDR="localhost:8085"
export RATE_LIMIT_PER_SEC="10000"

# Run locally
go run ./cmd/main.go
```

Verify health & OpenAPI documentation:
- Health check: `curl http://localhost:8080/health`
- Swagger UI: Open browser at `http://localhost:8080/swagger/`

---

## 4. Executable Documentation (`requests.http`)

```http
### Health Check
GET http://localhost:8080/health

### Place Limit Buy Order
POST http://localhost:8080/orders
Content-Type: application/json

{
  "account_id": 1001,
  "symbol": "AAPL",
  "side": "BUY",
  "type": "LIMIT",
  "price": "150.00",
  "quantity": "10"
}

### Query Order Book Depth
GET http://localhost:8080/book?symbol=AAPL&depth=20
```

---

## 5. Engineering Roadmap (Concurrency & Network Optimization)

> Market-data concerns (trade tape, OHLCV aggregation, WebSocket streaming) now
> live in `market-data-service`; this roadmap covers only the gateway's own scope:
> ingress, routing, orchestration and event production.

| Version | Phase | Optimization Target |
| :--- | :--- | :--- |
| **v1.0.0** | **Baseline Gateway** | Go standard HTTP server, JSON routing, in-memory rate limiter, OpenTelemetry integration. |
| **v1.0.1** | **Pagination for Orders & Trades** | Add `limit`/`offset` query params to order/history and trades endpoints. |
| **v1.0.2** | **Market-Data Proxy Resilience** | Add timeout, retry and circuit breaker to the `market-data-service` proxy calls. |
| **v1.1.0** | **gRPC Client Pool & Multiplexing** | Replace HTTP/1.1 downstream clients with persistent HTTP/2 gRPC connection pools; keep-alive tuning to eliminate TCP connection churn. |
| **v1.1.1** | **Retry & Circuit Breaker** | Add exponential backoff retries and circuit breaker for downstream service calls. |
| **v1.2.0** | **Trade Event Producer Reliability** | Harden NATS publishing (backpressure, buffering, graceful shutdown) so no trade event is dropped. |
| **v2.0.0** | **Kernel-Bypass / eBPF Gateway** | Offload DDoS mitigation and token-bucket rate limiting to eBPF / XDP. Target ingress latency < 50 µs. |
| **v2.0.1** | **eBPF Rate Limiting Integration** | Deploy eBPF program for per-IP token bucket, integrate with auth middleware. |

---

## 6. API Contract & Code Generation (OpenAPI-first)

The gateway's public API surface is defined **once** in a single source of truth:

- **Spec:** [`internal/swagger/openapi.yaml`](internal/swagger/openapi.yaml)
- **Swagger UI:** served live from that spec at `/swagger/doc.yaml` (no hand-written
  `openapi.json`).
- **Go types:** generated from the spec into `internal/api/types.gen.go`.

Regenerate everything with:

```bash
go generate ./...
```

CI (`.github/workflows/generate-check.yml`) re-runs generation and fails on any
drift, so the spec, generated types and Swagger UI can never silently diverge.


