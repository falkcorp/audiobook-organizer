// file: internal/itunes/relocate_oracle.go
// version: 1.0.0
// guid: 7a2e9c14-6b83-4d50-9f27-1c8b5a0e3d62
// last-edited: 2026-07-24
//
// Post-write acceptance oracle for the 2-way-sync relocate path (P2 of the
// 2-way-sync system design). After a relocate write, this compares the decompressed
// ITL payload BEFORE vs AFTER at the per-track RAW-BYTE level and confirms the write
// did EXACTLY what was planned and nothing else:
//
//   - every relocated PID changed ONLY its location pair (0x0D/0x0B); every other
//     byte of the track — the mith header (play count, rating, resume bookmark,
//     dates) and every other mhod atom — is byte-identical;
//   - every other track is byte-identical;
//   - no track was added or removed (relocate is 0 adds / 0 removes).
//
// Raw-byte comparison is the point: it catches ANY unintended change, including
// atoms the LE parser does not decode (bookmarks, artwork, sort keys) — which a
// parsed-field diff (ComputeITLDiff) cannot see. A non-empty verdict.Violations is
// the auto-rollback trigger: the decoupled write cycle restores the .bak and alerts.
//
// The byte-preservation property this checks was proven on the real 97,999-track
// library by internal/itunes/itl_preserve_proof_test.go (findings §F6); this oracle
// enforces it on every production write rather than trusting it.

package itunes

import (
	"bytes"
	"fmt"
)

// OracleViolationKind classifies how a write departed from a pure relocate.
type OracleViolationKind string

const (
	ViolationAdded          OracleViolationKind = "track-added"          // PID present after, absent before
	ViolationRemoved        OracleViolationKind = "track-removed"        // PID present before, absent after
	ViolationUnexpected     OracleViolationKind = "unexpected-change"    // a NON-relocated track's bytes changed
	ViolationNonLocationMut OracleViolationKind = "non-location-mutated" // a relocated track changed beyond its location pair
)

// OracleViolation is one departure from the planned relocate.
type OracleViolation struct {
	PID    string              `json:"pid"`
	Kind   OracleViolationKind `json:"kind"`
	Detail string              `json:"detail,omitempty"`
}

// RelocateOracleVerdict is the acceptance result. OK is true iff Violations is empty.
type RelocateOracleVerdict struct {
	OK                bool              `json:"ok"`
	TracksBefore      int               `json:"tracks_before"`
	TracksAfter       int               `json:"tracks_after"`
	RelocatedExpected int               `json:"relocated_expected"`
	RelocatedVerified int               `json:"relocated_verified"` // relocated PIDs with all non-location bytes identical
	UnchangedVerified int               `json:"unchanged_verified"` // untouched PIDs byte-identical
	LocationChanged   int               `json:"location_changed"`   // relocated PIDs whose location pair actually differs
	Violations        []OracleViolation `json:"violations,omitempty"`
}

// oracleMaxViolations caps the violation list so a catastrophic write (everything
// changed) cannot produce an unbounded report; the count fields stay exact.
const oracleMaxViolations = 200

// VerifyRelocateWrite is the relocate acceptance oracle. before/after are the
// DECOMPRESSED LE payloads (e.g. from DecryptAndInflateITL) of the library before
// and after the write; relocatedPIDs is the set of lower-hex PIDs the plan intended
// to relocate. READ-ONLY. Returns a verdict; Violations non-empty ⇒ auto-rollback.
func VerifyRelocateWrite(before, after []byte, relocatedPIDs map[string]bool) (*RelocateOracleVerdict, error) {
	beforeBlocks, err := splitMithBlocksByPID(before)
	if err != nil {
		return nil, fmt.Errorf("oracle: split before: %w", err)
	}
	afterBlocks, err := splitMithBlocksByPID(after)
	if err != nil {
		return nil, fmt.Errorf("oracle: split after: %w", err)
	}

	v := &RelocateOracleVerdict{
		TracksBefore:      len(beforeBlocks),
		TracksAfter:       len(afterBlocks),
		RelocatedExpected: len(relocatedPIDs),
	}
	addViol := func(pid string, kind OracleViolationKind, detail string) {
		if len(v.Violations) < oracleMaxViolations {
			v.Violations = append(v.Violations, OracleViolation{PID: pid, Kind: kind, Detail: detail})
		}
	}

	for pid, beforeBlock := range beforeBlocks {
		afterBlock, present := afterBlocks[pid]
		if !present {
			addViol(pid, ViolationRemoved, "track disappeared (relocate removes nothing)")
			continue
		}
		if relocatedPIDs[pid] {
			mutated, locChanged := relocateTrackDelta(beforeBlock, afterBlock)
			if mutated {
				addViol(pid, ViolationNonLocationMut, "track changed beyond its location pair")
			} else {
				v.RelocatedVerified++
			}
			if locChanged {
				v.LocationChanged++
			}
		} else if !bytes.Equal(beforeBlock, afterBlock) {
			addViol(pid, ViolationUnexpected, "non-relocated track changed")
		} else {
			v.UnchangedVerified++
		}
	}
	// Additions: a PID present after but not before.
	for pid := range afterBlocks {
		if _, ok := beforeBlocks[pid]; !ok {
			addViol(pid, ViolationAdded, "track appeared (relocate adds nothing)")
		}
	}

	v.OK = len(v.Violations) == 0
	return v, nil
}

