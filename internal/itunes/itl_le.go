// file: internal/itunes/itl_le.go
// version: 1.4.0
// guid: b4e8d927-6c3f-4a81-9e02-f7b3c8d4e56a
// last-edited: 2026-07-17

package itunes

import (
	"bytes"
	"log"
	"strings"
)

// ---------------------------------------------------------------------------
// Little-endian chunk walker — read path (v10+ ITL format)
// ---------------------------------------------------------------------------

// walkChunksLEImpl walks top-level msdh containers in LE format.
func walkChunksLEImpl(data []byte, lib *ITLLibrary) {
	offset := 0

	for offset+16 <= len(data) {
		tag := readTag(data, offset)
		if tag != "msdh" {
			// Try to skip unknown data
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		blockType := int(readUint32LE(data, offset+12))

		if totalLen < 16 || offset+totalLen > len(data) {
			break
		}

		contentStart := offset + headerLen
		contentEnd := offset + totalLen

		switch blockType {
		case 0x01:
			walkMsdhTracksLE(data, contentStart, contentEnd, lib)
		case 0x02:
			walkMsdhPlaylistsLE(data, contentStart, contentEnd, lib)
		}

		offset += totalLen
	}
}

// walkMsdhTracksLE walks mith/mhoh blocks inside a track-list msdh container.
//
// iTunes LE libraries pack mhoh string blocks as children of mith — the mith
// totalLen field encompasses both the fixed track header and its mhoh children.
// Advancing by totalLen after parsing the mith header would skip every child,
// leaving all string fields (Name, Album, Artist …) empty. The invariant is:
//   - advance outer cursor by totalLen (so chunk accounting matches the on-disk layout)
//   - but inner-walk [offset+headerLen, offset+totalLen) to reach the mhoh children
func walkMsdhTracksLE(data []byte, start, end int, lib *ITLLibrary) {
	offset := start
	var currentTrack *ITLTrack

	for offset+8 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		// headerLen is the fixed portion; totalLen covers headerLen + all children.
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))

		// For containers that carry children, outer advancement uses totalLen;
		// non-container tags (mlth, mhoh as sibling) use headerLen.
		advanceBy := headerLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah" || tag == "miph") && totalLen > headerLen && totalLen <= end-offset {
			advanceBy = totalLen
		}
		if advanceBy < 8 || offset+advanceBy > end {
			break
		}

		switch tag {
		case "mlth":
			// Track list header — no children to walk.

		case "miah":
			// Track item array — descend into it.
			walkMiahContent(data, offset, advanceBy, lib, &currentTrack)

		case "mith":
			// Parse fixed track fields from the header portion only.
			t := parseMithLE(data, offset, headerLen)
			lib.Tracks = append(lib.Tracks, t)
			currentTrack = &lib.Tracks[len(lib.Tracks)-1]
			// When mith has children (totalLen > headerLen), the mhoh string
			// blocks live in [offset+headerLen, offset+totalLen); walk them now
			// rather than relying on the outer loop where they are unreachable.
			if totalLen > headerLen {
				childEnd := offset + totalLen
				if childEnd > end {
					childEnd = end
				}
				innerOffset := offset + headerLen
				for innerOffset+8 <= childEnd {
					childTag := readTag(data, innerOffset)
					if childTag == "" {
						break
					}
					childHeaderLen := int(readUint32LE(data, innerOffset+4))
					childTotalLen := int(readUint32LE(data, innerOffset+8))
					childLen := childHeaderLen
					if childTag == "mhoh" && childTotalLen > childHeaderLen && innerOffset+childTotalLen <= childEnd {
						childLen = childTotalLen
					}
					if childLen < 8 || innerOffset+childLen > childEnd {
						break
					}
					if childTag == "mhoh" {
						parseMhohLE(data, innerOffset, childLen, currentTrack)
					}
					innerOffset += childLen
				}
			}

		case "mhoh":
			// Flat-sibling layout (used by tests and some older writers):
			// mhoh appears directly after mith with no nesting.
			if currentTrack != nil {
				mhohLen := headerLen
				if totalLen > headerLen && totalLen <= end-offset {
					mhohLen = totalLen
				}
				parseMhohLE(data, offset, mhohLen, currentTrack)
			}
		}

		offset += advanceBy
	}
}

