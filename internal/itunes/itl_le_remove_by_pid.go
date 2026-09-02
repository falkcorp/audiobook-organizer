// file: internal/itunes/itl_le_remove_by_pid.go
// version: 1.2.1
// guid: e6f7a8b9-c0d1-2e3f-4a5b-6c7d8e9f0a1b
//
// Track removal from LE-format ITL payloads.
//
// v1.2.0: RemoveTracksByPIDLE is now a SAFE removal that excises master
// `mith` chunks AND cleans up resulting orphan `mtph` references in the
// playlist list, in one combined pass. The historic broken implementation
// is preserved as removeTracksByPIDLEUnsafe for regression tests.
// last-edited: 2026-09-02

package itunes

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// RemoveTracksByPIDLE removes tracks identified by the given persistent IDs
// from an LE-format ITL payload, atomically:
//
//  1. Excises each matching `mith` chunk from the master track list, then
//     decrements `mlth` count and the master msdh totalLen.
//  2. Locates every `mtph` playlist track-item that referenced one of the
//     removed TrackIDs (now-orphaned by step 1) and excises them, updating
//     each enclosing `miph` header (totalLen at +8, count at +16) and the
//     playlist-list msdh totalLen.
//
// Returns the modified payload and the number of master tracks removed.
//
// The auxiliary album list (mlah) is intentionally left alone — iTunes
// tolerates pre-existing dangling album→TID references (last-good has 50+
// of them and opens fine). Touching mlah was historically the source of
// repeated corruption regressions; the simpler "leave it" policy keeps
// the writer correct.
//
// Callers must still invoke VerifyITLNoNewDanglingRefsLE after the
// combined mutation pass as a defense-in-depth check.
func RemoveTracksByPIDLE(data []byte, pids map[string]bool) ([]byte, int) {
	if len(pids) == 0 {
		return data, 0
	}

	// Capture master TID set BEFORE removal so we can compute which TIDs
	// disappear after the splice. (We need to know specifically which TIDs
	// were removed, not just how many.)
	masterBefore := CollectMasterTrackIDsLE(data)

	// Phase 1: master-side mith removal (uses the historic implementation).
	afterMith, removed := removeTracksByPIDLEUnsafe(data, pids)
	if removed == 0 {
		return data, 0
	}

	// Phase 2: locate now-orphaned mtph items and splice them out.
	masterAfter := CollectMasterTrackIDsLE(afterMith)
	if masterAfter == nil {
		// Couldn't re-locate master — bail conservatively and abort
		// the whole removal so we never produce a half-cleaned ITL.
		slog.Error("RemoveTracksByPIDLE could not re-locate master after mith splice — aborting remove")
		return data, 0
	}

	// Build the set of TIDs that disappeared, used to scope the orphan
	// hunt to *newly-orphaned* references and avoid touching the
	// pre-existing dangling refs that iTunes already tolerates.
	newlyRemovedTIDs := make(map[uint32]struct{}, len(masterBefore)-len(masterAfter))
	for tid := range masterBefore {
		if _, still := masterAfter[tid]; !still {
			newlyRemovedTIDs[tid] = struct{}{}
		}
	}
	if len(newlyRemovedTIDs) == 0 {
		return afterMith, removed
	}

	hits := LocateDanglingMtphLE(afterMith, masterAfter)
	// Filter to only mtph items pointing at TIDs we just removed.
	var scopedHits []MtphHitLE
	for _, h := range hits {
		if _, removed := newlyRemovedTIDs[h.TrackID]; removed {
			scopedHits = append(scopedHits, h)
		}
	}
	if len(scopedHits) == 0 {
		return afterMith, removed
	}

	cleaned := RepairITLDropDanglingMtphLE(afterMith, scopedHits)
	return cleaned, removed
}

// removeTracksByPIDLEUnsafe is the OLD, KNOWN-BROKEN implementation kept only
// for use in regression tests that need to reproduce the corruption it
// caused. DO NOT call this from production code paths — it produces ITL
// payloads that iTunes rejects as damaged.
//
//nolint:unused // referenced by tests
func removeTracksByPIDLEUnsafe(data []byte, pids map[string]bool) ([]byte, int) {
	if len(pids) == 0 {
		return data, 0
	}

	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		return data, 0
	}

	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen

	// Find mlth header
	mlthOffset := -1
	mlthHeaderLen := 0
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlth" {
		mlthOffset = contentStart
		mlthHeaderLen = int(readUint32LE(data, contentStart+4))
	}

	// Walk tracks, collect spans to remove
	type span struct{ start, end int }
	var removeSpans []span
	removedCount := 0

	offset := contentStart + mlthHeaderLen
	var currentSpanStart int
	inTrack := false
	currentPIDMatch := false

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
			// Finish previous track span if it was a match
			if inTrack && currentPIDMatch {
				removeSpans = append(removeSpans, span{currentSpanStart, offset})
				removedCount++
			}
			// Start new track
			currentSpanStart = offset
			inTrack = true
			currentPIDMatch = false

			// Extract PID from mith (bytes 128-135, reversed for LE)
			if offset+136 <= len(data) {
				pid := extractMithPIDLE(data, offset)
				if pids[pid] {
					currentPIDMatch = true
				}
			}
		}

		offset += length
	}
	// Handle last track
	if inTrack && currentPIDMatch {
		removeSpans = append(removeSpans, span{currentSpanStart, offset})
		removedCount++
	}

	if removedCount == 0 {
		return data, 0
	}

	// Sort spans in descending order so we can splice from back to front
	// without invalidating earlier offsets
	sort.Slice(removeSpans, func(i, j int) bool {
		return removeSpans[i].start > removeSpans[j].start
	})

	result := make([]byte, len(data))
	copy(result, data)

	totalRemoved := 0
	for _, s := range removeSpans {
		size := s.end - s.start
		// Remove [s.start : s.end] from result
		result = append(result[:s.start], result[s.end:]...)
		totalRemoved += size
	}

	// Update mlth track count
	if mlthOffset >= 0 && mlthOffset+12 <= len(result) {
		oldCount := int(readUint32LE(result, mlthOffset+8))
		newCount := max(oldCount-removedCount, 0)
		writeUint32LE(result, mlthOffset+8, uint32(newCount))
	}

	// Update msdh totalLen
	if msdhOffset+12 <= len(result) {
		writeUint32LE(result, msdhOffset+8, uint32(msdhTotalLen-totalRemoved))
	}

	return result, removedCount
}

// extractMithPIDLE reads the 8-byte persistent ID from an LE mith block.
// In LE format, PIDs are stored in reversed byte order at offset 128-135.
// Returns a lowercase 16-char hex string matching the canonical format.
func extractMithPIDLE(data []byte, mithOffset int) string {
	var pid [8]byte
	for i := range 8 {
		pid[i] = data[mithOffset+135-i]
	}
	return strings.ToLower(hex.EncodeToString(pid[:]))
}

// ExtractTrackPIDLE is the exported version of extractMithPIDLE for use by
// other packages that need to read PIDs from parsed track data.
func ExtractTrackPIDLE(data []byte, mithOffset int) string {
	return extractMithPIDLE(data, mithOffset)
}

// GeneratePIDHex generates a random 8-byte persistent ID and returns it
// as a lowercase 16-char hex string. Use this when provisioning new tracks
// for non-iTunes books.
func GeneratePIDHex() string {
	pid := randomPID()
	return fmt.Sprintf("%016x", pid)
}
