package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Worker struct {
	Store   *Store
	Planner Planner
	Timeout time.Duration
	mu      sync.Mutex
	active  map[string]context.CancelFunc
}

func (w *Worker) Start(parent context.Context) (func(), error) {
	if w.Timeout <= 0 || w.Store == nil || w.Planner.Graph == nil {
		return nil, Invalid
	}
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	w.active = make(map[string]context.CancelFunc)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				work, err := w.Store.Claim(ctx)
				if err == nil {
					w.execute(ctx, work)
					continue
				}
				if !errors.Is(err, Missing) && ctx.Err() == nil {
					slog.Error("claim failed", "code", FailureCode(err))
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(125 * time.Millisecond):
				}
			}
		}()
	}
	return func() { cancel(); wg.Wait() }, nil
}

func (w *Worker) execute(parent context.Context, work work) {
	ctx, cancel := context.WithTimeout(parent, w.Timeout)
	defer cancel()
	w.mu.Lock()
	w.active[work.ID] = cancel
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.active, work.ID); w.mu.Unlock() }()
	result, err := w.Planner.Run(ctx, work.P, work.R, func(step string) error { return w.Store.Progress(ctx, work.ID, step) })
	if parent.Err() != nil {
		return
	}
	finish, release := context.WithTimeout(context.Background(), 3*time.Second)
	defer release()
	var stored *Plan
	var code string
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		code = string(Deadline)
	case err != nil:
		code = FailureCode(err)
	default:
		stored = &result
	}
	if err = w.Store.Finish(finish, work.ID, code, stored); err != nil {
		slog.Error("result persistence failed", "run_id", work.ID)
		_ = w.Store.Finish(finish, work.ID, "persistence_failed", nil)
	}
}

func (w *Worker) StopRun(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cancel := w.active[id]; cancel != nil {
		cancel()
	}
}

type API struct {
	Store  *Store
	Worker *Worker
	Graph  Graph
	Assets http.Handler
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sendError(w http.ResponseWriter, err error) {
	code := FailureCode(err)
	status := http.StatusInternalServerError
	switch Failure(code) {
	case Missing:
		status = 404
	case Invalid, UnknownCourse:
		status = 422
	case Busy:
		status = 429
	case Revision, Idempotency, Changed:
		status = 409
	case Deadline:
		status = 504
	case Dependency, CatalogSize:
		status = 503
	}
	write(w, status, map[string]any{"error": code})
}

func readInput(w http.ResponseWriter, r *http.Request, dst any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return Invalid
	}
	reader := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	reader.DisallowUnknownFields()
	if err := reader.Decode(dst); err != nil {
		return Invalid
	}
	if err := reader.Decode(new(any)); err != io.EOF {
		return Invalid
	}
	return nil
}

type memberHandler func(http.ResponseWriter, *http.Request, string)

func (a API) member(next memberHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok {
			write(w, 401, map[string]string{"error": "not_authenticated"})
			return
		}
		owner, err := a.Store.Owner(r.Context(), token)
		if errors.Is(err, Missing) {
			write(w, 401, map[string]string{"error": "not_authenticated"})
			return
		}
		if err != nil {
			sendError(w, err)
			return
		}
		next(w, r, owner)
	}
}

