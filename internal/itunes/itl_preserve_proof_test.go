// file: internal/itunes/itl_preserve_proof_test.go
// version: 1.0.0
// guid: 1e7c4a83-5b90-4d62-9f18-3a6c2b7e0d51
// last-edited: 2026-07-24
//
// Field-/bookmark-preservation BYTE-PROOF for the iTunes 2-way-sync write paths
// (P0 of the 2-way-sync system design). The concern (memory + design §INV-F2): a
// relocate rewrites a track's location and a cleanup removes a track — do EITHER
// silently mutate any OTHER field of any OTHER track, or drop unparsed atoms (the
// audiobook play-position bookmark is NOT parsed by the binary LE parser, so it
// survives only if the write path copies it through byte-for-byte)?
//
// This proves it the strongest possible way: a per-track RAW-BYTE comparison of the
// decompressed ITL payload before vs after a real relocate + remove. Because it
// compares raw bytes (not parsed fields) it catches ANY change to ANY atom the
// parser ignores — bookmarks, artwork refs, sort keys, everything.
//
// Env-gated so it does not run in CI (it needs a real multi-thousand-track library
// with real bookmarks; a synthetic fixture would prove nothing about real atoms):
//
//	ITL_PRESERVE_PROOF_PATH=/path/to/copy-of-iTunes\ Library.itl \
//	  go test ./internal/itunes/ -run TestITLPreservationByteProof -v
//
// Operates entirely on a COPY passed by path; never writes the input.

package itunes

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mhodAtom struct {
	typ   uint32
	bytes []byte
}

// extractMhods splits one mith track block into its header length and the ordered
// list of its mhoh atoms (each carries its hohmType at +12).
func extractMhods(block []byte) (headerLen int, atoms []mhodAtom) {
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
		atoms = append(atoms, mhodAtom{typ: readUint32LE(block, off+12), bytes: append([]byte(nil), block[off:off+l]...)})
		off += l
	}
	return headerLen, atoms
}

// splitMithBlocks walks the track section (msdh type 1) and returns
// lower-hex PID -> raw mith block bytes (a private copy per block).
func splitMithBlocks(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		t.Fatal("no msdh type 1 (track section) found")
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen
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
			out[pid] = append([]byte(nil), data[offset:offset+length]...)
		}
		offset += length
	}
	return out
}

// nonLocationAtoms returns the atoms of a block excluding the location pair
// (0x0D WinPath / 0x0B URL), in order — the set a relocate MUST leave untouched.
func nonLocationAtoms(block []byte) []mhodAtom {
	_, atoms := extractMhods(block)
	out := atoms[:0:0]
	for _, a := range atoms {
		if a.typ == 0x0D || a.typ == 0x0B {
			continue
		}
		out = append(out, a)
	}
	return out
}

