# API Gateway (`api-gateway`)

> **Edge Proxy, Authentication, Rate-Limiting & Real-Time Market Data Hub** written in Go.

---

## 1. Primary Responsibility
The Gateway is the unified entry point for clients (Web UI, Mobile, Algorithmic Bots). It handles:
1. **Security & Ingress:** JWT authentication, API Key validation, and per-account Token Bucket rate limiting.
2. **Request Routing:** Validates requests and orchestrates downstream calls to `risk-engine`, `matching-engine`, `clearing-settlement`, and `promotion-service`.
3. **Market Data Streaming:** WebSocket feed broadcasting real-time trade executions and L2 Order Book depth updates to frontends.

---

## 2. Boundaries & Downstream Services
- **Downstream RPCs:**
  - `matching-engine:9001` (Order placement & cancellation)
  - `risk-engine:9002` (Pre-trade checks & balance queries)
  - `clearing-settlement:8083` (Ledger, deposits, withdrawals)
  - `promotion-service:8084` (Vouchers & reward tracking)
- **Tracing:** OpenTelemetry trace propagation across all downstream HTTP/gRPC calls.

---

## 3. Quick Start (Run in 2 minutes)

```bash
# Set environment variables pointing to local or containerized services
export MATCHING_ENGINE_ADDR="localhost:9001"
export RISK_ENGINE_ADDR="localhost:9002"
export SETTLEMENT_ADDR="localhost:8083"
export PROMOTION_ADDR="localhost:8084"
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
GET http://localhost:8080/orderbook?symbol=AAPL&depth=20
```

---

## 5. Engineering Roadmap (Concurrency & Network Optimization)

| Version | Phase | Optimization Target & Concurrency Challenge |
| :--- | :--- | :--- |
| **v1.0.0** *(Current)* | **Baseline Gateway** | Go standard HTTP server, JSON routing, in-memory rate limiter, OpenTelemetry integration. |
| **v1.1.0** | **gRPC Client Pool & Multiplexing** | Replace HTTP/1.1 downstream clients with persistent HTTP/2 gRPC connection pools. Keep-alive tuning to eliminate TCP connection churn. |
| **v1.2.0** | **High-Fanout WebSocket Engine** | Replace standard `net/http` WebSocket with `gobwas/ws` or `epoll`/`kqueue` event-loop architecture to sustain **100,000+ concurrent live priceboard connections** with minimal memory (< 2KB per connection). |
| **v2.0.0** | **Kernel-Bypass / eBPF Gateway** | Offload DDoS mitigation and token-bucket rate limiting to eBPF / XDP at the Linux kernel level. Target ingress latency overhead < 50 microseconds. |

