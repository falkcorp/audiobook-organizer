// file: internal/itunes/itl_be.go
// version: 1.1.1
// guid: a3f7c821-5b4e-4d92-8f01-e6a2b9c3d47f
// last-edited: 2026-07-12

package itunes

import (
	"bytes"
	"strings"
)

// ---------------------------------------------------------------------------
// Big-endian chunk walker — read path
// ---------------------------------------------------------------------------

func walkChunksBE(data []byte, lib *ITLLibrary) {
	offset := 0
	var currentTrack *ITLTrack
	var currentPlaylist *ITLPlaylist

	for offset+8 <= len(data) {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		length := int(readUint32BE(data, offset+4))
		if length < 8 || offset+length > len(data) {
			break
		}

		switch tag {
		case "hdsm":
			// hdsm: extended length at offset+8 per PR #36
			extLen := int(readUint32BE(data, offset+8))
			// The hdsm contains sub-chunks; we process them inside
			// For parsing, we walk into hdsm's sub-content
			subStart := offset + 12
			if extLen > length && offset+extLen <= len(data) {
				// Extra data between length and extLen
				walkHdsmContentBE(data, subStart, offset+extLen, lib, &currentTrack, &currentPlaylist)
				offset += extLen
			} else {
				walkHdsmContentBE(data, subStart, offset+length, lib, &currentTrack, &currentPlaylist)
				offset += length
			}
			continue

		case "htim":
			// htim: track record
			currentPlaylist = nil
			t := parseHtimBE(data, offset, length)
			lib.Tracks = append(lib.Tracks, t)
			currentTrack = &lib.Tracks[len(lib.Tracks)-1]

		case "hpim":
			// hpim: playlist record
			currentTrack = nil
			p := parseHpimBE(data, offset, length)
			lib.Playlists = append(lib.Playlists, p)
			currentPlaylist = &lib.Playlists[len(lib.Playlists)-1]

		case "hptm":
			// hptm: playlist item
			if currentPlaylist != nil {
				trackID := parseHptmBE(data, offset, length)
				if trackID >= 0 {
					currentPlaylist.Items = append(currentPlaylist.Items, trackID)
				}
			}
			// TODO: extract checked state from hptm

		case "hohm":
			if currentTrack != nil {
				parseHohmBE(data, offset, length, currentTrack)
			} else if currentPlaylist != nil {
				parsePlaylistHohmBE(data, offset, length, currentPlaylist)
			}
		}

		offset += length
	}
}

func walkHdsmContentBE(data []byte, start, end int, lib *ITLLibrary, currentTrack **ITLTrack, currentPlaylist **ITLPlaylist) {
	offset := start
	for offset+8 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		length := int(readUint32BE(data, offset+4))
		if length < 8 || offset+length > end {
			break
		}

		switch tag {
		case "htim":
			*currentPlaylist = nil
			t := parseHtimBE(data, offset, length)
			lib.Tracks = append(lib.Tracks, t)
			*currentTrack = &lib.Tracks[len(lib.Tracks)-1]

		case "hpim":
			*currentTrack = nil
			p := parseHpimBE(data, offset, length)
			lib.Playlists = append(lib.Playlists, p)
			*currentPlaylist = &lib.Playlists[len(lib.Playlists)-1]

		case "hptm":
			if *currentPlaylist != nil {
				trackID := parseHptmBE(data, offset, length)
				if trackID >= 0 {
					(*currentPlaylist).Items = append((*currentPlaylist).Items, trackID)
				}
			}

		case "hohm":
			if *currentTrack != nil {
				parseHohmBE(data, offset, length, *currentTrack)
			} else if *currentPlaylist != nil {
				parsePlaylistHohmBE(data, offset, length, *currentPlaylist)
			}
		}
		offset += length
	}
}

