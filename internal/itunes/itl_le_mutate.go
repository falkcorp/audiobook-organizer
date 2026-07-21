// file: internal/itunes/itl_le_mutate.go
// version: 2.1.0
// guid: d5e6f7a8-b9c0-1d2e-3f4a-5b6c7d8e9f00
//
// LE-format ITL mutation: add and remove tracks from v10+ (msdh/mith/mhoh)
// iTunes libraries. Works on the decompressed payload directly.

package itunes

import (
	"crypto/rand"
	"encoding/binary"
)

// AddTracksLE inserts new tracks into the track-list msdh (blockType=1)
// of an LE-format decompressed payload. Returns the modified payload.
func AddTracksLE(data []byte, tracks []ITLNewTrack) []byte {
	if len(tracks) == 0 {
		return data
	}

	// Find the track-list msdh (blockType=1)
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		return data
	}

	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen

	// Find mlth inside — it has the track count at +8
	mlthOffset := -1
	mlthHeaderLen := 0
	maxTrackID := 0
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlth" {
		mlthOffset = contentStart
		mlthHeaderLen = int(readUint32LE(data, contentStart+4))
	}

	// Walk existing tracks to find max track ID and end of track data
	trackEndOffset := contentStart + mlthHeaderLen
	offset := trackEndOffset
	for offset+8 <= contentEnd {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		length := headerLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah") && totalLen > headerLen && totalLen <= contentEnd-offset {
			length = totalLen
		}
		if length < 8 || offset+length > contentEnd {
			break
		}
		if tag == "mith" && offset+20 <= len(data) {
			id := int(readUint32LE(data, offset+16))
			if id > maxTrackID {
				maxTrackID = id
			}
		}
		trackEndOffset = offset + length
		offset += length
	}

	// Build new mith+mhoh chunks in LE format.
	// mith totalLen must include all following mhoh sub-blocks.
	var newChunks []byte
	for i, tr := range tracks {
		trackID := maxTrackID + 1 + i

		// Build mhoh sub-blocks first so we know the total size.
		// appendMhohLE skips types absent from the corpus table (built=false)
		// rather than writing an invented encoding (CRIT-1) — all six below are
		// in the table.
		var mhohData []byte
		// Location first (0x0D + its sibling 0x0B), then metadata — matches iTunes
		// convention. A 0x0D native path MUST be accompanied by the 0x0B LocalURL
		// (the ITLSafetyContract rejects a lone 0x0D). tr.Location is the canonical
		// WinPath; derive the pair and write BOTH, mirroring the update path. If it
		// can't be normalized, skip the location entirely rather than emit a bare
		// 0x0D that the safety contract would reject.
		if tr.Location != "" {
			if pair, err := normalizeLocationValue(tr.Location); err == nil {
				mhohData = appendMhohLE(mhohData, 0x0D, pair.WinPath)
				mhohData = appendMhohLE(mhohData, 0x0B, pair.URL)
			}
		}
		if tr.Name != "" {
			mhohData = appendMhohLE(mhohData, 0x02, tr.Name)
		}
		if tr.Album != "" {
			mhohData = appendMhohLE(mhohData, 0x03, tr.Album)
		}
		if tr.Artist != "" {
			mhohData = appendMhohLE(mhohData, 0x04, tr.Artist)
		}
		if tr.Genre != "" {
			mhohData = appendMhohLE(mhohData, 0x05, tr.Genre)
		}
		if tr.Kind != "" {
			mhohData = appendMhohLE(mhohData, 0x06, tr.Kind)
		}

		mith := buildMithLE(trackID, tr)
		// Update mith totalLen to include all mhoh sub-blocks
		totalLen := 156 + len(mhohData)
		writeUint32LE(mith, 8, uint32(totalLen))

		newChunks = append(newChunks, mith...)
		newChunks = append(newChunks, mhohData...)
	}

	// Splice: data[:trackEndOffset] + newChunks + data[trackEndOffset:]
	result := make([]byte, 0, len(data)+len(newChunks))
	result = append(result, data[:trackEndOffset]...)
	result = append(result, newChunks...)
	result = append(result, data[trackEndOffset:]...)

	// Update mlth track count
	if mlthOffset >= 0 {
		oldCount := int(readUint32LE(result, mlthOffset+8))
		writeUint32LE(result, mlthOffset+8, uint32(oldCount+len(tracks)))
	}

	// Update msdh totalLen
	newMsdhTotal := msdhTotalLen + len(newChunks)
	writeUint32LE(result, msdhOffset+8, uint32(newMsdhTotal))

	return result
}

// RemoveLastNTracksLE removes the last N tracks from the track-list msdh.
// Returns the modified payload.
func RemoveLastNTracksLE(data []byte, n int) []byte {
	if n <= 0 {
		return data
	}

	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		return data
	}

	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen

	// Find mlth
	mlthOffset := -1
	mlthHeaderLen := 0
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlth" {
		mlthOffset = contentStart
		mlthHeaderLen = int(readUint32LE(data, contentStart+4))
	}

	// Walk all tracks to build a list of track start/end offsets
	type trackSpan struct{ start, end int }
	var spans []trackSpan
	offset := contentStart + mlthHeaderLen
	var currentStart int
	inTrack := false

	for offset+8 <= contentEnd {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		length := headerLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah") && totalLen > headerLen && totalLen <= contentEnd-offset {
			length = totalLen
		}
		if length < 8 || offset+length > contentEnd {
			break
		}

		if tag == "mith" {
			if inTrack {
				spans = append(spans, trackSpan{currentStart, offset})
			}
			currentStart = offset
			inTrack = true
		}
		offset += length
	}
	if inTrack {
		spans = append(spans, trackSpan{currentStart, offset})
	}

	if n > len(spans) {
		n = len(spans)
	}
	if n == 0 {
		return data
	}

	// Remove the last N tracks
	removeStart := spans[len(spans)-n].start
	removeEnd := spans[len(spans)-1].end
	removeSize := removeEnd - removeStart

	result := make([]byte, 0, len(data)-removeSize)
	result = append(result, data[:removeStart]...)
	result = append(result, data[removeEnd:]...)

	// Update mlth count
	if mlthOffset >= 0 {
		oldCount := int(readUint32LE(result, mlthOffset+8))
		newCount := oldCount - n
		if newCount < 0 {
			newCount = 0
		}
		writeUint32LE(result, mlthOffset+8, uint32(newCount))
	}

	// Update msdh totalLen
	writeUint32LE(result, msdhOffset+8, uint32(msdhTotalLen-removeSize))

	return result
}

