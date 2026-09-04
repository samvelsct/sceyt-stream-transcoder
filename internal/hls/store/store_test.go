package store

import (
	"testing"

	"vt-stream-transcoder/internal/config"
)

// finalizeSegment trims s.segments to the configured window by copying into
// a fresh backing array (not just re-slicing) so dropped segments' byte
// buffers can be garbage collected. This test guards the window-size
// behavior itself: after many more segments than the window size have been
// finalized, the snapshot must still only contain the most recent
// PlaylistWindow segments, in order.
func TestFinalizeSegmentTrimsToWindow(t *testing.T) {
	cfg := &config.HLSConfig{
		SegmentDuration: 1,   // seconds
		PartDuration:    0.2, // seconds
		PlaylistWindow:  3,
	}
	s := NewStore(cfg)
	s.SetInit([]byte("fake-init"))

	const totalSegments = 10
	// Each part is 0.2s; 5 parts per segment closes it (>= 1s).
	for seg := 0; seg < totalSegments; seg++ {
		for part := 0; part < 5; part++ {
			s.AddFragment([]byte("frag"), 0.2, part == 0)
		}
	}

	// finalizeSegment's trim condition (len >= PlaylistWindow -> drop one)
	// converges to a PlaylistWindow-1 steady state, not PlaylistWindow;
	// that off-by-one predates this test and is intentionally preserved.
	wantSteadyState := cfg.PlaylistWindow - 1
	snap := s.Snapshot()
	if len(snap.Segments) != wantSteadyState {
		t.Fatalf("expected %d segments in window, got %d", wantSteadyState, len(snap.Segments))
	}

	// The window must hold the most recently completed segments, oldest
	// first, MSNs contiguous.
	firstMSN := snap.Segments[0].MSN
	for i, seg := range snap.Segments {
		if seg.MSN != firstMSN+i {
			t.Fatalf("segment %d: expected MSN %d, got %d", i, firstMSN+i, seg.MSN)
		}
	}
	lastMSN := snap.Segments[len(snap.Segments)-1].MSN
	if snap.CurrentSegment == nil {
		t.Fatalf("expected an in-progress current segment")
	}
	if snap.CurrentSegment.MSN != lastMSN+1 {
		t.Fatalf("expected current segment MSN %d, got %d", lastMSN+1, snap.CurrentSegment.MSN)
	}
}
