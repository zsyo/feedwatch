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
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"feedwatch/internal/pace"
	"feedwatch/internal/source"
	githubsource "feedwatch/internal/source/github"
	"feedwatch/internal/webui/store"
)

// Fetcher is the one network seam of the cycle. Tests inject a fake; production
// uses RegistryFetcher so a new source type still only needs a Register call.
type Fetcher interface {
	Fetch(ctx context.Context, url, sourceType, releaseScope string, cursor source.Cursor) (source.Result, error)
}

type RegistryFetcher struct {
	reg    *source.Registry
	opts   map[string]string
	token  string
	client *http.Client
	log    *slog.Logger
}

func NewRegistryFetcher(reg *source.Registry, opts map[string]string, log *slog.Logger) RegistryFetcher {
	if log == nil {
		log = slog.Default()
	}
	return RegistryFetcher{reg: reg, opts: opts, log: log}
}

// WithGitHubCredentials attaches the optional global GitHub token. It returns
// a copy so callers can build one fetcher per process without mutating a shared
// value after the poll loop has started.
func (f RegistryFetcher) WithGitHubCredentials(token string, client *http.Client) RegistryFetcher {
	f.token = token
	f.client = client
	return f
}

func (f RegistryFetcher) Fetch(ctx context.Context, url, sourceType, releaseScope string, cursor source.Cursor) (source.Result, error) {
	typ := sourceType
	if typ == "" {
		typ = "atom"
	}
	// GitHub subscriptions store "github:owner/repo" as their URL so they can
	// share the atom url_key namespace. The source expects the bare repo id.
	specURL := url
	if githubsource.IsValidSourceType(typ) {
		specURL = strings.TrimPrefix(specURL, githubsource.StoredPrefix)
	}
	opts := f.opts
	if githubsource.IsValidSourceType(typ) {
		opts = make(map[string]string, len(f.opts)+2)
		maps.Copy(opts, f.opts)
		if f.token != "" {
			opts["token"] = f.token
		}
		if releaseScope != "" {
			opts["release_scope"] = releaseScope
		}
	}
	src, err := f.reg.Build(source.SourceSpec{
		Name: url, Type: typ, URL: specURL, Options: opts, Client: f.client,
	}, f.log)
	if err != nil {
		return source.Result{}, err
	}
	return src.Fetch(ctx, cursor)
}

// HTTPFetcherOptions builds the shared source options from the DB settings.
// Source-specific values (GitHub's token and release_scope) are added later:
// the token comes from the encrypted admin setting, and release_scope from
// each subscription's filter_json via the batch cache / source factory.
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
	// batch is the optional GitHub GraphQL prefetcher. nil means every
	// subscription uses REST (the default when no token is configured).
	batch githubsource.BatchFetcher
	// cache holds the most recent releases per url_key so subscription-driven
	// baseline snapshots can reuse poll results instead of re-fetching.
	cache *FeedCache
}

// SetBatchFetcher enables the per-cycle GraphQL prefetch channel.
func (o *Orchestrator) SetBatchFetcher(b githubsource.BatchFetcher) { o.batch = b }

// SetFeedCache attaches the shared release cache. The composition root owns
// the instance and passes the same one to the baseline service.
func (o *Orchestrator) SetFeedCache(c *FeedCache) { o.cache = c }

// FetchGate exposes the poll cycle's rate limiter so the baseline service can
// share it; subscription-triggered fetches then obey poll.concurrency/spacing.
func (o *Orchestrator) FetchGate() *pace.Gate { return o.gate }

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