// findMsdhByType finds the msdh container with the given blockType.
// Returns (offset, headerLen, totalLen) or (-1, 0, 0) if not found.
func findMsdhByType(data []byte, blockType int) (int, int, int) {
	offset := 0
	for offset+16 <= len(data) {
		tag := readTag(data, offset)
		if tag != "msdh" {
			break
		}
		hdrLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		bt := int(readUint32LE(data, offset+12))

		if totalLen < 16 || offset+totalLen > len(data) {
			break
		}
		if bt == blockType {
			return offset, hdrLen, totalLen
		}
		offset += totalLen
	}
	return -1, 0, 0
}

// buildMithLE builds a 156-byte LE track header (mith chunk).
func buildMithLE(trackID int, tr ITLNewTrack) []byte {
	buf := make([]byte, 156)
	copy(buf[0:4], "mith")
	writeUint32LE(buf, 4, 156) // headerLen
	writeUint32LE(buf, 8, 156) // totalLen (no sub-chunks in mith itself)
	writeUint32LE(buf, 16, uint32(trackID))
	writeUint32LE(buf, 36, uint32(tr.Size))
	writeUint32LE(buf, 40, uint32(tr.TotalTime))
	binary.LittleEndian.PutUint16(buf[44:46], uint16(tr.TrackNumber))
	if tr.Year > 0 {
		binary.LittleEndian.PutUint16(buf[54:56], uint16(tr.Year))
	}
	if tr.BitRate > 0 {
		binary.LittleEndian.PutUint16(buf[58:60], uint16(tr.BitRate))
	}
	if tr.SampleRate > 0 {
		binary.LittleEndian.PutUint16(buf[60:62], uint16(tr.SampleRate))
	}
	buf[104] = byte(tr.DiscNumber)
	// Random persistent ID (stored in reverse byte order for LE format)
	var pid [8]byte
	_, _ = rand.Read(pid[:])
	for i := 0; i < 8; i++ {
		buf[135-i] = pid[i]
	}
	return buf
}

// mhohFixedHeaderLen is the correct headerLen value for LE mhoh chunks.
// iTunes uses this fixed value to locate type-specific data within the chunk.
// Setting headerLen = totalLen (the full chunk size) corrupts the library —
// see regression test TestRewriteHohmLocationLE_PreservesHeaderLen.
const mhohFixedHeaderLen = 24

// buildMhohLE builds an LE metadata chunk (mhoh) for a given type and string,
// emitting an iTunes-conformant header via encodeMhohITunes (TASK-005, CRIT-1).
//
// The full 40-byte header is set DETERMINISTICALLY from MhohHeaderBytes: byte
// +27 is left 0x00 (iTunes never writes a non-zero +27 — K3), the +24 u32 carries
// the corpus encoding indicator, and bytes +32..+39 stay zero. This is the
// "append" writer path; rewriteHohmLocationLE is the "replace" path — both build
// the SAME header from the SAME inputs so their output is byte-identical for
// identical input.
//
// Returns (nil, false) when the type is absent from the corpus table — the
// caller must then preserve the original block unmodified and WARN, rather than
// write an invented encoding (SPEC §5 ITW-2: "never invent flags").
func buildMhohLE(mhohType uint32, value string) ([]byte, bool) {
	payload, hdr, err := encodeMhohITunes(mhohType, value)
	if err != nil {
		return nil, false
	}
	buf := make([]byte, hdr.TotalLen)
	copy(buf[0:4], "mhoh")
	writeUint32LE(buf, 4, hdr.HeaderLen) // headerLen: fixed 24 (NOT totalLen — K5)
	writeUint32LE(buf, 8, hdr.TotalLen)  // totalLen: 40 + strLen
	writeUint32LE(buf, 12, mhohType)
	writeUint32LE(buf, 24, hdr.At24) // +24: corpus encoding indicator (K3)
	// byte +27 stays 0x00 (zero-initialized) — iTunes' invariant (K3).
	writeUint32LE(buf, 28, hdr.StrLen)
	// bytes +32..+39 stay zero (reserved tail).
	copy(buf[40:], payload)
	return buf, true
}

// appendMhohLE appends an iTunes-conformant mhoh block for (mhohType, value) to
// dst and returns the result. If the type is absent from the corpus table the
// block is skipped (no invented encoding — CRIT-1) and dst is returned unchanged.
func appendMhohLE(dst []byte, mhohType uint32, value string) []byte {
	if chunk, ok := buildMhohLE(mhohType, value); ok {
		return append(dst, chunk...)
	}
	return dst
}

// writeUint32LE is defined in itl.go — reuse that.
