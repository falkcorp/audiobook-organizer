// file: internal/itunes/itl_le_verify.go
// version: 1.0.0
// guid: 8d9e0f1a-2b3c-4d5e-6f7a-8b9c0d1e2f3a
//
// Consistency verification for LE-format ITL payloads. Detects dangling
// references that would cause iTunes to refuse the library as "damaged":
//
//   * `mtph` (playlist track items) referencing TrackIDs that don't exist
//     in the master track list (msdh blockType 1).
//
// This guards against the May-2026 corruption class where RemoveTracksByPIDLE
// excised mith blocks but left orphaned references in playlists, causing
// iTunes to mark the library file as damaged on next open.

package itunes

import (
	"fmt"
)

// VerifyITLNoNewDanglingRefsLE checks that `after` does not introduce any new
// dangling playlist→track references that were not already present in
// `before`. iTunes tolerates a small number of pre-existing orphan mtph items,
// but introducing new ones causes it to mark the library as damaged on next
// open.
//
// Returns nil if `after` is at least as clean as `before`. Returns a non-nil
// error naming the newly-introduced dangling TIDs otherwise. If either
// payload isn't recognizably LE, the check is skipped (returns nil).
func VerifyITLNoNewDanglingRefsLE(before, after []byte) error {
	if !detectLE(after) {
		return nil
	}
	afterTIDs := CollectMasterTrackIDsLE(after)
	if afterTIDs == nil {
		return nil
	}
	afterDangling := FindDanglingMtphRefsLE(after, afterTIDs)
	if len(afterDangling) == 0 {
		return nil
	}

	// Build the baseline orphan set so we can ignore pre-existing ones.
	preExisting := map[uint32]struct{}{}
	if before != nil && detectLE(before) {
		if beforeTIDs := CollectMasterTrackIDsLE(before); beforeTIDs != nil {
			for _, tid := range FindDanglingMtphRefsLE(before, beforeTIDs) {
				preExisting[tid] = struct{}{}
			}
		}
	}

	var introduced []uint32
	for _, tid := range afterDangling {
		if _, ok := preExisting[tid]; !ok {
			introduced = append(introduced, tid)
		}
	}
	if len(introduced) == 0 {
		return nil
	}

	const sample = 5
	preview := introduced
	if len(preview) > sample {
		preview = preview[:sample]
	}
	return fmt.Errorf("itl consistency check failed: write would introduce %d new dangling playlist track refs (e.g. TrackIDs %v); refusing to write to avoid corrupting iTunes library", len(introduced), preview)
}

// VerifyITLNoDanglingRefsLE checks that `data` has no dangling playlist→track
// references at all. Prefer VerifyITLNoNewDanglingRefsLE when validating a
// write, since iTunes tolerates a small number of pre-existing orphans.
func VerifyITLNoDanglingRefsLE(data []byte) error {
	if !detectLE(data) {
		return nil
	}

	tids := CollectMasterTrackIDsLE(data)
	if tids == nil {
		// Couldn't locate master track list — don't fail-closed on parse
		// surprises, but log the situation by returning nil. Callers can
		// still detect catastrophic corruption via track count assertions.
		return nil
	}

	missing := FindDanglingMtphRefsLE(data, tids)
	if len(missing) == 0 {
		return nil
	}

	const sample = 5
	preview := missing
	if len(preview) > sample {
		preview = preview[:sample]
	}
	return fmt.Errorf("itl consistency check failed: %d playlist track refs point at non-existent tracks (e.g. TrackIDs %v); refusing to write to avoid corrupting iTunes library", len(missing), preview)
}

// CollectMasterTrackIDsLE walks the master-track-list msdh (blockType 1) and
// returns the set of TrackIDs present. Returns nil if the master list cannot
// be located.
func CollectMasterTrackIDsLE(data []byte) map[uint32]struct{} {
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		return nil
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := min(msdhOffset+msdhTotalLen, len(data))

	tids := make(map[uint32]struct{}, 100000)
	offset := contentStart
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlth" {
		mlthHeaderLen := int(readUint32LE(data, contentStart+4))
		offset = contentStart + mlthHeaderLen
	}

	for offset+12 <= contentEnd {
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
			tid := readUint32LE(data, offset+16)
			tids[tid] = struct{}{}
		}
		offset += length
	}
	return tids
}

// FindDanglingMtphRefsLE walks the playlist-list msdh (blockType 2) and
// returns TrackIDs referenced by `mtph` items that are not in the master
// track set. Returns an empty slice if everything is consistent.
func FindDanglingMtphRefsLE(data []byte, masterTIDs map[uint32]struct{}) []uint32 {
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 2)
	if msdhOffset < 0 {
		return nil
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := min(msdhOffset+msdhTotalLen, len(data))

	var missing []uint32
	seen := make(map[uint32]struct{})
	scanMtphRange(data, contentStart, contentEnd, masterTIDs, seen, &missing)
	return missing
}

// scanMtphRange recursively walks chunks in [start,end), descending into
// container chunks (miph and similar) so it can find mtph items nested
// inside playlist headers. For every mtph found whose TID is not in
// masterTIDs and not already seen, it is appended to *missing.
func scanMtphRange(data []byte, start, end int, masterTIDs map[uint32]struct{}, seen map[uint32]struct{}, missing *[]uint32) {
	offset := start
	for offset+12 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			return
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))

		// Determine the size of THIS chunk to advance the walker.
		chunkSize := headerLen
		isContainer := (tag == "miph" || tag == "mith" || tag == "mhoh" || tag == "miah") &&
			totalLen > headerLen && offset+totalLen <= end
		if isContainer {
			chunkSize = totalLen
		}
		if chunkSize < 8 || offset+chunkSize > end {
			return
		}

		switch tag {
		case "mtph":
			if offset+28 <= len(data) {
				tid := readUint32LE(data, offset+24)
				if tid != 0 {
					if _, ok := masterTIDs[tid]; !ok {
						if _, dup := seen[tid]; !dup {
							seen[tid] = struct{}{}
							*missing = append(*missing, tid)
						}
					}
				}
			}
		case "miph":
			// Descend past the miph fixed header into its children
			// (mtph items + per-playlist mhoh metadata).
			if headerLen >= 8 && headerLen < chunkSize {
				scanMtphRange(data, offset+headerLen, offset+chunkSize, masterTIDs, seen, missing)
			}
		}

		offset += chunkSize
	}
}
