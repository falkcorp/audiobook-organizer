// file: internal/itunes/itl_safety_contract_test.go
// version: 1.0.0
// guid: 2eb18728-e3ee-494d-b37d-3bb7e7c516a4
//
// Regression suite for ITLSafetyContract (fable5 TASK-003), implementing the
// 13 contract tests of SPEC 2 §6 (docs/specs/fable5-spec-itunes-writeback-
// hardening.md), plus a guard-isolation harness.
//
// The damaged production libraries are ephemeral evidence, not test inputs;
// their *signatures* (K1..K9) are encoded as test-local corrupting mutations
// applied to a minimal valid LE payload built in-memory here (buildCleanPayload).
// Production code never contains corruptors — all mutators live in this _test.go.
//
// Pattern per test: build the clean LE payload → apply ONE specific corruption →
// assert the NAMED guard fires AND every other guard stays silent. The clean
// payload is also asserted to pass every guard (TestContract_CleanPasses).

package itunes

import (
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// In-memory LE payload fixture builder
// ---------------------------------------------------------------------------
//
// Layout produced (matches the chunk grammar the contract + production walkers
// expect):
//
//	msdh(type 1, tracks)
//	  mlth(count=N)
//	  mith[0] (TID=10, PID) { mhoh 0x02 Name, mhoh 0x0D Location, mhoh 0x0B URL }
//	  mith[1] (TID=20, PID) { ... }
//	  ...
//	msdh(type 2, playlists)
//	  mlph
//	  miph(declared=K) { mtph(TID=10), mtph(TID=20), ... }
//
// Plus a matching hdfm header whose BE count fields (0x44/0x48/0x4C/0x54) agree
// with the payload, so guardCountCoherence passes on the clean fixture.

const (
	fxMsdhHeaderLen = 96
	fxMithHeaderLen = 156
	fxMlthHeaderLen = 96
	fxMlphHeaderLen = 96
	fxMiphHeaderLen = 96
	fxMtphHeaderLen = 76
)

type fxTrack struct {
	tid      uint32
	name     string
	location string // native Windows path (0x0D); "" => podcast (no 0x0D)
	localURL string // 0x0B; for podcasts pass an http(s):// URL
}

// buildMhoh assembles an iTunes-conformant LE mhoh block: headerLen=24,
// totalLen=40+strLen, the encoding indicator written as a u32 at +24 (so its
// high byte at +27 is 0 for the small indicator values), strLen at +28, zeros
// +32..+39, string at +40.
func buildMhoh(hohmType uint32, at24 uint32, encFlag byte, str []byte) []byte {
	total := 40 + len(str)
	b := make([]byte, total)
	copy(b[0:4], "mhoh")
	binary.LittleEndian.PutUint32(b[4:8], 24)
	binary.LittleEndian.PutUint32(b[8:12], uint32(total))
	binary.LittleEndian.PutUint32(b[12:16], hohmType)
	binary.LittleEndian.PutUint32(b[24:28], at24)
	b[27] = encFlag // legacy flag byte; iTunes-conformant => 0
	binary.LittleEndian.PutUint32(b[28:32], uint32(len(str)))
	copy(b[40:], str)
	return b
}

// asciiMhoh builds a clean ASCII-encoded text mhoh. For 0x0D/0x0B fields iTunes
// uses at24=3 (Windows-1252, K16-corrected) for 0x0D and at24=0 (ASCII) for
// 0x0B URLs.
func asciiMhoh(hohmType uint32, at24 uint32, s string) []byte {
	return buildMhoh(hohmType, at24, 0x00, []byte(s))
}

func buildMith(tid uint32, children []byte) []byte {
	total := fxMithHeaderLen + len(children)
	b := make([]byte, total)
	copy(b[0:4], "mith")
	binary.LittleEndian.PutUint32(b[4:8], fxMithHeaderLen)
	binary.LittleEndian.PutUint32(b[8:12], uint32(total))
	binary.LittleEndian.PutUint32(b[16:20], tid)
	// Unique nonzero PID derived from tid (stored reversed for LE).
	var pid [8]byte
	binary.BigEndian.PutUint64(pid[:], uint64(tid)|0x1000000000000000)
	for i := 0; i < 8; i++ {
		b[135-i] = pid[i]
	}
	copy(b[fxMithHeaderLen:], children)
	return b
}

func buildMlth(count int) []byte {
	b := make([]byte, fxMlthHeaderLen)
	copy(b[0:4], "mlth")
	binary.LittleEndian.PutUint32(b[4:8], fxMlthHeaderLen)
	binary.LittleEndian.PutUint32(b[8:12], uint32(count))
	return b
}

func buildMlph(count int) []byte {
	b := make([]byte, fxMlphHeaderLen)
	copy(b[0:4], "mlph")
	binary.LittleEndian.PutUint32(b[4:8], fxMlphHeaderLen)
	binary.LittleEndian.PutUint32(b[8:12], uint32(count))
	return b
}

func buildMtph(tid uint32) []byte {
	b := make([]byte, fxMtphHeaderLen)
	copy(b[0:4], "mtph")
	binary.LittleEndian.PutUint32(b[4:8], fxMtphHeaderLen)
	binary.LittleEndian.PutUint32(b[8:12], fxMtphHeaderLen)
	binary.LittleEndian.PutUint32(b[24:28], tid)
	return b
}

func buildMiph(declared int, children []byte) []byte {
	total := fxMiphHeaderLen + len(children)
	b := make([]byte, total)
	copy(b[0:4], "miph")
	binary.LittleEndian.PutUint32(b[4:8], fxMiphHeaderLen)
	binary.LittleEndian.PutUint32(b[8:12], uint32(total))
	binary.LittleEndian.PutUint32(b[16:20], uint32(declared)) // declared item count
	copy(b[fxMiphHeaderLen:], children)
	return b
}

func buildMsdh(blockType int, body []byte) []byte {
	total := fxMsdhHeaderLen + len(body)
	b := make([]byte, total)
	copy(b[0:4], "msdh")
	binary.LittleEndian.PutUint32(b[4:8], fxMsdhHeaderLen)
	binary.LittleEndian.PutUint32(b[8:12], uint32(total))
	binary.LittleEndian.PutUint32(b[12:16], uint32(blockType))
	copy(b[fxMsdhHeaderLen:], body)
	return b
}

// buildTrackMith builds one mith with Name(0x02)+Location(0x0D)+URL(0x0B)
// children (or just Name+URL for podcasts with empty location).
func buildTrackMith(tr fxTrack) []byte {
	var children []byte
	children = append(children, asciiMhoh(0x02, 3, tr.name)...)
	if tr.location != "" {
		children = append(children, asciiMhoh(0x0D, 3, tr.location)...)
		children = append(children, asciiMhoh(0x0B, 0, winPathToLocalURL(tr.location))...)
	} else {
		// Podcast: no 0x0D; 0x0B carries the http(s):// URL as given.
		children = append(children, asciiMhoh(0x0B, 0, tr.localURL)...)
	}
	return buildMith(tr.tid, children)
}

// buildPayloadFromTracks assembles the full LE payload from a track list. The
// playlist references every track's TID.
func buildPayloadFromTracks(tracks []fxTrack) []byte {
	// Track msdh.
	trackBody := buildMlth(len(tracks))
	for _, tr := range tracks {
		trackBody = append(trackBody, buildTrackMith(tr)...)
	}
	trackMsdh := buildMsdh(1, trackBody)

	// Playlist msdh: one playlist referencing all tracks.
	var mtphChildren []byte
	for _, tr := range tracks {
		mtphChildren = append(mtphChildren, buildMtph(tr.tid)...)
	}
	plBody := buildMlph(1)
	plBody = append(plBody, buildMiph(len(tracks), mtphChildren)...)
	plMsdh := buildMsdh(2, plBody)

	out := append([]byte{}, trackMsdh...)
	out = append(out, plMsdh...)
	return out
}

func cleanTracks() []fxTrack {
	return []fxTrack{
		{tid: 10, name: "Chapter One", location: `W:\itunes\Media\Audiobooks\Adrian Tchaikovsky\01 Children of Time.mp3`},
		{tid: 20, name: "Chapter Two", location: `W:\itunes\Media\Audiobooks\Adrian Tchaikovsky\02 Children of Time.mp3`},
		{tid: 30, name: "A Podcast Episode", location: "", localURL: "https://feeds.example.com/ep/30.mp3"},
	}
}

// buildCleanPayload returns a valid LE payload that passes every guard.
func buildCleanPayload() []byte {
	return buildPayloadFromTracks(cleanTracks())
}

// buildHeaderFor produces a minimal hdfm header whose BE count fields agree with
// the given payload (tracks @0x44, playlists @0x48, albums @0x4C, artists @0x54).
// The fixture payload has no album/artist msdh, so those counts are 0.
func buildHeaderFor(payload []byte) *hdfmHeader {
	_, tracks := countMasterTracks(payload)
	playlists, _ := countPlaylistsAndCheckMiph(payload)

	const version = "12.13.10.3"
	// Remainder must be long enough to reach file offset 0x54+4. The header
	// begins at 0; remainder begins at 17+len(version). Size it to cover 0x60.
	remStart := 17 + len(version)
	remLen := 0x60 - remStart
	if remLen < 0 {
		remLen = 0
	}
	rem := make([]byte, remLen)
	put := func(fileOff int, v uint32) {
		off := fileOff - remStart
		if off >= 0 && off+4 <= len(rem) {
			binary.BigEndian.PutUint32(rem[off:off+4], v)
		}
	}
	put(0x44, uint32(tracks))
	put(0x48, uint32(playlists))
	put(0x4C, 0)
	put(0x54, 0)

	return &hdfmHeader{
		headerLen:       uint32(17 + len(version) + len(rem)),
		fileLen:         0,
		unknown:         0,
		version:         version,
		headerRemainder: rem,
	}
}

// ---------------------------------------------------------------------------
// Mutation locator helpers (test-local)
// ---------------------------------------------------------------------------

// firstMhohOffset returns the payload offset of the first mhoh of the given
// hohmType (0 => any), or -1.
func firstMhohOffset(t *testing.T, payload []byte, hohmType uint32) int {
	t.Helper()
	found := -1
	forEachMhoh(payload, func(off, _ int) {
		if found >= 0 {
			return
		}
		if hohmType == 0 || readUint32LE(payload, off+12) == hohmType {
			found = off
		}
	})
	return found
}

// firstMsdhOffset returns the offset of the msdh container with the given type.
func firstMsdhOffset(payload []byte, blockType int) int {
	off, _, _ := findMsdhByType(payload, blockType)
	return off
}

// rewriteMhohString replaces the string body of the mhoh at off with newStr,
// rebuilding totalLen/strLen and returning a fresh payload (offsets shift).
func rewriteMhohString(payload []byte, off int, newStr string) []byte {
	hohmType := readUint32LE(payload, off+12)
	at24 := readUint32LE(payload, off+24)
	encFlag := payload[off+27]
	oldSpan := int(readUint32LE(payload, off+8))
	rebuilt := buildMhoh(hohmType, at24, encFlag, []byte(newStr))
	return spliceReplace(payload, off, off+oldSpan, rebuilt)
}

// spliceReplace replaces payload[start:end] with repl and fixes up the enclosing
// mith/miph and msdh totalLen fields so the container framing stays exact (so
// the only intentional corruption is the one the test injects, not a tiling
// break). It returns a new slice.
func spliceReplace(payload []byte, start, end int, repl []byte) []byte {
	delta := len(repl) - (end - start)
	out := make([]byte, 0, len(payload)+delta)
	out = append(out, payload[:start]...)
	out = append(out, repl...)
	out = append(out, payload[end:]...)
	if delta == 0 {
		return out
	}
	// Patch any container (msdh/mith/miph) whose [headerStart, contentEnd) span
	// encloses `start` by adjusting its totalLen at +8.
	patchEnclosingTotalLens(out, start, delta)
	return out
}

// patchEnclosingTotalLens walks the top-level msdh containers and their
// mith/miph children, adjusting the totalLen of every container that encloses
// `pos` by delta. Walk uses the ORIGINAL (post-splice) framing except for the
// container that needs adjusting; since we adjust outermost-in, we recompute as
// we descend.
func patchEnclosingTotalLens(data []byte, pos, delta int) {
	offset := 0
	for offset+16 <= len(data) {
		if readTag(data, offset) != "msdh" {
			return
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		// Was this container's pre-splice span enclosing pos? Use post-splice
		// totalLen-delta to reconstruct the original end for the container that
		// contains pos.
		origEnd := offset + totalLen
		if pos >= offset && pos < origEnd {
			binary.LittleEndian.PutUint32(data[offset+8:offset+12], uint32(totalLen+delta))
			// Descend into mith/miph children of this msdh.
			patchChildTotalLens(data, offset+headerLen, offset+totalLen+delta, pos, delta)
			return
		}
		offset += totalLen
	}
}

func patchChildTotalLens(data []byte, start, end, pos, delta int) {
	offset := start
	for offset+12 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			return
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		isContainer := (tag == "mith" || tag == "miph" || tag == "miah") && totalLen > headerLen
		if isContainer {
			span = totalLen
		}
		// Only patch ancestors that STRICTLY enclose pos (offset < pos). A leaf
		// chunk that begins exactly at pos is the replaced block itself — its
		// totalLen already came from the replacement, so we must not touch it.
		if isContainer && offset < pos && pos < offset+span {
			binary.LittleEndian.PutUint32(data[offset+8:offset+12], uint32(totalLen+delta))
			patchChildTotalLens(data, offset+headerLen, offset+span+delta, pos, delta)
			return
		}
		if span < 8 {
			return
		}
		offset += span
	}
}

// ---------------------------------------------------------------------------
// Guard-isolation harness
// ---------------------------------------------------------------------------

// assertOnlyGuardFires runs the contract and asserts that exactly the named
// guard reports violations and every other guard passes. `before` may be nil.
func assertOnlyGuardFires(t *testing.T, before, after []byte, hdr *hdfmHeader, cfg ContractConfig, wantGuard string) {
	t.Helper()
	v := RunSafetyContract(before, after, hdr, cfg)
	if v.Pass {
		t.Fatalf("expected guard %q to fire, but contract passed", wantGuard)
	}
	firedNamed := false
	for _, r := range v.Results {
		if r.Guard == wantGuard {
			if !r.Pass() {
				firedNamed = true
			}
			continue
		}
		if !r.Pass() {
			t.Errorf("unexpected violation from guard %q (only %q should fire): %+v", r.Guard, wantGuard, r.Violations)
		}
	}
	if !firedNamed {
		t.Fatalf("named guard %q did not fire; failed guards: %v", wantGuard, v.FailedGuards())
	}
}

func defCfg() ContractConfig { return DefaultContractConfig() }

// ---------------------------------------------------------------------------
// SPEC 2 §6 — the 13 contract tests
// ---------------------------------------------------------------------------

// TestContract_CleanPasses: the unmutated fixture passes every guard.
func TestContract_CleanPasses(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	v := RunSafetyContract(clean, clean, hdr, defCfg())
	if !v.Pass {
		t.Fatalf("clean payload should pass all guards; failed: %v\n%s", v.FailedGuards(), v.Error())
	}
	// AuditITL single-library mode is exercised separately (needs a full .itl).
	auditV := RunSafetyContract(nil, clean, hdr, defCfg())
	if !auditV.Pass {
		t.Fatalf("clean payload should pass in audit mode; failed: %v\n%s", auditV.FailedGuards(), auditV.Error())
	}
}

// TestContract_DanglingMtph: excise a mith but keep its mtph reference.
func TestContract_DanglingMtph(t *testing.T) {
	clean := buildCleanPayload()

	// Use the preserved unsafe remover to drop the first track's mith while
	// leaving its mtph orphaned (exactly the K1 production signature). It updates
	// mlth/miph counts and the msdh totalLen, so count-coherence stays silent —
	// the ONLY remaining fault is the orphaned mtph reference.
	pid := pidForTID(clean, 10)
	after, removed := removeTracksByPIDLEUnsafe(clean, map[string]bool{pid: true})
	if removed != 1 {
		t.Fatalf("expected to remove 1 mith, removed %d", removed)
	}

	// before = clean (no pre-existing orphans), after = orphaned.
	assertOnlyGuardFires(t, clean, after, buildHeaderFor(after), defCfg(), "no-new-dangling-refs")
}

// TestContract_HeaderCountDesync: remove a track but keep the old header.
func TestContract_HeaderCountDesync(t *testing.T) {
	clean := buildCleanPayload()
	oldHdr := buildHeaderFor(clean) // claims 3 tracks

	// Remove track 10 cleanly (mith + its orphan mtph) so the ONLY inconsistency
	// is the header vs payload count — not a dangling ref.
	after := removeTrackAndMtph(clean, 10)

	assertOnlyGuardFires(t, clean, after, oldHdr, defCfg(), "count-coherence")
}

// TestContract_MhohForeignFlag: set byte +27 = 3 on one mhoh (K3 / CRIT-1).
func TestContract_MhohForeignFlag(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	off := firstMhohOffset(t, clean, 0x02)
	if off < 0 {
		t.Fatal("no 0x02 mhoh found")
	}
	after := append([]byte{}, clean...)
	after[off+27] = 3 // foreign encoding flag iTunes never writes

	assertOnlyGuardFires(t, clean, after, hdr, defCfg(), "mhoh-format")
}

// TestContract_MhohHeaderLen: set headerLen = totalLen on one mhoh (K5 / HIGH-6).
func TestContract_MhohHeaderLen(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	off := firstMhohOffset(t, clean, 0x02)
	if off < 0 {
		t.Fatal("no 0x02 mhoh found")
	}
	after := append([]byte{}, clean...)
	totalLen := readUint32LE(after, off+8)
	binary.LittleEndian.PutUint32(after[off+4:off+8], totalLen) // headerLen := totalLen

	assertOnlyGuardFires(t, clean, after, hdr, defCfg(), "mhoh-format")
}

// TestContract_MhohLenArithmetic: totalLen != 40+strLen (K7).
func TestContract_MhohLenArithmetic(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	off := firstMhohOffset(t, clean, 0x02)
	if off < 0 {
		t.Fatal("no 0x02 mhoh found")
	}
	after := append([]byte{}, clean...)
	// Corrupt the declared strLen at +28 so totalLen != 40+strLen, but keep it
	// in-bounds so the only failure is the arithmetic mismatch.
	binary.LittleEndian.PutUint32(after[off+28:off+32], 1)

	assertOnlyGuardFires(t, clean, after, hdr, defCfg(), "mhoh-format")
}

// TestContract_LocationURLIn0x0D: write a URL into 0x0D (K4 / CRIT-2).
func TestContract_LocationURLIn0x0D(t *testing.T) {
	clean := buildCleanPayload()
	off := firstMhohOffset(t, clean, 0x0D)
	if off < 0 {
		t.Fatal("no 0x0D mhoh found")
	}
	// Replace the native path with a file:// URL — the exact production bug.
	after := rewriteMhohString(clean, off, "file://localhost/W:/itunes/Media/Audiobooks/Adrian%20Tchaikovsky/01%20Children%20of%20Time.mp3")
	hdr := buildHeaderFor(after)

	assertOnlyGuardFires(t, clean, after, hdr, defCfg(), "location-form")
}

// TestContract_StagingPathLeak: a location containing ".itunes-writeback/".
func TestContract_StagingPathLeak(t *testing.T) {
	clean := buildCleanPayload()
	off := firstMhohOffset(t, clean, 0x0D)
	if off < 0 {
		t.Fatal("no 0x0D mhoh found")
	}
	after := rewriteMhohString(clean, off, `W:\audiobook-organizer\.itunes-writeback\Media\book.mp3`)
	// The sibling 0x0B no longer round-trips after we changed 0x0D; rewrite it
	// to match so the ONLY violation is the staging marker, not a pairing break.
	off0B := firstMhohOffsetIn(after, 0x0B)
	after = rewriteMhohString(after, off0B, winPathToLocalURL(`W:\audiobook-organizer\.itunes-writeback\Media\book.mp3`))
	hdr := buildHeaderFor(after)

	// Absolute-property test of `after` (no contrasting `before`), so the
	// delta-based bounded-delta guard has nothing to bound.
	assertOnlyGuardFires(t, nil, after, hdr, defCfg(), "location-form")
}

// TestContract_MiphCountMismatch: decrement a miph declared count (K8).
func TestContract_MiphCountMismatch(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	plMsdh := firstMsdhOffset(clean, 2)
	if plMsdh < 0 {
		t.Fatal("no playlist msdh")
	}
	// Locate the miph and decrement its declared item count at +16.
	miphOff := plMsdh + fxMsdhHeaderLen + fxMlphHeaderLen
	if readTag(clean, miphOff) != "miph" {
		t.Fatalf("expected miph at %d, got %q", miphOff, readTag(clean, miphOff))
	}
	after := append([]byte{}, clean...)
	declared := readUint32LE(after, miphOff+16)
	binary.LittleEndian.PutUint32(after[miphOff+16:miphOff+20], declared-1)

	assertOnlyGuardFires(t, clean, after, hdr, defCfg(), "count-coherence")
}

// TestContract_TidDuplicate: duplicate a TID in the master list.
func TestContract_TidDuplicate(t *testing.T) {
	// Build a payload whose second track reuses the first track's TID.
	tracks := cleanTracks()
	tracks[1].tid = tracks[0].tid // duplicate TID (still ascending-broken too)
	after := buildPayloadFromTracks(tracks)
	hdr := buildHeaderFor(after)

	// Absolute-property test of `after`; no contrasting `before`.
	assertOnlyGuardFires(t, nil, after, hdr, defCfg(), "tid-pid-sanity")
}

// TestContract_TidUnsorted: swap two TIDs so the master list is not ascending.
func TestContract_TidUnsorted(t *testing.T) {
	tracks := cleanTracks()
	tracks[0].tid, tracks[1].tid = tracks[1].tid, tracks[0].tid // 20,10,30 — unsorted
	after := buildPayloadFromTracks(tracks)
	hdr := buildHeaderFor(after)

	// Absolute-property test of `after`; no contrasting `before`.
	assertOnlyGuardFires(t, nil, after, hdr, defCfg(), "tid-pid-sanity")
}

// TestContract_TruncatedContainer: shrink an msdh totalLen so the containers no
// longer tile the payload exactly (the truncation/splice class).
//
// We shrink the LAST (playlist) msdh totalLen by a whole mtph chunk's worth and
// drop the corresponding declared count + mtph child, so the only structural
// fault is that the msdh containers cover fewer bytes than the payload holds —
// a tiling gap — while every COUNT stays internally consistent (so only
// container-tiling fires, not count-coherence).
func TestContract_TruncatedContainer(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	plMsdh := firstMsdhOffset(clean, 2)
	miphOff := plMsdh + fxMsdhHeaderLen + fxMlphHeaderLen
	if readTag(clean, miphOff) != "miph" {
		t.Fatalf("expected miph at %d", miphOff)
	}

	after := append([]byte{}, clean...)
	// Shrink the playlist msdh totalLen by one mtph chunk; the trailing mtph
	// bytes physically remain in the slice but fall OUTSIDE every container's
	// declared span → containers cover fewer bytes than len(payload).
	msdhTotal := readUint32LE(after, plMsdh+8)
	binary.LittleEndian.PutUint32(after[plMsdh+8:plMsdh+12], msdhTotal-uint32(fxMtphHeaderLen))
	// Keep the miph internally consistent with what now fits: shrink its totalLen
	// and its declared count so count-coherence does NOT also fire.
	miphTotal := readUint32LE(after, miphOff+8)
	binary.LittleEndian.PutUint32(after[miphOff+8:miphOff+12], miphTotal-uint32(fxMtphHeaderLen))
	declared := readUint32LE(after, miphOff+16)
	binary.LittleEndian.PutUint32(after[miphOff+16:miphOff+20], declared-1)

	assertOnlyGuardFires(t, clean, after, hdr, defCfg(), "container-tiling")
}

// TestContract_FailClosedOnUnparseable: corrupt the master-list msdh tag.
func TestContract_FailClosedOnUnparseable(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	after := append([]byte{}, clean...)
	copy(after[0:4], "XXXX") // destroy the first msdh tag → not LE / unparseable

	// parse-roundtrip must FAIL CLOSED (the historic fail-open bug, MED-7).
	v := RunSafetyContract(clean, after, hdr, defCfg())
	if v.Pass {
		t.Fatal("expected fail-closed on unparseable payload, got pass")
	}
	prFired := false
	for _, r := range v.Results {
		if r.Guard == "parse-roundtrip" && !r.Pass() {
			prFired = true
		}
	}
	if !prFired {
		t.Fatalf("parse-roundtrip did not fire on unparseable payload; failed: %v", v.FailedGuards())
	}
}

// TestContract_BoundedDelta: remove > RemovedTracksMax tracks.
func TestContract_BoundedDelta(t *testing.T) {
	// before has 5001 tracks; after has 0 → removed 5001 > cap 5000.
	before := buildManyTrackPayload(5001)
	after := buildManyTrackPayload(0)
	hdr := buildHeaderFor(after)

	cfg := defCfg() // RemovedTracksMax=5000
	v := RunSafetyContract(before, after, hdr, cfg)
	if v.Pass {
		t.Fatal("expected bounded-delta to fire on 5001 removed tracks")
	}
	bdFired := false
	for _, r := range v.Results {
		if r.Guard == "bounded-delta" && !r.Pass() {
			bdFired = true
		}
	}
	if !bdFired {
		t.Fatalf("bounded-delta did not fire; failed: %v", v.FailedGuards())
	}

	// Force must override the bounded-delta guardrail (only).
	cfg.Force = true
	for _, r := range RunSafetyContract(before, after, hdr, cfg).Results {
		if r.Guard == "bounded-delta" && !r.Pass() {
			t.Fatal("Force=true should override bounded-delta")
		}
	}
}

// ---------------------------------------------------------------------------
// Additional coverage: AuditITL on a real round-tripped .itl, config defaults
// ---------------------------------------------------------------------------

// TestAuditITL_FailClosedOnGarbage: AuditITL rejects non-ITL bytes.
func TestAuditITL_FailClosedOnGarbage(t *testing.T) {
	v := AuditITL([]byte("not an itl file at all"))
	if v.Pass {
		t.Fatal("AuditITL should fail closed on garbage input")
	}
}

// buildITLFile encrypts+deflates the payload and prepends a matching hdfm header,
// producing a complete, decodable .itl byte stream for AuditITL.
func buildITLFile(t *testing.T, payload []byte) []byte {
	t.Helper()
	hdr := buildHeaderFor(payload)
	deflated := itlDeflate(payload)
	encrypted := itlEncrypt(hdr, deflated)
	fileLen := uint32(len(encrypted)) + hdr.headerLen
	header := buildHdfmHeader(hdr.version, hdr.headerRemainder, fileLen, hdr.unknown)
	out := append([]byte{}, header...)
	out = append(out, encrypted...)
	return out
}

// TestAuditITL_CleanLibraryPasses: a full in-memory .itl round-trips through
// decode (decrypt + fail-closed inflate) and passes every guard — exercising the
// AuditITL success path and decodeITLForContract.
func TestAuditITL_CleanLibraryPasses(t *testing.T) {
	itl := buildITLFile(t, buildCleanPayload())
	v := AuditITL(itl)
	if !v.Pass {
		t.Fatalf("AuditITL on clean library should pass; failed: %v\n%s", v.FailedGuards(), v.Error())
	}
	if v.Summary.AfterTracks != 3 || v.Summary.AfterPlaylists != 1 {
		t.Fatalf("unexpected summary: %+v", v.Summary)
	}
}

// TestAuditITL_DetectsCorruptLibrary: AuditITL flags a K3 carrier at read time
// (HIGH-6 — already-corrupt libraries are detectable without a `before`).
func TestAuditITL_DetectsCorruptLibrary(t *testing.T) {
	payload := buildCleanPayload()
	off := firstMhohOffsetIn(payload, 0x02)
	payload[off+27] = 3 // foreign encoding flag
	itl := buildITLFile(t, payload)

	v := AuditITL(itl)
	if v.Pass {
		t.Fatal("AuditITL should flag a K3-carrier library")
	}
	saw := false
	for _, r := range v.FailedGuards() {
		if r == "mhoh-format" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected mhoh-format to fire; failed: %v", v.FailedGuards())
	}
}

// TestLocationForm_PodcastExempt: a podcast track (no 0x0D, http(s):// in 0x0B)
// passes location-form — the pairing rule does not apply to it.
func TestLocationForm_PodcastExempt(t *testing.T) {
	tracks := []fxTrack{
		{tid: 10, name: "Ep 1", location: "", localURL: "https://feeds.example.com/1.mp3"},
		{tid: 20, name: "Ep 2", location: "", localURL: "http://feeds.example.com/2.mp3"},
	}
	payload := buildPayloadFromTracks(tracks)
	res := guardLocationForm(nil, payload, nil, defCfg())
	if !res.Pass() {
		t.Fatalf("podcast tracks should pass location-form: %+v", res.Violations)
	}
}

// TestLocationForm_MissingSibling0B: a track with 0x0D but no 0x0B sibling fails.
func TestLocationForm_MissingSibling0B(t *testing.T) {
	// Build a mith with only a 0x0D child (no 0x0B).
	children := asciiMhoh(0x02, 3, "Name")
	children = append(children, asciiMhoh(0x0D, 3, `W:\m\a.mp3`)...)
	mith := buildMith(10, children)
	trackBody := append(buildMlth(1), mith...)
	trackMsdh := buildMsdh(1, trackBody)
	plMsdh := buildMsdh(2, append(buildMlph(1), buildMiph(1, buildMtph(10))...))
	payload := append(append([]byte{}, trackMsdh...), plMsdh...)

	res := guardLocationForm(nil, payload, nil, defCfg())
	if res.Pass() {
		t.Fatal("track with 0x0D but no 0x0B sibling should fail location-form")
	}
}

// TestLocationForm_BadURLRoundTrip: a NEWLY-INTRODUCED 0x0B URL that does not
// round-trip the 0x0D path fails location-form in write mode. Pair
// consistency is a no-new check (iTunes itself leaves stale pairs — see the
// golden corpus), so audit mode (before == nil) tolerates pre-existing
// mismatches; a clean `before` makes this mismatch new.
func TestLocationForm_BadURLRoundTrip(t *testing.T) {
	buildWith0B := func(url string) []byte {
		children := asciiMhoh(0x02, 3, "Name")
		children = append(children, asciiMhoh(0x0D, 3, `W:\m\a.mp3`)...)
		children = append(children, asciiMhoh(0x0B, 0, url)...)
		mith := buildMith(10, children)
		trackMsdh := buildMsdh(1, append(buildMlth(1), mith...))
		plMsdh := buildMsdh(2, append(buildMlph(1), buildMiph(1, buildMtph(10))...))
		return append(append([]byte{}, trackMsdh...), plMsdh...)
	}
	before := buildWith0B("file://localhost/W:/m/a.mp3")
	after := buildWith0B("file://localhost/W:/WRONG/path.mp3")

	if res := guardLocationForm(before, after, nil, defCfg()); res.Pass() {
		t.Fatal("newly non-round-tripping 0x0B should fail location-form in write mode")
	}
	// The same mismatch pre-existing in `before` is tolerated (no-new).
	if res := guardLocationForm(after, after, nil, defCfg()); !res.Pass() {
		t.Fatalf("pre-existing pair mismatch must not fail location-form: %+v", res.Violations)
	}
	// Audit mode tolerates iTunes-authored mismatches too.
	if res := guardLocationForm(nil, after, nil, defCfg()); !res.Pass() {
		t.Fatalf("audit mode must tolerate pre-existing pair mismatch: %+v", res.Violations)
	}
}

// TestParseRoundtrip_EmptyAndBE: too-small and non-LE payloads fail closed.
func TestParseRoundtrip_EmptyAndBE(t *testing.T) {
	if guardParseRoundtrip(nil, []byte{1, 2, 3}, nil, defCfg()).Pass() {
		t.Fatal("tiny payload should fail parse-roundtrip")
	}
	be := make([]byte, 32)
	copy(be[0:4], "hdfm") // not 'msdh' → BE/foreign
	if guardParseRoundtrip(nil, be, nil, defCfg()).Pass() {
		t.Fatal("non-LE payload should fail parse-roundtrip (BE refusal)")
	}
}

// TestWinPathToLocalURL_Escaping: the 0x0B renderer matches iTunes' escaping.
func TestWinPathToLocalURL_Escaping(t *testing.T) {
	got := winPathToLocalURL(`W:\itunes Media\Children of Time.mp3`)
	want := "file://localhost/W:/itunes%20Media/Children%20of%20Time.mp3"
	if got != want {
		t.Fatalf("winPathToLocalURL escaping: got %q want %q", got, want)
	}
	if !isWindowsAbsPath(`C:\a\b.mp3`) {
		t.Fatal("C:\\a\\b.mp3 should be a Windows abs path")
	}
	if isWindowsAbsPath("/unix/path") || isWindowsAbsPath(`C:/forward/slash`) {
		t.Fatal("unix and forward-slash paths must not be Windows abs paths")
	}
}

// TestContractConfig_Defaults: zero config normalizes to SPEC defaults.
func TestContractConfig_Defaults(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	// Pass an explicit zero config; normalizeConfig should apply 5000/20.
	v := RunSafetyContract(clean, clean, hdr, ContractConfig{})
	if !v.Pass {
		t.Fatalf("clean payload with zero config should pass; failed: %v", v.FailedGuards())
	}
	d := DefaultContractConfig()
	if d.RemovedTracksMax != 5000 || d.RewrittenMhohPctMax != 20 || d.Force {
		t.Fatalf("unexpected defaults: %+v", d)
	}
}

// TestContractVerdict_ErrorString: a failing verdict renders a non-empty,
// guard-named error; a passing verdict renders empty.
func TestContractVerdict_ErrorString(t *testing.T) {
	clean := buildCleanPayload()
	hdr := buildHeaderFor(clean)
	if e := RunSafetyContract(clean, clean, hdr, defCfg()).Error(); e != "" {
		t.Fatalf("passing verdict should have empty Error(), got %q", e)
	}
	bad := append([]byte{}, clean...)
	copy(bad[0:4], "ZZZZ")
	if e := RunSafetyContract(clean, bad, hdr, defCfg()).Error(); e == "" {
		t.Fatal("failing verdict should have non-empty Error()")
	}
}

// TestContract_MhohRewriteBoundedDelta: rewriting >20% of mhoh blocks fires
// bounded-delta (the HIGH-3 blast-radius cap), and Force overrides it.
func TestContract_MhohRewriteBoundedDelta(t *testing.T) {
	before := buildCleanPayload()
	// Rewrite every Name (0x02) mhoh → ~33% of blocks rewritten.
	after := append([]byte{}, before...)
	var offs []int
	forEachMhoh(after, func(off, _ int) {
		if readUint32LE(after, off+12) == 0x02 {
			offs = append(offs, off)
		}
	})
	for _, off := range offs {
		// Flip a content byte (same length → no reframing needed).
		after[off+40] ^= 0xFF
	}
	hdr := buildHeaderFor(after)

	v := RunSafetyContract(before, after, hdr, defCfg())
	bdFired := false
	for _, r := range v.Results {
		if r.Guard == "bounded-delta" && !r.Pass() {
			bdFired = true
		}
	}
	if !bdFired {
		t.Fatalf("expected bounded-delta to fire on >20%% mhoh rewrite; failed: %v", v.FailedGuards())
	}
}

// ---------------------------------------------------------------------------
// Test-local builders for delta tests
// ---------------------------------------------------------------------------

// buildManyTrackPayload builds a payload with n tracks (ascending TIDs) and a
// playlist referencing them. n may be 0 (empty master list + empty playlist).
func buildManyTrackPayload(n int) []byte {
	tracks := make([]fxTrack, n)
	for i := 0; i < n; i++ {
		tid := uint32((i + 1) * 2)
		tracks[i] = fxTrack{
			tid:      tid,
			name:     "Track",
			location: `W:\m\` + itoa(i) + `.mp3`,
		}
	}
	return buildPayloadFromTracks(tracks)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// PID / removal helpers
// ---------------------------------------------------------------------------

// pidForTID walks the master list and returns the hex PID of the mith with the
// given TID (matching the LE-reversed storage used by removeTracksByPIDLEUnsafe).
func pidForTID(payload []byte, tid uint32) string {
	tids, pids := collectMithTidsPids(payload)
	for i, t := range tids {
		if t == tid {
			return pids[i]
		}
	}
	return ""
}

// removeTrackAndMtph removes the mith for tid AND its mtph reference, leaving a
// clean (consistent except for the stale header) payload — used by the
// header-desync test so dangling-refs does NOT also fire.
func removeTrackAndMtph(payload []byte, tid uint32) []byte {
	// Remove the mith span from the track msdh.
	out := removeMithByTID(payload, tid)
	// Remove the matching mtph from the playlist, decrementing the miph count.
	out = removeMtphByTID(out, tid)
	return out
}

func removeMithByTID(payload []byte, tid uint32) []byte {
	trackMsdh := firstMsdhOffset(payload, 1)
	headerLen := int(readUint32LE(payload, trackMsdh+4))
	totalLen := int(readUint32LE(payload, trackMsdh+8))
	contentStart := trackMsdh + headerLen
	contentEnd := trackMsdh + totalLen
	offset := contentStart
	if readTag(payload, contentStart) == "mlth" {
		offset = contentStart + int(readUint32LE(payload, contentStart+4))
	}
	for offset+12 <= contentEnd {
		tag := readTag(payload, offset)
		if tag == "" {
			break
		}
		hlen := int(readUint32LE(payload, offset+4))
		tlen := int(readUint32LE(payload, offset+8))
		span := hlen
		if tag == "mith" && tlen > hlen {
			span = tlen
		}
		if tag == "mith" && readUint32LE(payload, offset+16) == tid {
			out := spliceReplace(payload, offset, offset+span, nil)
			// Decrement mlth count.
			mlthOff := contentStart
			c := readUint32LE(out, mlthOff+8)
			binary.LittleEndian.PutUint32(out[mlthOff+8:mlthOff+12], c-1)
			return out
		}
		offset += span
	}
	return payload
}

func removeMtphByTID(payload []byte, tid uint32) []byte {
	plMsdh := firstMsdhOffset(payload, 2)
	miphOff := plMsdh + fxMsdhHeaderLen + fxMlphHeaderLen
	if readTag(payload, miphOff) != "miph" {
		return payload
	}
	miphTotal := int(readUint32LE(payload, miphOff+8))
	miphHeader := int(readUint32LE(payload, miphOff+4))
	offset := miphOff + miphHeader
	end := miphOff + miphTotal
	for offset+12 <= end {
		tag := readTag(payload, offset)
		if tag == "" {
			break
		}
		hlen := int(readUint32LE(payload, offset+4))
		tlen := int(readUint32LE(payload, offset+8))
		span := hlen
		if tag == "mhoh" && tlen > hlen {
			span = tlen
		}
		if tag == "mtph" && readUint32LE(payload, offset+24) == tid {
			out := spliceReplace(payload, offset, offset+span, nil)
			// Decrement the miph declared count at +16.
			d := readUint32LE(out, miphOff+16)
			binary.LittleEndian.PutUint32(out[miphOff+16:miphOff+20], d-1)
			return out
		}
		offset += span
	}
	return payload
}

// firstMhohOffsetIn is firstMhohOffset without the *testing.T (for chained
// mutations inside a single test).
func firstMhohOffsetIn(payload []byte, hohmType uint32) int {
	found := -1
	forEachMhoh(payload, func(off, _ int) {
		if found >= 0 {
			return
		}
		if hohmType == 0 || readUint32LE(payload, off+12) == hohmType {
			found = off
		}
	})
	return found
}
