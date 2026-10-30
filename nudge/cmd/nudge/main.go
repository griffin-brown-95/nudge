// Command nudge is the API behind the "A or B?" experiment page.
//
// Environment:
//
//	PORT              listen port (default 8080; set by Cloud Run)
//	DATABASE_URL      Supabase pooler URL, transaction mode (port 6543)
//	STORE             "postgres" (default) or "memory" for local frontend work
//	ALLOWED_ORIGIN    comma-separated origins allowed by CORS
//	EXPERIMENTS_PATH  experiment config (default experiments.yaml)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nudge/internal/assign"
	"nudge/internal/config"
	"nudge/internal/httpapi"
	"nudge/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(envOr("EXPERIMENTS_PATH", "experiments.yaml"))
	if err != nil {
		return err
	}

	var st store.Store
	switch envOr("STORE", "postgres") {
	case "memory":
		log.Warn("using in-memory store: events are NOT persisted")
		st = store.NewMemory()
	case "postgres":
		url := os.Getenv("DATABASE_URL")
		if url == "" {
			return errors.New("DATABASE_URL is required (or set STORE=memory)")
		}
		pg, err := store.NewPostgres(ctx, url)
		if err != nil {
			return err
		}
		defer pg.Close()
		st = pg
	default:
		return errors.New("STORE must be postgres or memory")
	}

	origins := strings.Split(os.Getenv("ALLOWED_ORIGIN"), ",")
	if strings.TrimSpace(os.Getenv("ALLOWED_ORIGIN")) == "" {
		log.Warn("ALLOWED_ORIGIN is empty: all browser requests will be refused")
	}

	api := httpapi.New(httpapi.Options{
		Assigner:       assign.New(cfg),
		BlockSize:      cfg.BlockSize,
		Store:          st,
		AllowedOrigins: origins,
		Limiter:        httpapi.NewLimiter(10, 60),
		Logger:         log,
	})

	srv := &http.Server{
		Addr:              ":" + envOr("PORT", "8080"),
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr, "experiment_version", cfg.Version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Info("shutting down")
	return srv.Shutdown(shutdownCtx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