// walkMiahContent walks the sub-blocks inside a miah (track item array) wrapper.
//
// Mirrors the mhoh-descent fix applied in walkMsdhTracksLE: when a mith block
// carries children (totalLen > headerLen), the mhoh string blocks live inside the
// mith span and are invisible to the outer loop unless we inner-walk them here.
func walkMiahContent(data []byte, miahStart, miahLen int, lib *ITLLibrary, currentTrack **ITLTrack) {
	miahHeaderLen := int(readUint32LE(data, miahStart+4))
	if miahHeaderLen < 8 {
		miahHeaderLen = 12 // fallback
	}
	offset := miahStart + miahHeaderLen
	end := miahStart + miahLen

	for offset+8 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))

		// Outer advancement uses totalLen for containers (matches on-disk layout);
		// inner-walk descends into [offset+headerLen, offset+totalLen) for children.
		advanceBy := headerLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah" || tag == "miph") && totalLen > headerLen && totalLen <= end-offset {
			advanceBy = totalLen
		}
		if advanceBy < 8 || offset+advanceBy > end {
			break
		}

		switch tag {
		case "mith":
			// Parse fixed track fields from the header portion only.
			t := parseMithLE(data, offset, headerLen)
			lib.Tracks = append(lib.Tracks, t)
			*currentTrack = &lib.Tracks[len(lib.Tracks)-1]
			// When mith has children (totalLen > headerLen), inner-walk the mhoh
			// string blocks rather than skipping them via the outer advance.
			if totalLen > headerLen {
				childEnd := offset + totalLen
				if childEnd > end {
					childEnd = end
				}
				innerOffset := offset + headerLen
				for innerOffset+8 <= childEnd {
					childTag := readTag(data, innerOffset)
					if childTag == "" {
						break
					}
					childHeaderLen := int(readUint32LE(data, innerOffset+4))
					childTotalLen := int(readUint32LE(data, innerOffset+8))
					childLen := childHeaderLen
					if childTag == "mhoh" && childTotalLen > childHeaderLen && innerOffset+childTotalLen <= childEnd {
						childLen = childTotalLen
					}
					if childLen < 8 || innerOffset+childLen > childEnd {
						break
					}
					if childTag == "mhoh" && *currentTrack != nil {
						parseMhohLE(data, innerOffset, childLen, *currentTrack)
					}
					innerOffset += childLen
				}
			}

		case "mhoh":
			// Flat-sibling layout: mhoh appears directly after mith with no nesting.
			if *currentTrack != nil {
				mhohLen := headerLen
				if totalLen > headerLen && totalLen <= end-offset {
					mhohLen = totalLen
				}
				parseMhohLE(data, offset, mhohLen, *currentTrack)
			}
		}
		offset += advanceBy
	}
}

// parseMithLE parses a little-endian track (mith) block.
func parseMithLE(data []byte, offset, length int) ITLTrack {
	t := ITLTrack{}
	if length < 24 {
		return t
	}

	base := offset
	safe := func(off, size int) bool { return base+off+size <= len(data) }

	if safe(16, 4) {
		t.TrackID = int(readUint32LE(data, base+16))
	}
	if safe(32, 4) {
		t.DateModified = macDateToTime(readUint32LE(data, base+32))
	}
	if safe(36, 4) {
		t.Size = int(readUint32LE(data, base+36))
	}
	if safe(40, 4) {
		t.TotalTime = int(readUint32LE(data, base+40))
	}
	if safe(44, 2) {
		t.TrackNumber = int(readUint16LE(data, base+44))
	}
	if safe(48, 2) {
		t.TrackCount = int(readUint16LE(data, base+48))
	}
	if safe(54, 2) {
		t.Year = int(int16(readUint16LE(data, base+54)))
	}
	if safe(58, 2) {
		t.BitRate = int(readUint16LE(data, base+58))
	}
	if safe(60, 2) {
		t.SampleRate = int(readUint16LE(data, base+60))
	}
	if safe(76, 4) {
		t.PlayCount = int(readUint32LE(data, base+76))
	}
	if safe(100, 4) {
		t.LastPlayDate = macDateToTime(readUint32LE(data, base+100))
	}
	if safe(104, 2) {
		t.DiscNumber = int(readUint16LE(data, base+104))
	}
	if safe(106, 2) {
		t.DiscCount = int(readUint16LE(data, base+106))
	}
	if safe(108, 1) {
		t.Rating = int(data[base+108])
	}
	if safe(120, 4) {
		t.DateAdded = macDateToTime(readUint32LE(data, base+120))
	}
	if safe(128, 8) {
		// LE format stores PID bytes in reverse order compared to XML hex strings.
		// Reverse them so PersistentID matches the XML format (BE / MSB first).
		for i := 0; i < 8; i++ {
			t.PersistentID[i] = data[base+135-i]
		}
	}
	// Album persistent ID at +300 if header is big enough
	if length > 308 && safe(300, 8) {
		for i := 0; i < 8; i++ {
			t.AlbumPersistentID[i] = data[base+307-i]
		}
	}

	return t
}

