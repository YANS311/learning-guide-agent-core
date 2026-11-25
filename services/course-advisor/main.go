package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"learning-guide/course-advisor/advisor"
)

//go:embed web/* testdata/handbook.json
var files embed.FS

func main() {
	if err := run(); err != nil {
		slog.Error("advisor stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	port := flag.Int("port", 8095, "loopback HTTP port")
	database := flag.String("db", "", "dedicated course-advisor.sqlite3")
	prereq := flag.String("prereq", "", "Phase 4 loopback URL")
	documents := flag.String("evidence", "", "versioned local evidence JSON")
	sampleEvidence := flag.Bool("sample-evidence", false, "enable embedded evidence for the matching synthetic snapshot")
	rag := flag.String("rag", "", "optional Python RAG loopback URL")
	modelURL := flag.String("ollama", "", "optional local explanation Ollama URL")
	model := flag.String("model", "", "local Ollama model identifier")
	flag.Parse()
	if *port < 1024 || *port > 65535 || *database == "" || *prereq == "" || (*modelURL == "") != (*model == "") ||
		(*sampleEvidence && *documents != "") {
		return errors.New("require dedicated --db and --prereq; optional --ollama requires --model")
	}
	graph, err := advisor.NewLocalHTTP(*prereq, 2*time.Second)
	if err != nil {
		return err
	}
	var remote *advisor.LocalHTTP
	if *rag != "" {
		remote, err = advisor.NewLocalHTTP(*rag, 2*time.Second)
		if err != nil {
			return err
		}
	}
	var narrator advisor.Narrator
	if *modelURL != "" {
		h, err := advisor.NewLocalHTTP(*modelURL, 8*time.Second)
		if err != nil {
			return err
		}
		narrator = advisor.LocalNarrator{HTTP: h, Model: *model}
	}
	data, err := files.ReadFile("testdata/handbook.json")
	if err != nil {
		return err
	}
	var sample []advisor.Evidence
	if err = json.Unmarshal(data, &sample); err != nil {
		return err
	}
	var evidenceSnapshot string
	if *sampleEvidence {
		evidenceSnapshot = "29cee747e9d778dd784c1f26aece1b44551a1df72c94e4a4ff9c3194f579aa42"
	} else {
		sample = nil
	}
	corpus, err := advisor.LoadCorpus(*documents, sample)
	if err != nil {
		return err
	}
	store, err := advisor.OpenStore(*database)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	worker := &advisor.Worker{Store: store, Timeout: 24 * time.Second, Planner: advisor.Planner{Graph: graph,
		Evidence: advisor.RetrievalAdapter{Remote: remote, Fallback: corpus}, Narrator: narrator, EvidenceSnapshot: evidenceSnapshot}}
	halt, err := worker.Start(ctx)
	if err != nil {
		return err
	}
	defer halt()
	web, err := fs.Sub(files, "web")
	if err != nil {
		return err
	}
	api := advisor.API{Store: store, Worker: worker, Graph: graph, Assets: http.FileServer(http.FS(web))}
	addr := "127.0.0.1:" + strconv.Itoa(*port)
	server := &http.Server{Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 4 * time.Second,
		ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 * 1024}
	go func() {
		<-ctx.Done()
		timeout, release := context.WithTimeout(context.Background(), 5*time.Second)
		defer release()
		_ = server.Shutdown(timeout)
	}()
	slog.Info("advisor listening", "url", fmt.Sprintf("http://%s", addr))
	err = server.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
