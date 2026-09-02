// file: internal/itunes/itl_le_repair.go
// version: 1.0.1
// guid: 1f2a3b4c-5d6e-7f8a-9b0c-1d2e3f4a5b6c
//
// Surgical repair for ITL files damaged by the May-2026 RemoveTracksByPIDLE
// bug: removes only the orphaned `mtph` playlist track items and updates the
// enclosing `miph` and playlist `msdh` length/count fields. Does NOT touch
// the master track list. Safe to run on production libraries.
// last-edited: 2026-09-02

package itunes

import (
	"fmt"
	"os"
	"sort"
)

// MtphHitLE describes a single orphaned `mtph` chunk located inside a
// playlist's `miph` container.
type MtphHitLE struct {
	Offset           int    // absolute offset of the mtph chunk in the decompressed payload
	Length           int    // chunk length in bytes (typically 84)
	ParentMiphOffset int    // absolute offset of the enclosing miph chunk
	TrackID          uint32 // dangling TrackID (not present in master list)
}

// LooksLikeLE is the exported wrapper for the package-private LE detector.
func LooksLikeLE(data []byte) bool { return detectLE(data) }

// LocateDanglingMtphLE returns the locations of every `mtph` item inside the
// playlist-list msdh whose TrackID is not present in masterTIDs. Hits are
// returned in document order; deduplication is NOT applied (a single TID
// can be referenced by multiple playlists, all of which need cleaning).
func LocateDanglingMtphLE(data []byte, masterTIDs map[uint32]struct{}) []MtphHitLE {
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 2)
	if msdhOffset < 0 {
		return nil
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := min(msdhOffset+msdhTotalLen, len(data))

	var hits []MtphHitLE
	locateMtphRange(data, contentStart, contentEnd, -1, masterTIDs, &hits)
	return hits
}

func locateMtphRange(data []byte, start, end, parentMiph int, masterTIDs map[uint32]struct{}, hits *[]MtphHitLE) {
	offset := start
	for offset+12 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			return
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
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
						*hits = append(*hits, MtphHitLE{
							Offset:           offset,
							Length:           chunkSize,
							ParentMiphOffset: parentMiph,
							TrackID:          tid,
						})
					}
				}
			}
		case "miph":
			if headerLen >= 8 && headerLen < chunkSize {
				locateMtphRange(data, offset+headerLen, offset+chunkSize, offset, masterTIDs, hits)
			}
		}
		offset += chunkSize
	}
}

// RepairITLDropDanglingMtphLE returns a new payload with the given mtph hits
// excised, and with each affected miph's totalLen field decremented by the
// total bytes removed inside it. The playlist-list msdh totalLen is also
// updated. The master track list is untouched, as are all other top-level
// msdh containers.
//
// The hits slice may be in any order; the function sorts internally.
func RepairITLDropDanglingMtphLE(data []byte, hits []MtphHitLE) []byte {
	if len(hits) == 0 {
		out := make([]byte, len(data))
		copy(out, data)
		return out
	}

	// Sort by offset descending so we can splice from back to front
	// without invalidating earlier offsets.
	sorted := make([]MtphHitLE, len(hits))
	copy(sorted, hits)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset > sorted[j].Offset })

	// Track per-miph bytes removed so we can decrement parent totalLens
	// after the splice. Keys are ORIGINAL miph offsets; we'll map them to
	// post-splice offsets at update time.
	bytesPerMiph := map[int]int{}
	for _, h := range sorted {
		if h.ParentMiphOffset >= 0 {
			bytesPerMiph[h.ParentMiphOffset] += h.Length
		}
	}

	// Build the new buffer by splicing.
	result := make([]byte, len(data))
	copy(result, data)
	totalRemoved := 0
	for _, h := range sorted {
		if h.Offset+h.Length > len(result) {
			continue
		}
		result = append(result[:h.Offset], result[h.Offset+h.Length:]...)
		totalRemoved += h.Length
	}

	// To translate ORIGINAL offsets to POST-splice offsets, remember that
	// every byte removed before an offset shifts that offset down. Sort
	// removals ascending and accumulate.
	ascHits := make([]MtphHitLE, len(hits))
	copy(ascHits, hits)
	sort.Slice(ascHits, func(i, j int) bool { return ascHits[i].Offset < ascHits[j].Offset })

	translate := func(origOffset int) int {
		removed := 0
		for _, h := range ascHits {
			if h.Offset < origOffset {
				removed += h.Length
			}
		}
		return origOffset - removed
	}

	// Decrement each affected miph's totalLen field.
	for origMiph, removed := range bytesPerMiph {
		newMiph := translate(origMiph)
		if newMiph+12 > len(result) || removed == 0 {
			continue
		}
		oldTotal := readUint32LE(result, newMiph+8)
		newTotal := max(int(oldTotal)-removed, 0)
		writeUint32LE(result, newMiph+8, uint32(newTotal))

		// Many miph headers store the playlist's track-item count at
		// +16 (uint32 LE). Decrement it by the count of mtph items we
		// pulled out of this playlist.
		count := 0
		for _, h := range ascHits {
			if h.ParentMiphOffset == origMiph {
				count++
			}
		}
		if newMiph+20 <= len(result) {
			oldCount := readUint32LE(result, newMiph+16)
			if int(oldCount) >= count {
				writeUint32LE(result, newMiph+16, uint32(int(oldCount)-count))
			}
		}
	}

	// Update playlist-list msdh totalLen.
	msdhOffset, _, msdhTotalLen := findMsdhByType(result, 2)
	if msdhOffset >= 0 && msdhOffset+12 <= len(result) {
		// findMsdhByType reads from the post-splice buffer so msdhTotalLen
		// here is the OLD value still encoded — we must subtract removed.
		newMsdhTotal := max(msdhTotalLen-totalRemoved, 0)
		writeUint32LE(result, msdhOffset+8, uint32(newMsdhTotal))
	}

	return result
}

// WriteITLBytes encodes and writes a (possibly modified) decompressed payload
// to outputPath, reusing the original file's HDFM header / encryption / and
// compression-detection. The result is byte-compatible with iTunes Library
// files written by the production writeback path.
func WriteITLBytes(inputPath, outputPath string, decompressed []byte) error {
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("reading source ITL: %w", err)
	}
	hdr, err := parseHdfmHeader(raw)
	if err != nil {
		return err
	}
	origPayload := raw[hdr.headerLen:]
	origDec := itlDecrypt(hdr, origPayload)
	_, wasCompressed, err := itlInflate(origDec)
	if err != nil {
		return fmt.Errorf("decompressing original ITL payload: %w", err)
	}

	_, err = writeITLFile(outputPath, hdr, decompressed, wasCompressed, 0)
	return err
}
