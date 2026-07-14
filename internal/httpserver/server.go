package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"vt-stream-transcoder/internal/hls/playlist"
	"vt-stream-transcoder/internal/hls/segmenter"
	"vt-stream-transcoder/internal/hls/store"
	"vt-stream-transcoder/internal/webrtchls"

	"vt-stream-transcoder/internal/config"

	zlog "github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// File classification helpers
// ---------------------------------------------------------------------------

// fileKind classifies a filename emitted by the library.
type fileKind int

const (
	kindUnknown  fileKind = iota
	kindInit              // init.mp4
	kindPlaylist          // *.m3u8  (library-generated; we discard and regenerate)
	kindPart              // part_NNNNN.m4s
	kindSegment           // seg_NNNNN.m4s  (complete segment, if library emits one)
)

func classifyFile(name string) (kind fileKind, seq int) {
	switch {
	case name == "init.mp4":
		return kindInit, 0
	case strings.HasSuffix(name, ".m3u8"):
		return kindPlaylist, 0
	case strings.HasPrefix(name, "part_") && strings.HasSuffix(name, ".m4s"):
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "part_"), ".m4s"))
		if err != nil {
			return kindUnknown, 0
		}
		return kindPart, n
	case strings.HasSuffix(name, ".m4s"):
		// generic .m4s — treat as part
		return kindPart, 0
	}
	return kindUnknown, 0
}

// ---------------------------------------------------------------------------
// Playlist state machine
// ---------------------------------------------------------------------------

// part holds a single LL-HLS partial segment.
type part struct {
	name     string  // e.g. "part_00001.m4s"
	duration float64 // seconds (estimated from config)
	data     []byte
}

// segment holds a complete HLS segment composed of parts.
type segment struct {
	msn      int     // Media Sequence Number
	parts    []part  // ordered parts
	duration float64 // sum of part durations
	data     []byte  // concatenated bytes (set when segment is complete)
	complete bool
}

// sessionState is the per-session in-memory store and playlist generator.
type sessionState struct {
	cfg *config.HLSConfig

	mu      sync.RWMutex
	updated *sync.Cond

	// raw file store (init + all media files)
	files map[string][]byte

	// playlist state
	nextPartSeq    int       // global part sequence counter
	segments       []segment // window of segments (completed + current in-progress)
	currentSeg     *segment  // pointer into segments slice (last element while in-progress)
	mediaSeqBase   int       // MSN of segments[0]
	partHoldBack   float64   // = 3 × partDuration  (per spec)
	cachedPlaylist []byte    // last generated playlist bytes
}

func newSessionState(cfg *config.HLSConfig) *sessionState {
	partDur := cfg.PartDuration
	if partDur <= 0 {
		partDur = 0.5
	}
	ss := &sessionState{
		cfg:          cfg,
		files:        make(map[string][]byte),
		partHoldBack: 3 * partDur,
	}
	ss.updated = sync.NewCond(&ss.mu)
	return ss
}

// ingest is called for every file the library emits (after .tmp is stripped).
func (ss *sessionState) ingest(name string, data []byte) {
	kind, seq := classifyFile(name)

	ss.mu.Lock()
	defer ss.mu.Unlock()

	switch kind {
	case kindInit:
		cp := make([]byte, len(data))
		copy(cp, data)
		ss.files[name] = cp

	case kindPlaylist:
		// Discard the library-generated playlist; we build our own.

	case kindPart:
		cp := make([]byte, len(data))
		copy(cp, data)
		ss.files[name] = cp

		p := part{
			name:     name,
			duration: ss.cfg.PartDuration,
			data:     cp,
		}
		if seq == 0 {
			// Fallback: use internal counter when sequence not parseable.
			seq = ss.nextPartSeq
		}
		ss.nextPartSeq = seq + 1

		partsPerSeg := ss.partsPerSegment()

		// Start a new segment if needed.
		if ss.currentSeg == nil || len(ss.currentSeg.parts) >= partsPerSeg {
			ss.sealCurrentSegment()
			msn := ss.mediaSeqBase + len(ss.segments)
			ss.segments = append(ss.segments, segment{msn: msn})
			ss.currentSeg = &ss.segments[len(ss.segments)-1]
		}

		ss.currentSeg.parts = append(ss.currentSeg.parts, p)
		ss.currentSeg.duration += p.duration

		// Seal the segment once we have enough parts.
		if len(ss.currentSeg.parts) >= partsPerSeg {
			ss.sealCurrentSegment()
		}

		ss.trimWindow()
		ss.cachedPlaylist = ss.buildPlaylist()

	default:
		// Store anything else verbatim.
		cp := make([]byte, len(data))
		copy(cp, data)
		ss.files[name] = cp
	}

	ss.updated.Broadcast()
}

