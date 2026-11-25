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
	"strconv"
	"strings"
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
	var auth *httpapi.Auth
	uri, marker, key := os.Getenv("GRAPHOPS_LAB_BOLT"), os.Getenv("GRAPHOPS_LAB_MARKER"), os.Getenv("GRAPHOPS_LAB_PLAN_KEY")
	secret := os.Getenv("GRAPHOPS_ADMIN_TOKEN")
	if secret != "" {
		role := os.Getenv("GRAPHOPS_ADMIN_ROLE")
		if role == "" {
			role = "operator"
		}
		ttl := 15 * time.Minute
		if configured := os.Getenv("GRAPHOPS_ADMIN_SESSION_SECONDS"); configured != "" {
			n, err := strconv.Atoi(configured)
			if err != nil || n < 1 || n > 3600 {
				log.Error("invalid session lifetime")
				os.Exit(2)
			}
			ttl = time.Duration(n) * time.Second
		}
		auth, err = httpapi.NewAuth(secret, role, ttl)
		if err != nil || strings.EqualFold(secret, marker) || strings.EqualFold(secret, key) {
			log.Error("invalid or reused administrator configuration")
			os.Exit(2)
		}
	}
	if uri != "" || marker != "" || key != "" {
		if auth == nil {
			log.Error("administrator configuration is required for lab mode")
			os.Exit(2)
		}
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
		if configured := os.Getenv("GRAPHOPS_LAB_PLAN_SECONDS"); configured != "" {
			n, parseErr := strconv.Atoi(configured)
			if parseErr != nil || n < 1 || n > 180 || lab.SetPlanLifetime(time.Duration(n)*time.Second) != nil {
				log.Error("invalid lab plan lifetime")
				os.Exit(2)
			}
		}
		probe, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := lab.Ready(probe); err != nil {
			log.Warn("lab identity or dependency is not ready")
		}
		cancel()
	}
	server := &http.Server{Addr: *address, Handler: httpapi.NewWithLab(log, 5*time.Second, lab, auth),
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