func parseHtimBE(data []byte, offset, length int) ITLTrack {
	t := ITLTrack{}
	// htim layout from Java titl ParseLibrary.readHtim():
	// +0:  "htim" tag (4)
	// +4:  length (4) — header length
	// +8:  recordLength (4) — total record length including sub-blocks
	// +12: subblocks count (4)
	// +16: song ID (4)
	// +20: block type (4)
	// +24: unknown (4)
	// +28: Mac OS file type (4)
	// +32: modification date (4)
	// +36: file size (4)
	// +40: playtime ms (4)
	// +44: track number (4) — PR #36 reads as int, not short
	// +48: track count (4)
	// +52: unknown (2)
	// +54: year (2)
	// +56: unknown (2)
	// +58: bit rate (2)
	// +60: sample rate (2)
	// +62: unknown (2)
	// +64: volume adjust (4)
	// +68: start time (4)
	// +72: end time (4)
	// +76: play count (4)
	// +80: unknown (2)
	// +82: compilation (2)
	// +84: unknown (12)
	// +96: play count again (4)
	// +100: last play date (4)
	// +104: disc number (1) + pad(1) + disc count(1) + pad(1) — PR #36
	// +108: rating (1)
	// +109: unknown (11)
	// +120: add date (4)
	// +124: unknown (4)
	// +128: persistent ID (8)
	// +136: unknown (20)
	// ... optionally album persistent ID at +300 (length > 156+144+8)
	if length < 24 {
		return t
	}

	base := offset
	safe := func(off, size int) bool { return base+off+size <= len(data) }

	if safe(16, 4) {
		t.TrackID = int(readUint32BE(data, base+16))
	}
	if safe(32, 4) {
		t.DateModified = macDateToTime(readUint32BE(data, base+32))
	}
	if safe(36, 4) {
		t.Size = int(readUint32BE(data, base+36))
	}
	if safe(40, 4) {
		t.TotalTime = int(readUint32BE(data, base+40))
	}
	if safe(44, 4) {
		t.TrackNumber = int(readUint32BE(data, base+44))
	}
	if safe(48, 4) {
		t.TrackCount = int(readUint32BE(data, base+48))
	}
	if safe(54, 2) {
		t.Year = int(int16(readUint16BE(data, base+54)))
	}
	if safe(58, 2) {
		t.BitRate = int(readUint16BE(data, base+58))
	}
	if safe(60, 2) {
		t.SampleRate = int(readUint16BE(data, base+60))
	}
	if safe(76, 4) {
		t.PlayCount = int(readUint32BE(data, base+76))
	}
	if safe(100, 4) {
		t.LastPlayDate = macDateToTime(readUint32BE(data, base+100))
	}
	if safe(104, 1) {
		t.DiscNumber = int(data[base+104])
	}
	if safe(106, 1) {
		t.DiscCount = int(data[base+106])
	}
	if safe(108, 1) {
		t.Rating = int(data[base+108])
	}
	if safe(120, 4) {
		t.DateAdded = macDateToTime(readUint32BE(data, base+120))
	}
	if safe(128, 8) {
		copy(t.PersistentID[:], data[base+128:base+136])
	}
	// Album persistent ID: at +300 if header is big enough (length > 308)
	if length > 308 && safe(300, 8) {
		copy(t.AlbumPersistentID[:], data[base+300:base+308])
	}

	return t
}

func parseHohmBE(data []byte, offset, length int, track *ITLTrack) {
	// hohm layout:
	// +0: tag (4), +4: length (4), +8: recLength (4), +12: hohmType (4)
	// +16: 12-byte header (byte 11 = encoding flag)
	// +28: 4-byte string data length
	// +32: 8-byte zeros
	// +40: string data
	if length < 40 {
		return
	}
	hohmType := int(readUint32BE(data, offset+12))
	encodingFlag := data[offset+16+11] // byte 11 of the 12-byte header

	strDataLen := int(readUint32BE(data, offset+28))
	strStart := offset + 40
	if strStart+strDataLen > offset+length || strStart+strDataLen > len(data) {
		// Clamp to available
		strDataLen = offset + length - strStart
		if strDataLen < 0 {
			return
		}
	}

	s, err := decodeHohmString(data[strStart:strStart+strDataLen], encodingFlag)
	if err != nil {
		return
	}

	switch hohmType {
	case 0x02:
		track.Name = s
	case 0x03:
		track.Album = s
	case 0x04:
		track.Artist = s
	case 0x05:
		track.Genre = s
	case 0x06:
		track.Kind = s
	case 0x0B:
		track.LocalURL = s
	case 0x0D:
		track.Location = s
	}
}

// parseHpimBE parses a playlist header (hpim) chunk.
func parseHpimBE(data []byte, offset, length int) ITLPlaylist {
	p := ITLPlaylist{}
	// hpim layout:
	// +0: "hpim" (4), +4: length (4), +8: recordLength (4), +12: subblocks (4)
	// +16: item count (4)
	// Remaining starts at offset+20, persistent ID at remaining[420:428]
	remaining := length - 20
	if remaining >= 428 {
		base := offset + 20
		copy(p.PersistentID[:], data[base+420:base+428])
	}
	return p
}