// sealCurrentSegment marks the in-progress segment as complete and
// concatenates its parts into a single []byte for direct serving.
func (ss *sessionState) sealCurrentSegment() {
	if ss.currentSeg == nil || ss.currentSeg.complete {
		return
	}
	var total int
	for _, p := range ss.currentSeg.parts {
		total += len(p.data)
	}
	buf := make([]byte, 0, total)
	for _, p := range ss.currentSeg.parts {
		buf = append(buf, p.data...)
	}
	ss.currentSeg.data = buf
	ss.currentSeg.complete = true
	ss.currentSeg = nil
}

// trimWindow keeps the sliding window at PlaylistLength completed segments
// (the in-progress segment is always kept).
func (ss *sessionState) trimWindow() {
	window := ss.cfg.PlaylistWindow
	if window < 1 {
		window = 3
	}

	// Count completed segments.
	completed := 0
	for i := range ss.segments {
		if ss.segments[i].complete {
			completed++
		}
	}

	excess := completed - window
	if excess <= 0 {
		return
	}

	// Drop oldest completed segments.
	dropped := 0
	i := 0
	for i < len(ss.segments) && dropped < excess {
		if ss.segments[i].complete {
			dropped++
			i++
		} else {
			break
		}
	}
	ss.segments = ss.segments[i:]
	ss.mediaSeqBase += i
}

// partsPerSegment returns how many parts make up one segment.
func (ss *sessionState) partsPerSegment() int {
	segDur := float64(ss.cfg.SegmentDuration)
	partDur := ss.cfg.PartDuration
	if partDur <= 0 {
		partDur = 0.5
	}
	n := int(segDur / partDur)
	if n < 1 {
		n = 1
	}
	return n
}

// ---------------------------------------------------------------------------
// Playlist generation
// ---------------------------------------------------------------------------

// buildPlaylist generates a complete LL-HLS Media Playlist (EXT-X-VERSION:9).
// Must be called with ss.mu held.
func (ss *sessionState) buildPlaylist() []byte {
	segDur := float64(ss.cfg.SegmentDuration)
	partDur := ss.cfg.PartDuration
	if partDur <= 0 {
		partDur = 0.5
	}
	holdBack := ss.partHoldBack

	var b strings.Builder

	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:9\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(segDur+0.5))
	fmt.Fprintf(&b, "#EXT-X-PART-INF:PART-TARGET=%.3f\n", partDur)
	fmt.Fprintf(&b, "#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,PART-HOLD-BACK=%.3f\n", holdBack)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", ss.mediaSeqBase)
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")

	for i, seg := range ss.segments {
		_ = i

		if seg.complete {
			// Emit EXT-X-PART lines for every part in the completed segment.
			for _, p := range seg.parts {
				fmt.Fprintf(&b, "#EXT-X-PART:DURATION=%.3f,URI=%q\n", p.duration, p.name)
			}
			// Emit the full segment tag.
			segName := fmt.Sprintf("seg_%05d.m4s", seg.msn)
			fmt.Fprintf(&b, "#EXTINF:%.3f,\n", seg.duration)
			b.WriteString(segName + "\n")
		} else {
			// In-progress segment: emit EXT-X-PART lines for arrived parts only.
			for _, p := range seg.parts {
				fmt.Fprintf(&b, "#EXT-X-PART:DURATION=%.3f,URI=%q\n", p.duration, p.name)
			}
			// Preload hint for the next part that hasn't arrived yet.
			nextPartName := fmt.Sprintf("part_%05d.m4s", ss.nextPartSeq)
			fmt.Fprintf(&b, "#EXT-X-PRELOAD-HINT:TYPE=PART,URI=%q\n", nextPartName)
		}
	}

	return []byte(b.String())
}