// relocateTrackDelta compares one track's before/after blocks and reports whether
// anything OTHER than the location pair changed (mutated), and whether the location
// pair itself changed (locChanged). A relocate is valid iff !mutated.
func relocateTrackDelta(before, after []byte) (mutated, locChanged bool) {
	hlB, atomsB := splitMhodAtoms(before)
	hlA, atomsA := splitMhodAtoms(after)

	// mith header must be identical except the 4-byte totalLen at offset 8, which
	// legitimately changes when the location string length changes.
	if hlB != hlA || !bytes.Equal(before[:8], after[:8]) || !bytes.Equal(before[12:hlB], after[12:hlA]) {
		mutated = true
	}

	// Compare non-location atoms element-wise (order preserved by the writer); and
	// detect whether the location atoms changed.
	nb := filterOutLocation(atomsB)
	na := filterOutLocation(atomsA)
	if len(nb) != len(na) {
		mutated = true
	} else {
		for i := range nb {
			if nb[i].typ != na[i].typ || !bytes.Equal(nb[i].bytes, na[i].bytes) {
				mutated = true
				break
			}
		}
	}

	lb := filterLocation(atomsB)
	la := filterLocation(atomsA)
	if len(lb) != len(la) {
		locChanged = true
	} else {
		for i := range lb {
			if lb[i].typ != la[i].typ || !bytes.Equal(lb[i].bytes, la[i].bytes) {
				locChanged = true
				break
			}
		}
	}
	return mutated, locChanged
}

// mhodAtom is one mhoh chunk within a track block (its hohmType + raw bytes).
type oracleMhod struct {
	typ   uint32
	bytes []byte
}

func filterOutLocation(atoms []oracleMhod) []oracleMhod {
	out := make([]oracleMhod, 0, len(atoms))
	for _, a := range atoms {
		if a.typ == 0x0D || a.typ == 0x0B {
			continue
		}
		out = append(out, a)
	}
	return out
}

func filterLocation(atoms []oracleMhod) []oracleMhod {
	out := make([]oracleMhod, 0, 2)
	for _, a := range atoms {
		if a.typ == 0x0D || a.typ == 0x0B {
			out = append(out, a)
		}
	}
	return out
}

// splitMhodAtoms splits one mith track block into its header length and ordered mhoh
// atoms (each carries its hohmType at +12).
func splitMhodAtoms(block []byte) (headerLen int, atoms []oracleMhod) {
	if len(block) < 8 {
		return 0, nil
	}
	headerLen = int(readUint32LE(block, 4))
	off := headerLen
	for off+16 <= len(block) {
		if readTag(block, off) != "mhoh" {
			break
		}
		hl := int(readUint32LE(block, off+4))
		tl := int(readUint32LE(block, off+8))
		l := hl
		if tl > hl && tl <= len(block)-off {
			l = tl
		}
		if l < 8 || off+l > len(block) {
			break
		}
		atoms = append(atoms, oracleMhod{typ: readUint32LE(block, off+12), bytes: block[off : off+l]})
		off += l
	}
	return headerLen, atoms
}

// splitMithBlocksByPID walks the track section (msdh type 1) and returns lower-hex
// PID -> raw mith block bytes. Production analogue of the byte-proof test helper.
func splitMithBlocksByPID(data []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		return nil, fmt.Errorf("track section (msdh type 1) not found")
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen
	if contentEnd > len(data) {
		contentEnd = len(data)
	}
	mlthHeaderLen := 0
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlth" {
		mlthHeaderLen = int(readUint32LE(data, contentStart+4))
	}
	offset := contentStart + mlthHeaderLen
	for offset+8 <= contentEnd {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		hl := int(readUint32LE(data, offset+4))
		tl := int(readUint32LE(data, offset+8))
		length := hl
		if tl > hl && tl <= contentEnd-offset {
			length = tl
		}
		if length < 8 || offset+length > contentEnd {
			break
		}
		if tag == "mith" && offset+136 <= len(data) {
			pid := extractMithPIDLE(data, offset)
			// Copy so the returned slice is independent of the input backing array.
			out[pid] = append([]byte(nil), data[offset:offset+length]...)
		}
		offset += length
	}
	return out, nil
}
