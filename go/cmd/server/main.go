package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	guide "learning-guide-agent-core/go"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func main() {
	port, err := strconv.Atoi(env("PORT", "8082"))
	if err != nil || port < 1 || port > 65535 {
		log.Fatal("PORT must be between 1 and 65535")
	}
	var model guide.Model
	if strings.EqualFold(os.Getenv("LLM_ENABLED"), "true") {
		model = &guide.LLMClient{BaseURL: env("LLM_BASE_URL", "http://localhost:11434/v1"), APIKey: env("LLM_API_KEY", "ollama"), Model: env("LLM_MODEL", "qwen2.5:7b")}
	}
	server := &http.Server{Addr: "127.0.0.1:" + strconv.Itoa(port), Handler: guide.NewServer(model), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("Learning guide Go example: http://%s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