// getPlaylist returns the cached playlist, blocking until one is ready.
func (ss *sessionState) getPlaylist(ctx context.Context) ([]byte, bool) {
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
		if ss.cachedPlaylist != nil {
			cp := make([]byte, len(ss.cachedPlaylist))
			copy(cp, ss.cachedPlaylist)
			return cp, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
		ss.updated.Wait()
	}
}

// invalidatePlaylist drops the cached playlist so the next call to
// getPlaylist blocks until a new one is generated.
func (ss *sessionState) invalidatePlaylist() {
	ss.mu.Lock()
	ss.cachedPlaylist = nil
	ss.mu.Unlock()
}

// getFile returns a raw stored file (init.mp4, part .m4s, etc.),
// blocking until it is available or ctx expires.
func (ss *sessionState) getFile(ctx context.Context, name string) ([]byte, bool) {
	// For completed-segment virtual names (seg_NNNNN.m4s) we assemble on the fly.
	if strings.HasPrefix(name, "seg_") && strings.HasSuffix(name, ".m4s") {
		return ss.getSegmentFile(ctx, name)
	}

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
			cp := make([]byte, len(d))
			copy(cp, d)
			return cp, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
		ss.updated.Wait()
	}
}

// getSegmentFile returns the concatenated bytes for a completed segment by
// its virtual name "seg_NNNNN.m4s".
func (ss *sessionState) getSegmentFile(ctx context.Context, name string) ([]byte, bool) {
	numStr := strings.TrimSuffix(strings.TrimPrefix(name, "seg_"), ".m4s")
	msn, err := strconv.Atoi(numStr)
	if err != nil {
		return nil, false
	}

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
		for i := range ss.segments {
			if ss.segments[i].msn == msn && ss.segments[i].complete {
				cp := make([]byte, len(ss.segments[i].data))
				copy(cp, ss.segments[i].data)
				return cp, true
			}
		}
		if ctx.Err() != nil {
			return nil, false
		}
		ss.updated.Wait()
	}
}

// listFiles returns a sorted list of all stored file names (for debugging).
func (ss *sessionState) listFiles() []string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	names := make([]string, 0, len(ss.files))
	for k := range ss.files {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Server is an HTTP server that serves LL-HLS content produced by go-webrtchls.
// Segments are tracked in memory and a standards-compliant LL-HLS playlist is
// generated on every new part.
//
// Route table:
//
//	GET /                                    – player page
//	GET /healthcheck                         – 200 OK
//	GET /streams/{sessionID}/init.mp4        – init segment
//	GET /streams/{sessionID}/stream.m3u8     – LL-HLS playlist (blocking reload)
//	GET /streams/{sessionID}/seg_NNNNN.m4s   – full segment (virtual, assembled from parts)
//	GET /streams/{sessionID}/part_NNNNN.m4s  – partial segment
type Server struct {
	cfg *config.HLSConfig
	mu  sync.RWMutex
	//states   map[string]*sessionState
	stores   map[string]*store.Store
	mux      *http.ServeMux
	httpSrv  *http.Server
	listenOn string
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func timingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		duration := time.Since(start)
		zlog.Info().Msgf("| %s | %s | %s | %d | %v", r.Method, r.RemoteAddr, r.URL.Path, rec.status, duration)
	})
}

