package advisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func requestHTTP(t *testing.T, base, token, method, path, id string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if id != "" {
		req.Header.Set("Idempotency-Key", id)
	}
	client := http.Client{Timeout: 4 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, raw
}

func waitRun(t *testing.T, s *Store, owner, id, expected string) Run {
	t.Helper()
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		r, err := s.Run(background, owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == expected {
			return r
		}
		if terminal(r.State) {
			t.Fatalf("unexpected terminal: %s %s", r.State, r.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("never reached %s", expected)
	return Run{}
}

func TestHTTPProgressReplayAndOwnerIsolation(t *testing.T) {
	s := demoStore(t)
	owner, token := newStudent(t, s)
	_, other := newStudent(t, s)
	var gated atomic.Bool
	release := make(chan struct{})
	graph := fakeGraph{check: func(ctx context.Context, id string) (CourseCheck, error) {
		gated.Store(true)
		select {
		case <-release:
			return (fakeGraph{}).Check(ctx, id)
		case <-ctx.Done():
			return CourseCheck{}, ctx.Err()
		}
	}}
	worker := &Worker{Store: s, Planner: Planner{Graph: graph}, Timeout: 4 * time.Second}
	stop, err := worker.Start(background)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	server := httptest.NewServer((API{Store: s, Worker: worker, Graph: graph}).Handler())
	defer server.Close()
	status, raw := requestHTTP(t, server.URL, token, "POST", "/api/v1/runs", "http-run-key1", want(3, "B2"))
	var run Run
	if status != 202 || json.Unmarshal(raw, &run) != nil || run.ID == "" {
		t.Fatal(status, string(raw))
	}
	root := "/api/v1/runs/" + run.ID
	if status, _ = requestHTTP(t, server.URL, other, "GET", root, "", nil); status != 404 {
		t.Fatal(status)
	}
	if status, _ = requestHTTP(t, server.URL, other, "GET", root+"/events", "", nil); status != 404 {
		t.Fatal(status)
	}
	if status, _ = requestHTTP(t, server.URL, "", "GET", root, "", nil); status != 401 {
		t.Fatal(status)
	}
	sse := func(last string) *http.Response {
		req, _ := http.NewRequest("GET", server.URL+root+"/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Last-Event-ID", last)
		reply, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	first := sse("0")
	data := make([]byte, 32)
	_, _ = first.Body.Read(data)
	_ = first.Body.Close()
	close(release)
	row := waitRun(t, s, owner, run.ID, "SUCCEEDED")
	if !gated.Load() || row.Plan == nil || len(row.Plan.Selected) != 1 {
		t.Fatal(row)
	}
	rejoin := sse("2")
	defer rejoin.Body.Close()
	stream, err := io.ReadAll(rejoin.Body)
	if err != nil || strings.Contains(string(stream), "id: 2\n") || !strings.Contains(string(stream), "event: completed") ||
		!strings.Contains(string(stream), "id: 6\n") {
		t.Fatal(string(stream), err)
	}
	if status, _ = requestHTTP(t, server.URL, token, "POST", "/api/v1/runs", "http-run-key1", want(3, "B2")); status != 200 {
		t.Fatal(status)
	}
}

func TestCancellationAndTaskTimeout(t *testing.T) {
	s := demoStore(t)
	owner, token := newStudent(t, s)
	second, _ := newStudent(t, s)
	graph := fakeGraph{check: func(ctx context.Context, _ string) (CourseCheck, error) {
		<-ctx.Done()
		return CourseCheck{}, ctx.Err()
	}}
	worker := &Worker{Store: s, Planner: Planner{Graph: graph}, Timeout: 650 * time.Millisecond}
	stop, err := worker.Start(background)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	server := httptest.NewServer((API{Store: s, Worker: worker, Graph: graph}).Handler())
	defer server.Close()
	first, _, err := s.Enqueue(background, owner, "cancel-key-1", want(3, "B2"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, owner, first.ID, "RUNNING")
	status, _ := requestHTTP(t, server.URL, token, "POST", "/api/v1/runs/"+first.ID+"/cancel", "", nil)
	if status != 200 {
		t.Fatal(status)
	}
	if r := waitRun(t, s, owner, first.ID, "CANCELLED"); r.Plan != nil {
		t.Fatal(r)
	}
	next, _, err := s.Enqueue(background, second, "timeout-key1", want(3, "B2"))
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRun(t, s, second, next.ID, "FAILED"); r.Error != string(Deadline) || r.Plan != nil {
		t.Fatal(r)
	}
	if err := s.Cancel(background, second, next.ID); !errors.Is(err, Revision) {
		t.Fatal(err)
	}
}
