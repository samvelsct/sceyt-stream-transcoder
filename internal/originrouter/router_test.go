package originrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vt-stream-transcoder/internal/registry"
)

type fakeResolver struct {
	records map[string]*registry.Record
	calls   atomic.Int64
}

func (f *fakeResolver) Get(_ context.Context, sessionID string) (*registry.Record, error) {
	f.calls.Add(1)
	if rec, ok := f.records[sessionID]; ok {
		return rec, nil
	}
	return nil, registry.ErrNotFound
}

type fakeLiveness struct {
	unhealthy map[string]bool
}

func (f *fakeLiveness) IsHealthy(_ context.Context, workerID string) (bool, error) {
	return !f.unhealthy[workerID], nil
}

func newOriginServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestServeHTTP_UnknownSessionReturns404(t *testing.T) {
	router := New(&fakeResolver{records: map[string]*registry.Record{}}, &fakeLiveness{}, time.Second)

	req := httptest.NewRequest("GET", pathPrefix+"nope/stream.m3u8", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestServeHTTP_RoutesToTheOwningOrigin(t *testing.T) {
	var gotPath, gotQuery, gotRange string
	origin := newOriginServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n"))
	})

	resolver := &fakeResolver{records: map[string]*registry.Record{
		"sess-1": {SessionID: "sess-1", WorkerID: "pod-a", Origin: origin, Generation: 1, Status: registry.StatusActive},
	}}
	router := New(resolver, &fakeLiveness{}, time.Second)

	req := httptest.NewRequest("GET", pathPrefix+"sess-1/stream.m3u8?_HLS_msn=3&_HLS_part=1", nil)
	req.Header.Set("Range", "bytes=0-100")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != pathPrefix+"sess-1/stream.m3u8" {
		t.Fatalf("unexpected path forwarded: %s", gotPath)
	}
	// Epic D2: query params (_HLS_msn/_HLS_part) and headers (Range) must
	// reach the origin exactly as the viewer sent them.
	if gotQuery != "_HLS_msn=3&_HLS_part=1" {
		t.Fatalf("expected query params preserved verbatim, got %q", gotQuery)
	}
	if gotRange != "bytes=0-100" {
		t.Fatalf("expected Range header preserved, got %q", gotRange)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "#EXTM3U") {
		t.Fatalf("unexpected body: %s", body)
	}
}

// The single most important regression test for Epic D2: the origin can
// legitimately hold the response open for several seconds (LL-HLS blocking
// playlist reload) and the router must not time it out early.
func TestServeHTTP_DoesNotTimeoutDuringSlowOriginResponse(t *testing.T) {
	origin := newOriginServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte("delayed-response"))
	})

	resolver := &fakeResolver{records: map[string]*registry.Record{
		"sess-slow": {SessionID: "sess-slow", WorkerID: "pod-a", Origin: origin, Generation: 1, Status: registry.StatusActive},
	}}
	router := New(resolver, &fakeLiveness{}, time.Second)

	req := httptest.NewRequest("GET", pathPrefix+"sess-slow/stream.m3u8?_HLS_msn=5", nil)
	rec := httptest.NewRecorder()

	start := time.Now()
	router.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 despite the delay, got %d: %s", rec.Code, rec.Body.String())
	}
	if elapsed < 2*time.Second {
		t.Fatalf("response returned suspiciously fast (%v) for a 2s-delayed origin — did something time out early?", elapsed)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != "delayed-response" {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestServeHTTP_DeadOwningInstanceReturns404_NeverReroutesToAnotherWorker(t *testing.T) {
	var altHit bool
	altOrigin := newOriginServer(t, func(w http.ResponseWriter, r *http.Request) {
		altHit = true
		w.WriteHeader(http.StatusOK)
	})
	_ = altOrigin // exists only to prove the router never talks to it (Epic D3)

	resolver := &fakeResolver{records: map[string]*registry.Record{
		"sess-dead": {SessionID: "sess-dead", WorkerID: "pod-dead", Origin: "10.0.0.99:9999", Generation: 1, Status: registry.StatusActive},
	}}
	router := New(resolver, &fakeLiveness{unhealthy: map[string]bool{"pod-dead": true}}, time.Second)

	req := httptest.NewRequest("GET", pathPrefix+"sess-dead/stream.m3u8", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a dead owning instance, got %d", rec.Code)
	}
	if altHit {
		t.Fatalf("router must never fall back to a different worker (Epic D3)")
	}
}

func TestServeHTTP_UnreachableOriginReturns502_EvictsCache(t *testing.T) {
	resolver := &fakeResolver{records: map[string]*registry.Record{
		// Nothing listens on this port — connection should fail immediately.
		"sess-unreachable": {SessionID: "sess-unreachable", WorkerID: "pod-a", Origin: "127.0.0.1:1", Generation: 1, Status: registry.StatusActive},
	}}
	router := New(resolver, &fakeLiveness{}, time.Minute)

	req := httptest.NewRequest("GET", pathPrefix+"sess-unreachable/stream.m3u8", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for an unreachable origin, got %d", rec.Code)
	}

	router.mu.Lock()
	_, cached := router.cache["sess-unreachable"]
	router.mu.Unlock()
	if cached {
		t.Fatalf("expected the cache entry to be evicted after a proxy failure so the next request re-resolves")
	}
}

func TestServeHTTP_UsesCacheWithinTTL(t *testing.T) {
	origin := newOriginServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	resolver := &fakeResolver{records: map[string]*registry.Record{
		"sess-cache": {SessionID: "sess-cache", WorkerID: "pod-a", Origin: origin, Generation: 1, Status: registry.StatusActive},
	}}
	router := New(resolver, &fakeLiveness{}, time.Minute)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", pathPrefix+"sess-cache/stream.m3u8", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}

	if calls := resolver.calls.Load(); calls != 1 {
		t.Fatalf("expected exactly 1 registry lookup within the cache TTL, got %d", calls)
	}
}
