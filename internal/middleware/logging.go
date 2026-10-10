package middleware

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/exchange/trading-system/services/api-gateway/internal/metrics"
)

var (
	httpRequestsTotal = metrics.Default.Counter(
		"http_requests_total", "Total HTTP requests handled.", "method", "path", "status")
	httpRequestDuration = metrics.Default.Histogram(
		"http_request_duration_seconds", "HTTP request latency in seconds.",
		[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}, "method", "path")
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

// Logging logs method, path, status code and latency for every request, and
// records Prometheus metrics.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start)

		httpRequestsTotal.Inc(r.Method, r.URL.Path, strconv.Itoa(rec.status))
		httpRequestDuration.Observe(elapsed.Seconds(), r.Method, r.URL.Path)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, elapsed)
	})
}