func (a API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]string{"state": "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Store.db.PingContext(r.Context()); err != nil {
			sendError(w, Dependency)
			return
		}
		catalog, err := a.Graph.Catalog(r.Context())
		if err != nil {
			sendError(w, err)
			return
		}
		write(w, 200, map[string]any{"state": "ok", "snapshot_version": catalog.Version, "courses": len(catalog.Courses)})
	})
	mux.HandleFunc("POST /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			sendError(w, Invalid)
			return
		}
		var body struct{}
		if err := readInput(w, r, &body); err != nil {
			sendError(w, err)
			return
		}
		token, err := a.Store.NewWorkspace(r.Context())
		if err != nil {
			sendError(w, err)
			return
		}
		write(w, 201, map[string]string{"token": token})
	})
	mux.HandleFunc("GET /api/v1/profile", a.member(func(w http.ResponseWriter, r *http.Request, owner string) {
		profile, err := a.Store.Profile(r.Context(), owner)
		if err != nil {
			sendError(w, err)
			return
		}
		write(w, 200, profile)
	}))
	mux.HandleFunc("PUT /api/v1/profile", a.member(func(w http.ResponseWriter, r *http.Request, owner string) {
		var profile Profile
		if err := readInput(w, r, &profile); err != nil {
			sendError(w, err)
			return
		}
		out, err := a.Store.SaveProfile(r.Context(), owner, profile)
		if err != nil {
			sendError(w, err)
			return
		}
		write(w, 200, out)
	}))
	mux.HandleFunc("GET /api/v1/courses", a.member(func(w http.ResponseWriter, r *http.Request, _ string) {
		catalog, err := a.Graph.Catalog(r.Context())
		if err != nil {
			sendError(w, err)
			return
		}
		write(w, 200, catalog)
	}))
	mux.HandleFunc("POST /api/v1/runs", a.member(func(w http.ResponseWriter, r *http.Request, owner string) {
		var request Request
		if err := readInput(w, r, &request); err != nil {
			sendError(w, err)
			return
		}
		run, replay, err := a.Store.Enqueue(r.Context(), owner, r.Header.Get("Idempotency-Key"), request)
		if err != nil {
			sendError(w, err)
			return
		}
		status := http.StatusAccepted
		if replay {
			status = http.StatusOK
		}
		write(w, status, run)
	}))
	mux.HandleFunc("GET /api/v1/runs", a.member(func(w http.ResponseWriter, r *http.Request, owner string) {
		limit, offset := 10, 0
		var err error
		if s := r.URL.Query().Get("limit"); s != "" {
			limit, err = strconv.Atoi(s)
		}
		if err != nil || limit < 1 || limit > 25 {
			sendError(w, Invalid)
			return
		}
		if s := r.URL.Query().Get("offset"); s != "" {
			offset, err = strconv.Atoi(s)
		}
		if err != nil || offset < 0 || offset > 100000 {
			sendError(w, Invalid)
			return
		}
		history, err := a.Store.History(r.Context(), owner, offset, limit)
		if err != nil {
			sendError(w, err)
			return
		}
		write(w, 200, history)
	}))
	mux.HandleFunc("GET /api/v1/runs/{id}", a.member(func(w http.ResponseWriter, r *http.Request, owner string) {
		run, err := a.Store.Run(r.Context(), owner, r.PathValue("id"))
		if err != nil {
			sendError(w, err)
			return
		}
		write(w, 200, run)
	}))
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", a.member(func(w http.ResponseWriter, r *http.Request, owner string) {
		id := r.PathValue("id")
		if err := a.Store.Cancel(r.Context(), owner, id); err != nil {
			sendError(w, err)
			return
		}
		a.Worker.StopRun(id)
		write(w, 200, map[string]string{"state": "CANCELLED"})
	}))
	mux.HandleFunc("GET /api/v1/runs/{id}/events", a.member(a.events))
	if a.Assets != nil {
		mux.Handle("GET /", a.Assets)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'")
		requestID, _ := randomID(8)
		w.Header().Set("X-Request-ID", requestID)
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			write(w, 403, map[string]string{"error": "origin_rejected"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func terminal(state string) bool { return state != "QUEUED" && state != "RUNNING" }

func (a API) events(w http.ResponseWriter, r *http.Request, owner string) {
	id := r.PathValue("id")
	after := 0
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			sendError(w, Invalid)
			return
		}
		after = n
	}
	if _, err := a.Store.Run(r.Context(), owner, id); err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	send := func() bool {
		rows, err := a.Store.Events(r.Context(), owner, id, after)
		if err != nil {
			return false
		}
		for _, e := range rows {
			body, err := json.Marshal(e)
			if err != nil {
				return false
			}
			if _, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Kind, body); err != nil {
				return false
			}
			after = e.Seq
		}
		return rc.Flush() == nil
	}
	_, _ = fmt.Fprint(w, ": connected\n\n")
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !send() {
			return
		}
		run, err := a.Store.Run(r.Context(), owner, id)
		if err != nil {
			return
		}
		if terminal(run.State) {
			send()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}
