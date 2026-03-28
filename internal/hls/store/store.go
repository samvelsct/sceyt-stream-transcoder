package store

import (
	"sync"
	"time"
	"vt-stream-transcoder/internal/config"

	zlog "github.com/rs/zerolog/log"
)

// partAccumulator collects fMP4 fragments until enough duration has
// accumulated to emit a complete LL-HLS part.
type partAccumulator struct {
	data        []byte
	duration    float64 // accumulated duration in seconds
	independent bool    // true if first fragment in this part was a keyframe
}

// Store is the central in-memory store for all media state.
// All public methods are safe for concurrent use.
type Store struct {
	mu               sync.RWMutex
	cfg              *config.HLSConfig
	init             *InitSegment
	segments         []*Segment // sliding window of completed segments
	currentSeg       *Segment   // segment currently being assembled
	currentPart      *partAccumulator
	nextMSN          int
	notifier         *Notifier
	diskWriter       *DiskWriter // optional; nil means no disk output
	SegmentDurationD time.Duration
	PartHoldBack     float64
}

// Snapshot is an immutable copy of store state for playlist generation.
type Snapshot struct {
	Init           *InitSegment
	Segments       []*Segment // window of completed segments
	CurrentSegment *Segment   // in-progress segment (may be nil before stream starts)
}

// NewStore creates a Store with the given configuration.
func NewStore(cfg *config.HLSConfig) *Store {
	return &Store{
		cfg:              cfg,
		notifier:         NewNotifier(),
		SegmentDurationD: time.Duration(cfg.SegmentDuration * float64(time.Second)),
		PartHoldBack:     3 * cfg.PartDuration,
	}
}

// Notifier returns the store's Notifier for use by HTTP handlers.
func (s *Store) Notifier() *Notifier {
	return s.notifier
}

// SetDiskWriter attaches a DiskWriter to the store.
// Must be called before streaming begins (before SetInit is called).
func (s *Store) SetDiskWriter(dw *DiskWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diskWriter = dw
}

// SetInit stores the ftyp+moov initialization segment.
func (s *Store) SetInit(data []byte) {
	s.mu.Lock()
	cp := make([]byte, len(data))
	copy(cp, data)
	s.init = &InitSegment{Data: cp}
	dw := s.diskWriter
	s.mu.Unlock()

	if dw != nil {
		dw.WriteInit(cp)
	}
}

// GetInit returns the initialization segment or nil if not yet available.
func (s *Store) GetInit() *InitSegment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.init
}

// AddFragment appends an fMP4 fragment (moof+mdat bytes) to the current part
// accumulator. When enough duration has accumulated it finalizes the part and
// (if needed) the segment.
func (s *Store) AddFragment(data []byte, durationSec float64, isKeyframe bool) {
	s.mu.Lock()

	// Lazy-initialize current segment and part.
	if s.currentSeg == nil {
		s.currentSeg = &Segment{
			MSN:       s.nextMSN,
			StartTime: time.Now(),
		}
		s.nextMSN++
	}
	if s.currentPart == nil {
		s.currentPart = &partAccumulator{
			independent: isKeyframe,
		}
	}

	// Accumulate fragment data and duration.
	s.currentPart.data = append(s.currentPart.data, data...)
	s.currentPart.duration += durationSec

	zlog.Debug().
		Int("frag_bytes", len(data)).
		Float64("frag_duration_sec", durationSec).
		Bool("keyframe", isKeyframe).
		Int("current_msn", s.currentSeg.MSN).
		Float64("accumulated_part_duration", s.currentPart.duration).
		Float64("part_duration_target", s.cfg.PartDuration).
		Int("parts_in_seg", len(s.currentSeg.Parts)).
		Msg("store: fragment added")

	// Finalize the part if enough duration has accumulated, capturing any
	// pending broadcast so we can fire it after releasing the lock.
	var pendingMSN, pendingPart int
	var hasPending bool
	if s.currentPart.duration >= s.cfg.PartDuration {
		pendingMSN, pendingPart, hasPending = s.finalizePart()
	}

	s.mu.Unlock()

	// Broadcast outside the lock so waiters don't race against it.
	if hasPending {
		s.notifier.Broadcast(pendingMSN, pendingPart)
	}
}

// finalizePart closes the current part and appends it to the current segment.
// Must be called with s.mu held. Returns the (msn, partIdx) to broadcast and
// whether a broadcast is needed; the caller must fire it after releasing s.mu.
func (s *Store) finalizePart() (broadcastMSN, broadcastPart int, ok bool) {
	if s.currentPart == nil || len(s.currentPart.data) == 0 {
		return 0, 0, false
	}

	part := &Part{
		Index:       len(s.currentSeg.Parts),
		SegmentMSN:  s.currentSeg.MSN,
		Duration:    int64(s.currentPart.duration * 1000),
		Data:        s.currentPart.data,
		Independent: s.currentPart.independent,
		WallTime:    time.Now(),
	}
	s.currentSeg.Parts = append(s.currentSeg.Parts, part)
	s.currentSeg.Duration += part.Duration
	s.currentSeg.Data = append(s.currentSeg.Data, part.Data...)
	s.currentPart = nil

	zlog.Debug().
		Int("msn", part.SegmentMSN).
		Int("part_idx", part.Index).
		Int("part_bytes", len(part.Data)).
		Int64("part_duration_ms", part.Duration).
		Bool("independent", part.Independent).
		Int64("seg_duration_ms", s.currentSeg.Duration).
		Int64("seg_duration_target_ms", s.SegmentDurationD.Milliseconds()).
		Msg("store: part finalized")

	broadcastMSN = s.currentSeg.MSN
	broadcastPart = part.Index

	// Check if we should close the current segment; if so the segment
	// broadcast supersedes the part broadcast.
	if s.currentSeg.Duration >= s.SegmentDurationD.Milliseconds() {
		broadcastMSN, broadcastPart = s.finalizeSegment()
	}

	return broadcastMSN, broadcastPart, true
}

