package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	zlog "github.com/rs/zerolog/log"
	"github.com/samvelsct/go-webrtchls/webrtchls"
)

// segmentStore holds in-memory HLS segments for one session, keyed by filename.
// The library emits files like:
//
//	init.mp4        – initialisation segment (static, cache long)
//	stream.m3u8     – current playlist (regenerated each segment)
//	part_00001.m4s  – partial segments (LL-HLS parts)
//	segNNNNN.m4s    – full segments (regular HLS)
type segmentStore struct {
	mu      sync.RWMutex
	files   map[string][]byte // filename → raw bytes
	updated *sync.Cond        // broadcasts on every new file
}

func newSegmentStore() *segmentStore {
	s := &segmentStore{files: make(map[string][]byte)}
	s.updated = sync.NewCond(&s.mu)
	return s
}

func (ss *segmentStore) put(name string, data []byte) {
	name = strings.TrimSuffix(name, ".tmp")
	ss.mu.Lock()
	cp := make([]byte, len(data))
	copy(cp, data)
	ss.files[name] = cp
	ss.updated.Broadcast()
	ss.mu.Unlock()
}

func (ss *segmentStore) get(name string) ([]byte, bool) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	d, ok := ss.files[name]
	return d, ok
}

// waitForFile blocks until the named file exists in the store or ctx expires.
func (ss *segmentStore) waitForFile(ctx context.Context, name string) ([]byte, bool) {
	// Fast path.
	if d, ok := ss.get(name); ok {
		return d, true
	}

	// Slow path: register a context-cancel watcher that will Broadcast so the
	// waiting goroutine does not hang forever.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			ss.mu.Lock()
			ss.updated.Broadcast()
			ss.mu.Unlock()
		case <-done:
		}
	}()
	defer close(done)

	ss.mu.Lock()
	defer ss.mu.Unlock()
	for {
		if d, ok := ss.files[name]; ok {
			return d, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
		ss.updated.Wait()
	}
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Server is an HTTP server that serves LL-HLS content produced by go-webrtchls.
// Each session's segments are stored in memory and served under
// /streams/{sessionID}/{filename}.
//
// Route table:
//
//	GET /                                   – simple player page
//	GET /healthcheck                        – 200 OK
//	GET /streams/{sessionID}/init.mp4       – init segment
//	GET /streams/{sessionID}/stream.m3u8    – playlist (blocking supported)
//	GET /streams/{sessionID}/*.m4s          – part or segment
type Server struct {
	mu       sync.RWMutex
	stores   map[string]*segmentStore // sessionID → store
	mux      *http.ServeMux
	httpSrv  *http.Server
	listenOn string
}

// New creates a new HTTP server that will listen on addr (e.g. ":8090").
func New(addr string) *Server {
	s := &Server{
		stores:   make(map[string]*segmentStore),
		listenOn: addr,
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/streams/", s.sessionRouter)
	s.mux.HandleFunc("/healthcheck", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.mux.HandleFunc("/", s.indexHandler)

	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}
	return s
}

// Start begins serving HTTP in a background goroutine.
func (s *Server) Start() {
	go func() {
		zlog.Info().Str("addr", s.listenOn).Msg("HLS HTTP server listening")
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.Error().Err(err).Msg("HLS HTTP server error")
		}
	}()
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) {
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		zlog.Error().Err(err).Msg("HLS HTTP server shutdown error")
	}
}

// RegisterSession registers a webrtchls.Session so that the segment callback
// is installed and segments are served under /streams/{sessionID}/...
// Call this after CreateSession succeeds.
func (s *Server) RegisterSession(sessionID string, session *webrtchls.Session) error {
	store := newSegmentStore()

	s.mu.Lock()
	s.stores[sessionID] = store
	s.mu.Unlock()

	err := session.SetSegmentCallback(func(name string, data []byte) {
		zlog.Debug().Str("session", sessionID).Str("file", name).Int("bytes", len(data)).Msg("segment received")
		store.put(name, data)
	})
	if err != nil {
		s.mu.Lock()
		delete(s.stores, sessionID)
		s.mu.Unlock()
		return fmt.Errorf("SetSegmentCallback: %w", err)
	}

	zlog.Info().Str("session", sessionID).Msg("HLS session registered")
	return nil
}

