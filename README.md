# api-gateway

## Purpose

The public edge of the exchange: it authenticates API keys, enforces permissions and per-IP rate limits, and routes requests to the services that own the domain. It keeps no business state and makes no trading decisions, so any replica can serve any request. Its non-goals are order state, matching, balances, the ledger and fee policy.

## Domain knowledge

Every API key resolves to one account identity and a set of permissions; the identity is forwarded internally as an account header and is trusted only when the internal token also matches. Write permissions are explicit: trading and deposits require the trade scope, withdrawals require their own scope, and a missing scope is rejected rather than downgraded. Requests may be signed with HMAC-SHA256 over the request URI for reads and over the raw body for writes, and signing becomes mandatory when the environment requires it.

Account identifiers are issued from a shared sequence that starts at 1001, so seeded demo accounts can never collide with real registrations. The seeded demo key is a development credential tied to account 1 and must never be enabled in a public deployment. Rate limiting is a token bucket keyed by client IP, defaulting to ten thousand requests per second, with zero disabling it entirely.

The health, metrics, Swagger, account registration and WebSocket routes are deliberately outside the authenticated surface. Everything else passes through authentication, rate limiting and permission checks in that order. The gateway also proxies the live market-data stream unchanged, because terminal fan-out belongs to the market-data domain.

## Decisions and consequences

**Backend for Frontend as a reverse proxy.** The gateway shapes the public API and forwards to domain services instead of embedding their logic. The benefit is a single authenticated edge that scales statelessly; the cost is that every new capability needs a route, a client and a contract update.

**API key with optional HMAC.** Keys are easy for bots and scripts to use, and signatures protect body integrity. The signature covers the payload only, with no timestamp or nonce, so a captured signed request can be replayed while its key remains valid.

**Distributed token bucket in Redis.** Limits are shared across replicas instead of being per process. The cost is that Redis is a hard startup dependency and adds a round trip to every request.

**Contract-first OpenAPI.** The public surface is defined once in the OpenAPI document and the Go types are generated from it, with CI failing on drift. This keeps documentation and code aligned, at the price of a generation step for every API change.

## Operations and known limits

The service exits at startup when Redis is unreachable, because per-replica auth or rate-limit state would diverge. Key lookups use a local cache with a Redis fallback, so a key created on another replica is accepted without a restart. Signature verification has no replay window, and the demo key exists for local development only.

Rate-limit rejections return 429, and authentication or permission failures return 401 or 403, so clients should treat those as control-plane signals rather than trading errors. The gateway does not retry or circuit-break upstream calls today, which means a slow dependency shows up as latency at the edge. A local run needs Redis plus the internal service addresses; the full stack comes from the deployment repository, where the gateway is reachable on port 8080.

## Future direction

Business: richer permission scopes and self-service key rotation are the natural next steps; nothing beyond the current account model is promised.

Technical: pagination passthrough, upstream timeouts with retries and circuit breaking, then persistent gRPC client pools to remove connection churn. WebSocket backpressure and origin pinning are planned before exposing the stream to untrusted clients.

Boundaries: the public WebSocket proxy is the most likely piece to split into a dedicated streaming edge, and authentication could move to an external identity provider while keeping the internal identity header.