// finalizeSegment closes the current segment and starts a new one.
// Must be called with s.mu held. Returns the (msn, partIdx=-1) to broadcast;
// the caller must fire it after releasing s.mu.
func (s *Store) finalizeSegment() (broadcastMSN, broadcastPart int) {
	if s.currentSeg == nil {
		return 0, 0
	}

	zlog.Debug().
		Int("msn", s.currentSeg.MSN).
		Int("parts", len(s.currentSeg.Parts)).
		Int64("duration_ms", s.currentSeg.Duration).
		Int("total_completed_segs", len(s.segments)).
		Msg("store: segment finalized")

	s.currentSeg.Completed = true
	completedSeg := s.currentSeg
	s.segments = append(s.segments, s.currentSeg)

	// Trim window.
	if len(s.segments) > s.cfg.PlaylistWindow {
		s.segments = s.segments[len(s.segments)-s.cfg.PlaylistWindow:]
	}

	completedMSN := s.currentSeg.MSN
	// Start fresh segment.
	s.currentSeg = &Segment{
		MSN:       s.nextMSN,
		StartTime: time.Now(),
	}
	s.nextMSN++

	if s.diskWriter != nil {
		s.diskWriter.WriteSegment(completedSeg)
	}

	return completedMSN, -1
}

// Finalize forces any in-progress part and segment to be closed, then
// instructs the disk writer (if any) to write the final EXT-X-ENDLIST
// playlist. Call this after the encoding pipeline has been fully flushed.
func (s *Store) Finalize() {
	s.mu.Lock()
	var pendingMSN, pendingPart int
	var hasPending bool
	if s.currentPart != nil && len(s.currentPart.data) > 0 {
		pendingMSN, pendingPart, hasPending = s.finalizePart()
	}
	if s.currentSeg != nil && s.currentSeg.Duration > 0 {
		pendingMSN, pendingPart = s.finalizeSegment()
		hasPending = true
	}
	dw := s.diskWriter
	s.mu.Unlock()

	if hasPending {
		s.notifier.Broadcast(pendingMSN, pendingPart)
	}
	if dw != nil {
		dw.Finalize()
	}
}

// GetSegment returns the completed segment with the given MSN, or nil.
func (s *Store) GetSegment(msn int) *Segment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, seg := range s.segments {
		if seg.MSN == msn {
			return seg
		}
	}
	return nil
}

// GetPart returns a completed part or nil if not yet available.
func (s *Store) GetPart(msn, partIdx int) *Part {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Look in completed segments first.
	for _, seg := range s.segments {
		if seg.MSN == msn && partIdx < len(seg.Parts) {
			return seg.Parts[partIdx]
		}
	}
	// Look in the current in-progress segment.
	if s.currentSeg != nil && s.currentSeg.MSN == msn && partIdx < len(s.currentSeg.Parts) {
		return s.currentSeg.Parts[partIdx]
	}
	return nil
}

// HasPart returns true if the given part is already available.
func (s *Store) HasPart(msn, partIdx int) bool {
	return s.GetPart(msn, partIdx) != nil
}

// GetCurrentMSN returns the MSN of the segment currently being assembled.
func (s *Store) GetCurrentMSN() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.currentSeg != nil {
		return s.currentSeg.MSN
	}
	return s.nextMSN
}

// GetCurrentPartIndex returns the index of the last completed part in the
// current segment, or -1 if no parts have been completed yet.
func (s *Store) GetCurrentPartIndex() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.currentSeg == nil {
		return -1
	}
	return len(s.currentSeg.Parts) - 1
}

// HasPartOrSegment returns true if targetMSN and (if targetPart >= 0)
// targetPart are available.
func (s *Store) HasPartOrSegment(targetMSN, targetPart int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if targetPart < 0 {
		for _, seg := range s.segments {
			if seg.MSN == targetMSN && seg.Completed {
				return true
			}
		}
		return false
	}
	// Inline part lookup — must not call GetPart() here as it would
	// attempt to re-acquire RLock while we already hold it, which
	// deadlocks when a writer is pending (AddFragment waiting for Lock).
	for _, seg := range s.segments {
		if seg.MSN == targetMSN && targetPart < len(seg.Parts) {
			return true
		}
	}
	if s.currentSeg != nil && s.currentSeg.MSN == targetMSN && targetPart < len(s.currentSeg.Parts) {
		return true
	}
	return false
}

// Snapshot returns an immutable snapshot of the current store state.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := Snapshot{}
	if s.init != nil {
		snap.Init = s.init
	}

	// Copy completed segments slice.
	snap.Segments = make([]*Segment, len(s.segments))
	copy(snap.Segments, s.segments)

	// Shallow copy current segment (including its parts slice).
	if s.currentSeg != nil {
		cur := *s.currentSeg
		cur.Parts = make([]*Part, len(s.currentSeg.Parts))
		copy(cur.Parts, s.currentSeg.Parts)
		snap.CurrentSegment = &cur
	}

	return snap
}
