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
	"syscall"
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
	mysqlLab := flag.Bool("mysql-lab", false, "use the synthetic MySQL lab")
	redisLab := flag.Bool("redis-lab", false, "use isolated Redis for caching and rate limiting")
	prereq := flag.String("prereq", "", "Phase 4 loopback URL")
	documents := flag.String("evidence", "", "versioned local evidence JSON")
	sampleEvidence := flag.Bool("sample-evidence", false, "enable embedded evidence for the matching synthetic snapshot")
	rag := flag.String("rag", "", "optional Python RAG loopback URL")
	modelURL := flag.String("ollama", "", "optional local explanation Ollama URL")
	model := flag.String("model", "", "local Ollama model identifier")
	flag.Parse()
	if *port < 1024 || *port > 65535 || (*database != "") == *mysqlLab || *prereq == "" || (*modelURL == "") != (*model == "") ||
		(*sampleEvidence && *documents != "") || (*mysqlLab && (*modelURL != "" || *documents != "" || *rag != "")) {
		return errors.New("choose exactly one of --db and --mysql-lab; isolated MySQL allows synthetic rules mode only")
	}
	upstream, err := advisor.NewLocalHTTP(*prereq, 2*time.Second)
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
	metrics := new(advisor.Metrics)
	upstream.Observe(metrics)
	if remote != nil {
		remote.Observe(metrics)
	}
	var graph advisor.Graph = upstream
	const synthetic = "29cee747e9d778dd784c1f26aece1b44551a1df72c94e4a4ff9c3194f579aa42"
	if *mysqlLab {
		graph = advisor.SnapshotGuard{Graph: graph, Required: synthetic}
	}
	var limiter *advisor.RedisLab
	marker := os.Getenv("ADVISOR_LAB_MARKER")
	if *redisLab {
		limiter, err = advisor.NewRedisLab(os.Getenv("ADVISOR_REDIS_ADDR"), os.Getenv("ADVISOR_REDIS_PASSWORD"), marker, metrics)
		if err != nil {
			return err
		}
		defer limiter.Close()
		graph = &advisor.CachedGraph{Graph: graph, Cache: limiter}
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
	var evidenceSnapshot, requiredSnapshot string
	if *mysqlLab {
		requiredSnapshot = synthetic
	}
	if *sampleEvidence {
		evidenceSnapshot = synthetic
	} else {
		sample = nil
	}
	corpus, err := advisor.LoadCorpus(*documents, sample)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var store *advisor.Store
	if *mysqlLab {
		boot, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		store, err = advisor.OpenMySQL(boot, os.Getenv("ADVISOR_MYSQL_DSN"), marker)
	} else {
		store, err = advisor.OpenStore(*database)
	}
	if err != nil {
		return err
	}
	defer store.Close()
	worker := &advisor.Worker{Store: store, Metrics: metrics, Timeout: 24 * time.Second, Planner: advisor.Planner{Graph: graph,
		Evidence: advisor.RetrievalAdapter{Remote: remote, Fallback: corpus}, Narrator: narrator,
		EvidenceSnapshot: evidenceSnapshot, RequiredSnapshot: requiredSnapshot}}
	halt, err := worker.Start(ctx)
	if err != nil {
		return err
	}
	defer halt()
	web, err := fs.Sub(files, "web")
	if err != nil {
		return err
	}
	api := advisor.API{Store: store, Worker: worker, Graph: graph, Assets: http.FileServer(http.FS(web)), Metrics: metrics}
	if limiter != nil {
		api.Limiter = limiter
	}
	addr := "127.0.0.1:" + strconv.Itoa(*port)
	server := &http.Server{Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 4 * time.Second,
		ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 * 1024}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		<-ctx.Done()
		timeout, release := context.WithTimeout(context.Background(), 5*time.Second)
		defer release()
		if err := server.Shutdown(timeout); err != nil {
			_ = server.Close()
		}
	}()
	slog.Info("advisor listening", "url", fmt.Sprintf("http://%s", addr))
	err = server.ListenAndServe()
	stop()
	<-closed
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
