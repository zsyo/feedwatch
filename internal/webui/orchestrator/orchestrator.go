//go:build webui

// Package orchestrator owns the WebUI variant's poll cycle. It deliberately
// does not reuse internal/watcher: that type assumes a static task list and a
// single notifier, while WebUI needs a dynamic URL pool, per-user filters and
// one-to-many delivery. All leaf pieces (atom source, filter, pace.Gate,
// notifier templates) are reused unchanged.
package orchestrator

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"feedwatch/internal/pace"
	"feedwatch/internal/source"
	"feedwatch/internal/webui/store"
)

// Fetcher is the one network seam of the cycle. Tests inject a fake; production
// uses RegistryFetcher so a new source type still only needs a Register call.
type Fetcher interface {
	Fetch(ctx context.Context, url string, cursor source.Cursor) (source.Result, error)
}

type RegistryFetcher struct {
	reg  *source.Registry
	opts map[string]string
	log  *slog.Logger
}

func NewRegistryFetcher(reg *source.Registry, opts map[string]string, log *slog.Logger) RegistryFetcher {
	if log == nil {
		log = slog.Default()
	}
	return RegistryFetcher{reg: reg, opts: opts, log: log}
}

func (f RegistryFetcher) Fetch(ctx context.Context, url string, cursor source.Cursor) (source.Result, error) {
	src, err := f.reg.Build(source.SourceSpec{
		Name: url, Type: "atom", URL: url, Options: f.opts,
	}, f.log)
	if err != nil {
		return source.Result{}, err
	}
	return src.Fetch(ctx, cursor)
}

// HTTPFetcherOptions builds the atom source options from the DB settings.
func HTTPFetcherOptions(timeout, userAgent string, retryMax int, retryBaseDelay string, conditional bool) map[string]string {
	return map[string]string{
		"http_timeout":     timeout,
		"user_agent":       userAgent,
		"retry_max":        strconv.Itoa(retryMax),
		"retry_base_delay": retryBaseDelay,
		"conditional":      strconv.FormatBool(conditional),
	}
}

type Options struct {
	Interval         time.Duration
	Timeout          time.Duration
	FetchConcurrency int
	FetchSpacing     time.Duration
	SubjectPrefix    string
}

func (o Options) withDefaults() Options {
	if o.Interval <= 0 {
		o.Interval = 30 * time.Minute
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	if o.FetchConcurrency < 1 {
		o.FetchConcurrency = 4
	}
	if o.SubjectPrefix == "" {
		o.SubjectPrefix = "[FeedWatch]"
	}
	return o
}

type Orchestrator struct {
	st      *store.Store
	fetcher Fetcher
	gate    *pace.Gate
	opt     Options
	log     *slog.Logger
	now     func() time.Time
}

func New(st *store.Store, f Fetcher, opt Options, log *slog.Logger) *Orchestrator {
	if log == nil {
		log = slog.Default()
	}
	opt = opt.withDefaults()
	return &Orchestrator{
		st: st, fetcher: f,
		gate: pace.New(opt.FetchConcurrency, opt.FetchSpacing),
		opt:  opt, log: log, now: time.Now,
	}
}

// Run polls once immediately and then on every tick until ctx is cancelled.
// A failing cycle is logged but never stops the loop (mirrors base behaviour).
func (o *Orchestrator) Run(ctx context.Context) error {
	if err := o.RunOnce(ctx); err != nil {
		o.log.Error("webui: poll cycle failed", "error", err)
	}
	ticker := time.NewTicker(o.opt.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := o.RunOnce(ctx); err != nil {
				o.log.Error("webui: poll cycle failed", "error", err)
			}
		}
	}
}

// Settings exposes DB-backed settings to the composition root.
func Settings(st *store.Store) (map[string]string, error) { return st.Settings() }
