package httpserver

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vt-stream-transcoder/internal/config"
	"vt-stream-transcoder/internal/hls/store"
)

type fakeSessionCounter struct {
	active int
	max    uint32
}

func (f *fakeSessionCounter) ActiveSessionCount() int      { return f.active }
func (f *fakeSessionCounter) MaxConcurrentStreams() uint32 { return f.max }

func TestMetricsHandler_WithoutCounter_OnlyReportsUp(t *testing.T) {
	s := New("127.0.0.1:0", &config.HLSConfig{SegmentDuration: 1, PartDuration: 0.2, PlaylistWindow: 5})

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	s.metricsHandler(rec, req)

	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "streambridge_up 1") {
		t.Fatalf("expected streambridge_up in output, got:\n%s", body)
	}
	if strings.Contains(string(body), "streambridge_active_sessions") {
		t.Fatalf("did not expect session metrics without a counter set, got:\n%s", body)
	}
}

func TestMetricsHandler_WithCounter_ReportsSessionLoad(t *testing.T) {
	s := New("127.0.0.1:0", &config.HLSConfig{SegmentDuration: 1, PartDuration: 0.2, PlaylistWindow: 5})
	s.SetSessionCounter(&fakeSessionCounter{active: 3, max: 100})

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	s.metricsHandler(rec, req)

	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "streambridge_active_sessions 3") {
		t.Fatalf("expected active_sessions=3, got:\n%s", body)
	}
	if !strings.Contains(string(body), "streambridge_max_concurrent_streams 100") {
		t.Fatalf("expected max_concurrent_streams=100, got:\n%s", body)
	}
}

// fakeDeleter mirrors the real internal/registry.Registry.Delete's
// generation-fencing behavior (reject a stale generation rather than
// deleting) so these tests exercise the same collaboration contract the
// real registry enforces, not just "was Delete called."
type fakeDeleter struct {
	mu       sync.Mutex
	current  map[string]int64 // sessionID -> generation "in the registry"
	deleted  []string         // sessionIDs successfully deleted (current generation matched)
	rejected []string         // sessionIDs rejected as stale
}

var errStaleGeneration = errStale{}

type errStale struct{}

func (errStale) Error() string { return "stale generation" }

func (f *fakeDeleter) register(sessionID string, generation int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current == nil {
		f.current = make(map[string]int64)
	}
	f.current[sessionID] = generation
}

func (f *fakeDeleter) Delete(_ context.Context, sessionID string, generation int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current[sessionID] != generation {
		f.rejected = append(f.rejected, sessionID)
		return errStaleGeneration
	}
	delete(f.current, sessionID)
	f.deleted = append(f.deleted, sessionID)
	return nil
}

func (f *fakeDeleter) wasDeleted(sessionID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.deleted {
		if id == sessionID {
			return true
		}
	}
	return false
}

func (f *fakeDeleter) wasRejected(sessionID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.rejected {
		if id == sessionID {
			return true
		}
	}
	return false
}

// After the grace period, UnregisterSession must clear the Ownership
// Registry record for the same generation the session was registered
// under (Epic C2) — the registry-side complement to the existing
// in-memory-store eviction the tests above already cover.
func TestUnregisterSession_ClearsRegistryAfterGracePeriod(t *testing.T) {
	origGrace := unregisterGracePeriod
	unregisterGracePeriod = 1 * time.Millisecond
	defer func() { unregisterGracePeriod = origGrace }()

	s := New("127.0.0.1:0", &config.HLSConfig{SegmentDuration: 1, PartDuration: 0.2, PlaylistWindow: 5})
	deleter := &fakeDeleter{}
	deleter.register("sess-reg-1", 7)
	s.SetRegistryDeleter(deleter)

	const sessionID = "sess-reg-1"
	st := store.NewStore(&config.HLSConfig{SegmentDuration: 1, PartDuration: 0.2, PlaylistWindow: 5})

	s.mu.Lock()
	s.stores[sessionID] = st
	s.mu.Unlock()
	s.SetGeneration(sessionID, 7)

	s.UnregisterSession(sessionID)

	deadline := time.Now().Add(2 * time.Second)
	for !deleter.wasDeleted(sessionID) {
		if time.Now().After(deadline) {
			t.Fatalf("registry record for %s was not cleared within deadline; deleted=%v rejected=%v",
				sessionID, deleter.deleted, deleter.rejected)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A re-registration under the same sessionID before the old grace-period
// timer fires must not clear the *new* registration's generation — mirrors
// the existing s.stores[sessionID] == st re-registration guard.
func TestUnregisterSession_DoesNotClearNewerGenerationOnReRegistration(t *testing.T) {
	origGrace := unregisterGracePeriod
	unregisterGracePeriod = 20 * time.Millisecond
	defer func() { unregisterGracePeriod = origGrace }()

	s := New("127.0.0.1:0", &config.HLSConfig{SegmentDuration: 1, PartDuration: 0.2, PlaylistWindow: 5})
	deleter := &fakeDeleter{}
	s.SetRegistryDeleter(deleter)

	const sessionID = "sess-reg-2"
	cfg := &config.HLSConfig{SegmentDuration: 1, PartDuration: 0.2, PlaylistWindow: 5}

	oldStore := store.NewStore(cfg)
	deleter.register(sessionID, 1)
	s.mu.Lock()
	s.stores[sessionID] = oldStore
	s.mu.Unlock()
	s.SetGeneration(sessionID, 1)

	s.UnregisterSession(sessionID) // starts the grace-period timer for generation 1

	// A brand-new session gets created (and registered) under the same ID
	// before the old timer fires — the registry's "current" generation
	// moves to 2, exactly as the real registry.Register would do.
	newStore := store.NewStore(cfg)
	deleter.register(sessionID, 2)
	s.mu.Lock()
	s.stores[sessionID] = newStore
	s.mu.Unlock()
	s.SetGeneration(sessionID, 2)

	time.Sleep(200 * time.Millisecond) // let the stale timer fire

	if deleter.wasDeleted(sessionID) {
		t.Fatalf("the stale grace-period timer must not have successfully deleted the newer record; deleted=%v", deleter.deleted)
	}
	if !deleter.wasRejected(sessionID) {
		t.Fatalf("expected the stale timer's Delete call to be rejected as stale; rejected=%v", deleter.rejected)
	}

	s.mu.RLock()
	gen := s.generations[sessionID]
	s.mu.RUnlock()
	if gen != 2 {
		t.Fatalf("expected the current generation to still be 2, got %d", gen)
	}
}
