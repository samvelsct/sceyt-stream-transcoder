package playlist

import (
	"fmt"
	"math"
	"strings"
	"time"
	"vt-stream-transcoder/internal/config"
	"vt-stream-transcoder/internal/hls/store"
)

// Generate produces a complete LL-HLS playlist (m3u8) from a store snapshot.
// It is a pure function with no side effects.
func Generate(snap store.Snapshot, segmentDuration float64, partDuration float64, partHoldBack float64) string {
	return generate(snap, segmentDuration, partDuration, partHoldBack, false)
}

// GenerateWithSkip produces a playlist using EXT-X-SKIP to omit old segments.
// Callers should pass this when the client sent _HLS_skip=YES.
func GenerateWithSkip(snap store.Snapshot, segmentDuration float64, partDuration float64, partHoldBack float64) string {
	return generate(snap, segmentDuration, partDuration, partHoldBack, true)
}

func generate(snap store.Snapshot, segmentDuration float64, partDuration float64, partHoldBack float64, skip bool) string {
	var b strings.Builder

	// -----------------------------------------------------------------------
	// Playlist header
	// -----------------------------------------------------------------------
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:9\n")
	// TARGETDURATION must be >= ceil(max actual segment duration).
	// Actual duration can exceed SegmentDuration by up to one part because
	// the segment is only closed after a complete part pushes it over the
	// threshold. Use ceil(SegmentDuration + PartDuration) to guarantee
	// every segment satisfies the spec constraint.
	targetDuration := int(segmentDuration)
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	// CAN-SKIP-UNTIL must be >= 6 × TARGETDURATION (not 6 × SegmentDuration).
	// draft-pantos-hls-rfc8216bis-20 §4.4.3.8: "The Skip Boundary MUST be
	// at least six times the Target Duration."
	fmt.Fprintf(&b,
		"#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,PART-HOLD-BACK=%.3f,CAN-SKIP-UNTIL=%.3f\n",
		partHoldBack,
		float64(targetDuration)*6,
	)
	fmt.Fprintf(&b, "#EXT-X-PART-INF:PART-TARGET=%.3f\n", partDuration)

	// -----------------------------------------------------------------------
	// EXT-X-SKIP: compute skipped count before writing EXT-X-MEDIA-SEQUENCE
	// because the sequence number must reflect the first segment that is
	// actually present in the playlist (i.e. after the skip).
	// Per spec, CAN-SKIP-UNTIL = 6 × TARGETDURATION. segsToKeep is derived
	// from actual SegmentDuration so it stays correct for any segment length.
	// -----------------------------------------------------------------------
	canSkipUntilSec := float64(targetDuration) * 6
	segsToKeep := int(math.Ceil(canSkipUntilSec / segmentDuration))
	skippedCount := 0
	segStart := 0

	if skip && len(snap.Segments) > segsToKeep {
		skippedCount = len(snap.Segments) - segsToKeep
		segStart = skippedCount
	}

	// Media sequence: always the MSN of the first segment in the full window
	// (before any skip). Per RFC 8216bis §4.4.5.2 EXT-X-MEDIA-SEQUENCE must
	// not be adjusted when EXT-X-SKIP is used — the player adds SKIPPED-
	// SEGMENTS to this base to locate the first visible segment.
	mediaSeq := 0
	if len(snap.Segments) > 0 {
		mediaSeq = snap.Segments[0].MSN
	} else if snap.CurrentSegment != nil {
		mediaSeq = snap.CurrentSegment.MSN
	}
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", mediaSeq)
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")

	if skippedCount > 0 {
		fmt.Fprintf(&b, "#EXT-X-SKIP:SKIPPED-SEGMENTS=%d\n", skippedCount)
	}

	// -----------------------------------------------------------------------
	// Segments in the sliding window
	// -----------------------------------------------------------------------
	programDateWritten := false

	for i, seg := range snap.Segments[segStart:] {
		absIdx := segStart + i
		isLast := absIdx == len(snap.Segments)-1

		if !programDateWritten {
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", seg.StartTime.UTC().Format(time.RFC3339Nano))
			programDateWritten = true
		}

		// Parts are shown before the last complete segment's EXTINF line
		// (and before the in-progress segment below).
		if isLast {
			writeParts(&b, seg)
		}

		fmt.Fprintf(&b, "#EXTINF:%.5f,\n", seg.FDuration())
		fmt.Fprintf(&b, "segment-%d.m4s\n", seg.MSN)
	}

	// -----------------------------------------------------------------------
	// In-progress segment: show completed parts + preload hint
	// -----------------------------------------------------------------------
	if snap.CurrentSegment != nil {
		cur := snap.CurrentSegment

		for _, part := range cur.Parts {
			writePartTag(&b, part)
		}

		// Preload hint for the next part (the one being encoded right now).
		nextPartIdx := len(cur.Parts)
		fmt.Fprintf(&b, "#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"part-%d-%d.m4s\"\n",
			cur.MSN, nextPartIdx)
	}

	return b.String()
}

// writeParts writes EXT-X-PART tags for all parts of a completed segment.
func writeParts(b *strings.Builder, seg *store.Segment) {
	for _, part := range seg.Parts {
		writePartTag(b, part)
	}
}

// GenerateDisk produces a standard HLS EVENT playlist for on-disk recording.
// It uses EXT-X-PLAYLIST-TYPE:EVENT so players start playback from the
// beginning of the recording rather than jumping to the live edge.
// When endList is true, EXT-X-ENDLIST is appended to seal the recording as VOD.
func GenerateDisk(snap store.Snapshot, cfg *config.HLSConfig, endList bool) string {
	var b strings.Builder

	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:6\n")
	// +1 to account for the part-overshoot: a segment can exceed SegmentDuration
	// by up to one part duration before being closed.
	targetDuration := int(cfg.SegmentDuration) + 1
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")

	mediaSeq := 0
	if len(snap.Segments) > 0 {
		mediaSeq = snap.Segments[0].MSN
	}
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", mediaSeq)
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")

	for _, seg := range snap.Segments {
		fmt.Fprintf(&b, "#EXTINF:%.5f,\n", seg.FDuration())
		fmt.Fprintf(&b, "segment-%d.m4s\n", seg.MSN)
	}

	if endList {
		b.WriteString("#EXT-X-ENDLIST\n")
	}

	return b.String()
}

// GenerateDiskPlaylist produces a playlist referencing each written segment.
// When endList is false the playlist type is EVENT (stream still recording).
// When endList is true the type is VOD and EXT-X-ENDLIST is appended.
func GenerateDiskPlaylist(segs []store.WrittenSeg, endList bool) string {
	var b strings.Builder

	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:6\n")

	var maxDur float64
	for _, s := range segs {
		if s.Duration > maxDur {
			maxDur = s.Duration
		}
	}
	targetDuration := int(maxDur) + 1
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	if endList {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	} else {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	}
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")

	for _, s := range segs {
		fmt.Fprintf(&b, "#EXTINF:%.5f,\n", s.Duration)
		fmt.Fprintf(&b, "%s\n", s.Filename)
	}

	if endList {
		b.WriteString("#EXT-X-ENDLIST\n")
	}

	return b.String()
}

// writePartTag writes a single EXT-X-PART line.
func writePartTag(b *strings.Builder, part *store.Part) {
	independent := ""
	if part.Independent {
		independent = ",INDEPENDENT=YES"
	}
	fmt.Fprintf(b, "#EXT-X-PART:DURATION=%.5f,URI=\"part-%d-%d.m4s\"%s\n",
		part.FDuration(), part.SegmentMSN, part.Index, independent)
}
