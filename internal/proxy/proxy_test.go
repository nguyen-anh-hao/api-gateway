package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestProxyPropagatesTraceContext guards the end-to-end trace chain
// api-gateway -> order-service: the reverse proxy must forward a `traceparent`
// header that points at the *gateway's* span, otherwise downstream services
// start a brand-new trace and the call graph is split in Jaeger.
func TestProxyPropagatesTraceContext(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		_ = tp.Shutdown(context.Background())
	})

	var gotTraceparent, gotToken string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		gotToken = r.Header.Get("X-Internal-Token")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p := New(strings.TrimPrefix(backend.URL, "http://"), "dev-internal-token")
	gateway := otelhttp.NewHandler(p, "api-gateway")

	rec := httptest.NewRecorder()
	gateway.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/orders", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200", rec.Code)
	}
	if gotToken != "dev-internal-token" {
		t.Fatalf("X-Internal-Token = %q, want %q", gotToken, "dev-internal-token")
	}
	if gotTraceparent == "" {
		t.Fatal("upstream received no traceparent header: trace context is not propagated")
	}

	upstreamCtx := otel.GetTextMapPropagator().Extract(
		context.Background(),
		propagation.HeaderCarrier(http.Header{"Traceparent": []string{gotTraceparent}}),
	)
	upstreamSC := trace.SpanContextFromContext(upstreamCtx)
	if !upstreamSC.IsValid() {
		t.Fatalf("upstream traceparent %q is not a valid W3C trace context", gotTraceparent)
	}

	var serverSpan trace.SpanContext
	for _, s := range exp.GetSpans() {
		if s.SpanKind == trace.SpanKindServer {
			serverSpan = s.SpanContext
		}
	}
	if !serverSpan.IsValid() {
		t.Fatal("no server span recorded for the gateway handler")
	}

	if upstreamSC.TraceID() != serverSpan.TraceID() {
		t.Fatalf("upstream trace id %s != gateway trace id %s (trace was split)",
			upstreamSC.TraceID(), serverSpan.TraceID())
	}
	if upstreamSC.SpanID() == serverSpan.SpanID() {
		t.Fatal("upstream parent span id equals the gateway span id: no client span was created")
	}
}