// parseMhohLE parses a little-endian metadata (mhoh) block for a track.
func parseMhohLE(data []byte, offset, length int, track *ITLTrack) {
	if length < 40 {
		return
	}
	hohmType := int(readUint32LE(data, offset+12))

	// Dual-convention decode (TASK-005): +27!=0 → legacy flag; +27==0 → +24 table.
	blockLen := length
	if offset+blockLen > len(data) {
		blockLen = len(data) - offset
	}
	s, err := decodeMhohBlock(data[offset : offset+blockLen])
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

// walkMsdhPlaylistsLE walks miph/mtph/mhoh blocks inside a playlist-list msdh container.
func walkMsdhPlaylistsLE(data []byte, start, end int, lib *ITLLibrary) {
	offset := start
	var currentPlaylist *ITLPlaylist

	for offset+8 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		// In LE format: mith/mhoh have headerLen at +4, totalLen at +8.
		// Use totalLen for mith/mhoh (includes sub-data), headerLen for others (mlth).
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		length := headerLen // default: use headerLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah" || tag == "miph") && totalLen > headerLen && totalLen <= end-offset {
			length = totalLen // container: use totalLen
		}
		if length < 8 || offset+length > end {
			break
		}

		switch tag {
		case "miph":
			p := parseMiphLE(data, offset, length)
			lib.Playlists = append(lib.Playlists, p)
			currentPlaylist = &lib.Playlists[len(lib.Playlists)-1]

		case "mtph":
			if currentPlaylist != nil {
				trackID := parseMtphLE(data, offset, length)
				if trackID >= 0 {
					currentPlaylist.Items = append(currentPlaylist.Items, trackID)
				}
			}

		case "mhoh":
			if currentPlaylist != nil {
				parsePlaylistMhohLE(data, offset, length, currentPlaylist)
			}
		}

		offset += length
	}
}

// parseMiphLE parses a little-endian playlist header (miph) block.
func parseMiphLE(data []byte, offset, length int) ITLPlaylist {
	p := ITLPlaylist{}
	// miph layout mirrors hpim: persistent ID at remaining[420:428]
	remaining := length - 20
	if remaining >= 428 {
		base := offset + 20
		if base+428 <= len(data) {
			// Reverse byte order for LE → BE PID matching
			for i := 0; i < 8; i++ {
				p.PersistentID[i] = data[base+427-i]
			}
		}
	}
	return p
}

// parseMtphLE parses a little-endian playlist track reference (mtph) block.
func parseMtphLE(data []byte, offset, length int) int {
	// mtph layout mirrors hptm: track ID at +24
	if length < 28 || offset+28 > len(data) {
		return -1
	}
	return int(readUint32LE(data, offset+24))
}

