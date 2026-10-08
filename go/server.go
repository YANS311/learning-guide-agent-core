package guide

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Server struct {
	dispatcher         Dispatcher
	modelEnabled       bool
	mu                 sync.Mutex
	requests, failures int
	latency            float64
	tools, routes      map[string]int
}

func NewServer(model Model) *Server {
	return &Server{dispatcher: Dispatcher{[]Agent{NewLearningAgent(model)}}, modelEnabled: model != nil, tools: map[string]int{}, routes: map[string]int{}}
}

func (s *Server) mode() string {
	if s.modelEnabled {
		return "model"
	}
	return "offline_demo"
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	chat := path == "/api/chat" || path == "/api/chat/stream"
	known := chat || path == "/" || path == "/api/health" || path == "/api/courses" || path == "/api/metrics"
	if !known {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	method := "GET"
	if chat {
		method = "POST"
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if !chat {
		switch path {
		case "/":
			writeJSON(w, 200, map[string]string{"name": "通用智能导学 Go 示例", "mode": s.mode()})
		case "/api/health":
			writeJSON(w, 200, map[string]string{"status": "ok", "mode": s.mode()})
		case "/api/courses":
			writeJSON(w, 200, map[string]any{"synthetic": true, "courses": Courses()})
		case "/api/metrics":
			s.mu.Lock()
			defer s.mu.Unlock()
			average := 0.0
			if s.requests > 0 {
				average = s.latency / float64(s.requests)
			}
			writeJSON(w, 200, map[string]any{"total_requests": s.requests, "error_count": s.failures, "avg_latency_ms": average, "tool_usage_counts": s.tools, "by_route": s.routes})
		}
		return
	}
	var body struct {
		Message *string `json:"message"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	if decoder.Decode(&body) != nil || body.Message == nil || strings.TrimSpace(*body.Message) == "" || utf8.RuneCountInString(*body.Message) > 2000 {
		writeJSON(w, 422, map[string]string{"error": "message must contain 1–2000 characters"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeJSON(w, 422, map[string]string{"error": "invalid JSON body"})
		return
	}
	var flusher http.Flusher
	if path == "/api/chat/stream" {
		var ok bool
		flusher, ok = w.(http.Flusher)
		if !ok {
			writeJSON(w, 500, map[string]string{"error": "stream unavailable"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		io.WriteString(w, "event: status\ndata: {\"status\":\"processing\"}\n\n")
		flusher.Flush()
	}
	start := time.Now()
	result, err := s.dispatcher.Dispatch(r.Context(), *body.Message)
	s.mu.Lock()
	s.requests++
	s.latency += float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		s.failures++
	}
	route := "demo"
	if s.modelEnabled && len(result.ToolsUsed) == 0 {
		route = "model"
	}
	s.routes[route]++
	for _, tool := range result.ToolsUsed {
		s.tools[tool]++
	}
	s.mu.Unlock()
	if flusher != nil {
		encoded, _ := json.Marshal(result)
		io.WriteString(w, "event: answer\ndata: "+string(encoded)+"\n\nevent: done\ndata: {}\n\n")
		flusher.Flush()
	} else {
		writeJSON(w, 200, result)
	}
}
