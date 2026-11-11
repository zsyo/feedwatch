// Command feedwatch polls release feeds and emails newly published versions.
// This file is the single composition root: it is the only place that knows
// both the configuration and the concrete implementations.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"feedwatch/internal/config"
	"feedwatch/internal/filter"
	"feedwatch/internal/notifier"
	emailnotify "feedwatch/internal/notifier/email"
	lognotify "feedwatch/internal/notifier/log"
	webhooknotify "feedwatch/internal/notifier/webhook"
	"feedwatch/internal/source"
	atomsource "feedwatch/internal/source/atom"
	"feedwatch/internal/store"
	jsonstore "feedwatch/internal/store/jsonfile"
	"feedwatch/internal/watcher"
)

// version can be overridden with -ldflags "-X main.version=...".
var version = "1.0.0"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && b
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// dryRunStatePath keeps dry-run state separate from the real state file, so
// local previews can never suppress a real baseline notification.
func dryRunStatePath(p string) string {
	ext := filepath.Ext(p)
	if ext == "" {
		return p + ".dryrun"
	}
	return strings.TrimSuffix(p, ext) + ".dryrun" + ext
}

func feedOptions(f config.FeedConfig, http config.HTTPConfig) map[string]string {
	opts := map[string]string{
		"http_timeout":     http.Timeout.String(),
		"retry_max":        strconv.Itoa(http.RetryMax),
		"retry_base_delay": http.RetryBaseDelay.String(),
		"conditional":      strconv.FormatBool(http.Conditional),
		"user_agent":       http.UserAgent,
	}
	// Per-feed options override the global HTTP defaults.
	maps.Copy(opts, f.Options)
	return opts
}

// channelSpecs is the only place that knows how YAML channels map to the
// neutral NotifierSpec. Secrets are read from the environment here and
// nowhere else; ${ENV_VAR} placeholders in options are expanded by config.
func channelSpecs(cfg config.Config) ([]notifier.NotifierSpec, error) {
	base := notifier.NotifierSpec{
		From:          cfg.Notify.Email.From,
		To:            cfg.Notify.Email.To,
		Cc:            cfg.Notify.Email.Cc,
		SubjectPrefix: cfg.Notify.Email.SubjectPrefix,
		APIKey:        os.Getenv(cfg.Notify.Email.APIKeyEnv),
	}

	// Legacy single-channel form: notify.type + notify.options.
	if len(cfg.Notify.Channels) == 0 {
		opts, missing := config.ExpandEnvRefs(cfg.Notify.Options)
		if len(missing) > 0 {
			return nil, fmt.Errorf("notify.options: unset environment variables: %s", strings.Join(missing, ", "))
		}
		base.Type, base.Options = cfg.Notify.Type, opts
		return []notifier.NotifierSpec{base}, nil
	}

	specs := make([]notifier.NotifierSpec, 0, len(cfg.Notify.Channels))
	for i, ch := range cfg.Notify.Channels {
		if !ch.Enabled {
			continue
		}
		// Global notify.options act as defaults; per-channel options win.
		merged := make(map[string]string, len(cfg.Notify.Options)+len(ch.Options))
		maps.Copy(merged, cfg.Notify.Options)
		maps.Copy(merged, ch.Options)
		opts, missing := config.ExpandEnvRefs(merged)
		if len(missing) > 0 {
			label := ch.Name
			if label == "" {
				label = fmt.Sprintf("%s#%d", ch.Type, i)
			}
			return nil, fmt.Errorf("notify channel %q: unset environment variables: %s",
				label, strings.Join(missing, ", "))
		}
		sp := base
		sp.Type, sp.Name, sp.Options = ch.Type, ch.Name, opts
		specs = append(specs, sp)
	}
	return specs, nil
}

func buildNotifier(cfg config.Config, reg *notifier.Registry, dryRun bool, log *slog.Logger) (notifier.Notifier, error) {
	if dryRun {
		// Dry-run replaces the entire fan-out, so no channel can send for real.
		return reg.Build(notifier.NotifierSpec{
			Type: "log", SubjectPrefix: cfg.Notify.Email.SubjectPrefix,
		}, log)
	}
	specs, err := channelSpecs(cfg)
	if err != nil {
		return nil, err
	}
	built := make([]notifier.Notifier, 0, len(specs))
	for _, sp := range specs {
		n, err := reg.Build(sp, log)
		if err != nil {
			return nil, fmt.Errorf("build notifier %q: %w", sp.Type, err)
		}
		built = append(built, n)
	}
	if len(built) == 1 {
		return built[0], nil
	}
	policy, err := notifier.ParseFailurePolicy(cfg.Notify.OnPartialFailure)
	if err != nil {
		return nil, err
	}
	return notifier.NewMulti(built, policy, log)
}

