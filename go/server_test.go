package guide

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func request(s *Server, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func TestOfflineCoursesAndMetrics(t *testing.T) {
	s := NewServer(nil)
	if !strings.Contains(request(s, "GET", "/api/health", "").Body.String(), "offline_demo") {
		t.Fatal("wrong mode")
	}
	response := request(s, "POST", "/api/chat", `{"message":"数据结构先修"}`)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "编程入门") {
		t.Fatal(response.Body.String())
	}
	metrics := request(s, "GET", "/api/metrics", "").Body.String()
	if !strings.Contains(metrics, `"total_requests":1`) || !strings.Contains(metrics, `"course_lookup":1`) || strings.Contains(metrics, "数据结构") || strings.Contains(metrics, "accuracy") {
		t.Fatal(metrics)
	}
	if !strings.Contains(request(s, "GET", "/api/courses", "").Body.String(), `"synthetic":true`) {
		t.Fatal("missing synthetic flag")
	}
}

func TestValidationAndMethods(t *testing.T) {
	s := NewServer(nil)
	for _, body := range []string{`{}`, `{"message":null}`, `{"message":3}`, `{"message":" "}`, `{"message":""}`, `{"message":"x"} {}`, `[]`, `{"message":"` + strings.Repeat("界", 2001) + `"}`} {
		if got := request(s, "POST", "/api/chat", body).Code; got != 422 {
			t.Fatalf("expected 422, got %d for %.60s", got, body)
		}
	}
	if request(s, "GET", "/api/chat", "").Code != 405 || request(s, "GET", "/unknown", "").Code != 404 {
		t.Fatal("bad routing")
	}
	if request(s, "POST", "/api/chat", `{"message":"`+strings.Repeat("界", 2000)+`"}`).Code != 200 {
		t.Fatal("Unicode length boundary failed")
	}
}

func TestSSEAndUnknownQuestion(t *testing.T) {
	s := NewServer(nil)
	response := request(s, "POST", "/api/chat/stream", `{"message":"算法实践先修"}`)
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("wrong stream type")
	}
	body := response.Body.String()
	if !strings.Contains(body, "数据结构") || !(strings.Index(body, "event: status") < strings.Index(body, "event: answer") && strings.Index(body, "event: answer") < strings.Index(body, "event: done")) {
		t.Fatal(body)
	}
	if !strings.Contains(request(s, "POST", "/api/chat", `{"message":"如何复习"}`).Body.String(), "离线学习示例") {
		t.Fatal("missing fallback")
	}
}

type brokenModel struct{}

func (brokenModel) Chat(context.Context, string) (string, error) {
	return "", errors.New("private provider error")
}

func TestModelFailureIsPrivateAndCounted(t *testing.T) {
	s := NewServer(brokenModel{})
	body := request(s, "POST", "/api/chat", `{"message":"如何复习"}`).Body.String()
	if !strings.Contains(body, "技术问题") || strings.Contains(body, "private") {
		t.Fatal(body)
	}
	if !strings.Contains(request(s, "GET", "/api/metrics", "").Body.String(), `"error_count":1`) {
		t.Fatal("failure not counted")
	}
}

func TestToolsRetryAndRespectCancellation(t *testing.T) {
	calls := 0
	tools := ToolRegistry{"read": func(ctx context.Context, params map[string]string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("temporary")
		}
		return "recovered", nil
	}}
	answer, err := tools.Execute(context.Background(), "read", nil, time.Second)
	if err != nil || answer != "recovered" || calls != 2 {
		t.Fatal(answer, err, calls)
	}
	tools["slow"] = func(ctx context.Context, params map[string]string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := tools.Execute(ctx, "slow", nil, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestModelTransportRetriesOnlyTransientFailures(t *testing.T) {
	for _, status := range []int{401, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-only-placeholder" {
					t.Error("wrong model request")
				}
				if calls == 1 {
					w.WriteHeader(status)
					return
				}
				io.WriteString(w, `{"choices":[{"message":{"content":"示例回答"}}]}`)
			}))
			defer provider.Close()
			client := LLMClient{BaseURL: provider.URL + "/v1", APIKey: "test-only-placeholder", Model: "demo", HTTP: provider.Client()}
			answer, err := client.Chat(context.Background(), "如何复习")
			if status == 401 && (err == nil || calls != 1) {
				t.Fatal("401 was retried")
			}
			if status == 500 && (err != nil || calls != 2 || answer != "示例回答") {
				t.Fatal(answer, err, calls)
			}
		})
	}
}

func TestConcurrentMetrics(t *testing.T) {
	s := NewServer(nil)
	var wait sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 30; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if request(s, "POST", "/api/chat", `{"message":"数据结构"}`).Code != 200 {
				failures.Add(1)
			}
		}()
	}
	wait.Wait()
	var metrics map[string]any
	json.Unmarshal(request(s, "GET", "/api/metrics", "").Body.Bytes(), &metrics)
	if failures.Load() != 0 || metrics["total_requests"] != float64(30) {
		t.Fatal(metrics)
	}
}
