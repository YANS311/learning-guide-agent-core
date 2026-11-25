package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"time"

	"learning-guide/graph-ops/internal/httpapi"
	"learning-guide/graph-ops/internal/neoops"
)

func main() {
	address := flag.String("listen", "127.0.0.1:8090", "loopback address and port")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	addr, err := netip.ParseAddrPort(*address)
	if err != nil || !addr.Addr().Unmap().IsLoopback() || addr.Port() == 0 {
		log.Error("a loopback address and nonzero port are required")
		os.Exit(2)
	}
	var lab *neoops.Service
	uri, marker, key := os.Getenv("GRAPHOPS_LAB_BOLT"), os.Getenv("GRAPHOPS_LAB_MARKER"), os.Getenv("GRAPHOPS_LAB_PLAN_KEY")
	if uri != "" || marker != "" || key != "" {
		repo, err := neoops.Open(uri, marker)
		if err != nil {
			log.Error("invalid lab configuration")
			os.Exit(2)
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = repo.Close(ctx)
		}()
		lab, err = neoops.New(repo, key)
		if err != nil {
			log.Error("invalid lab signing configuration")
			os.Exit(2)
		}
	}
	server := &http.Server{Addr: *address, Handler: httpapi.NewWithLab(log, 5*time.Second, lab),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 8 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Info("starting loopback preview service", "listen", *address)
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Error("graceful shutdown failed", "error", err)
			os.Exit(1)
		}
	}
}