// parsePlaylistMhohLE parses a little-endian metadata (mhoh) block in playlist context.
func parsePlaylistMhohLE(data []byte, offset, length int, playlist *ITLPlaylist) {
	if length < 16 {
		return
	}
	hohmType := int(readUint32LE(data, offset+12))

	switch hohmType {
	case 0x64:
		if length < 40 {
			return
		}
		// Dual-convention decode (TASK-005).
		blockLen := length
		if offset+blockLen > len(data) {
			blockLen = len(data) - offset
		}
		s, err := decodeMhohBlock(data[offset : offset+blockLen])
		if err != nil {
			return
		}
		playlist.Title = s

	case 0x65:
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
// Little-endian chunk walker — write path (v10+ ITL format)
// ---------------------------------------------------------------------------

// rewriteChunksLEImpl walks msdh containers and rewrites location mhoh blocks.
// matched, when non-nil, receives the lowercased persistent ID of every track
// whose location block was actually rewritten (DL-5 per-row accounting).
func rewriteChunksLEImpl(data []byte, updateMap map[string]string, matched map[string]bool) ([]byte, int) {
	var out bytes.Buffer
	offset := 0
	updatedCount := 0

	msdhCount := 0
	for offset+16 <= len(data) {
		tag := readTag(data, offset)
		if tag != "msdh" {
			out.Write(data[offset:])
			break
		}

		totalLen := int(readUint32LE(data, offset+8))
		blockType := int(readUint32LE(data, offset+12))
		msdhCount++

		if totalLen < 16 || offset+totalLen > len(data) {
			out.Write(data[offset:])
			break
		}

		if blockType == 0x01 {
			// Track-list container: rewrite sub-chunks
			msdh := data[offset : offset+totalLen]
			var currentPID string
			rewritten, cnt := rewriteMsdhContentLE(msdh, updateMap, &currentPID, matched)
			out.Write(rewritten)
			updatedCount += cnt
		} else {
			// Non-track containers: copy as-is
			out.Write(data[offset : offset+totalLen])
		}

		offset += totalLen
	}

	return out.Bytes(), updatedCount
}

// recordMatchedPID marks pid (already lowercased) as actually rewritten in
// matched, when the caller asked for per-PID accounting (DL-5).
func recordMatchedPID(matched map[string]bool, pid string) {
	if matched != nil && pid != "" {
		matched[pid] = true
	}
}

// rewriteMsdhContentLE rewrites mith/mhoh content inside an msdh container.
func rewriteMsdhContentLE(msdh []byte, updateMap map[string]string, currentPID *string, matched map[string]bool) ([]byte, int) {
	if len(msdh) < 16 {
		return msdh, 0
	}

	headerLen := int(readUint32LE(msdh, 4))
	if headerLen < 16 || headerLen > len(msdh) {
		return msdh, 0
	}

	var out bytes.Buffer
	out.Write(msdh[:headerLen]) // Write msdh header

	updatedCount := 0
	subOffset := headerLen
	contentEnd := len(msdh)

	trackCount := 0
	tagCounts := make(map[string]int)
	for subOffset+8 <= contentEnd {
		tag := readTag(msdh, subOffset)
		if tag == "" {
			break
		}
		chunkHeaderLen := int(readUint32LE(msdh, subOffset+4))
		chunkTotalLen := int(readUint32LE(msdh, subOffset+8))
		chunkLen := chunkHeaderLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah" || tag == "miph") && chunkTotalLen > chunkHeaderLen && subOffset+chunkTotalLen <= contentEnd {
			chunkLen = chunkTotalLen
		}
		if chunkLen < 8 || subOffset+chunkLen > contentEnd {
			break
		}
		tagCounts[tag]++

		switch tag {
		case "mlth":
			out.Write(msdh[subOffset : subOffset+chunkLen])

		case "miah":
			// Track item array wrapper — contains mith + mhoh sub-blocks
			// We need to descend into it and rewrite its content
			miahData := msdh[subOffset : subOffset+chunkLen]
			rewritten, cnt := rewriteMiahContentLE(miahData, updateMap, currentPID, matched)
			out.Write(rewritten)
			updatedCount += cnt
			trackCount++

		case "mith":
			// mith is a container: headerLen = fixed track fields, totalLen includes mhoh sub-blocks
			trackCount++
			if subOffset+136 <= len(msdh) {
				pid := pidToHexLE([8]byte(msdh[subOffset+128 : subOffset+136]))
				*currentPID = strings.ToLower(pid)
			}
			// Walk mhoh sub-blocks inside this mith
			mithData := msdh[subOffset : subOffset+chunkLen]
			rewritten, cnt := rewriteMithContentLE(mithData, updateMap, *currentPID, matched)
			out.Write(rewritten)
			updatedCount += cnt

		case "mhoh":
			if newLoc, ok := shouldUpdateMhohLE(msdh, subOffset, chunkLen, *currentPID, updateMap); ok {
				rewritten := rewriteHohmLocationLE(msdh, subOffset, chunkLen, newLoc)
				out.Write(rewritten)
				updatedCount++
				recordMatchedPID(matched, *currentPID)
			} else {
				out.Write(msdh[subOffset : subOffset+chunkLen])
			}

		default:
			out.Write(msdh[subOffset : subOffset+chunkLen])
		}
		subOffset += chunkLen
	}

	// Write any trailing bytes
	if subOffset < len(msdh) {
		out.Write(msdh[subOffset:])
	}

	result := out.Bytes()

	// Update msdh totalLen field (offset 8)
	newLen := uint32(len(result))
	writeUint32LE(result, 8, newLen)

	return result, updatedCount
}

// rewriteMiahContentLE walks mith + mhoh blocks inside a miah (track item array) wrapper.
// miah layout: tag(4) + headerLen(4) + totalLen(4) + ... then sub-blocks
func rewriteMiahContentLE(miah []byte, updateMap map[string]string, currentPID *string, matched map[string]bool) ([]byte, int) {
	if len(miah) < 12 {
		return miah, 0
	}
	miahHeaderLen := int(readUint32LE(miah, 4))
	if miahHeaderLen < 8 || miahHeaderLen > len(miah) {
		return miah, 0
	}

	var out bytes.Buffer
	out.Write(miah[:miahHeaderLen]) // preserve miah header

	updatedCount := 0
	subOffset := miahHeaderLen

	for subOffset+8 <= len(miah) {
		tag := readTag(miah, subOffset)
		if tag == "" {
			break
		}
		chunkHeaderLen := int(readUint32LE(miah, subOffset+4))
		chunkTotalLen := int(readUint32LE(miah, subOffset+8))
		chunkLen := chunkHeaderLen
		if (tag == "mith" || tag == "mhoh") && chunkTotalLen > chunkHeaderLen && subOffset+chunkTotalLen <= len(miah) {
			chunkLen = chunkTotalLen
		}
		if chunkLen < 8 || subOffset+chunkLen > len(miah) {
			break
		}

		switch tag {
		case "mith":
			if subOffset+136 <= len(miah) {
				pid := pidToHexLE([8]byte(miah[subOffset+128 : subOffset+136]))
				*currentPID = strings.ToLower(pid)
			}
			out.Write(miah[subOffset : subOffset+chunkLen])

		case "mhoh":
			if newLoc, ok := shouldUpdateMhohLE(miah, subOffset, chunkLen, *currentPID, updateMap); ok {
				rewritten := rewriteHohmLocationLE(miah, subOffset, chunkLen, newLoc)
				out.Write(rewritten)
				updatedCount++
				recordMatchedPID(matched, *currentPID)
			} else {
				out.Write(miah[subOffset : subOffset+chunkLen])
			}

		default:
			out.Write(miah[subOffset : subOffset+chunkLen])
		}
		subOffset += chunkLen
	}

	// Write trailing bytes
	if subOffset < len(miah) {
		out.Write(miah[subOffset:])
	}

	result := out.Bytes()
	// Update miah length fields
	if len(result) >= 12 {
		writeUint32LE(result, 4, uint32(len(result)))
		writeUint32LE(result, 8, uint32(len(result)))
	}

	return result, updatedCount
}

// rewriteMithContentLE walks mhoh sub-blocks inside a mith container and rewrites locations.
func rewriteMithContentLE(mith []byte, updateMap map[string]string, currentPID string, matched map[string]bool) ([]byte, int) {
	if len(mith) < 12 {
		return mith, 0
	}
	mithHeaderLen := int(readUint32LE(mith, 4)) // fixed track header portion
	if mithHeaderLen < 8 || mithHeaderLen >= len(mith) {
		return mith, 0
	}

	var out bytes.Buffer
	out.Write(mith[:mithHeaderLen]) // copy the fixed track header

	updatedCount := 0
	subOffset := mithHeaderLen

	for subOffset+8 <= len(mith) {
		tag := readTag(mith, subOffset)
		if tag == "" {
			break
		}
		mhohHeaderLen := int(readUint32LE(mith, subOffset+4))
		mhohTotalLen := int(readUint32LE(mith, subOffset+8))
		chunkLen := mhohHeaderLen
		if tag == "mhoh" && mhohTotalLen > mhohHeaderLen && subOffset+mhohTotalLen <= len(mith) {
			chunkLen = mhohTotalLen
		}
		if chunkLen < 8 || subOffset+chunkLen > len(mith) {
			break
		}

		if tag == "mhoh" {
			if newLoc, ok := shouldUpdateMhohLE(mith, subOffset, chunkLen, currentPID, updateMap); ok {
				rewritten := rewriteHohmLocationLE(mith, subOffset, chunkLen, newLoc)
				out.Write(rewritten)
				updatedCount++
				recordMatchedPID(matched, currentPID)
			} else {
				out.Write(mith[subOffset : subOffset+chunkLen])
			}
		} else {
			out.Write(mith[subOffset : subOffset+chunkLen])
		}
		subOffset += chunkLen
	}

	// Trailing bytes
	if subOffset < len(mith) {
		out.Write(mith[subOffset:])
	}

	result := out.Bytes()
	// Update mith totalLen (offset 8)
	if len(result) >= 12 {
		writeUint32LE(result, 8, uint32(len(result)))
	}

	return result, updatedCount
}

// shouldUpdateMhohLE checks if a mhoh block should be updated with a new location.
//
// The updateMap value is the CANONICAL Windows path (the single source of truth —
// SPEC §1b / TASK-006). This function derives the two renderings from ONE
// LocationPair: hohm 0x0D gets the plain WinPath, hohm 0x0B gets the
// file://localhost/ percent-escaped URL. No caller ever passes a raw string to
// either field directly, which is what makes the CRIT-2 "URL-in-0x0D" bug
// unrepresentable.
//
// WHY this replaced the old inline "file://localhost/"+TrimPrefix hack: that code
// (a) wrote whatever caller value verbatim into 0x0D (the CRIT-2 corruption when
// the value was URL-shaped), and (b) produced a 0x0B URL with NO percent-escaping,
// so it never round-tripped the T003 location-form guard.
//
// An unmappable value (relative path, staging-dir leak, podcast URL — none of
// which has a valid 0x0D Windows path) is SKIPPED with a WARN: the block is left
// unmodified rather than written with a corrupt value.
func shouldUpdateMhohLE(data []byte, offset, length int, currentPID string, updateMap map[string]string) (string, bool) {
	if length < 40 {
		return "", false
	}
	hohmType := int(readUint32LE(data, offset+12))
	if hohmType != 0x0D && hohmType != 0x0B {
		return "", false
	}
	if currentPID == "" {
		return "", false
	}
	raw, ok := updateMap[currentPID]
	if !ok {
		return "", false
	}

	pair, err := normalizeLocationValue(raw)
	if err != nil {
		// Unmappable location: never write a raw/corrupt value into 0x0D or 0x0B.
		// Skip this block (the guard would reject it anyway) and WARN so the skip
		// is visible in logs/metrics.
		log.Printf("[itl] WARN shouldUpdateMhohLE: PID %s location %q unmappable, skipping update: %v", currentPID, raw, err)
		return "", false
	}

	if hohmType == 0x0B {
		return pair.URL, true
	}
	return pair.WinPath, true
}

// rewriteHohmLocationLE rewrites a location/metadata mhoh block with a new
// string, emitting an iTunes-conformant header via encodeMhohITunes (TASK-005,
// CRIT-1). This is the "replace" writer path; buildMhohLE is the "append" path —
// both build the SAME 40-byte header from the SAME inputs, so for identical
// (hohmType, value) inputs their output is byte-identical.
//
// WHY this stops blind-copying +16..+27: the OLD code copied the original
// block's bytes +16..+27 (including the +24 indicator) and only overwrote +27
// with an invented "encoding flag" (1/3) — propagating whatever stale/foreign
// header the source carried and stamping the CRIT-1 corruption byte. The header
// is now set DETERMINISTICALLY: +24 = corpus indicator, +27 = 0x00, +32..+39 = 0.
//
// If the block's hohmType is absent from the corpus table, the original block is
// returned UNMODIFIED (caller-visible WARN is emitted by shouldUpdateMhohLE
// callers; the function never invents an encoding for an unknown type).
func rewriteHohmLocationLE(data []byte, offset, length int, newLocation string) []byte {
	hohmType := readUint32LE(data, offset+12)

	payload, hdr, err := encodeMhohITunes(hohmType, newLocation)
	if err != nil {
		// Out-of-corpus type: preserve the original block byte-for-byte rather
		// than write an invented header (CRIT-1). WARN so the skip is visible.
		log.Printf("[itl] WARN rewriteHohmLocationLE: hohmType 0x%X absent from corpus table; preserving original block unmodified: %v", hohmType, err)
		out := make([]byte, length)
		copy(out, data[offset:offset+length])
		return out
	}

	buf := make([]byte, hdr.TotalLen)
	copy(buf[0:4], data[offset:offset+4]) // tag ("mhoh")
	writeUint32LE(buf, 4, hdr.HeaderLen)  // headerLen: fixed 24 (NOT totalLen — K5)
	writeUint32LE(buf, 8, hdr.TotalLen)   // totalLen: 40 + strLen
	writeUint32LE(buf, 12, hohmType)      // hohmType
	writeUint32LE(buf, 24, hdr.At24)      // +24: corpus encoding indicator (K3)
	// byte +27 stays 0x00 (zero-initialized) — iTunes' invariant (K3).
	writeUint32LE(buf, 28, hdr.StrLen) // strLen
	// bytes +32..+39 stay zero (reserved tail).
	copy(buf[40:], payload)

	return buf
}
