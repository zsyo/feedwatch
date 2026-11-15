//go:build webui

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"feedwatch/internal/source"
	atomsource "feedwatch/internal/source/atom"
	"feedwatch/internal/webui"
	"feedwatch/internal/webui/api"
	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/orchestrator"
	"feedwatch/internal/webui/presets"
	"feedwatch/internal/webui/secrets"
	"feedwatch/internal/webui/store"
)

// webuiLaunch takes over the process in webui builds. The base build has a
// constant-false twin in webui_hook.go, so neither build sees both paths.
func webuiLaunch(cfgPath, logLevel, logFormat string, once, dryRun bool) (bool, error) {
	return true, runWebUI(cfgPath, logLevel, logFormat, once, dryRun)
}

func runWebUI(cfgPath, logLevel, logFormat string, once, dryRun bool) error {
	log := newLogger(logLevel, logFormat)
	dbPath := envOr("FEEDWATCH_DB_PATH", "data/feedwatch.db")
	addr := envOr("FEEDWATCH_HTTP_ADDR", ":8080")
	ttl := 7 * 24 * time.Hour
	if raw := os.Getenv("FEEDWATCH_WEBUI_SESSION_TTL"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("webui: invalid FEEDWATCH_WEBUI_SESSION_TTL %q: %w", raw, err)
		}
		ttl = parsed
	}
	secure := envBool("FEEDWATCH_WEBUI_COOKIE_SECURE")

	// Fails fast on an unwritable data directory (WAL needs -wal/-shm siblings),
	// before any HTTP listener or notification is started.
	st, err := store.Open(dbPath, log)
	if err != nil {
		return err
	}
	defer st.Close()

	// The master key lives next to the database so a volume backup carries both.
	// A failure here is fatal: running with a fresh key would silently orphan
	// every stored channel credential.
	sealer, err := secrets.LoadOrCreate(filepath.Dir(dbPath), log)
	if err != nil {
		return err
	}

	// The WebUI has no configuration file: a fresh database gets one admin and
	// the built-in defaults, nothing else.
	if _, err := webui.Seed(st, log); err != nil {
		return fmt.Errorf("webui: seed database: %w", err)
	}
	if cfgPath != "" {
		log.Warn("webui: -config is ignored; the WebUI variant is configured entirely in the browser",
			"path", cfgPath)
	}

	// Seed writes the built-in defaults on an empty database; from then on the
	// settings table (editable via /api/admin/settings) is the only source.
	settings, err := st.Settings()
	if err != nil {
		return err
	}
	dur := func(key string, def time.Duration) time.Duration {
		if d, err := time.ParseDuration(settings[key]); err == nil {
			return d
		}
		return def
	}
	intVal := func(key string, def int) int {
		if n, err := strconv.Atoi(settings[key]); err == nil {
			return n
		}
		return def
	}
	boolVal := func(key string, def bool) bool {
		if b, err := strconv.ParseBool(settings[key]); err == nil {
			return b
		}
		return def
	}

	srcReg := source.NewRegistry()
	srcReg.Register("atom", atomsource.New)
	fetchOpts := orchestrator.HTTPFetcherOptions(
		dur("http.timeout", 15*time.Second).String(),
		settings["http.user_agent"],
		intVal("http.retry_max", 3),
		dur("http.retry_base_delay", 500*time.Millisecond).String(),
		boolVal("http.conditional", true),
	)
	fetcher := orchestrator.NewRegistryFetcher(srcReg, fetchOpts, log)
	orch := orchestrator.New(st, fetcher, orchestrator.Options{
		Interval:         dur("poll.interval", 30*time.Minute),
		Timeout:          dur("poll.timeout", 2*time.Minute),
		FetchConcurrency: intVal("poll.concurrency", 4),
		FetchSpacing:     dur("poll.request_spacing", 100*time.Millisecond),
		SubjectPrefix:    settings["notify.subject_prefix"],
	}, log)
	worker := orchestrator.NewDeliveryWorker(st, log, dur("notify.send_spacing", 250*time.Millisecond), sealer)

	assets, err := webui.Assets()
	if err != nil {
		return fmt.Errorf("webui: load embedded assets: %w", err)
	}
	am := auth.New(st.DB(), ttl, secure)

	// The remote preset loader is built once from DB settings. Like the polling
	// and delivery knobs, presets.* is read at startup, not hot-reloaded.
	presetLoader := presets.NewLoader()
	presetLoader.BaseURL = settings["presets.base_url"]
	if presetLoader.BaseURL == "" {
		presetLoader.BaseURL = webui.DefaultPresetBaseURL
	}
	presetLoader.Timeout = dur("presets.timeout", 15*time.Second)
	if ua := settings["http.user_agent"]; ua != "" {
		presetLoader.UserAgent = ua
	}
	srv := api.New(st, am, assets, log, version, sealer, api.WithPresetLoader(presetLoader))

	if dryRun {
		log.Info("webui: -dry-run is ignored; the WebUI variant has no dry-run poll mode")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// -once is a full offline-friendly smoke cycle: poll, plan, deliver, exit.
	if once {
		if err := orch.RunOnce(ctx); err != nil {
			return err
		}
		if _, err := orchestrator.PlanDeliveries(ctx, st.DB()); err != nil {
			return err
		}
		if _, _, err := worker.DeliverPending(ctx); err != nil {
			return err
		}
		log.Info("webui: -once cycle finished")
		return nil
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go purgeSessions(ctx, am, log)
	go orch.Run(ctx)
	go runDeliveryLoop(ctx, st, worker, log)
	shutdownErr := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr <- httpServer.Shutdown(shutdownCtx)
	}()

	log.Info("webui: listening", "addr", addr, "db", dbPath,
		"poll_interval", dur("poll.interval", 30*time.Minute).String(),
		"send_spacing", dur("notify.send_spacing", 250*time.Millisecond).String(),
		"session_ttl", ttl.String(), "cookie_secure", secure)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return <-shutdownErr
	}
	return err
}

func runDeliveryLoop(ctx context.Context, st *store.Store, w *orchestrator.DeliveryWorker, log *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	drain := func() {
		if n, err := orchestrator.PlanDeliveries(ctx, st.DB()); err != nil {
			log.Warn("webui: plan deliveries failed", "error", err)
		} else if n > 0 {
			log.Info("webui: planned independent deliveries", "groups", n)
		}
		if sent, failed, err := w.DeliverPending(ctx); err != nil {
			log.Warn("webui: delivery pass failed", "error", err)
		} else if sent+failed > 0 {
			log.Info("webui: delivery pass finished", "sent", sent, "failed", failed)
		}
	}
	drain() // pick up the first-start baseline immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			drain()
		}
	}
}

func purgeSessions(ctx context.Context, am *auth.Manager, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := am.PurgeExpired(); err != nil {
				log.Warn("webui: purge expired sessions failed", "error", err)
			} else if n > 0 {
				log.Info("webui: purged expired sessions", "count", n)
			}
		}
	}
}
