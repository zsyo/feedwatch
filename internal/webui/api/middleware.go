//go:build webui

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"

	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/store"
)

type handler func(http.ResponseWriter, *http.Request, Ctx)

type stateKey struct{}

// handle composes: recover/headers/access log → session auth → CSRF → rate
// limit → role guard → audit → handler. Public routes still get the outer
// layer and rate limiting, but a missing session is not fatal.
func (s *Server) handle(role string, public bool, h handler) http.Handler {
	var next http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(w, r, ctxFrom(r))
	})
	next = s.audit(next)
	next = s.roleGuard(role, next)
	next = s.rateLimit(next)
	next = s.csrf(next)
	next = s.sessionAuth(public, next)
	return s.base(next)
}

func ctxPtr(r *http.Request) *Ctx {
	v, _ := r.Context().Value(stateKey{}).(*Ctx)
	if v == nil {
		return &Ctx{}
	}
	return v
}

func ctxFrom(r *http.Request) Ctx { return *ctxPtr(r) }

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.wrote {
		return
	}
	s.status = status
	s.wrote = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

func (s *Server) base(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("http: recovered panic", "panic", v, "path", r.URL.Path)
				if !rec.wrote {
					fail(rec, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}
		}()
		h := rec.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		requestID := randomID()
		h.Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), stateKey{}, &Ctx{RequestID: requestID})
		start := time.Now()
		next.ServeHTTP(rec, r.WithContext(ctx))
		s.log.Info("http", "request_id", requestID, "method", r.Method,
			"path", r.URL.Path, "status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) sessionAuth(public bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := ctxPtr(r)
		sess, u, err := s.auth.FromRequest(r)
		if err != nil {
			if !public {
				fail(w, http.StatusUnauthorized, "unauthorized", "login required")
				return
			}
		} else {
			c.Session, c.User = sess, u
			if u.MustChangePassword && !allowedBeforePasswordChange(r.URL.Path) {
				fail(w, http.StatusForbidden, "password_change_required", "change the initial password first")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func allowedBeforePasswordChange(path string) bool {
	switch path {
	case "/api/auth/me", "/api/auth/password", "/api/auth/logout", "/api/meta":
		return true
	default:
		// Let the SPA load so the user can actually reach the change-password UI.
		return !isAPIPath(path)
	}
}

func isAPIPath(path string) bool { return len(path) >= 5 && path[:5] == "/api/" }

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.IsSafeMethod(r) {
			next.ServeHTTP(w, r)
			return
		}
		c := ctxPtr(r)
		// The login endpoint has no session yet; it is protected by rate limiting.
		if c.Session.CSRFToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !auth.CheckCSRF(c.Session.CSRFToken, auth.CSRFHeader(r)) {
			fail(w, http.StatusForbidden, "csrf_failed", "missing or invalid X-CSRF-Token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) roleGuard(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role == "admin" && !auth.IsAdmin(ctxPtr(r).User) {
			fail(w, http.StatusForbidden, "forbidden", "administrator role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		switch {
		case r.URL.Path == "/api/auth/login":
			limit = 10
		case !auth.IsSafeMethod(r):
			limit = 60
		}
		if limit > 0 && !s.limiter.allow(clientIP(r)+"|"+r.URL.Path, limit, time.Minute) {
			fail(w, http.StatusTooManyRequests, "rate_limited", "too many requests, try again later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) audit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.IsSafeMethod(r) || !isAPI(r) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
		c := ctxPtr(r)
		actor := c.User.ID
		if _, err := s.db.Exec(`INSERT INTO audit_log (ts, actor_id, action, target, detail_json, ip)
			VALUES (?,?,?,?,?,?)`, store.Now(), actor, r.Method+" "+r.URL.Path, r.URL.Path, "{}", clientIP(r),
		); err != nil {
			s.log.Warn("webui: audit log write failed", "error", err)
		}
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

type bucket struct {
	count int
	reset time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]bucket
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]bucket)}
}

func (l *rateLimiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.buckets) > 1000 {
		for k, b := range l.buckets {
			if now.After(b.reset) {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		l.buckets[key] = bucket{count: 1, reset: now.Add(window)}
		return true
	}
	if b.count >= limit {
		return false
	}
	b.count++
	l.buckets[key] = b
	return true
}
