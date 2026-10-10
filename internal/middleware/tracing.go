package middleware

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Tracing wraps the handler with the OpenTelemetry HTTP instrumentation so each
// request emits a server span and inbound W3C trace context is extracted.
func Tracing(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, name)
	}
}
