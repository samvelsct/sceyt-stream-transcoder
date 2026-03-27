package segmenter

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
)

// BoxHeader is the 8-byte prefix of every ISO Base Media File Format box.
type BoxHeader struct {
	Size   uint32  // total byte length of the box, including this header
	FourCC [4]byte // box type (e.g. "moof", "mdat", "moov", "ftyp")
}

// Fragment holds a complete moof+mdat pair with timing metadata extracted
// from the moof box.
type Fragment struct {
	Data        []byte  // raw bytes of moof + mdat
	DurationSec float64 // duration of this fragment in seconds
	IsKeyframe  bool    // true if the first sample is a sync (IDR) frame
}

// FragmentParser reads a fragmented MP4 byte stream produced by FFmpeg and
// emits:
//   - one InitData callback with the ftyp+moov bytes
//   - repeated Fragment callbacks for each moof+mdat pair
type FragmentParser struct {
	r          io.Reader
	OnInit     func(data []byte)
	OnFragment func(f Fragment)

	// video timescale extracted from the moov box (trak/mdia/mdhd/timescale).
	// We approximate it as 90000 (standard for H.264 over RTP) because parsing
	// nested moov boxes in raw bytes is complex.  The segmenter can override
	// this via SetTimescale if needed.
	timescale uint32
}

// NewFragmentParser creates a parser that reads from r.
func NewFragmentParser(r io.Reader) *FragmentParser {
	return &FragmentParser{r: r, timescale: 90000}
}

// SetTimescale overrides the assumed video timescale.
func (p *FragmentParser) SetTimescale(ts uint32) {
	p.timescale = ts
}

// Run reads the stream until EOF or an error occurs. It calls OnInit once
// for the initialization segment and OnFragment for every media fragment.
func (p *FragmentParser) Run() error {
	var initBuf []byte
	initDone := false

	for {
		hdr, payload, err := p.readBox()
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read box: %w", err)
		}

		fourcc := string(hdr.FourCC[:])

		if !initDone {
			boxBytes := boxHeaderBytes(hdr)
			boxBytes = append(boxBytes, payload...)
			initBuf = append(initBuf, boxBytes...)

			if fourcc == "moov" {
				initDone = true
				// Try to extract timescale from moov.
				if ts := extractVideoTimescale(payload); ts > 0 {
					p.timescale = ts
				}
				if p.OnInit != nil {
					p.OnInit(initBuf)
				}
			}
			continue
		}

		if fourcc == "moof" {
			// Read the following mdat box.
			mdatHdr, mdatPayload, err := p.readBox()
			if err != nil {
				return fmt.Errorf("read mdat after moof: %w", err)
			}

			moofBytes := boxHeaderBytes(hdr)
			moofBytes = append(moofBytes, payload...)
			mdatBytes := boxHeaderBytes(mdatHdr)
			mdatBytes = append(mdatBytes, mdatPayload...)

			frag := Fragment{
				Data: append(moofBytes, mdatBytes...),
			}

			// Parse moof to extract duration and keyframe flag.
			dur, isKey := parseMoof(payload, p.timescale)
			frag.DurationSec = dur
			frag.IsKeyframe = isKey

			if p.OnFragment != nil {
				p.OnFragment(frag)
			}
		}
	}
}

// readBox reads one box header + payload from the stream.
func (p *FragmentParser) readBox() (BoxHeader, []byte, error) {
	var hdrBuf [8]byte
	if _, err := io.ReadFull(p.r, hdrBuf[:]); err != nil {
		return BoxHeader{}, nil, err
	}

	var hdr BoxHeader
	hdr.Size = binary.BigEndian.Uint32(hdrBuf[:4])
	copy(hdr.FourCC[:], hdrBuf[4:8])

	if hdr.Size < 8 {
		return hdr, nil, fmt.Errorf("invalid box size %d for %s", hdr.Size, hdr.FourCC)
	}

	payloadLen := int(hdr.Size) - 8
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(p.r, payload); err != nil {
		return hdr, nil, err
	}
	return hdr, payload, nil
}

// boxHeaderBytes serialises a BoxHeader back into its 8-byte wire form.
func boxHeaderBytes(hdr BoxHeader) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[:4], hdr.Size)
	copy(b[4:8], hdr.FourCC[:])
	return b
}

