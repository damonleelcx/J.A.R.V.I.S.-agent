package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Middleware is a handler decorator.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first argument is the outermost layer,
// which is the order they are read in.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wrote {
		return
	}
	s.status = code
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.WriteHeader(http.StatusOK)
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush forwards to the underlying writer when it supports flushing, so that
// the streaming endpoints (the execution timeline) are not buffered by this
// wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RequestID assigns a correlation id and echoes it.
//
// An inbound X-Request-Id is honoured so a trace survives a reverse proxy, but
// it is validated first: an unchecked client-supplied value lands in log fields
// and would let a caller forge or pollute correlation ids.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rid := r.Header.Get("X-Request-Id")
			if !isSafeCorrelationID(rid) {
				rid = id.New(id.PrefixRequest)
			}
			ctx := logx.WithRequestID(r.Context(), rid)
			ctx = logx.WithTrace(ctx, rid, id.New(id.PrefixSpan))
			w.Header().Set("X-Request-Id", rid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isSafeCorrelationID bounds and constrains a client-supplied id.
func isSafeCorrelationID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// AccessLog records one line per request.
func AccessLog(log *logx.Logger, clk clock.Clock) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := clk.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			log.Info(r.Context(), logx.EventHTTPRequest,
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", clk.Now().Sub(start).Milliseconds(),
				"remote", ClientIPString(r),
			)
		})
	}
}

// Recover turns a panic into a 500 instead of a dropped connection.
//
// A panic here is a defect, so the stack is logged in full — but never returned
// to the caller, because a stack trace names internal paths and package layout.
func Recover(log *logx.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					// http.ErrAbortHandler is the documented way to abandon a
					// response deliberately; it is not a defect.
					if p == http.ErrAbortHandler {
						panic(p)
					}
					log.Error(r.Context(), logx.EventHTTPPanic,
						"panic", p, "stack", string(debug.Stack()),
						"method", r.Method, "path", r.URL.Path)
					WriteError(w, r, log, errs.New("httpapi.Recover", errs.CodeInternal).
						WithDetail("the request handler panicked"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps request body size.
func BodyLimit(max int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders applies conservative defaults.
func SecurityHeaders(isProduction bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			// No third-party origins are used, so the policy can be maximally
			// restrictive. 'unsafe-inline' for styles only, because the console
			// ships one inline stylesheet rather than a separate asset request.
			h.Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
					"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
			if isProduction {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// rate limiting
// ---------------------------------------------------------------------------

// RateLimiter is a fixed-window per-key limiter.
//
// # Scope, stated plainly
//
// This is in-process. Two FORGE instances behind a load balancer each enforce
// the limit independently, so the effective ceiling is the limit times the
// instance count. That is acceptable for the private, single-node deployments
// this product targets, and it is the wrong tool the moment there are several
// nodes — at which point the limiter moves to shared storage.
//
// It is a blunt second line, not the primary control: account lockout is the
// real defence for credential guessing, and it is enforced in the database
// where every instance sees it.
type RateLimiter struct {
	mu       sync.Mutex
	counters map[string]*window
	limit    int
	period   time.Duration
	clock    clock.Clock
}

type window struct {
	count    int
	resetsAt time.Time
}

// NewRateLimiter returns a limiter permitting limit requests per period per key.
func NewRateLimiter(limit int, period time.Duration, clk clock.Clock) *RateLimiter {
	return &RateLimiter{
		counters: make(map[string]*window),
		limit:    limit,
		period:   period,
		clock:    clk,
	}
}

// Allow reports whether a request for key may proceed.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.clock.Now()
	w, ok := rl.counters[key]
	if !ok || now.After(w.resetsAt) {
		rl.counters[key] = &window{count: 1, resetsAt: now.Add(rl.period)}
		// Opportunistic sweep. Without it the map grows once per distinct client
		// address forever, which is a slow memory leak an attacker can drive.
		if len(rl.counters) > 10_000 {
			for k, v := range rl.counters {
				if now.After(v.resetsAt) {
					delete(rl.counters, k)
				}
			}
		}
		return true
	}
	if w.count >= rl.limit {
		return false
	}
	w.count++
	return true
}

// LimitByIP rejects requests from an address that has exceeded the limiter.
func LimitByIP(rl *RateLimiter, log *logx.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := ClientIPString(r)
			if !rl.Allow(key) {
				log.Warn(r.Context(), logx.EventHTTPRateLimit,
					"remote", key, "method", r.Method, "path", r.URL.Path)
				WriteError(w, r, log, errs.New("httpapi.LimitByIP", errs.CodeRateLimited).
					WithDetail("too many requests from this address; wait for the interval in Retry-After"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// client address
// ---------------------------------------------------------------------------

// ClientIP resolves the caller's address.
//
// X-Forwarded-For is deliberately NOT consulted. Any client can send that
// header, so trusting it lets an attacker forge their address and evade both
// the rate limiter and the audit trail. A deployment behind a trusted proxy
// should terminate that proxy's header at the proxy itself and pass the real
// address on the connection, or this function gains an explicit allow-list of
// trusted proxy addresses — which is a decision to make deliberately, not a
// default to inherit.
func ClientIP(r *http.Request) *netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return nil
	}
	return &addr
}

// ClientIPString renders the caller's address for logs and limiter keys.
func ClientIPString(r *http.Request) string {
	if a := ClientIP(r); a != nil {
		return a.String()
	}
	return "unknown"
}
