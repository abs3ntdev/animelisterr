package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abs3ntdev/animelisterr/pkg/config"
	"github.com/abs3ntdev/animelisterr/pkg/feed"
	"github.com/abs3ntdev/animelisterr/pkg/migrations"
	"github.com/abs3ntdev/animelisterr/pkg/refresher"
	"github.com/abs3ntdev/animelisterr/pkg/scrape"
	"github.com/abs3ntdev/animelisterr/pkg/server"
	"github.com/abs3ntdev/animelisterr/pkg/sonarr"
	"github.com/abs3ntdev/animelisterr/pkg/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Run any pending schema migrations before the app touches the DB.
	// This opens its own short-lived connection and returns before the
	// main pool is created, so the pool always sees a current schema.
	if err := migrations.Run(ctx, cfg.DB.DSN(), log); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(2)
	}

	st, err := store.New(ctx, cfg.DB.DSN())
	if err != nil {
		log.Error("db connect", "err", err)
		os.Exit(2)
	}
	defer st.Close()
	log.Info("db connected", "host", cfg.DB.Host, "port", cfg.DB.Port, "name", cfg.DB.Name)

	sc := sonarr.New(cfg.Sonarr.BaseURL, cfg.Sonarr.APIKey, cfg.UserAgent)
	if err := sc.Ping(ctx); err != nil {
		// non-fatal: Sonarr may be temporarily down on startup. Warn and continue.
		log.Warn("sonarr ping failed", "err", err, "url", cfg.Sonarr.BaseURL)
	} else {
		log.Info("sonarr reachable", "url", cfg.Sonarr.BaseURL)
	}

	ref := &refresher.Refresher{
		Log:     log,
		Feed:    feed.New(cfg.FeedURL, cfg.UserAgent),
		Scraper: scrape.New(cfg.UserAgent),
		Sonarr:  sc,
		Store:   st,
		TopN:    cfg.TopN,
	}

	go ref.RunLoop(ctx, cfg.RefreshInterval)

	srv := &server.Server{
		Log:            log,
		Store:          st,
		Refresher:      ref,
		MaxSeasonCount: cfg.MaxSeasonCount,
		TopN:           cfg.TopN,
	}
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("http listen", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