// parseMoof extracts the total sample duration and whether the first sample
// is a sync frame from a raw moof box payload (without the 8-byte header).
//
// The parsing is a minimal hand-rolled walk of the relevant boxes:
//
//	moof > traf > tfhd  (default sample flags)
//	moof > traf > tfdt  (base media decode time)
//	moof > traf > trun  (sample table: duration, flags)
//
// Duration is returned as seconds using the provided timescale.
// If parsing fails the function returns (0.033, true) as a safe fallback
// (one 30 fps frame, keyframe assumed).
func parseMoof(payload []byte, timescale uint32) (durationSec float64, isKeyframe bool) {
	if timescale == 0 {
		timescale = 90000
	}

	// moof may contain multiple traf boxes (one per track, e.g. video + audio).
	// We only care about the video traf, which is always the first traf box
	// with a non-zero duration (the fragmenter puts video first).
	off := 0
	for off+8 <= len(payload) {
		size := int(binary.BigEndian.Uint32(payload[off:]))
		if size < 8 || off+size > len(payload) {
			break
		}
		if string(payload[off+4:off+8]) == "traf" {
			dur, kf := parseTraf(payload[off+8:off+size], timescale)
			if dur > 0 {
				return float64(dur) / float64(timescale), kf
			}
		}
		off += size
	}

	log.Printf("parseMoof: could not determine duration (timescale=%d), using fallback 0.033s", timescale)
	return 0.033, true
}

// parseTraf extracts total sample duration (in timescale units) and keyframe
// flag from a single traf box payload (without its 8-byte header).
func parseTraf(traf []byte, timescale uint32) (totalDuration uint64, isKeyframe bool) {
	isKeyframe = true // safe default

	// tfhd: default sample duration and default sample flags.
	var defaultSampleFlags uint32
	var defaultSampleDuration uint32
	if tfhd := findChildBox(traf, "tfhd"); tfhd != nil && len(tfhd) >= 4 {
		tfhdFlags := uint32(tfhd[1])<<16 | uint32(tfhd[2])<<8 | uint32(tfhd[3])
		toff := 4 + 4 // version(1)+flags(3) + track_ID(4)
		if tfhdFlags&0x000001 != 0 {
			toff += 8 // base_data_offset
		}
		if tfhdFlags&0x000002 != 0 {
			toff += 4 // sample_description_index
		}
		if tfhdFlags&0x000008 != 0 && toff+4 <= len(tfhd) { // default_sample_duration
			defaultSampleDuration = binary.BigEndian.Uint32(tfhd[toff:])
			toff += 4
		}
		if tfhdFlags&0x000010 != 0 {
			toff += 4 // default_sample_size
		}
		if tfhdFlags&0x000020 != 0 && toff+4 <= len(tfhd) { // default_sample_flags
			defaultSampleFlags = binary.BigEndian.Uint32(tfhd[toff:])
		}
	}

	// Iterate all trun boxes within this traf (there may be more than one).
	off := 0
	for off+8 <= len(traf) {
		size := int(binary.BigEndian.Uint32(traf[off:]))
		if size < 8 || off+size > len(traf) {
			break
		}
		if string(traf[off+4:off+8]) == "trun" {
			trun := traf[off+8 : off+size]
			d, kf := parseTrun(trun, defaultSampleDuration, defaultSampleFlags)
			totalDuration += d
			if d > 0 {
				isKeyframe = kf
			}
		}
		off += size
	}

	return totalDuration, isKeyframe
}

// parseTrun extracts total duration (timescale units) and keyframe flag from a
// trun box payload (without its 8-byte header).
func parseTrun(trun []byte, defaultSampleDuration, defaultSampleFlags uint32) (totalDuration uint64, isKeyframe bool) {
	isKeyframe = true
	if len(trun) < 8 {
		return 0, true
	}

	version := trun[0]
	flags := uint32(trun[1])<<16 | uint32(trun[2])<<8 | uint32(trun[3])
	sampleCount := binary.BigEndian.Uint32(trun[4:8])
	if sampleCount == 0 {
		return 0, true
	}

	off := 8
	if flags&0x000001 != 0 { // data_offset_present
		off += 4
	}
	if flags&0x000004 != 0 { // first_sample_flags_present
		if off+4 > len(trun) {
			return 0, true
		}
		firstFlags := binary.BigEndian.Uint32(trun[off:])
		isKeyframe = (firstFlags>>16)&0x1 == 0
		off += 4
	} else {
		isKeyframe = (defaultSampleFlags>>16)&0x1 == 0
	}

	hasDuration := flags&0x000100 != 0
	hasSize := flags&0x000200 != 0
	hasFlags := flags&0x000400 != 0
	hasComposition := flags&0x000800 != 0

	entrySize := 0
	if hasDuration {
		entrySize += 4
	}
	if hasSize {
		entrySize += 4
	}
	if hasFlags {
		entrySize += 4
	}
	if hasComposition {
		if version == 1 {
			entrySize += 8
		} else {
			entrySize += 4
		}
	}

	if hasDuration {
		for i := uint32(0); i < sampleCount; i++ {
			if off+4 > len(trun) {
				break
			}
			totalDuration += uint64(binary.BigEndian.Uint32(trun[off:]))
			off += entrySize
		}
	} else if defaultSampleDuration > 0 {
		// No per-sample duration in trun; use tfhd default.
		totalDuration = uint64(defaultSampleDuration) * uint64(sampleCount)
	}

	return totalDuration, isKeyframe
}