func notifyLabel(cfg config.Config, dryRun bool) string {
	if dryRun {
		return "log (dry-run)"
	}
	if len(cfg.Notify.Channels) == 0 {
		return cfg.Notify.Type
	}
	parts := make([]string, 0, len(cfg.Notify.Channels))
	for _, ch := range cfg.Notify.Channels {
		if ch.Enabled {
			parts = append(parts, ch.Type)
		}
	}
	return strings.Join(parts, "+")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "feedwatch:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", envOr("FEEDWATCH_CONFIG", "config.yaml"), "path to the YAML config file")
	once := flag.Bool("once", false, "run exactly one poll cycle then exit")
	dryRun := flag.Bool("dry-run", envBool("FEEDWATCH_DRY_RUN"), "preview mails in the log; never send, never touch the real state file")
	logLevel := flag.String("log-level", envOr("FEEDWATCH_LOG_LEVEL", ""), "override log.level")
	logFormat := flag.String("log-format", envOr("FEEDWATCH_LOG_FORMAT", ""), "override log.format (text|json)")
	statePath := flag.String("state-path", envOr("FEEDWATCH_STATE_PATH", ""), "override store.path")
	pollInterval := flag.String("poll-interval", envOr("FEEDWATCH_POLL_INTERVAL", ""), "override poll.interval")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("feedwatch", version)
		return nil
	}

	cfg, err := config.Load(*cfgPath, config.Overrides{
		StatePath:    *statePath,
		LogLevel:     *logLevel,
		LogFormat:    *logFormat,
		PollInterval: *pollInterval,
		DryRun:       *dryRun,
	})
	if err != nil {
		return err
	}
	log := newLogger(cfg.Log.Level, cfg.Log.Format)

	if *dryRun {
		cfg.Store.Path = dryRunStatePath(cfg.Store.Path)
	}
	enabled := 0
	for _, f := range cfg.Feeds {
		if f.Enabled {
			enabled++
		}
	}
	log.Info("feedwatch starting",
		"version", version, "feeds", enabled, "interval", cfg.Poll.Interval.String(),
		"notify", notifyLabel(cfg, *dryRun), "dry_run", *dryRun, "state", cfg.Store.Path)
	if *dryRun {
		log.Info("dry-run mode: notifications are only written to the log; state is isolated")
	}

	// ---- registries: adding a new implementation needs one Register() line ----
	srcReg := source.NewRegistry()
	srcReg.Register("atom", atomsource.New)

	notifReg := notifier.NewRegistry()
	notifReg.Register("email", emailnotify.New)
	notifReg.Register("log", lognotify.New)
	// One line registers every HTTP-based channel; see webhook.Adapters().
	for typ, a := range webhooknotify.Adapters() {
		notifReg.Register(typ, webhooknotify.Factory(a))
	}

	stReg := store.NewRegistry()
	stReg.Register("jsonfile", jsonstore.New)

	st, err := stReg.Build(store.StoreSpec{
		Type: cfg.Store.Type, Path: cfg.Store.Path, Options: cfg.Store.Options,
	}, log)
	if err != nil {
		return fmt.Errorf("build store: %w", err)
	}

	n, err := buildNotifier(cfg, notifReg, *dryRun, log)
	if err != nil {
		return fmt.Errorf("build notifier: %w", err)
	}

	tasks := make([]watcher.FeedTask, 0, enabled)
	for _, f := range cfg.Feeds {
		if !f.Enabled {
			continue
		}
		src, err := srcReg.Build(source.SourceSpec{
			Name: f.Name, Type: f.Type, URL: f.URL, Options: feedOptions(f, cfg.HTTP),
		}, log)
		if err != nil {
			return fmt.Errorf("build source %q: %w", f.Name, err)
		}
		flt, err := filter.New(filter.Rules{
			StableOnly:     f.Filter.StableOnly,
			Include:        f.Filter.Include,
			Exclude:        f.Filter.Exclude,
			VersionPattern: f.Filter.VersionPattern,
		})
		if err != nil {
			return fmt.Errorf("build filter for %q: %w", f.Name, err)
		}
		tasks = append(tasks, watcher.FeedTask{Name: f.Name, Source: src, Filter: flt})
	}

	w := watcher.New(tasks, st, n, watcher.Options{
		Interval:   cfg.Poll.Interval.D(),
		Timeout:    cfg.Poll.Timeout.D(),
		RunOnStart: cfg.Poll.RunOnStart,
		NotifyMode: cfg.Notify.Mode,
	}, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		return w.RunOnce(ctx)
	}
	return w.Run(ctx)
}
