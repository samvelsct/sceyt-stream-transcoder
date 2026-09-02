// Package originrouter implements the Origin Router (Epic D): a stateless
// HTTP reverse proxy that routes a viewer's LL-HLS request to whichever
// StreamBridge instance the Ownership Registry (internal/registry) says
// owns that session.
//
// This is the one piece of the scaling design Fleet Controller has no role
// in — Fleet Controller only ever helps place a *new* session; it has no
// concept of routing an anonymous viewer's request to an *existing* one.
package originrouter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	zlog "github.com/rs/zerolog/log"

	"vt-stream-transcoder/internal/registry"
)

// pathPrefix mirrors internal/httpserver.Server's own route so a sessionID
// is parsed identically on both sides of the proxy.
const pathPrefix = "/live/streams/"

// OwnershipResolver resolves a session to its current ownership record.
// *registry.Registry satisfies this.
type OwnershipResolver interface {
	Get(ctx context.Context, sessionID string) (*registry.Record, error)
}

// LivenessChecker is Epic D4's liveness cross-check seam — see
// internal/fleetclient for the real (Fleet Controller-backed) and no-op
// implementations.
type LivenessChecker interface {
	IsHealthy(ctx context.Context, workerID string) (bool, error)
}

type cacheEntry struct {
	record    *registry.Record
	expiresAt time.Time
}

// Router is the Origin Router's HTTP handler. Safe for concurrent use;
// holds no per-session routing decision of its own beyond an ownership
// lookup cache — restarting a Router replica loses nothing but that cache.
type Router struct {
	resolver OwnershipResolver
	liveness LivenessChecker
	cacheTTL time.Duration
	proxy    *httputil.ReverseProxy

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type ctxKey int

const targetCtxKey ctxKey = iota

// New builds a Router. cacheTTL bounds how long a resolved sessionID->owner
// lookup is trusted before being re-read from the registry (D1); pass a
// fleetclient.NoopChecker{} for liveness when Fleet Controller integration
// isn't configured.
func New(resolver OwnershipResolver, liveness LivenessChecker, cacheTTL time.Duration) *Router {
	if cacheTTL <= 0 {
		cacheTTL = 2 * time.Second
	}
	r := &Router{
		resolver: resolver,
		liveness: liveness,
		cacheTTL: cacheTTL,
		cache:    make(map[string]cacheEntry),
	}
	r.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := pr.In.Context().Value(targetCtxKey).(*url.URL)
			pr.SetURL(target)
			pr.Out.Host = target.Host
			// Deliberately not touching Path/RawQuery/Header beyond what
			// SetURL requires — _HLS_msn, _HLS_part, Range, conditional and
			// auth headers, and all query parameters must reach the origin
			// exactly as the viewer sent them (Epic D2).
		},
		Transport: &http.Transport{
			// No ResponseHeaderTimeout: LL-HLS's blocking playlist reload
			// can legitimately hold a response open for up to
			// 3*SegmentDuration seconds (internal/httpserver/server.go's
			// servePlaylist/servePart) — the origin's own timeout must be
			// authoritative, not a proxy-imposed one.
			ResponseHeaderTimeout: 0,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   100,
		},
		FlushInterval: -1, // stream immediately, no buffering
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			sessionID := sessionIDFromPath(req.URL.Path)
			zlog.Warn().Err(err).Msgf("[%s] origin router: proxy error", sessionID)
			// Epic D3: a dead/unreachable owner means the session is
			// unreachable, not a reason to guess at another worker. Evict
			// the cache entry so the *next* request re-resolves fresh
			// (picking up a newer registration if one exists), but this
			// request gets a clean error rather than a silently-wrong
			// reroute.
			r.evict(sessionID)
			http.Error(w, "origin unreachable", http.StatusBadGateway)
		},
	}
	return r
}

// ServeHTTP resolves ownership, cross-checks liveness, and proxies. Returns
// 404 for an unknown/orphaned session and never attempts a different
// worker for a session it can't reach (Epic D3).
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	sessionID := sessionIDFromPath(req.URL.Path)
	if sessionID == "" {
		http.NotFound(w, req)
		return
	}

	rec, err := r.resolve(req.Context(), sessionID)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			// Deliberately WARN, not silent: an unknown sessionID is
			// expected for a genuinely bogus/expired one, but it's also
			// exactly what a session whose CreateSession-time registry
			// Register() call failed looks like (e.g. a transient
			// twemproxy blip) -- that case is a live-viewer-facing bug,
			// not routine behavior, and was previously invisible in these
			// logs. Not cached (see resolve()), so this fires on every
			// request for the affected session, not just the first.
			zlog.Warn().Msgf("[%s] origin router: session not found in registry", sessionID)
			http.Error(w, "session "+sessionID+" not found", http.StatusNotFound)
			return
		}
		zlog.Error().Err(err).Msgf("[%s] origin router: registry lookup failed", sessionID)
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}

	// Epic D4: confirm the owning instance is still known-healthy before
	// proxying into it, instead of hanging on a dead address. Checked fresh
	// every request (not cached) since liveness can change faster than the
	// ownership cache TTL.
	healthy, err := r.liveness.IsHealthy(req.Context(), rec.WorkerID)
	if err != nil {
		zlog.Warn().Err(err).Msgf("[%s] origin router: liveness check failed, proxying anyway", sessionID)
	} else if !healthy {
		r.evict(sessionID)
		http.Error(w, "session "+sessionID+"'s owning instance is no longer available", http.StatusNotFound)
		return
	}

	target, err := url.Parse("http://" + rec.Origin)
	if err != nil {
		zlog.Error().Err(err).Msgf("[%s] origin router: invalid origin %q", sessionID, rec.Origin)
		http.Error(w, "invalid origin", http.StatusInternalServerError)
		return
	}

	ctx := context.WithValue(req.Context(), targetCtxKey, target)
	r.proxy.ServeHTTP(w, req.WithContext(ctx))
}

func (r *Router) resolve(ctx context.Context, sessionID string) (*registry.Record, error) {
	r.mu.Lock()
	if e, ok := r.cache[sessionID]; ok && time.Now().Before(e.expiresAt) {
		r.mu.Unlock()
		return e.record, nil
	}
	r.mu.Unlock()

	rec, err := r.resolver.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[sessionID] = cacheEntry{record: rec, expiresAt: time.Now().Add(r.cacheTTL)}
	r.mu.Unlock()
	return rec, nil
}

func (r *Router) evict(sessionID string) {
	r.mu.Lock()
	delete(r.cache, sessionID)
	r.mu.Unlock()
}

// sessionIDFromPath mirrors internal/httpserver.Server.sessionRouter's
// parsing exactly, so the Origin Router and the origin instance it proxies
// to never disagree about path structure.
func sessionIDFromPath(path string) string {
	trimmed := strings.TrimPrefix(path, pathPrefix)
	if trimmed == path {
		return "" // didn't have the expected prefix
	}
	slashIdx := strings.IndexByte(trimmed, '/')
	if slashIdx < 0 {
		return ""
	}
	return trimmed[:slashIdx]
}