// New creates a new HTTP server that will listen on addr (e.g. ":8090").
func New(addr string, cfg *config.HLSConfig) *Server {
	s := &Server{
		cfg: cfg,
		//states:   make(map[string]*sessionState),
		stores:   make(map[string]*store.Store),
		listenOn: addr,
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/live/streams/", s.sessionRouter)
	s.mux.HandleFunc("/healthcheck", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.mux.HandleFunc("/live/", s.indexHandler)

	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: timingMiddleware(s.mux),
	}

	// ------------------------------------------------------------------
	// 5. TLS/HTTP2 server — only started when cert and key are provided.
	// -tls-cert server.crt -tls-key server.key
	// ------------------------------------------------------------------
	//tlsCert, err := tls.LoadX509KeyPair("localserver.crt", "localserver.key")
	//if err != nil {
	//	log.Fatalf("load TLS cert/key: %v", err)
	//}
	//
	//tlsCfg := &tls.Config{
	//	Certificates: []tls.Certificate{tlsCert},
	//	NextProtos:   []string{"h2", "http/1.1"},
	//	MinVersion:   tls.VersionTLS12,
	//}
	//
	//tlsServer := &http.Server{
	//	Addr:      ":8443",
	//	Handler:   timingMiddleware(s.mux),
	//	TLSConfig: tlsCfg,
	//}
	//if err := http2.ConfigureServer(tlsServer, &http2.Server{}); err != nil {
	//	log.Fatalf("configure HTTP/2 on TLS server: %v", err)
	//}
	//
	//go func() {
	//	log.Printf("HTTPS + HTTP/2 listening on %s", ":8443")
	//	if err := tlsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
	//		log.Fatalf("TLS serve: %v", err)
	//	}
	//}()

	return s
}

// Start begins serving HTTP in a background goroutine.
func (s *Server) Start() {
	go func() {
		zlog.Info().Msgf("HLS HTTP server listening: %s", s.listenOn)
		if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// RegisterSession installs the segment callback on session and begins
// accumulating parts into the in-memory playlist.
func (s *Server) RegisterSession(sessionID string, session *webrtchls.Session) error {
	//state := newSessionState(s.cfg)
	st := store.NewStore(s.cfg)
	if s.cfg.WriteOnDisk {
		outDir := s.cfg.OutputDir + "/" + sessionID
		playlistName := filepath.Base("output.m3u8")
		dw, err := store.NewDiskWriter(outDir, playlistName, func(segs []store.WrittenSeg, endList bool) string {
			return playlist.GenerateDiskPlaylist(segs, endList)
		})
		if err != nil {
			zlog.Error().Msgf("[%s] disk writer init failed: %v", sessionID, err)
		} else {
			st.SetDiskWriter(dw)
			zlog.Info().Msgf("[%s] writing LL-HLS to %s", sessionID, outDir)
		}
	}
	s.mu.Lock()
	//s.states[sessionID] = state
	s.stores[sessionID] = st
	s.mu.Unlock()

	err := session.SetSegmentCallback(func(name string, data []byte) {
		name = strings.TrimSuffix(name, ".tmp")
		zlog.Info().Msgf("[%s] %s | %d: segment callback", sessionID, name, len(data))
		if name == "init.mp4" {
			st.SetInit(data)
		} else if strings.HasSuffix(name, ".m3u8") {
			// discard library-generated playlist
		} else if strings.HasSuffix(name, ".m4s") {
			dur, isKey := segmenter.ParseMoofFromBytes(data, 0)
			st.AddFragment(data, dur, isKey)
		}
	})
	if err != nil {
		s.mu.Lock()
		//delete(s.states, sessionID)
		delete(s.stores, sessionID)
		s.mu.Unlock()
		return fmt.Errorf("SetSegmentCallback: %w", err)
	}

	zlog.Info().Msgf("[%s] HLS session registered: part_duration=%.3f, segment_duration=%.3f, playlist_window=%d", sessionID, s.cfg.PartDuration, s.cfg.SegmentDuration, s.cfg.PlaylistWindow)
	return nil
}

// UnregisterSession finalizes and removes a session's state.
// Finalize is called before the store is removed so that any clients blocked
// on a blocking playlist reload receive a final playlist with #EXT-X-ENDLIST.
func (s *Server) UnregisterSession(sessionID string) {
	s.mu.RLock()
	st := s.stores[sessionID]
	s.mu.RUnlock()

	if st != nil {
		st.Finalize()
	}

	s.mu.Lock()
	delete(s.stores, sessionID)
	s.mu.Unlock()
	zlog.Info().Msgf("[%s] HLS session unregistered", sessionID)
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func (s *Server) sessionRouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/live/streams/")
	slashIdx := strings.IndexByte(path, '/')
	if slashIdx < 0 {
		http.NotFound(w, r)
		return
	}
	sessionID := path[:slashIdx]
	resource := path[slashIdx+1:]

	s.mu.RLock()
	//state, ok := s.states[sessionID]
	st, ok := s.stores[sessionID]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("session %q not found", sessionID), http.StatusNotFound)
		return
	}

	setCORSHeaders(w)

	ext := strings.ToLower(filepath.Ext(resource))
	switch {
	case resource == "init.mp4":
		//s.serveFile(w, r, state, "init.mp4", "video/mp4", "max-age=3600", 10*time.Second)
		s.serveInit(w, st)

	case ext == ".m3u8":
		//s.servePlaylist(w, r, state)
		s.servePlaylist(w, r, st)

	case strings.HasPrefix(resource, "segment-"):
		s.serveSegment(w, r, st, resource)

	case strings.HasPrefix(resource, "part-"):
		s.servePart(w, r, st, resource)

	case strings.HasPrefix(resource, "disk/part-"):
		s.servePartFromDisk(w, r, st, strings.TrimPrefix(resource, "disk/"))

	//case ext == ".m4s":
	//	s.serveMedia(w, r, state, resource)

	default:
		http.NotFound(w, r)
	}
}

// serveFile waits for a named raw file and serves it.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, state *sessionState, name, contentType, cacheControl string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	data, ok := state.getFile(ctx, name)
	if !ok {
		http.Error(w, name+" not yet available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	if _, err := w.Write(data); err != nil {
		zlog.Debug().Msgf("%s file write error %v", name, err)
	}
}

func (s *Server) serveInit(w http.ResponseWriter, st *store.Store) {
	setCORSHeaders(w)
	init := st.GetInit()
	if init == nil {
		http.Error(w, "init segment not yet available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "max-age=3600")
	if _, err := w.Write(init.Data); err != nil {
		log.Printf("server: init write error: %v", err)
	}
}

// servePlaylist handles LL-HLS playlist requests including blocking reload
// (_HLS_msn / _HLS_part query parameters).
func (s *Server) servePlaylist(w http.ResponseWriter, r *http.Request, st *store.Store) {
	setCORSHeaders(w)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache, no-store")

	q := r.URL.Query()
	msnStr := q.Get("_HLS_msn")
	partStr := q.Get("_HLS_part")
	skipParam := q.Get("_HLS_skip")
	useSkip := skipParam == "YES"

	generatePlaylist := func() {
		snap := st.Snapshot()
		var body string
		if useSkip {
			body = playlist.GenerateWithSkip(snap, s.cfg.SegmentDuration, s.cfg.PartDuration, st.PartHoldBack)
		} else {
			body = playlist.Generate(snap, s.cfg.SegmentDuration, s.cfg.PartDuration, st.PartHoldBack)
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			log.Printf("server: playlist write error: %v", err)
		}
	}

	// Non-blocking request: serve current playlist immediately.
	if msnStr == "" {
		zlog.Debug().Msg("playlist request (non-blocking)")
		generatePlaylist()
		return
	}

	targetMSN, err := strconv.Atoi(msnStr)
	if err != nil {
		http.Error(w, "invalid _HLS_msn", http.StatusBadRequest)
		return
	}
	targetPart := -1
	if partStr != "" {
		targetPart, err = strconv.Atoi(partStr)
		if err != nil {
			http.Error(w, "invalid _HLS_part", http.StatusBadRequest)
			return
		}
	}

	currentMSN := st.GetCurrentMSN()
	//currentPartIdx := st.GetCurrentPartIndex()
	hasIt := st.HasPartOrSegment(targetMSN, targetPart)

	// Per spec: if MSN is more than 2 ahead of current, return 400.
	if targetMSN > currentMSN+2 {
		zlog.Warn().
			Int("req_msn", targetMSN).
			Int("store_current_msn", currentMSN).
			Msg("playlist request: MSN too far in the future")
		http.Error(w, "MSN too far in the future", http.StatusBadRequest)
		return
	}

	// Already available — serve immediately.
	if hasIt {
		generatePlaylist()
		return
	}

	// Block for up to 3× TargetDuration, then return 503.
	timeout := time.Duration(s.cfg.SegmentDuration*3) * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	cancelWatch := st.Notifier().OnContextCancel(ctx)
	defer cancelWatch()

	if err := st.Notifier().WaitFor(ctx, targetMSN, targetPart); err != nil {
		zlog.Debug().Msgf("playlist [%d - %d] wait failed %v", targetMSN, targetPart, err)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			http.Error(w, "timeout waiting for segment", http.StatusServiceUnavailable)
		}
		// client disconnect or other context cancellation — just return
		return
	}

	generatePlaylist()
}

func (s *Server) serveSegment(w http.ResponseWriter, r *http.Request, st *store.Store, name string) {
	setCORSHeaders(w)
	trimmed := strings.TrimPrefix(name, "segment-")
	trimmed = strings.TrimSuffix(trimmed, ".m4s")
	msn, err := strconv.Atoi(trimmed)
	if err != nil {
		http.Error(w, "invalid segment URI", http.StatusBadRequest)
		return
	}

	seg := st.GetSegment(msn)
	if seg == nil || !seg.Completed {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(seg.Data); err != nil {
		log.Printf("server: segment write error: %v", err)
	}
}

func (s *Server) servePart(w http.ResponseWriter, r *http.Request, st *store.Store, name string) {
	setCORSHeaders(w)
	trimmed := strings.TrimPrefix(name, "part-")
	trimmed = strings.TrimSuffix(trimmed, ".m4s")
	parts := strings.SplitN(trimmed, "-", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid part URI", http.StatusBadRequest)
		return
	}
	msn, err1 := strconv.Atoi(parts[0])
	partIdx, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		http.Error(w, "invalid part URI", http.StatusBadRequest)
		return
	}

	//currentMSN := st.GetCurrentMSN()
	//currentPartIdx := st.GetCurrentPartIndex()
	hasIt := st.HasPart(msn, partIdx)

	if !hasIt {
		timeout := time.Duration(s.cfg.SegmentDuration*3) * time.Second
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		cancelWatch := st.Notifier().OnContextCancel(ctx)
		defer cancelWatch()

		if err := st.Notifier().WaitFor(ctx, msn, partIdx); err != nil {
			zlog.Debug().Msgf("wait for part [%d - %d] failed %v", msn, partIdx, err)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				http.Error(w, "timeout waiting for part", http.StatusServiceUnavailable)
			}
			return
		}
	}

	part := st.GetPart(msn, partIdx)
	if part == nil {
		zlog.Warn().Msgf("part [%d - %d] still nil after wait — returning 404", msn, partIdx)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(part.Data); err != nil {
		log.Printf("server: part write error: %v", err)
	}
}

// servePartFromDisk reads a part exclusively from disk (part-{MSN}-{idx}.m4s)
// and never touches the in-memory store. Returns 404 if the file does not exist.
func (s *Server) servePartFromDisk(w http.ResponseWriter, r *http.Request, st *store.Store, name string) {
	setCORSHeaders(w)
	trimmed := strings.TrimPrefix(name, "part-")
	trimmed = strings.TrimSuffix(trimmed, ".m4s")
	parts := strings.SplitN(trimmed, "-", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid part URI", http.StatusBadRequest)
		return
	}
	msn, err1 := strconv.Atoi(parts[0])
	partIdx, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		http.Error(w, "invalid part URI", http.StatusBadRequest)
		return
	}

	dw := st.GetDiskWriter()
	if dw == nil {
		http.Error(w, "disk writer not configured", http.StatusServiceUnavailable)
		return
	}

	data, err := dw.ReadPart(msn, partIdx)
	if err != nil {
		zlog.Debug().Msgf("part [%d - %d] not found on disk %v", msn, partIdx, err)
		http.NotFound(w, r)
		return
	}

	zlog.Debug().Msgf("serving part [%d - %d] bytes=%d from disk (explicit)", msn, partIdx, len(data))
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(data); err != nil {
		log.Printf("server: part write error: %v", err)
	}
}

// serveMedia serves a .m4s file (part or virtual segment).
func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request, state *sessionState, name string) {
	segDur := float64(state.cfg.SegmentDuration)
	timeout := time.Duration(segDur*3) * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	data, ok := state.getFile(ctx, name)
	if !ok {
		if ctx.Err() == context.DeadlineExceeded {
			http.Error(w, "timeout waiting for "+name, http.StatusServiceUnavailable)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(data); err != nil {
		zlog.Debug().Msgf("media file %s write error %v", name, err)
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
  <p>Streams are served at <code>/live/streams/{sessionID}/stream.m3u8</code></p>
  <label>Session ID: <input id="sid" value=""></label>
  <button onclick="load()">Load Stream</button>
  <br><br>
  <video id="video" controls autoplay muted style="width:100%;max-width:400px"></video>
  <script>
    function load() {
      const sid = document.getElementById('sid').value.trim();
      if (!sid) return;
      const src = '/live/streams/' + sid + '/stream.m3u8';
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
	if r.URL.Path != "/live/" {
		http.NotFound(w, r)
		return
	}
	//	fmt.Fprint(w, indexHTML)
	data, err := os.ReadFile("internal/httpserver/hls_local.html") // replace with your file
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Set content-type if needed
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(data)
	if err != nil {
		return
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}
