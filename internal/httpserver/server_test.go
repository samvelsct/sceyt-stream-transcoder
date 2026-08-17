package httpserver

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vt-stream-transcoder/internal/config"
	"vt-stream-transcoder/internal/hls/store"
)

// After a session is destroyed, a playlist request arriving shortly after
// should still resolve (not 404) and contain #EXT-X-ENDLIST, so players
// polling right around session teardown see a clean end-of-stream instead
// of an error.
func TestPlaylistAfterUnregisterServesEndList(t *testing.T) {
	cfg := &config.HLSConfig{
		SegmentDuration: 1,
		PartDuration:    0.2,
		PlaylistWindow:  5,
	}
	s := New("127.0.0.1:0", cfg)

	const sessionID = "sess-1"
	st := store.NewStore(cfg)
	st.SetInit([]byte("fake-init"))
	st.AddFragment(make([]byte, 10), 1.2, true) // closes a full segment

	s.mu.Lock()
	s.stores[sessionID] = st
	s.mu.Unlock()

	s.UnregisterSession(sessionID)

	req := httptest.NewRequest("GET", "/live/streams/"+sessionID+"/playlist.m3u8", nil)
	rec := httptest.NewRecorder()
	s.sessionRouter(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200 after unregister within grace period, got %d: %s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "#EXT-X-ENDLIST") {
		t.Fatalf("expected playlist to contain #EXT-X-ENDLIST, got:\n%s", body)
	}
}

// Once the grace period elapses, the store is evicted and requests 404 as before.
func TestPlaylistAfterGracePeriodExpires(t *testing.T) {
	origGrace := unregisterGracePeriod
	unregisterGracePeriod = 1 * time.Millisecond
	defer func() { unregisterGracePeriod = origGrace }()

	cfg := &config.HLSConfig{
		SegmentDuration: 1,
		PartDuration:    0.2,
		PlaylistWindow:  5,
	}
	s := New("127.0.0.1:0", cfg)

	const sessionID = "sess-2"
	st := store.NewStore(cfg)
	st.SetInit([]byte("fake-init"))

	s.mu.Lock()
	s.stores[sessionID] = st
	s.mu.Unlock()

	s.UnregisterSession(sessionID)

	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.RLock()
		_, ok := s.stores[sessionID]
		s.mu.RUnlock()
		if !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("store was not evicted after grace period")
		}
		time.Sleep(5 * time.Millisecond)
	}

	req := httptest.NewRequest("GET", "/live/streams/"+sessionID+"/playlist.m3u8", nil)
	rec := httptest.NewRecorder()
	s.sessionRouter(rec, req)

	if rec.Code != 404 {
		t.Fatalf("expected 404 after grace period expired, got %d", rec.Code)
	}
}