// parseHptmBE parses a playlist item (hptm) chunk and returns the track ID.
func parseHptmBE(data []byte, offset, length int) int {
	// hptm layout:
	// +0: "hptm" (4), +4: length (4)
	// +8: 16 unknown bytes
	// +24: track key/song ID (4)
	if length < 28 || offset+28 > len(data) {
		return -1
	}
	return int(readUint32BE(data, offset+24))
	// TODO: extract checked state from hptm
}

// parsePlaylistHohmBE parses a hohm chunk in playlist context.
func parsePlaylistHohmBE(data []byte, offset, length int, playlist *ITLPlaylist) {
	if length < 16 {
		return
	}
	hohmType := int(readUint32BE(data, offset+12))

	switch hohmType {
	case 0x64:
		// Playlist title — same string format as track hohm
		if length < 40 {
			return
		}
		encodingFlag := data[offset+16+11]
		strDataLen := int(readUint32BE(data, offset+28))
		strStart := offset + 40
		if strStart+strDataLen > offset+length || strStart+strDataLen > len(data) {
			strDataLen = offset + length - strStart
			if strDataLen < 0 {
				return
			}
		}
		s, err := decodeHohmString(data[strStart:strStart+strDataLen], encodingFlag)
		if err != nil {
			return
		}
		playlist.Title = s

	case 0x65:
		// Smart criteria: 8 zero bytes + raw blob
		blobStart := offset + 40 + 8
		if blobStart < offset+length && blobStart < len(data) {
			end := offset + length
			if end > len(data) {
				end = len(data)
			}
			playlist.SmartCriteria = make([]byte, end-blobStart)
			copy(playlist.SmartCriteria, data[blobStart:end])
			playlist.IsSmart = true
		}

	case 0x66:
		// Smart info: 8 zero bytes + raw blob
		blobStart := offset + 40 + 8
		if blobStart < offset+length && blobStart < len(data) {
			end := offset + length
			if end > len(data) {
				end = len(data)
			}
			playlist.SmartInfo = make([]byte, end-blobStart)
			copy(playlist.SmartInfo, data[blobStart:end])
		}
	}
}

// ---------------------------------------------------------------------------
// Big-endian chunk walker — write path
// ---------------------------------------------------------------------------

// rewriteChunksBE walks through decompressed ITL data chunk by chunk,
// replacing location strings (hohm type 0x0D) for matching persistent IDs.
// Returns the new data buffer and count of updates made.
//
//lint:ignore U1000 kept: big-endian ITL write-back, parallel to the wired rewriteChunksLE; not yet hooked into the write path (BE write-back pending, 2026-07-12)
func rewriteChunksBE(data []byte, updateMap map[string]string) ([]byte, int) {
	var out bytes.Buffer
	offset := 0
	updatedCount := 0
	var currentPID string

	for offset+8 <= len(data) {
		tag := readTag(data, offset)
		if tag == "" {
			// Write remaining bytes
			out.Write(data[offset:])
			break
		}
		length := int(readUint32BE(data, offset+4))
		if length < 8 || offset+length > len(data) {
			out.Write(data[offset:])
			break
		}

		switch tag {
		case "hdsm":
			// Per PR #36: extendedLength at offset+8
			extLen := int(readUint32BE(data, offset+8))
			actualLen := length
			if extLen > length && offset+extLen <= len(data) {
				actualLen = extLen
			}
			// Write the hdsm chunk through, but process sub-chunks inside
			// For simplicity, recursively rewrite hdsm content
			hdsm := data[offset : offset+actualLen]
			rewritten, cnt := rewriteHdsmContentBE(hdsm, updateMap, &currentPID)
			out.Write(rewritten)
			updatedCount += cnt
			offset += actualLen

		case "htim":
			// Extract persistent ID from htim
			if offset+136 <= len(data) {
				pid := pidToHex([8]byte(data[offset+128 : offset+136]))
				currentPID = strings.ToLower(pid)
			}
			out.Write(data[offset : offset+length])
			offset += length

		case "hohm":
			if newLoc, ok := shouldUpdateHohmBE(data, offset, length, currentPID, updateMap); ok {
				rewritten := rewriteHohmLocationBE(data, offset, length, newLoc)
				out.Write(rewritten)
				updatedCount++
			} else {
				out.Write(data[offset : offset+length])
			}
			offset += length

		default:
			out.Write(data[offset : offset+length])
			offset += length
		}
	}

	return out.Bytes(), updatedCount
}