func TestITLPreservationByteProof(t *testing.T) {
	path := os.Getenv("ITL_PRESERVE_PROOF_PATH")
	if path == "" {
		t.Skip("set ITL_PRESERVE_PROOF_PATH to a COPY of a real .itl to run the byte-proof")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	before, err := DecryptAndInflateITL(raw)
	if err != nil {
		t.Fatalf("decrypt/inflate: %v", err)
	}

	lib, err := ParseITL(path)
	if err != nil {
		t.Fatalf("ParseITL: %v", err)
	}
	beforeBlocks := splitMithBlocks(t, before)
	t.Logf("library: %d tracks parsed, %d mith blocks split", len(lib.Tracks), len(beforeBlocks))

	// Sample: relocate ~300 tracks whose location is a W:\ WinPath (transform to a
	// longer path so the length-change path of the writer is exercised), and remove
	// ~30 OTHER tracks (disjoint).
	const wantRelo, wantRemove = 300, 30
	var updates []ITLMetadataUpdate
	reloPIDs := map[string]bool{}
	removePIDs := map[string]bool{}
	for i := range lib.Tracks {
		tr := &lib.Tracks[i]
		pid := strings.ToLower(pidToHex(tr.PersistentID))
		if _, ok := beforeBlocks[pid]; !ok {
			continue
		}
		if len(updates) < wantRelo {
			if !strings.HasPrefix(tr.Location, `W:\`) {
				continue
			}
			newLoc := `W:\PROOF-RELO\` + tr.Location[3:] // insert a dir → changes bytes AND length
			updates = append(updates, ITLMetadataUpdate{PersistentID: pid, Location: newLoc})
			reloPIDs[pid] = true
		} else if len(removePIDs) < wantRemove {
			if reloPIDs[pid] {
				continue
			}
			removePIDs[pid] = true
		} else {
			break
		}
	}
	if len(updates) == 0 || len(removePIDs) == 0 {
		t.Fatalf("insufficient sample: relo=%d remove=%d (no W:\\ locations?)", len(updates), len(removePIDs))
	}

	// Apply the REAL write-path mutations to the decompressed payload.
	afterRelo, reloCount := UpdateMetadataLE(before, updates)
	if reloCount != len(updates) {
		t.Fatalf("relocate applied %d, expected %d", reloCount, len(updates))
	}
	after, removeCount := RemoveTracksByPIDLE(afterRelo, removePIDs)
	if removeCount != len(removePIDs) {
		t.Fatalf("remove applied %d, expected %d", removeCount, len(removePIDs))
	}
	afterBlocks := splitMithBlocks(t, after)

	// --- Assertions ---
	var (
		untouchedIdentical int
		relocatedProven    int
		extraAtomTypes     = map[uint32]int{} // non-basic atoms preserved on relocated tracks
		basic              = map[uint32]bool{0x02: true, 0x03: true, 0x04: true, 0x05: true, 0x06: true, 0x0C: true}
	)

	for pid, beforeBlock := range beforeBlocks {
		afterBlock, present := afterBlocks[pid]

		switch {
		case removePIDs[pid]:
			if present {
				t.Errorf("removed PID %s still present after remove", pid)
			}
			continue
		case !present:
			t.Errorf("PID %s vanished but was not scheduled for removal", pid)
			continue
		case reloPIDs[pid]:
			// header identical except totalLen [8:12]
			hlB, _ := extractMhods(beforeBlock)
			hlA, _ := extractMhods(afterBlock)
			if hlB != hlA || !bytes.Equal(beforeBlock[:8], afterBlock[:8]) || !bytes.Equal(beforeBlock[12:hlB], afterBlock[12:hlA]) {
				t.Errorf("relocated PID %s: mith header changed beyond totalLen", pid)
			}
			// every NON-location atom byte-identical, same order (the bookmark proof)
			nb := nonLocationAtoms(beforeBlock)
			na := nonLocationAtoms(afterBlock)
			if len(nb) != len(na) {
				t.Errorf("relocated PID %s: non-location atom count %d->%d", pid, len(nb), len(na))
				continue
			}
			ok := true
			for k := range nb {
				if nb[k].typ != na[k].typ || !bytes.Equal(nb[k].bytes, na[k].bytes) {
					t.Errorf("relocated PID %s: non-location atom 0x%X mutated", pid, nb[k].typ)
					ok = false
				}
				if !basic[nb[k].typ] {
					extraAtomTypes[nb[k].typ]++
				}
			}
			if ok {
				relocatedProven++
			}
		default:
			// untouched track: WHOLE block must be byte-identical
			if !bytes.Equal(beforeBlock, afterBlock) {
				t.Errorf("untouched PID %s changed (%d->%d bytes)", pid, len(beforeBlock), len(afterBlock))
			} else {
				untouchedIdentical++
			}
		}
	}

	t.Logf("PROOF: relocated=%d (all non-location atoms byte-identical), removed=%d, untouched-identical=%d",
		relocatedProven, len(removePIDs), untouchedIdentical)
	t.Logf("non-basic atom types preserved across relocated tracks (bookmark/artwork/sort/etc.): %v", extraAtomTypes)
	if len(extraAtomTypes) == 0 {
		t.Log("NOTE: sampled relocated tracks carried no non-basic atoms; preservation still proven for header + basic atoms")
	}
}

// TestITLPreservationThroughEncode closes the gap the direct-LE proof leaves: it
// runs a real relocate mutation and then encodes it through the FULL production
// encode path — WriteITLBytes → writeITLFile: CRIT-3 header regeneration →
// recompress → re-encrypt — and re-reads/re-decrypts the written file to re-run the
// per-track byte comparison. This proves the header-regeneration/recompress/
// re-encrypt layer, which the direct-LE proof skips, does not touch any track
// record. (WriteITLBytes is the encode half of SafeWriteITL WITHOUT the safety
// contract — see TestITLRelocateContractStatus / F7 for why the contract path
// itself cannot currently write this library.)
func TestITLPreservationThroughEncode(t *testing.T) {
	path := os.Getenv("ITL_PRESERVE_PROOF_PATH")
	if path == "" {
		t.Skip("set ITL_PRESERVE_PROOF_PATH to a COPY of a real .itl to run the byte-proof")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	before, err := DecryptAndInflateITL(raw)
	if err != nil {
		t.Fatalf("decrypt/inflate: %v", err)
	}
	lib, err := ParseITL(path)
	if err != nil {
		t.Fatalf("ParseITL: %v", err)
	}
	beforeBlocks := splitMithBlocks(t, before)

	var updates []ITLMetadataUpdate
	reloPIDs := map[string]bool{}
	for i := range lib.Tracks {
		tr := &lib.Tracks[i]
		if !strings.HasPrefix(tr.Location, `W:\`) {
			continue
		}
		pid := strings.ToLower(pidToHex(tr.PersistentID))
		if _, ok := beforeBlocks[pid]; !ok {
			continue
		}
		updates = append(updates, ITLMetadataUpdate{PersistentID: pid, Location: `W:\PROOF-RELO\` + tr.Location[3:]})
		reloPIDs[pid] = true
		if len(updates) >= 300 {
			break
		}
	}
	if len(updates) == 0 {
		t.Fatal("no W:\\ locations to relocate")
	}
	mutated, n := UpdateMetadataLE(before, updates)
	if n != len(updates) {
		t.Fatalf("relocate applied %d, expected %d", n, len(updates))
	}

	// Encode through the production encode path (header-regen + recompress +
	// re-encrypt), then read it back and decrypt.
	out := filepath.Join(t.TempDir(), "out.itl")
	if err := WriteITLBytes(path, out, mutated); err != nil {
		t.Fatalf("WriteITLBytes (production encode path): %v", err)
	}
	afterRaw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	after, err := DecryptAndInflateITL(afterRaw)
	if err != nil {
		t.Fatalf("decrypt/inflate written file: %v", err)
	}
	afterBlocks := splitMithBlocks(t, after)

	var untouchedIdentical, relocatedProven int
	for pid, beforeBlock := range beforeBlocks {
		afterBlock, present := afterBlocks[pid]
		if !present {
			t.Errorf("PID %s vanished through the encode path", pid)
			continue
		}
		if reloPIDs[pid] {
			nb := nonLocationAtoms(beforeBlock)
			na := nonLocationAtoms(afterBlock)
			if len(nb) != len(na) {
				t.Errorf("relocated PID %s: non-location atom count %d->%d", pid, len(nb), len(na))
				continue
			}
			ok := true
			for k := range nb {
				if nb[k].typ != na[k].typ || !bytes.Equal(nb[k].bytes, na[k].bytes) {
					t.Errorf("relocated PID %s: non-location atom 0x%X mutated by encode path", pid, nb[k].typ)
					ok = false
				}
			}
			if ok {
				relocatedProven++
			}
		} else if !bytes.Equal(beforeBlock, afterBlock) {
			t.Errorf("untouched PID %s changed through encode path (%d->%d bytes)", pid, len(beforeBlock), len(afterBlock))
		} else {
			untouchedIdentical++
		}
	}
	t.Logf("ENCODE-PATH PROOF (header-regen+recompress+re-encrypt): relocated=%d (non-location atoms byte-identical), untouched-identical=%d / %d tracks",
		relocatedProven, untouchedIdentical, len(beforeBlocks))
}

// TestITLRelocateContractStatus documents the F7 blocker: the full contract write
// path (UpdateITLLocations → SafeWriteITL) currently REFUSES to write the live AO
// library, because its media legitimately lives under ".itunes-writeback/iTunes
// Media/" (iTunes is pointed at the AO library there) and the location-form guard
// rejects any ".itunes-writeback/" substring as a staging-dir leak. Force does NOT
// override location-form (only the bounded-delta guard). Informational: it logs the
// guard verdict rather than asserting, so it stays correct if run against a library
// whose paths have been canonicalized.
func TestITLRelocateContractStatus(t *testing.T) {
	path := os.Getenv("ITL_PRESERVE_PROOF_PATH")
	if path == "" {
		t.Skip("set ITL_PRESERVE_PROOF_PATH to a COPY of a real .itl to run the byte-proof")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lib, err := ParseITL(path)
	if err != nil {
		t.Fatalf("ParseITL: %v", err)
	}
	var updates []ITLLocationUpdate
	for i := range lib.Tracks {
		tr := &lib.Tracks[i]
		if !strings.HasPrefix(tr.Location, `W:\`) {
			continue
		}
		updates = append(updates, ITLLocationUpdate{
			PersistentID: strings.ToLower(pidToHex(tr.PersistentID)),
			NewLocation:  tr.Location, // no-op relocate: isolate the guard, not the change
		})
		if len(updates) >= 5 {
			break
		}
	}
	work := filepath.Join(t.TempDir(), "work.itl")
	if err := os.WriteFile(work, raw, 0o644); err != nil {
		t.Fatalf("write work copy: %v", err)
	}
	_, err = UpdateITLLocations(work, work, updates)
	switch {
	case err == nil:
		t.Log("F7 STATUS: contract write path ACCEPTED — library paths are clean of the .itunes-writeback/ staging marker")
	case strings.Contains(err.Error(), "location-form"):
		t.Logf("F7 CONFIRMED: contract write path BLOCKED by location-form (library media under .itunes-writeback/ is treated as a staging leak). Relocate op (P2) needs this reconciled. err=%v", err)
	default:
		t.Logf("F7 STATUS: contract write path failed for another reason: %v", err)
	}
}
