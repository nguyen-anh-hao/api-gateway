// Package proxy provides thin reverse-proxy handlers for the gateway. The
// gateway forwards /orders*, /trades and /ws without understanding their
// business semantics.
package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// New builds a reverse proxy to target (host:port). When internalToken is set
// it is attached as X-Internal-Token so downstream services can reject traffic
// that bypassed the gateway.
func New(target, internalToken string) *httputil.ReverseProxy {
	u, err := url.Parse("http://" + target)
	if err != nil {
		slog.Error("invalid proxy target", "target", target, "err", err)
		u = &url.URL{Scheme: "http", Host: target}
	}
	p := httputil.NewSingleHostReverseProxy(u)
	// Instrument the upstream hop: the transport starts a client span and
	// injects the W3C `traceparent` header into the forwarded request. Without
	// it the trace stops at the gateway (the inbound header is only parsed into
	// the request context, never re-serialised), so order-service starts a
	// brand-new trace instead of continuing the gateway one.
	// otelhttp preserves io.ReadWriteCloser on 101 Switching Protocols replies,
	// so WebSocket upgrades proxied by this transport keep working.
	p.Transport = otelhttp.NewTransport(http.DefaultTransport)
	// FlushInterval(-1) flushes every write immediately; required for WebSocket
	// upgrades and long-lived streaming responses.
	p.FlushInterval = -1
	director := p.Director
	p.Director = func(r *http.Request) {
		director(r)
		r.Host = u.Host
		if internalToken != "" {
			r.Header.Set("X-Internal-Token", internalToken)
		}
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("upstream proxy error", "path", r.URL.Path, "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "upstream unavailable"})
	}
	return p
}
