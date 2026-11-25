package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTP(t *testing.T) {
	var logs bytes.Buffer
	api := New(slog.New(slog.NewJSONHandler(&logs, nil)), time.Second)
	for _, tc := range []struct {
		method, path, media, body, want string
		status                          int
	}{
		{"GET", live, "", "", "snapshot_source", 200},
		{"GET", ready, "", "", "database_connected", 200},
		{"GET", "/key-material-sentinel", "", "", "not_found", 404},
		{"PUT", preview, "", "", "method_not_allowed", 405},
		{"POST", validate, "text/plain", "{}", "unsupported_media_type", 415},
		{"POST", validate, "application/json", " ", "empty_input", 400},
		{"POST", validate, "application/json", string([]byte{0xff}), "invalid_encoding", 400},
		{"POST", validate, "application/json", "{", "invalid_json", 400},
		{"POST", validate, "application/json", "{} {}", "invalid_json", 400},
		{"POST", validate, "application/json", `{"courses":[{"id":"X","name":"A","hours":"1"}]}`, "invalid_type", 400},
		{"POST", validate, "application/json", `{"courses":[{"id":"X","name":"A","hours":null}]}`, "invalid_json", 400},
		{"POST", validate, "application/json", `{"courses":[{"id":"X","ID":"Y","name":"A"}]}`, "invalid_json", 400},
		{"POST", validate, "application/json", `{"unexpected":1}`, "invalid_json", 400},
		{"POST", validate, "application/json", strings.Repeat(" ", MaxBody+1), "body_limit", 413},
		{"POST", validate, "text/csv", "wrong,header", "invalid_csv_header", 400},
		{"POST", validate, "application/json", `{"courses":[]}`, "no_records", 422},
		{"POST", validate, "application/json", `{"courses":[{"id":"X","name":"request-body-sentinel"}]}`, "valid", 200},
		{"POST", preview, "application/json", `{"current":{},"target":{}}`, "executable", 200},
		{"POST", preview, "application/json", `{"mode":"snapshot","current":{"courses":[],"relations":[]},"target":{"courses":[],"relations":[]}}`, "invalid_scope", 422},
		{"POST", preview, "application/json", `{}`, "required_field", 422},
		{"GET", metrics, "", "", "graphops_diff_duration_seconds_count 3", 200},
	} {
		t.Run(tc.method+" "+tc.path+" "+tc.want, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.media)
			req.Header.Set("X-Request-ID", "untrusted-request-id")
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, req)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if id := rec.Header().Get("X-Request-ID"); len(id) != 32 || id == "untrusted-request-id" {
				t.Fatalf("request id: %q", id)
			}
			if tc.status == 405 && rec.Header().Get("Allow") != "POST" {
				t.Fatal("missing Allow")
			}
			if tc.path != metrics && !json.Valid(rec.Body.Bytes()) {
				t.Fatal("response is not JSON")
			}
		})
	}
	for _, value := range []string{"request-body-sentinel", "key-material-sentinel", "untrusted-request-id"} {
		if strings.Contains(logs.String(), value) {
			t.Fatal("user input leaked into logs")
		}
	}
	if !strings.Contains(logs.String(), `"msg":"diff"`) || !strings.Contains(logs.String(), `"msg":"parse"`) {
		t.Fatal("missing stage logs")
	}
}

type panickingReader struct{}

func (panickingReader) Read([]byte) (int, error) { panic("test") }

func TestRecoveryAndDeadline(t *testing.T) {
	api := New(slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	req := httptest.NewRequest(http.MethodPost, validate, panickingReader{})
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	api.ServeHTTP(res, req)
	if res.Code != 500 || !strings.Contains(res.Body.String(), "internal_error") {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	for _, canceledContext := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodGet, live, nil)
		var ctx context.Context
		var cancel context.CancelFunc
		expected := "request_timeout"
		if canceledContext {
			ctx, cancel = context.WithCancel(req.Context())
			expected = "request_canceled"
		} else {
			ctx, cancel = context.WithDeadline(req.Context(), time.Now().Add(-time.Second))
		}
		cancel()
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != 408 || !strings.Contains(rec.Body.String(), expected) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
}
