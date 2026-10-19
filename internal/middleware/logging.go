package middleware

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/nguyen-anh-hao/api-gateway/internal/metrics"
)

var (
	httpRequestsTotal = metrics.Default.Counter(
		"http_requests_total", "Total HTTP requests handled.", "method", "path", "status")
	httpRequestDuration = metrics.Default.Histogram(
		"http_request_duration_seconds", "HTTP request latency in seconds.",
		[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}, "method", "path")
	httpRequestsInFlight = metrics.Default.Gauge(
		"http_requests_in_flight", "HTTP requests currently being served.", "method")
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush and Hijack are delegated so WebSocket upgrades keep working through
// this middleware.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.ResponseWriter.(http.Hijacker).Hijack()
}

// slowRequestThreshold is the latency above which a successful request is
// logged at warn level instead of the quieter debug level.
const slowRequestThreshold = 1 * time.Second

// Logging logs method, path, status code and latency, and records Prometheus
// metrics. To keep the gateway quiet in production, successful/fast requests are
// logged at debug, while failures (4xx/5xx) and slow requests are logged at warn.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		httpRequestsInFlight.Add(1, r.Method)
		defer httpRequestsInFlight.Add(-1, r.Method)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start)

		httpRequestsTotal.Inc(r.Method, r.URL.Path, strconv.Itoa(rec.status))
		httpRequestDuration.Observe(elapsed.Seconds(), r.Method, r.URL.Path)

		// Put the essential request info in the message so Grafana/Loki's default
		// view is readable at a glance; keep structured fields for filtering.
		msg := r.Method + " " + r.URL.Path + " -> " + strconv.Itoa(rec.status)
		fields := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status, "duration_ms", elapsed.Milliseconds()}
		switch {
		case rec.status >= http.StatusBadRequest:
			slog.WarnContext(r.Context(), msg, fields...)
		case elapsed >= slowRequestThreshold:
			slog.WarnContext(r.Context(), msg, fields...)
		default:
			slog.DebugContext(r.Context(), msg, fields...)
		}
	})
}
