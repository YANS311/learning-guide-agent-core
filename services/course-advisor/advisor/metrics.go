package advisor

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
)

var buckets = [8]float64{.005, .02, .05, .15, .5, 1, 3, 10}

type histogram struct {
	bucket [8]atomic.Uint64
	count  atomic.Uint64
	nanos  atomic.Int64
}

func (h *histogram) observe(d time.Duration) {
	for i, b := range buckets {
		if d.Seconds() <= b {
			h.bucket[i].Add(1)
		}
	}
	h.count.Add(1)
	h.nanos.Add(d.Nanoseconds())
}

func (h *histogram) write(w http.ResponseWriter, name string) {
	fmt.Fprintf(w, "# TYPE %s histogram\n", name)
	for i, b := range buckets {
		fmt.Fprintf(w, "%s_bucket{le=\"%g\"} %d\n", name, b, h.bucket[i].Load())
	}
	count := h.count.Load()
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n%s_count %d\n%s_sum %.9f\n", name, count, name, count, name, float64(h.nanos.Load())/1e9)
}

type Metrics struct {
	repo                                                                      Repository
	requests, errors, throttles, unavailable                                  atomic.Uint64
	succeeded, failed, cancelResponses, retried, recovered                    atomic.Uint64
	cacheHits, cacheMisses, redisErrors, dependencyRequests, dependencyErrors atomic.Uint64
	workers, streams                                                          atomic.Int64
	httpLatency, queueWait, runLatency                                        histogram
}

type httpObservation struct {
	http.ResponseWriter
	status int
}

func (w *httpObservation) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *httpObservation) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (m *Metrics) observeHTTP(status int, d time.Duration) {
	m.requests.Add(1)
	if status >= 400 {
		m.errors.Add(1)
	}
	if status == 429 {
		m.throttles.Add(1)
	}
	if status == 503 {
		m.unavailable.Add(1)
	}
	m.httpLatency.observe(d)
}
func (m *Metrics) recordRun(success bool, d time.Duration) {
	if success {
		m.succeeded.Add(1)
	} else {
		m.failed.Add(1)
	}
	m.runLatency.observe(d)
}
func (s *Store) stats(ctx context.Context) (int, sql.DBStats, error) {
	var queued int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM run WHERE state='QUEUED'").Scan(&queued)
	return queued, s.db.Stats(), err
}
func (m *Metrics) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	for _, metric := range []struct {
		name string
		n    uint64
	}{
		{"http_requests", m.requests.Load()}, {"http_errors", m.errors.Load()},
		{"http_throttled", m.throttles.Load()}, {"http_unavailable", m.unavailable.Load()},
		{"task_success", m.succeeded.Load()}, {"task_failed", m.failed.Load()},
		{"cancel_responses", m.cancelResponses.Load()}, {"task_retry_claims", m.retried.Load()},
		{"task_lease_recoveries", m.recovered.Load()}, {"cache_hits", m.cacheHits.Load()},
		{"cache_misses", m.cacheMisses.Load()}, {"redis_errors", m.redisErrors.Load()},
		{"dependency_requests", m.dependencyRequests.Load()}, {"dependency_errors", m.dependencyErrors.Load()},
	} {
		fmt.Fprintf(w, "# TYPE advisor_%s_total counter\nadvisor_%s_total %d\n", metric.name, metric.name, metric.n)
	}
	m.httpLatency.write(w, "advisor_http_duration_seconds")
	m.queueWait.write(w, "advisor_task_wait_seconds")
	m.runLatency.write(w, "advisor_task_duration_seconds")
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(w, "advisor_workers_active %d\nadvisor_sse_open %d\nadvisor_go_goroutines %d\nadvisor_heap_alloc_bytes %d\n",
		m.workers.Load(), m.streams.Load(), runtime.NumGoroutine(), mem.HeapAlloc)
	if s, ok := m.repo.(interface {
		stats(context.Context) (int, sql.DBStats, error)
	}); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 400*time.Millisecond)
		defer cancel()
		queued, pool, err := s.stats(ctx)
		if err == nil {
			fmt.Fprintf(w, "advisor_queue_depth %d\n", queued)
		}
		fmt.Fprintf(w, "advisor_sql_pool_open %d\nadvisor_sql_pool_in_use %d\nadvisor_sql_pool_wait_total %d\n",
			pool.OpenConnections, pool.InUse, pool.WaitCount)
	}
}
