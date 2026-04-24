package segmenter

import "encoding/binary"

// FixVideoEmptyEdit removes the leading empty-edit entry from the video
// track's elst box inside a ftyp+moov initialization segment.
//
// Some encoders (e.g. FFmpeg with WebRTC input) emit an elst for the video
// track whose first entry is an empty edit (media_time == -1) with a short
// segment_duration (typically one encoder-delay frame, e.g. 21 ms).  This
// causes players to insert an audio-only gap at the start of playback, making
// audio appear to "lead" video by that amount.
//
// The function rewrites that first entry to {segment_duration=0,
// media_time=0, rate=1} and sets entry_count=1, eliminating the gap while
// keeping the box size unchanged.  It is a no-op if no empty edit is found.
//
// The returned slice is always a fresh copy; the original initData is not
// modified.
func FixVideoEmptyEdit(initData []byte) []byte {
	out := make([]byte, len(initData))
	copy(out, initData)
	walkMoovForElst(out)
	return out
}

// walkMoovForElst scans top-level boxes for the moov box and delegates.
func walkMoovForElst(data []byte) {
	off := 0
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off:]))
		if size < 8 || off+size > len(data) {
			break
		}
		if string(data[off+4:off+8]) == "moov" {
			fixElstInTraks(data[off+8 : off+size])
			return
		}
		off += size
	}
}

// fixElstInTraks walks trak boxes inside a moov payload.
func fixElstInTraks(moov []byte) {
	off := 0
	for off+8 <= len(moov) {
		size := int(binary.BigEndian.Uint32(moov[off:]))
		if size < 8 || off+size > len(moov) {
			break
		}
		if string(moov[off+4:off+8]) == "trak" {
			trak := moov[off+8 : off+size]
			if isVideoTrak(trak) {
				fixElstInTrak(trak)
			}
		}
		off += size
	}
}

// isVideoTrak returns true when the trak's hdlr handler_type is "vide".
func isVideoTrak(trak []byte) bool {
	mdia := findChildBox(trak, "mdia")
	if mdia == nil {
		return false
	}
	hdlr := findChildBox(mdia, "hdlr")
	// hdlr payload: version(1)+flags(3)+pre_defined(4)+handler_type(4)
	return hdlr != nil && len(hdlr) >= 12 && string(hdlr[8:12]) == "vide"
}

// fixElstInTrak locates the elst box inside trak > edts and patches it.
func fixElstInTrak(trak []byte) {
	off := 0
	for off+8 <= len(trak) {
		size := int(binary.BigEndian.Uint32(trak[off:]))
		if size < 8 || off+size > len(trak) {
			break
		}
		if string(trak[off+4:off+8]) == "edts" {
			edts := trak[off+8 : off+size]
			eoff := 0
			for eoff+8 <= len(edts) {
				esize := int(binary.BigEndian.Uint32(edts[eoff:]))
				if esize < 8 || eoff+esize > len(edts) {
					break
				}
				if string(edts[eoff+4:eoff+8]) == "elst" {
					patchElst(edts[eoff+8 : eoff+esize])
					return
				}
				eoff += esize
			}
		}
		off += size
	}
}

// patchElst rewrites a version-0 elst that starts with an empty edit
// (media_time == -1) so that the first (and only counted) entry becomes
// {segment_duration=0, media_time=0, rate=1}, removing the presentation gap.
//
// elst payload layout (version 0):
//
//	version(1) + flags(3) + entry_count(4) + N × entry(12)
//	entry: segment_duration(4) + media_time(4) + rate_integer(2) + rate_fraction(2)
func patchElst(payload []byte) {
	if len(payload) < 8+12 {
		return // too short to contain even one entry
	}
	version := payload[0]
	if version != 0 {
		return // only handle version 0 (version 1 uses 8-byte fields)
	}
	entryCount := binary.BigEndian.Uint32(payload[4:8])
	if entryCount < 1 {
		return
	}

	// Entry 0 starts at offset 8.
	mediaTime := int32(binary.BigEndian.Uint32(payload[12:16]))
	if mediaTime != -1 {
		return // not an empty edit; nothing to fix
	}

	// Overwrite entry 0: segment_duration=0, media_time=0, rate=1, fraction=0.
	binary.BigEndian.PutUint32(payload[8:12], 0)  // segment_duration = 0 (rest of clip)
	binary.BigEndian.PutUint32(payload[12:16], 0) // media_time = 0
	binary.BigEndian.PutUint16(payload[16:18], 1) // rate_integer = 1
	binary.BigEndian.PutUint16(payload[18:20], 0) // rate_fraction = 0

	// Set entry_count = 1 so players ignore any trailing entries.
	binary.BigEndian.PutUint32(payload[4:8], 1)
}
