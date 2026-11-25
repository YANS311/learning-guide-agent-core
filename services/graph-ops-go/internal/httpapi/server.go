package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"learning-guide/graph-ops/internal/graph"
	"learning-guide/graph-ops/internal/importer"
)

const MaxBody = 2 << 20
const (
	live     = "/health/live"
	ready    = "/health/ready"
	validate = "/api/v1/graph/import/validate"
	preview  = "/api/v1/graph/sync/preview"
	metrics  = "/metrics"
)

var operations = map[string]string{live: "GET", ready: "GET", validate: "POST", preview: "POST", metrics: "GET"}

type counter struct {
	count   uint64
	seconds float64
}
type API struct {
	logger              *slog.Logger
	timeout             time.Duration
	mu                  sync.Mutex
	requests            map[string]counter
	validated, rejected uint64
	diff                counter
}

func New(logger *slog.Logger, timeout time.Duration) *API {
	return &API{logger: logger, timeout: timeout, requests: make(map[string]counter)}
}

type response struct {
	status int
	code   string
	body   any
}

func failure(status int, code, location, message string) response {
	return response{status, code, map[string]any{"errors": []graph.Issue{{Code: code, Location: location, Message: message}}}}
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		http.Error(w, "request id unavailable", http.StatusInternalServerError)
		return
	}
	requestID := hex.EncodeToString(id)
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ctx, cancel := context.WithTimeout(r.Context(), a.timeout)
	defer cancel()
	route := r.URL.Path
	if _, ok := operations[route]; !ok {
		route = "unknown"
	}
	log := a.logger.With("request_id", requestID, "route", route)
	result := a.run(ctx, w, r, route, log)
	a.mu.Lock()
	total := a.requests[route]
	total.count++
	total.seconds += time.Since(start).Seconds()
	a.requests[route] = total
	if route == validate && r.Method == "POST" {
		if result.status == 200 {
			a.validated++
		} else {
			a.rejected++
		}
	}
	a.mu.Unlock()
	log.Info("response", "status", result.status, "error_code", result.code, "duration_ms", time.Since(start).Milliseconds())
	if s, ok := result.body.(string); ok {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(result.status)
		_, _ = io.WriteString(w, s)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.status)
	_ = json.NewEncoder(w).Encode(result.body)
}

func (a *API) run(ctx context.Context, w http.ResponseWriter, r *http.Request, route string, log *slog.Logger) (out response) {
	defer func() {
		if recover() != nil {
			out = failure(500, "internal_error", "request", "internal service error")
		}
	}()
	method, exists := operations[route]
	if !exists {
		return failure(404, "not_found", "path", "route not found")
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		return failure(405, "method_not_allowed", "method", "method not allowed")
	}
	if ctx.Err() != nil {
		return canceled(ctx.Err())
	}
	switch route {
	case live, ready:
		return response{200, "", map[string]any{"status": "ok", "snapshot_source": "request", "database_connected": false}}
	case metrics:
		return response{200, "", a.metricText()}
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (media != "application/json" && !(route == validate && media == "text/csv")) {
		return failure(415, "unsupported_media_type", "content_type", "expected JSON or CSV on the validation endpoint")
	}
	reader := http.MaxBytesReader(w, r.Body, MaxBody)
	defer reader.Close()
	// Bound stalled reads as well as validation and diff processing.
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetReadDeadline(deadline)
	body, err := io.ReadAll(reader)
	var limit *http.MaxBytesError
	switch {
	case errors.As(err, &limit):
		return failure(413, "body_limit", "body", "request exceeds the byte limit")
	case ctx.Err() != nil:
		return canceled(ctx.Err())
	case errors.Is(err, context.DeadlineExceeded):
		return canceled(context.DeadlineExceeded)
	case err != nil:
		var timeout interface{ Timeout() bool }
		if errors.As(err, &timeout) && timeout.Timeout() {
			return canceled(context.DeadlineExceeded)
		}
		return failure(400, "read_error", "body", "request body could not be read")
	case len(bytes.TrimSpace(body)) == 0:
		return failure(400, "empty_input", "body", "request body is empty")
	case !utf8.Valid(body):
		return failure(400, "invalid_encoding", "body", "request body must be UTF-8")
	}
	log.Info("parse", "format", media)
	return a.process(ctx, route, media, body, log)
}

func (a *API) process(ctx context.Context, route, media string, body []byte, log *slog.Logger) response {
	var data graph.Data
	var input graph.Preview
	var problem *graph.Issue
	switch {
	case route == preview:
		problem = importer.DecodeJSON(ctx, body, &input)
	case media == "text/csv":
		data, problem = importer.DecodeCSV(ctx, body)
	default:
		problem = importer.DecodeJSON(ctx, body, &data)
	}
	if ctx.Err() != nil {
		return canceled(ctx.Err())
	}
	if problem != nil {
		return failure(400, problem.Code, problem.Location, problem.Message)
	}
	log.Info("validate")
	if route == validate {
		if len(data.Courses) == 0 && len(data.Relations) == 0 {
			return failure(422, "no_records", "data", "at least one record is required for import validation")
		}
		issues, err := graph.Validate(ctx, data)
		if err != nil {
			return canceled(err)
		}
		status, code := 200, ""
		if len(issues) != 0 {
			status, code = 422, "validation_failed"
		} else {
			issues = []graph.Issue{}
		}
		return response{status, code, map[string]any{"valid": status == 200, "courses": len(data.Courses), "relations": len(data.Relations), "errors": issues}}
	}
	log.Info("diff")
	start := time.Now()
	plan, err := graph.MakePlan(ctx, input)
	a.mu.Lock()
	a.diff.count++
	a.diff.seconds += time.Since(start).Seconds()
	a.mu.Unlock()
	if err != nil {
		return canceled(err)
	}
	if !plan.Executable {
		return response{422, "validation_failed", plan}
	}
	return response{200, "", plan}
}

func canceled(err error) response {
	if errors.Is(err, context.Canceled) {
		return failure(408, "request_canceled", "request", "request canceled")
	}
	return failure(408, "request_timeout", "request", "request deadline exceeded")
}

func (a *API) metricText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out bytes.Buffer
	fmt.Fprintln(&out, "# TYPE graphops_http_requests_total counter")
	routes := []string{live, ready, validate, preview, metrics, "unknown"}
	for _, r := range routes {
		fmt.Fprintf(&out, "graphops_http_requests_total{route=%q} %d\n", r, a.requests[r].count)
	}
	fmt.Fprintln(&out, "# TYPE graphops_http_request_duration_seconds summary")
	for _, r := range routes {
		c := a.requests[r]
		fmt.Fprintf(&out, "graphops_http_request_duration_seconds_sum{route=%q} %g\ngraphops_http_request_duration_seconds_count{route=%q} %d\n", r, c.seconds, r, c.count)
	}
	fmt.Fprintf(&out, "# TYPE graphops_import_validation_total counter\ngraphops_import_validation_total{result=\"success\"} %d\ngraphops_import_validation_total{result=\"failure\"} %d\n", a.validated, a.rejected)
	fmt.Fprintf(&out, "# TYPE graphops_diff_duration_seconds summary\ngraphops_diff_duration_seconds_sum %g\ngraphops_diff_duration_seconds_count %d\n", a.diff.seconds, a.diff.count)
	return out.String()
}
