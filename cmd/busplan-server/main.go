// busplan-server exposes the planner as an HTTP service. See internal/server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/busscheduler/internal/app"
	"github.com/example/busscheduler/internal/results"
	"github.com/example/busscheduler/internal/server"
)

func main() {
	var cfg app.Config
	cfg.Bind(flag.CommandLine)
	cfg.BindServer(flag.CommandLine)
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc, closeFn, err := cfg.Build(ctx, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer closeFn()

	s := server.New(svc, &results.Store{TTL: cfg.ResultTTL}, log, cfg.MaxConcurrentPlans)
	s.PlanTimeout = cfg.PlanTimeout
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a synchronous plan may legitimately run for minutes.
		// Callers that cannot hold a connection that long should use ?async=true.
	}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()

	log.Info("listening", "addr", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}