func rewriteHdsmContentBE(hdsm []byte, updateMap map[string]string, currentPID *string) ([]byte, int) {
	if len(hdsm) < 12 {
		return hdsm, 0
	}

	// hdsm header: tag(4) + length(4) + extLen(4) = 12 bytes minimum
	basicLen := int(readUint32BE(hdsm, 4))
	extLen := int(readUint32BE(hdsm, 8))

	// The hdsm header is 12 bytes, sub-chunks start at offset 12
	var out bytes.Buffer
	out.Write(hdsm[:12]) // Write hdsm header

	updatedCount := 0
	subOffset := 12

	// Determine where sub-content ends
	contentEnd := basicLen
	if extLen > basicLen && extLen <= len(hdsm) {
		contentEnd = extLen
	}
	if contentEnd > len(hdsm) {
		contentEnd = len(hdsm)
	}

	for subOffset+8 <= contentEnd {
		tag := readTag(hdsm, subOffset)
		if tag == "" {
			break
		}
		chunkLen := int(readUint32BE(hdsm, subOffset+4))
		if chunkLen < 8 || subOffset+chunkLen > contentEnd {
			break
		}

		switch tag {
		case "htim":
			// PID lives at offset 128–135 within htim (matches primary rewriteChunksBE).
			// Previous code read offset 100–107 (copy-paste error from an older layout).
			if subOffset+136 <= len(hdsm) {
				pid := pidToHex([8]byte(hdsm[subOffset+128 : subOffset+136]))
				*currentPID = strings.ToLower(pid)
			}
			out.Write(hdsm[subOffset : subOffset+chunkLen])

		case "hohm":
			if newLoc, ok := shouldUpdateHohmBE(hdsm, subOffset, chunkLen, *currentPID, updateMap); ok {
				rewritten := rewriteHohmLocationBE(hdsm, subOffset, chunkLen, newLoc)
				out.Write(rewritten)
				updatedCount++
			} else {
				out.Write(hdsm[subOffset : subOffset+chunkLen])
			}

		default:
			out.Write(hdsm[subOffset : subOffset+chunkLen])
		}
		subOffset += chunkLen
	}

	// Write any trailing bytes
	if subOffset < len(hdsm) {
		out.Write(hdsm[subOffset:])
	}

	result := out.Bytes()

	// Update hdsm length fields
	newLen := uint32(len(result))
	writeUint32BE(result, 4, newLen)
	writeUint32BE(result, 8, newLen)

	return result, updatedCount
}

func shouldUpdateHohmBE(data []byte, offset, length int, currentPID string, updateMap map[string]string) (string, bool) {
	if length < 40 {
		return "", false
	}
	hohmType := int(readUint32BE(data, offset+12))
	// 0x0D = file location, 0x0B = local URL (used by audiobooks/podcasts per titl issue #25)
	if hohmType != 0x0D && hohmType != 0x0B {
		return "", false
	}
	if currentPID == "" {
		return "", false
	}
	newLoc, ok := updateMap[currentPID]
	if ok && hohmType == 0x0B {
		// For URL-style locations, encode as file:// URL
		if !strings.HasPrefix(newLoc, "file://") {
			newLoc = "file://localhost/" + strings.TrimPrefix(newLoc, "/")
		}
	}
	return newLoc, ok
}

func rewriteHohmLocationBE(data []byte, offset, length int, newLocation string) []byte {
	// Encode new string
	encodedStr, encodingFlag := encodeHohmString(newLocation)

	// Build new hohm chunk
	// Header: tag(4) + length(4) + recLength(4) + hohmType(4) + 12-byte header + 4-byte strLen + 8-byte zeros + string data
	newStrDataLen := len(encodedStr)
	newChunkLen := 40 + newStrDataLen

	buf := make([]byte, newChunkLen)
	// Copy tag
	copy(buf[0:4], data[offset:offset+4])
	// New length
	writeUint32BE(buf, 4, uint32(newChunkLen))
	// New recLength (same as length for hohm)
	writeUint32BE(buf, 8, uint32(newChunkLen))
	// hohmType
	copy(buf[12:16], data[offset+12:offset+16])
	// Copy the 12-byte header, update encoding flag
	if offset+28 <= len(data) {
		copy(buf[16:28], data[offset+16:offset+28])
	}
	buf[16+11] = encodingFlag
	// String data length
	writeUint32BE(buf, 28, uint32(newStrDataLen))
	// 8 bytes zeros at 32-39 (already zero)
	// String data
	copy(buf[40:], encodedStr)

	return buf
}
