// file: internal/itunes/itl_read_helpers.go
// version: 1.0.0
// guid: 2d6f9a41-8c3e-4b7d-a5f2-91e0c4b8d763
// last-edited: 2026-10-07
//
// Read-side ITL helpers that lived in the removed writer files (iTunes is
// import-only since 2026-10-07). The safety-contract audit, the identity
// computation, the mhoh audit and the PID repair planner still use them; none
// of them writes the library.

package itunes

import (
	"log/slog"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// headerFixedPrefix is the bytes before the version string: "hdfm"(4) +
// headerLen(4) + fileLen(4) + unknown(4) + verLen(1) = 17. The remainder
// (which carries the count fields) begins at 17 + len(version).
const headerFixedPrefix = 17

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

// canonicalWinLocationForFile canonicalizes a single local FilePath into the
// native Windows ITL 0x0D form (W:\...). ReverseRemapPath yields forward slashes;
// the ITL 0x0D form needs backslashes and isWindowsAbsPath rejects any '/', so we
// flip separators before validating. An unmappable path (still /mnt/... → \mnt\...
// with no drive letter) is rejected → skipped, never written raw (CRIT-2).
// metricLabel distinguishes the caller in the unmappable metric.
func canonicalWinLocationForFile(localPath, pidForLog, metricLabel string, mappings []PathMapping) (string, bool) {
	if localPath == "" {
		return "", false
	}
	winish := strings.ReplaceAll(ReverseRemapPath(localPath, mappings), "/", `\`)
	pair, err := NewLocationPair(winish)
	if err != nil {
		metrics.RecordITunesLocationUnmappable(metricLabel)
		slog.Warn("ITL relocate: skipping file with unmappable location (never written raw — CRIT-2)",
			"pid", pidForLog, "local", localPath, "error", err.Error())
		return "", false
	}
	return pair.WinPath, true
}
