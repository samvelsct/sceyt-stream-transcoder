package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// DiskWriter persists the LL-HLS stream to a directory on disk.
// Wire it to a Store via Store.SetDiskWriter before streaming begins.
//
// On start: writes init.mp4 immediately when the init segment is available.
// On segment complete: writes segment-N.m4s immediately.
// On stop: writes the final VOD playlist (playlistName).
// WrittenSeg describes one segment file written to disk.
type WrittenSeg struct {
	Filename string
	Duration float64
}

type DiskWriter struct {
	dir          string
	playlistName string // filename for the m3u8, e.g. "output.m3u8"
	playlistFn   func(segs []WrittenSeg, endList bool) string

	mu              sync.Mutex // protects writtenSegments
	writtenSegments []WrittenSeg
}

// NewDiskWriter creates a DiskWriter that writes into dir using playlistName
// as the playlist filename. dir is created (with all parents) if it does not
// already exist.
func NewDiskWriter(dir, playlistName string, playlistFn func(segs []WrittenSeg, endList bool) string) (*DiskWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("disk writer: mkdir %s: %w", dir, err)
	}
	return &DiskWriter{dir: dir, playlistName: playlistName, playlistFn: playlistFn}, nil
}

// WriteInit writes the ftyp+moov initialization segment to init.mp4.
func (dw *DiskWriter) WriteInit(data []byte) {
	dw.writeFile("init.mp4", data)
}

// WriteSegment writes a completed segment to disk immediately as segment-N.m4s
// and updates the playlist file.
// Called synchronously from store.finalizeSegment.
func (dw *DiskWriter) WriteSegment(seg *Segment) {
	filename := fmt.Sprintf("segment-%d.m4s", seg.MSN)
	dw.writeFile(filename, seg.Data)

	dw.mu.Lock()
	dw.writtenSegments = append(dw.writtenSegments, WrittenSeg{
		Filename: filename,
		Duration: seg.FDuration(),
	})
	segs := dw.writtenSegments
	dw.mu.Unlock()

	dw.writeFile(dw.playlistName, []byte(dw.playlistFn(segs, false)))
}

// Finalize flushes the final playlist with EXT-X-ENDLIST after the stream ends.
// Call once after the stream has ended and all segments have been flushed.
func (dw *DiskWriter) Finalize() {
	dw.mu.Lock()
	segs := dw.writtenSegments
	dw.mu.Unlock()

	var totalDuration float64
	for _, s := range segs {
		totalDuration += s.Duration
	}

	playlist := dw.playlistFn(segs, true)
	dw.writeFile(dw.playlistName, []byte(playlist))

	log.Printf("[disk_writer] finalized: %.3fs, %d segment(s) -> %s",
		totalDuration, len(segs), dw.playlistName)
}

func (dw *DiskWriter) writeFile(name string, data []byte) {
	path := filepath.Join(dw.dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("[disk_writer] write %s: %v", path, err)
	}
}
