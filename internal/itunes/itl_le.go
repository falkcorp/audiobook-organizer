// file: internal/itunes/itl_le.go
// version: 1.5.0
// guid: b4e8d927-6c3f-4a81-9e02-f7b3c8d4e56a
// last-edited: 2026-10-07

package itunes

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
				childEnd := min(offset+totalLen, end)
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
				childEnd := min(offset+totalLen, end)
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
		for i := range 8 {
			t.PersistentID[i] = data[base+135-i]
		}
	}
	// Album persistent ID at +300 if header is big enough
	if length > 308 && safe(300, 8) {
		for i := range 8 {
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
			for i := range 8 {
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
			end := min(offset+length, len(data))
			playlist.SmartCriteria = make([]byte, end-blobStart)
			copy(playlist.SmartCriteria, data[blobStart:end])
			playlist.IsSmart = true
		}

	case 0x66:
		blobStart := offset + 40 + 8
		if blobStart < offset+length && blobStart < len(data) {
			end := min(offset+length, len(data))
			playlist.SmartInfo = make([]byte, end-blobStart)
			copy(playlist.SmartInfo, data[blobStart:end])
		}
	}
}

// ---------------------------------------------------------------------------
// Little-endian chunk walker — write path (v10+ ITL format)
// ---------------------------------------------------------------------------
