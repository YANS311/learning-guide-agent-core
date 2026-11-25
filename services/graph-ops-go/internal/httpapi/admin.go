package httpapi

import (
	"context"
	"embed"
	"net/http"
	"strconv"
)

const adminImport = "/api/v1/admin/import"
const adminHistory = "/api/v1/admin/history"
const adminDetail = "/api/v1/admin/operation"
const adminMonitoring = "/api/v1/admin/monitoring"

//go:embed web/*
var web embed.FS

var assets = map[string][2]string{
	"/console":           {"index.html", "text/html; charset=utf-8"},
	"/console/style.css": {"style.css", "text/css; charset=utf-8"},
	"/console/app.js":    {"app.js", "text/javascript; charset=utf-8"},
}

func staticConsole(w http.ResponseWriter, r *http.Request) bool {
	resource, ok := assets[r.URL.Path]
	if !ok {
		return false
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return true
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", resource[1])
	if r.Method == "GET" {
		data, _ := web.ReadFile("web/" + resource[0])
		_, _ = w.Write(data)
	}
	return true
}

func (a *API) adminRead(ctx context.Context, r *http.Request, route string) response {
	switch route {
	case adminHistory:
		q := r.URL.Query()
		limit := 20
		if q.Has("limit") {
			n, err := strconv.Atoi(q.Get("limit"))
			if err != nil {
				return failure(422, "invalid_pagination", "limit", "invalid page size")
			}
			limit = n
		}
		result, err := a.lab.History(ctx, limit, q.Get("cursor"), q.Get("status"))
		if err != nil {
			return labError(err)
		}
		return response{200, "", result}
	case adminDetail:
		result, err := a.lab.Detail(ctx, r.URL.Query().Get("id"))
		if err != nil {
			return labError(err)
		}
		return response{200, "", result}
	default:
		ready := false
		if a.lab != nil {
			ready = a.labReady(ctx).status == 200
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		var requests uint64
		for _, c := range a.requests {
			requests += c.count
		}
		m := a.labMetrics
		var avg float64
		if m.tx.count != 0 {
			avg = m.tx.seconds * 1000 / float64(m.tx.count)
		}
		return response{200, "", map[string]any{
			"process_started_at": a.started, "live": true, "neo4j_ready": ready, "lab_configured": a.lab != nil,
			"http_requests": requests, "validations": a.validated + a.rejected, "validation_failures": a.rejected,
			"sync_success": m.success, "sync_failures": m.failure, "version_conflicts": m.versionConflicts,
			"idempotency_replays": m.replays, "confirmed_rollbacks": m.rollbacks, "commit_unknown": m.unknown,
			"average_transaction_ms": avg, "transaction_count": m.tx.count,
		}}
	}
}