// UnregisterSession removes a session's store.
// Call this after DestroySession.
func (s *Server) UnregisterSession(sessionID string) {
	s.mu.Lock()
	delete(s.stores, sessionID)
	s.mu.Unlock()
	zlog.Info().Str("session", sessionID).Msg("HLS session unregistered")
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func (s *Server) sessionRouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/streams/")
	slashIdx := strings.IndexByte(path, '/')
	if slashIdx < 0 {
		http.NotFound(w, r)
		return
	}
	sessionID := path[:slashIdx]
	resource := path[slashIdx+1:]

	s.mu.RLock()
	store, ok := s.stores[sessionID]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("session %q not found", sessionID), http.StatusNotFound)
		return
	}

	setCORSHeaders(w)

	switch {
	case resource == "init.mp4":
		s.serveStatic(w, r, store, "init.mp4", "video/mp4", "max-age=3600")

	case strings.HasSuffix(resource, ".m3u8"):
		s.servePlaylist(w, r, store, resource)

	case strings.HasSuffix(resource, ".m4s"):
		s.serveMedia(w, r, store, resource)

	default:
		http.NotFound(w, r)
	}
}

// serveStatic serves a file that is expected to be available soon (waits up to
// 10 s before returning 503).
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, store *segmentStore, name, contentType, cacheControl string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data, ok := store.waitForFile(ctx, name)
	if !ok {
		http.Error(w, name+" not yet available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	if _, err := w.Write(data); err != nil {
		zlog.Debug().Err(err).Str("file", name).Msg("write error")
	}
}

// servePlaylist serves the M3U8 playlist.
// Supports the standard HLS blocking-reload query parameters:
//
//	_HLS_msn  – ignored for file-based output (library manages sequence numbers)
//	_HLS_part – ignored for file-based output
//
// When a blocking parameter is present the handler waits for the playlist to be
// (re-)written before responding, which is the correct LL-HLS behaviour.
func (s *Server) servePlaylist(w http.ResponseWriter, r *http.Request, store *segmentStore, name string) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache, no-store")

	isBlocking := r.URL.Query().Get("_HLS_msn") != ""

	if isBlocking {
		// For a blocking request we need a *fresh* playlist.  We delete the
		// cached copy so that waitForFile will block until the library emits the
		// next version.
		store.mu.Lock()
		delete(store.files, name)
		store.mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	data, ok := store.waitForFile(ctx, name)
	if !ok {
		if ctx.Err() == context.DeadlineExceeded {
			http.Error(w, "timeout waiting for playlist", http.StatusServiceUnavailable)
		}
		return
	}
	if _, err := w.Write(data); err != nil {
		zlog.Debug().Err(err).Str("file", name).Msg("playlist write error")
	}
}

// serveMedia serves .m4s parts and segments.
// Waits up to 15 s (3× a typical 5 s segment) before returning 503.
func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request, store *segmentStore, name string) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	data, ok := store.waitForFile(ctx, name)
	if !ok {
		if ctx.Err() == context.DeadlineExceeded {
			http.Error(w, "timeout waiting for "+name, http.StatusServiceUnavailable)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "max-age=60")
	if _, err := w.Write(data); err != nil {
		zlog.Debug().Err(err).Str("file", name).Msg("media write error")
	}
}

// ---------------------------------------------------------------------------
// Index / player page
// ---------------------------------------------------------------------------

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>VT Stream Transcoder – LL-HLS Player</title>
  <script src="https://cdn.jsdelivr.net/npm/hls.js@latest"></script>
</head>
<body>
  <h1>VT Stream Transcoder &rarr; LL-HLS</h1>
  <p>Streams are served at <code>/streams/{sessionID}/stream.m3u8</code></p>
  <label>Session ID: <input id="sid" value=""></label>
  <button onclick="load()">Load Stream</button>
  <br><br>
  <video id="video" controls autoplay muted style="width:100%;max-width:400px"></video>
  <script>
    function load() {
      const sid = document.getElementById('sid').value.trim();
      if (!sid) return;
      const src = '/streams/' + sid + '/stream.m3u8';
      const video = document.getElementById('video');
      if (Hls.isSupported()) {
        const hls = new Hls({ lowLatencyMode: true });
        hls.loadSource(src);
        hls.attachMedia(video);
      } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
        video.src = src;
      }
    }
  </script>
</body>
</html>
`

func (s *Server) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}
