package advisor

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadRetryAndEvidenceFailure(t *testing.T) {
	var reads, writes atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") == "" {
			t.Error("missing correlation ID")
		}
		if r.Method == "POST" {
			writes.Add(1)
			fmt.Fprint(w, "{bad")
			return
		}
		if reads.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprintf(w, `{"snapshot_version":%q,"data":{"total":1,"items":[{"id":"M1","name":"Sample","credits":3}]}}`, fixtureVersion)
	}))
	defer s.Close()
	h, err := NewLocalHTTP(s.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Catalog(background); err != nil || reads.Load() != 2 {
		t.Fatal(reads.Load(), err)
	}
	corpus, err := LoadCorpus("", []Evidence{{DocID: "DOC", Version: "1", Section: "signal", Source: "source", Excerpt: "signal"}})
	if err != nil {
		t.Fatal(err)
	}
	out, mode, err := (RetrievalAdapter{Remote: h, Fallback: corpus}).Find(background, "signal")
	if err != nil || len(out) != 1 || mode != "LOCAL_CORPUS" || writes.Load() != 1 {
		t.Fatal(out, mode, err, writes.Load())
	}
}

func TestSSEAdmissionAndDisconnect(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	defer s.Close()
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	owner, token := newStudent(t, s)
	run, _, err := s.Enqueue(background, owner, "sse-admission-1", want(4, "B2"))
	if err != nil {
		t.Fatal(err)
	}
	metrics := new(Metrics)
	server := httptest.NewServer((API{Store: s, Graph: fakeGraph{}, Metrics: metrics}).Handler())
	defer server.Close()
	client := &http.Client{Timeout: 4 * time.Second}
	ctx, release := context.WithCancel(background)
	defer release()
	pull := func() *http.Response {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/runs/"+run.ID+"/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return res
	}
	bodies := []*http.Response{}
	defer func() {
		for _, r := range bodies {
			r.Body.Close()
		}
	}()
	for n := 0; n < 64; n++ {
		res := pull()
		bodies = append(bodies, res)
		if res.StatusCode != 200 {
			t.Fatalf("stream %d: %d", n, res.StatusCode)
		}
	}
	final := pull()
	if final.Body.Close(); final.StatusCode != 429 {
		t.Fatal(final.StatusCode)
	}
	release()
	for _, r := range bodies {
		r.Body.Close()
	}
	for i := 0; i < 50 && metrics.streams.Load() != 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if metrics.streams.Load() != 0 {
		t.Fatal("stream slots were not released")
	}
}

func TestCrossOriginAndRequestLimit(t *testing.T) {
	s := demoStore(t)
	_, token := newStudent(t, s)
	server := httptest.NewServer((API{Store: s, Graph: fakeGraph{}}).Handler())
	defer server.Close()
	for _, item := range []struct {
		origin, host, body string
		expected           int
	}{
		{"http://192.0.2.10", "", `{}`, 403},
		{"http://example.test:8095", "example.test:8095", `{}`, 403},
		{"", "", `{"goal":"` + strings.Repeat("x", 33000) + `"}`, 422},
	} {
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/runs", strings.NewReader(item.body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", item.origin)
		if item.host != "" {
			req.Host = item.host
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != item.expected {
			t.Fatal(response.StatusCode, item.expected)
		}
	}
}
