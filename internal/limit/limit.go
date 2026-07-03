// SPDX-License-Identifier: Elastic-2.0

// Package limit provides in-process, edge-independent rate limiting and request
// hardening, so the CorralAI brain is abuse-resistant as a standalone binary —
// NOT reliant on Cloudflare/nginx/etc., which a self-hoster may not run. Every
// protection here travels inside the binary.
package limit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Keyed is a per-key token-bucket rate limiter with idle-key eviction.
type Keyed struct {
	mu   sync.Mutex
	lims map[string]*entry
	r    rate.Limit
	b    int
}

type entry struct {
	lim  *rate.Limiter
	seen time.Time
}

// New builds a limiter allowing perMinute sustained requests with the given burst.
func New(perMinute, burst int) *Keyed {
	k := &Keyed{lims: map[string]*entry{}, r: rate.Limit(float64(perMinute) / 60.0), b: burst}
	go k.gc()
	return k
}

func (k *Keyed) allow(key string) bool {
	k.mu.Lock()
	e := k.lims[key]
	if e == nil {
		e = &entry{lim: rate.NewLimiter(k.r, k.b)}
		k.lims[key] = e
	}
	e.seen = time.Now()
	k.mu.Unlock()
	return e.lim.Allow()
}

func (k *Keyed) gc() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		cut := time.Now().Add(-30 * time.Minute)
		k.mu.Lock()
		for key, e := range k.lims {
			if e.seen.Before(cut) {
				delete(k.lims, key)
			}
		}
		k.mu.Unlock()
	}
}

// ByIP rate-limits per client IP (pre-auth: caps unauthenticated floods and token
// brute-force). ipHeader (e.g. "CF-Connecting-IP", "X-Real-IP") is honored only
// when set — use it ONLY behind a proxy you trust to set it, else it's spoofable;
// with it empty, RemoteAddr is used (correct for a directly-exposed binary).
func (k *Keyed) ByIP(ipHeader string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !k.allow("ip:" + clientIP(r, ipHeader)) {
			tooMany(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ByKey rate-limits per a caller-supplied key (e.g. the verified principal,
// post-auth). An empty key is not limited (so it must run after authentication).
func (k *Keyed) ByKey(keyFn func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key := keyFn(r); key != "" && !k.allow(key) {
			tooMany(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
}

func clientIP(r *http.Request, header string) string {
	if header != "" {
		if v := r.Header.Get(header); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// MaxBody caps request body size (anti-OOM); MCP messages are tiny, so a small
// limit is safe. Applies via http.MaxBytesReader so oversize bodies 413.
func MaxBody(n int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}
