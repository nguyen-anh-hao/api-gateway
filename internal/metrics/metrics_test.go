package metrics

import (
	"strings"
	"sync"
	"testing"
)

// TestConcurrentWriteAndScrape guards the race that used to crash the gateway:
// /metrics iterated the label maps while request handlers incremented them.
// Run with: go test -race ./internal/metrics
func TestConcurrentWriteAndScrape(t *testing.T) {
	r := New()
	counter := r.Counter("test_counter", "test", "method")
	hist := r.Histogram("test_histogram", "test", []float64{0.01, 0.1, 1}, "method")
	gauge := r.Gauge("test_gauge", "test", "method")

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20000; n++ {
				counter.Inc("GET")
				hist.Observe(0.02, "GET")
				gauge.Set(float64(n), "GET")
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 2000; n++ {
				var b strings.Builder
				r.writeAll(&b)
				if !strings.Contains(b.String(), "test_counter") {
					t.Errorf("scrape missing counter output")
					return
				}
			}
		}()
	}
	wg.Wait()
}