// ParseMoofFromBytes parses the duration and keyframe flag from a complete
// moof+mdat byte slice as delivered by the native fragmenter onFragment
// callback (fragIdx >= 1).
//
// timescale must equal the H.264 encoder's FPS value (the fragmenter sets
// time_base = {1, fps}, so the timescale embedded in the moov box equals fps).
func ParseMoofFromBytes(data []byte, timescale uint32) (durationSec float64, isKeyframe bool) {
	off := 0
	for off+8 <= len(data) {
		boxSize := int(binary.BigEndian.Uint32(data[off:]))
		if boxSize < 8 || off+boxSize > len(data) {
			break
		}
		if string(data[off+4:off+8]) == "moof" {
			return parseMoof(data[off+8:off+boxSize], timescale)
		}
		off += boxSize
	}
	return 0.033, true
}

// findChildBox searches payload for a direct child box with the given fourcc
// and returns its payload (without the 8-byte header), or nil if not found.
func findChildBox(payload []byte, fourcc string) []byte {
	off := 0
	for off+8 <= len(payload) {
		size := int(binary.BigEndian.Uint32(payload[off:]))
		if size < 8 || off+size > len(payload) {
			break
		}
		if string(payload[off+4:off+8]) == fourcc {
			return payload[off+8 : off+size]
		}
		off += size
	}
	return nil
}

// ExtractVideoTimescaleFromInit parses the ftyp+moov initialization segment
// bytes (as delivered by the native fragmenter's fragIdx==0 callback) and
// returns the video track's timescale from the mdhd box.  Returns 0 if the
// timescale cannot be determined.
func ExtractVideoTimescaleFromInit(initData []byte) uint32 {
	off := 0
	for off+8 <= len(initData) {
		size := int(binary.BigEndian.Uint32(initData[off:]))
		if size < 8 || off+size > len(initData) {
			break
		}
		if string(initData[off+4:off+8]) == "moov" {
			return extractVideoTimescale(initData[off+8 : off+size])
		}
		off += size
	}
	return 0
}

// extractVideoTimescale attempts to read the video track's timescale from a
// moov box payload.  Returns 0 if not found.
func extractVideoTimescale(moovPayload []byte) uint32 {
	// Walk trak boxes looking for a video track (hdlr type == "vide").
	off := 0
	for off+8 <= len(moovPayload) {
		size := int(binary.BigEndian.Uint32(moovPayload[off:]))
		if size < 8 || off+size > len(moovPayload) {
			break
		}
		fourcc := string(moovPayload[off+4 : off+8])
		if fourcc == "trak" {
			trakPayload := moovPayload[off+8 : off+size]
			if ts := timescaleFromTrak(trakPayload); ts > 0 {
				return ts
			}
		}
		off += size
	}
	return 0
}

func timescaleFromTrak(trak []byte) uint32 {
	mdia := findChildBox(trak, "mdia")
	if mdia == nil {
		return 0
	}
	// Check hdlr for handler_type == "vide".
	hdlr := findChildBox(mdia, "hdlr")
	if hdlr == nil || len(hdlr) < 12 {
		return 0
	}
	// hdlr: version(1) + flags(3) + pre_defined(4) + handler_type(4)
	handlerType := string(hdlr[8:12])
	if handlerType != "vide" {
		return 0
	}
	mdhd := findChildBox(mdia, "mdhd")
	if mdhd == nil {
		return 0
	}
	// mdhd version 0: version(1)+flags(3)+creation_time(4)+modification_time(4)+timescale(4)
	// mdhd version 1: version(1)+flags(3)+creation_time(8)+modification_time(8)+timescale(4)
	version := mdhd[0]
	if version == 0 && len(mdhd) >= 16 {
		return binary.BigEndian.Uint32(mdhd[12:16])
	}
	if version == 1 && len(mdhd) >= 24 {
		return binary.BigEndian.Uint32(mdhd[20:24])
	}
	return 0
}
