package store

import "time"

// InitSegment holds the ftyp+moov bytes that clients must download once
// before playing any media segments.
type InitSegment struct {
	Data []byte
}

// Part represents one LL-HLS partial segment (one moof+mdat pair from FFmpeg).
type Part struct {
	Index       int    // part index within its parent segment (0, 1, 2, ...)
	SegmentMSN  int    // Media Sequence Number of the parent segment
	Duration    int64  // actual duration in seconds
	Data        []byte // raw fMP4 bytes (one or more moof+mdat pairs)
	Independent bool   // true if first sample is a keyframe (IDR)
	WallTime    time.Time
}

func (p *Part) FDuration() float64 {
	return float64(p.Duration) / 1000.0
}

// Segment represents a full HLS segment made up of multiple Parts.
type Segment struct {
	MSN       int       // Media Sequence Number (monotonically increasing)
	Duration  int64     // total duration in seconds (sum of part durations)
	Parts     []*Part   // ordered parts comprising this segment
	Data      []byte    // full segment bytes (concatenation of all Part.Data)
	StartTime time.Time // wall-clock time when this segment started (EXT-X-PROGRAM-DATE-TIME)
	Completed bool      // false while still being assembled
}

func (s *Segment) FDuration() float64 {
	return float64(s.Duration) / 1000.0
}
