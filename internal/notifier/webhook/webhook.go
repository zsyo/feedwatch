// Package webhook hosts every HTTP-based notification channel.
//
// The design keeps the plumbing in one place: this file owns rendering, the
// HTTP client, retries (429 / 5xx / network errors) and logging. Each concrete
// channel only implements Adapter: turn a rendered template.Message into a
// Request, and optionally inspect the *successful* response body for
// vendor-specific error codes (many IM bots return HTTP 200 with errcode != 0).
//
// Adding a channel therefore means adding one small adapter file plus one
// entry in adapters.go. No watcher / config / template changes are required.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
	notiftpl "feedwatch/internal/notifier/template"
)

// Adapter is the channel-specific half of a webhook notifier.
type Adapter interface {
	Type() string
	// Validate is called once at startup so a missing credential fails fast
	// instead of silently breaking the first poll cycle.
	Validate(o Options) error
	Build(m notiftpl.Message, o Options) (Request, error)
	// Check is called only for 2xx responses. Returning an error fails the
	// channel without a retry (vendor-side errors are not transient).
	Check(status int, body []byte) error
}

// Options are the expanded (env references already resolved) channel options.
type Options struct {
	Values map[string]string
	Now    func() time.Time // injectable for deterministic signature tests
}

func (o Options) Get(key string) string { return strings.TrimSpace(o.Values[key]) }

func (o Options) Or(key, def string) string {
	if v := o.Get(key); v != "" {
		return v
	}
	return def
}

func (o Options) Time() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

type Request struct {
	Method  string // empty means POST
	URL     string
	Headers map[string]string
	Body    []byte
}

type Notifier struct {
	adapter       Adapter
	name          string
	values        map[string]string
	client        *http.Client
	subjectPrefix string
	retryMax      int
	retryBase     time.Duration
	log           *slog.Logger
	now           func() time.Time
}

// Factory adapts an Adapter to notifier.Factory so the composition root can
// register every channel with a single loop.
func Factory(a Adapter) notifier.Factory {
	return func(spec notifier.NotifierSpec, log *slog.Logger) (notifier.Notifier, error) {
		return New(a, spec, log)
	}
}

func New(a Adapter, spec notifier.NotifierSpec, log *slog.Logger) (notifier.Notifier, error) {
	if a == nil {
		return nil, fmt.Errorf("webhook: nil adapter")
	}
	if log == nil {
		log = slog.Default()
	}
	values := make(map[string]string, len(spec.Options))
	maps.Copy(values, spec.Options)
	if err := a.Validate(Options{Values: values}); err != nil {
		return nil, fmt.Errorf("%s: %w", a.Type(), err)
	}

	timeout := 10 * time.Second
	if raw := strings.TrimSpace(values["timeout"]); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("%s: invalid timeout %q: %w", a.Type(), raw, err)
		}
		timeout = d
	}
	retryMax := 2
	if raw := strings.TrimSpace(values["retry_max"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s: invalid retry_max %q", a.Type(), raw)
		}
		retryMax = n
	}
	retryBase := 500 * time.Millisecond
	if raw := strings.TrimSpace(values["retry_base_delay"]); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("%s: invalid retry_base_delay %q", a.Type(), raw)
		}
		retryBase = d
	}

	client := spec.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	prefix := spec.SubjectPrefix
	if v := strings.TrimSpace(values["subject_prefix"]); v != "" {
		prefix = v
	}
	return &Notifier{
		adapter: a, name: spec.Name, values: values, client: client,
		subjectPrefix: prefix, retryMax: retryMax, retryBase: retryBase, log: log,
		now: time.Now,
	}, nil
}

func (n *Notifier) Type() string { return n.adapter.Type() }
func (n *Notifier) Name() string { return n.name }

func (n *Notifier) Send(ctx context.Context, note domain.Notification) error {
	m, err := notiftpl.RenderMessage(note, n.subjectPrefix)
	if err != nil {
		return fmt.Errorf("%s: %w", n.Type(), err)
	}
	req, err := n.adapter.Build(m, Options{Values: n.values, Now: n.now})
	if err != nil {
		return fmt.Errorf("%s: build request: %w", n.Type(), err)
	}
	if err := validateURL(req.URL); err != nil {
		return fmt.Errorf("%s: %w", n.Type(), err)
	}
	status, body, err := n.post(ctx, req)
	if err != nil {
		return fmt.Errorf("%s: %w", n.Type(), err)
	}
	if err := n.adapter.Check(status, body); err != nil {
		return fmt.Errorf("%s: %w", n.Type(), err)
	}
	return nil
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid webhook_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook_url must be http(s), got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("webhook_url has no host")
	}
	return nil
}

// post retries only transient conditions. Vendor error codes are deliberately
// checked after the loop: retrying a bad token or a bad payload is pointless.
func (n *Notifier) post(ctx context.Context, req Request) (int, []byte, error) {
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(req.Body))
		if err != nil {
			return 0, nil, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("User-Agent", "feedwatch/1.0")
		httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
		for k, v := range req.Headers {
			httpReq.Header.Set(k, v)
		}

		resp, err := n.client.Do(httpReq)
		if err != nil {
			if attempt >= n.retryMax {
				return 0, nil, fmt.Errorf("send: %w", err)
			}
			if !wait(ctx, n.backoff(attempt, 0)) {
				return 0, nil, ctx.Err()
			}
			continue
		}
		body := readLimited(resp.Body)
		_ = resp.Body.Close()

		status := resp.StatusCode
		if status == http.StatusTooManyRequests || status >= 500 {
			delay := retryAfter(resp.Header.Get("Retry-After"), n.now())
			if delay <= 0 {
				delay = telegramRetryAfter(body)
			}
			transient := fmt.Errorf("http %d: %s", status, clip(body))
			if attempt >= n.retryMax {
				return 0, nil, transient
			}
			if !wait(ctx, n.backoff(attempt, delay)) {
				return 0, nil, ctx.Err()
			}
			continue
		}
		if status < 200 || status > 299 {
			return 0, nil, fmt.Errorf("http %d: %s", status, clip(body))
		}
		return status, body, nil
	}
}

func (n *Notifier) backoff(attempt int, retryAfter time.Duration) time.Duration {
	shift := min(attempt, 10)
	d := min(n.retryBase*time.Duration(1<<shift), 30*time.Second)
	return min(max(d, retryAfter), 60*time.Second)
}

func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func readLimited(r io.Reader) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	return b
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 512 {
		s = s[:512] + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}

// retryAfter accepts both "seconds" and an HTTP-date, per RFC 7231.
func retryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// Telegram puts the backoff hint in the JSON body: {"parameters":{"retry_after":3}}.
func telegramRetryAfter(body []byte) time.Duration {
	var r struct {
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &r) == nil && r.Parameters.RetryAfter > 0 {
		return time.Duration(r.Parameters.RetryAfter) * time.Second
	}
	return 0
}
