// file: internal/itunes/itl_safety_contract.go
// version: 1.2.0
// guid: 404bbed1-87ba-4e56-b9e4-a492a2281163
// last-edited: 2026-07-03
//
// ITLSafetyContract — the iTunes writeback write-safety contract (fable5 TASK-003).
//
// This file implements SPEC 2 §2 (docs/specs/fable5-spec-itunes-writeback-hardening.md):
// an ordered list of named, individually-testable guards that run over a
// (before, after) pair of DECOMPRESSED little-endian ITL payloads plus the
// proposed hdfm header. The contract is DETECTION ONLY — no byte written to
// disk, no writer behavior changed. SafeWriteITL (TASK-004) wires it in.
//
// Why this exists: a corrupt .itl destroys years of curated iTunes playlists.
// Four production libraries were renamed "(Damaged)" by iTunes; the empirical
// corruption classes (K1..K12, SPEC §1) are guarded here. Each guard cites the
// K-number(s) it catches in its doc comment.
//
// Every guard returns a structured GuardResult with per-Violation offset/chunk/
// message — never a bare bool — because auditability is part of the contract
// (SPEC §2). All guards must pass before any byte reaches disk.

package itunes

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Contract types (NORMATIVE — SPEC 2 §2)
// ---------------------------------------------------------------------------

// Violation is a single structured finding from a guard. Offset is the payload
// byte offset of the offending chunk (or -1 when not byte-addressable, e.g. a
// header-vs-payload count mismatch). Chunk is the 4-byte chunk tag the problem
// was found in ("mhoh", "mith", "miph", "hdfm", ...).
type Violation struct {
	Offset  int
	Chunk   string
	Message string
}

// GuardResult is the outcome of one guard. Violations empty == pass.
type GuardResult struct {
	Guard      string // stable name, e.g. "mhoh-format" — normative for tests
	Violations []Violation
}

// Pass reports whether the guard found no violations.
func (r GuardResult) Pass() bool { return len(r.Violations) == 0 }

// ContractSummary captures coarse counts for both payloads, surfaced in the
// verdict for audit logs / diagnostics.
type ContractSummary struct {
	BeforeTracks    int
	AfterTracks     int
	AfterPlaylists  int
	AfterMhohBlocks int
}

// ContractVerdict is the aggregate result of running every guard.
type ContractVerdict struct {
	Pass    bool
	Results []GuardResult
	Summary ContractSummary
}

// FailedGuards returns the names of guards that produced at least one violation.
func (v ContractVerdict) FailedGuards() []string {
	var names []string
	for _, r := range v.Results {
		if !r.Pass() {
			names = append(names, r.Guard)
		}
	}
	return names
}

// Error renders a stable, log-friendly summary of all violations. Returns "" if
// the verdict passed (so callers can do `if e := v.Error(); e != "" { ... }`).
func (v ContractVerdict) Error() string {
	if v.Pass {
		return ""
	}
	var b strings.Builder
	b.WriteString("ITLSafetyContract REJECTED write:")
	for _, r := range v.Results {
		if r.Pass() {
			continue
		}
		for _, viol := range r.Violations {
			fmt.Fprintf(&b, " [%s@%d/%s: %s]", r.Guard, viol.Offset, viol.Chunk, viol.Message)
		}
	}
	return b.String()
}

// ContractConfig holds the tunable guardrails (SPEC 2 §2, `bounded-delta`).
type ContractConfig struct {
	// RemovedTracksMax: a single writeback may not remove more than this many
	// tracks without Force. Default 5000.
	RemovedTracksMax int
	// RewrittenMhohPctMax: a single writeback may not rewrite more than this
	// percent of mhoh blocks without Force. Default 20.
	RewrittenMhohPctMax int
	// Force overrides the bounded-delta guardrail only. It does NOT disable any
	// structural guard — corruption is never opt-out.
	Force bool

	// ExpectedIdentity, when non-nil, arms the library-identity guard (K13):
	// the `after` payload must carry the same Library PID and retain at least
	// IdentityMinOverlapPct of the fingerprinted track-PID sample. SafeWriteITL
	// populates this from the .identity.json sidecar when present.
	ExpectedIdentity *LibraryIdentity
	// IdentityMinOverlapPct is the minimum percentage of ExpectedIdentity's
	// PID sample that must be present in `after`. Default 90.
	IdentityMinOverlapPct int
	// AdoptLibrary is the explicit operator acknowledgment that the library at
	// this path is intentionally a different library (fresh iTunes rebuild,
	// deliberate baseline swap). It bypasses the library-identity guard for
	// ONE write, after which the new identity is fingerprinted. Unlike Force
	// it never affects any other guard.
	AdoptLibrary bool

	// ExpectedTrackCount, when > 0, arms the expected-magnitude guard (K14):
	// the `after` track count must be within MagnitudeTolerancePct of it.
	// Callers derive it from external truth (the DB's synced-book count, or
	// the identity sidecar's TrackCount).
	ExpectedTrackCount int
	// MagnitudeTolerancePct is the allowed deviation for expected-magnitude.
	// Default 10.
	MagnitudeTolerancePct int
}

// DefaultContractConfig returns the SPEC-mandated defaults.
func DefaultContractConfig() ContractConfig {
	return ContractConfig{
		RemovedTracksMax:    5000,
		RewrittenMhohPctMax: 20,
		Force:               false,
	}
}

// guardFn is the pure-function shape every guard satisfies. `before` may be nil
// in single-library audit mode (AuditITL).
type guardFn func(before, after []byte, hdr *hdfmHeader, cfg ContractConfig) GuardResult

// orderedGuards lists guards cheapest-first; the order is normative (SPEC §2).
func orderedGuards() []guardFn {
	return []guardFn{
		guardParseRoundtrip,
		guardContainerTiling,
		guardCountCoherence,
		guardNoNewDanglingRefs,
		guardMhohFormat,
		guardLocationForm,
		guardTidPidSanity,
		guardExpectedMagnitude,
		guardLibraryIdentity,
		guardBoundedDelta,
	}
}

// ---------------------------------------------------------------------------
// Public entry points
// ---------------------------------------------------------------------------

// RunSafetyContract runs every guard over the (before, after) decompressed LE
// payloads and the proposed hdfm header, returning the aggregate verdict.
//
// `after` is the proposed payload that would be written; `before` is the
// last-known-good payload (may be nil in audit mode). `hdr` is the proposed
// hdfm header carrying the BE count fields at file offsets 0x44/0x48/0x4C/0x54
// — pass the header that WILL be written so K2 (stale-count) is checkable
// before encryption/compression. cfg may be the zero value, in which case the
// SPEC defaults are applied.
func RunSafetyContract(before, after []byte, hdr *hdfmHeader, cfg ContractConfig) ContractVerdict {
	cfg = normalizeConfig(cfg)

	verdict := ContractVerdict{Pass: true, Summary: summarize(before, after)}
	for _, g := range orderedGuards() {
		res := g(before, after, hdr, cfg)
		verdict.Results = append(verdict.Results, res)
		if !res.Pass() {
			verdict.Pass = false
		}
	}
	return verdict
}

// AuditITL runs the contract in single-library mode (before == nil) against an
// already-on-disk library, closing HIGH-6: a library corrupted by an old writer
// (K5 carrier) is detectable at read time. `data` is the raw .itl file bytes;
// it is parsed (decrypt + fail-closed inflate) here. The bounded-delta and
// no-new-dangling-refs guards degrade gracefully with no `before` (they only
// assert absolute properties of `after`).
func AuditITL(data []byte) ContractVerdict {
	hdr, payload, err := decodeITLForContract(data)
	if err != nil {
		// Fail closed: an un-decodable library is a violation, not a pass.
		return ContractVerdict{
			Pass: false,
			Results: []GuardResult{{
				Guard: "parse-roundtrip",
				Violations: []Violation{{
					Offset: 0, Chunk: "hdfm",
					Message: fmt.Sprintf("library does not decode: %v", err),
				}},
			}},
		}
	}
	return RunSafetyContract(nil, payload, hdr, DefaultContractConfig())
}

// ---------------------------------------------------------------------------
// Guard: parse-roundtrip  (catches MED-7, gross corruption)
// ---------------------------------------------------------------------------

// guardParseRoundtrip asserts `after` is a recognizable LE payload whose master
// track list can be located. It FAILS CLOSED (inverting the historic fail-open
// behavior of VerifyITLNoDanglingRefsLE, MED-7) when the payload is not LE, the
// master-list msdh is unlocatable, or no tracks are present — precisely the
// states where a library is most damaged.
//
// Catches: MED-7 (silent inflate failure → garbage parse), and gross splice
// corruption that destroys the msdh framing. Also enforces the BE-refusal
// posture (K12): a BE payload does not begin with "msdh", so it fails here.
func guardParseRoundtrip(_, after []byte, _ *hdfmHeader, _ ContractConfig) GuardResult {
	const name = "parse-roundtrip"
	if len(after) < 16 {
		return fail(name, 0, "", "payload too small to be a valid ITL payload")
	}
	if !detectLE(after) {
		// BE or corrupt: refuse. The contract is LE-only; BE writes are refused
		// rather than guarded (SPEC K12 / LOW-2).
		return fail(name, 0, readTag(after, 0), "payload is not little-endian (does not start with 'msdh'); BE writeback is refused")
	}
	msdhOffset, _, _ := findMsdhByType(after, 1)
	if msdhOffset < 0 {
		return fail(name, 0, "msdh", "master track-list msdh (blockType 1) not locatable — fail closed (MED-7)")
	}
	tids := CollectMasterTrackIDsLE(after)
	if tids == nil {
		return fail(name, msdhOffset, "msdh", "master track list unparseable — fail closed (MED-7)")
	}
	if len(tids) == 0 {
		return fail(name, msdhOffset, "mlth", "master track list parsed to zero tracks — fail closed")
	}
	return pass(name)
}

// ---------------------------------------------------------------------------
// Guard: container-tiling  (catches truncation/splice errors)
// ---------------------------------------------------------------------------

// guardContainerTiling asserts the top-level msdh containers tile the payload
// exactly: every container's totalLen is sane, they are contiguous with no gap
// or overlap, and they cover the payload to its end. It also descends into the
// track (type 1) and playlist (type 2) containers and verifies their immediate
// children walk to contentEnd with no trailing gap.
//
// Catches: truncation and splice errors that leave a hole or overrun (the class
// behind a shrunk msdh totalLen — see TestContract_TruncatedContainer).
func guardContainerTiling(_, after []byte, _ *hdfmHeader, _ ContractConfig) GuardResult {
	const name = "container-tiling"
	var viol []Violation

	offset := 0
	for offset+16 <= len(after) {
		tag := readTag(after, offset)
		if tag != "msdh" {
			viol = append(viol, Violation{Offset: offset, Chunk: tag, Message: fmt.Sprintf("expected 'msdh' container at offset %d, found %q", offset, tag)})
			break
		}
		headerLen := int(readUint32LE(after, offset+4))
		totalLen := int(readUint32LE(after, offset+8))
		blockType := int(readUint32LE(after, offset+12))

		if totalLen < 16 {
			viol = append(viol, Violation{Offset: offset, Chunk: "msdh", Message: fmt.Sprintf("msdh totalLen %d < 16", totalLen)})
			break
		}
		if headerLen < 16 || headerLen > totalLen {
			viol = append(viol, Violation{Offset: offset, Chunk: "msdh", Message: fmt.Sprintf("msdh headerLen %d out of range (totalLen %d)", headerLen, totalLen)})
			break
		}
		if offset+totalLen > len(after) {
			viol = append(viol, Violation{Offset: offset, Chunk: "msdh", Message: fmt.Sprintf("msdh totalLen %d overruns payload end (offset %d, len %d)", totalLen, offset, len(after))})
			break
		}

		// Verify the immediate children of types 1 and 2 tile [contentStart,contentEnd).
		if blockType == 1 || blockType == 2 {
			if gapOff, ok := childWalkGap(after, offset+headerLen, offset+totalLen); !ok {
				viol = append(viol, Violation{Offset: gapOff, Chunk: "msdh", Message: fmt.Sprintf("child chunks of msdh type %d do not tile to contentEnd (gap/overrun near offset %d)", blockType, gapOff)})
			}
		}

		offset += totalLen
	}

	if offset != len(after) && len(viol) == 0 {
		viol = append(viol, Violation{Offset: offset, Chunk: "msdh", Message: fmt.Sprintf("msdh containers do not tile payload exactly: covered %d of %d bytes", offset, len(after))})
	}

	return GuardResult{Guard: name, Violations: viol}
}

// childWalkGap walks the chunks in [start,end) advancing by each chunk's span
// (totalLen for containers that carry children, else headerLen) and reports
// whether the walk reaches exactly `end`. Returns (gapOffset, ok).
func childWalkGap(data []byte, start, end int) (int, bool) {
	offset := start
	for offset+12 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			return offset, false
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah" || tag == "miph") && totalLen > headerLen && offset+totalLen <= end {
			span = totalLen
		}
		if span < 8 || offset+span > end {
			return offset, false
		}
		offset += span
	}
	return offset, offset == end
}

// ---------------------------------------------------------------------------
// Guard: count-coherence  (catches K2, K8 — the CRIT-3 header desync)
// ---------------------------------------------------------------------------

// guardCountCoherence asserts the proposed hdfm header's BE count fields agree
// with the actual payload, and that every miph's declared item count matches its
// actual mtph children. This is the guard for CRIT-3 / K2: damaged-1/2 carried a
// header that said 90,900 tracks while the payload had 90,898.
//
//	header @0x44 (BE) == mlth track count == actual mith blocks
//	header @0x48 (BE) == miph (playlist) count
//	header @0x4C (BE) == miah (album) count
//	header @0x54 (BE) == miih (artist) count
//	per-miph declared count (+16) == actual mtph children   (K8)
//
// The header offsets are file-absolute; we reconstruct the full hdfm bytes from
// the proposed header so the same 0x44/0x48/0x4C/0x54 offsets used everywhere in
// the spec apply directly.
func guardCountCoherence(_, after []byte, hdr *hdfmHeader, _ ContractConfig) GuardResult {
	const name = "count-coherence"
	var viol []Violation

	actualTracks, actualMith := countMasterTracks(after)
	actualPlaylists, miphViol := countPlaylistsAndCheckMiph(after)
	actualAlbums := countMsdhItems(after, 9, "miah")
	actualArtists := countMsdhItems(after, 11, "miih")

	if actualTracks != actualMith {
		viol = append(viol, Violation{Offset: -1, Chunk: "mlth", Message: fmt.Sprintf("mlth count %d != actual mith blocks %d", actualTracks, actualMith)})
	}

	if hdr != nil {
		full := reconstructHdfmHeader(hdr)
		check := func(off int, field string, want int) {
			if off+4 > len(full) {
				return // header too short to carry this field; not our class to flag
			}
			got := int(readUint32BE(full, off))
			if got != want {
				viol = append(viol, Violation{Offset: -1, Chunk: "hdfm", Message: fmt.Sprintf("header @0x%X %s=%d != payload count %d (K2/CRIT-3)", off, field, got, want)})
			}
		}
		check(0x44, "tracks", actualMith)
		check(0x48, "playlists", actualPlaylists)
		check(0x4C, "albums", actualAlbums)
		check(0x54, "artists", actualArtists)
	}

	viol = append(viol, miphViol...)
	return GuardResult{Guard: name, Violations: viol}
}

// ---------------------------------------------------------------------------
// Guard: no-new-dangling-refs  (catches K1 — wraps existing verifier, fail-closed)
// ---------------------------------------------------------------------------

// guardNoNewDanglingRefs wraps the existing VerifyITLNoNewDanglingRefsLE
// (itl_le_verify.go) and converts it to fail-closed (MED-7): if the master list
// cannot be collected from `after`, that is itself a violation here rather than
// a silent pass. In audit mode (before == nil) it asserts the absolute property
// that `after` carries no dangling mtph refs at all.
//
// Catches: K1 — orphan mtph items left behind when mith blocks are excised
// (the May-2026 corruption class; damaged-1 had 6 orphans across 3 miph parents).
func guardNoNewDanglingRefs(before, after []byte, _ *hdfmHeader, _ ContractConfig) GuardResult {
	const name = "no-new-dangling-refs"

	tids := CollectMasterTrackIDsLE(after)
	if tids == nil {
		return fail(name, 0, "msdh", "master track list unlocatable — fail closed (was fail-open, MED-7)")
	}
	dangling := FindDanglingMtphRefsLE(after, tids)
	if len(dangling) == 0 {
		return pass(name)
	}

	// Subtract pre-existing orphans (iTunes tolerates a small number that were
	// already present in `before`); only NEW dangling refs are a violation.
	preExisting := map[uint32]struct{}{}
	if before != nil && detectLE(before) {
		if beforeTIDs := CollectMasterTrackIDsLE(before); beforeTIDs != nil {
			for _, tid := range FindDanglingMtphRefsLE(before, beforeTIDs) {
				preExisting[tid] = struct{}{}
			}
		}
	}

	var introduced []uint32
	for _, tid := range dangling {
		if _, ok := preExisting[tid]; !ok {
			introduced = append(introduced, tid)
		}
	}
	if len(introduced) == 0 {
		return pass(name)
	}
	sort.Slice(introduced, func(i, j int) bool { return introduced[i] < introduced[j] })
	return fail(name, -1, "mtph", fmt.Sprintf("%d new dangling playlist→track refs (e.g. TrackIDs %v)", len(introduced), previewU32(introduced)))
}

// ---------------------------------------------------------------------------
// Guard: mhoh-format  (catches K3, K5, K7 — the CRIT-1 encoding-flag class)
// ---------------------------------------------------------------------------

// guardMhohFormat enforces the per-block mhoh byte invariants iTunes-authored
// libraries satisfy (SPEC §2, §4; T002 corpus table ITunesMhohEncoding):
//
//	headerLen == 24            (K5: damaged-1 had ~60K blocks with headerLen 41–210)
//	byte +27   == 0x00         (K3: iTunes writes 0; our old encoder stamped 1/3)
//	totalLen   == 40 + strLen  (K7: length-prefix arithmetic)
//	bytes +32..+39 all zero
//	+24 ∈ ITunesMhohEncoding[type].AllowedAt24   (when the type is in the table)
//
// SCOPE — which mhoh types are checked, and why:
//   - The T002 corpus table ITunesMhohEncoding enumerates the TEXT-STRING mhoh
//     types. Types ABSENT from the table are binary-blob payloads (0x36 raw
//     audio, 0x65 SmartCriteria, 0x66 SmartInfo, ...) whose +24..+39 bytes are
//     NOT a string-encoding header — applying the format check to them yields
//     false positives. So format checks (headerLen/+27/len-arithmetic/tail-zero)
//     are applied only to types present in the table.
//   - Three IN-table types (0x15, 0x69, 0x6C) are documented in the table as
//     having a NON-STANDARD length layout (tail_zero=false; totalLen != 40+strLen
//     does not hold), yet their encoding indicator is text-style. For those we
//     skip the len-arithmetic + tail-zero checks but still enforce headerLen==24,
//     +27==0, and +24 ∈ AllowedAt24. This mirrors the corpus exactly.
func guardMhohFormat(_, after []byte, _ *hdfmHeader, _ ContractConfig) GuardResult {
	const name = "mhoh-format"
	var viol []Violation

	forEachMhoh(after, func(offset, span int) {
		hohmType := readUint32LE(after, offset+12)
		entry, inTable := ITunesMhohEncoding[hohmType]
		if !inTable {
			// Binary-blob mhoh type — not a string-encoding header; skip.
			return
		}

		headerLen := readUint32LE(after, offset+4)
		if headerLen != entry.HeaderLen {
			viol = append(viol, Violation{Offset: offset, Chunk: "mhoh", Message: fmt.Sprintf("type 0x%X headerLen=%d, want %d (K5)", hohmType, headerLen, entry.HeaderLen)})
		}

		// byte +27 must be 0x00 (K3): iTunes' encoding indicator lives at +24.
		if offset+28 <= len(after) && after[offset+27] != 0x00 {
			viol = append(viol, Violation{Offset: offset, Chunk: "mhoh", Message: fmt.Sprintf("type 0x%X byte +27=0x%02X, want 0x00 (K3: foreign encoding flag)", hohmType, after[offset+27])})
		}

		// +24 encoding indicator ∈ corpus-allowed set for this type.
		at24 := readUint32LE(after, offset+24)
		if !entry.AllowedAt24Contains(at24) {
			viol = append(viol, Violation{Offset: offset, Chunk: "mhoh", Message: fmt.Sprintf("type 0x%X +24 indicator=%d not in corpus set %v (K3)", hohmType, at24, entry.AllowedAt24)})
		}

		// Length arithmetic + tail-zero only for standard-layout text types.
		if !mhohNonStandardLen(hohmType) {
			strLen := int(readUint32LE(after, offset+28))
			totalLen := int(readUint32LE(after, offset+8))
			if strLen < 0 || offset+40+strLen > len(after) {
				viol = append(viol, Violation{Offset: offset, Chunk: "mhoh", Message: fmt.Sprintf("type 0x%X strLen=%d out of bounds (K7)", hohmType, strLen)})
			} else if totalLen != 40+strLen {
				viol = append(viol, Violation{Offset: offset, Chunk: "mhoh", Message: fmt.Sprintf("type 0x%X totalLen=%d != 40+strLen=%d (K7)", hohmType, totalLen, 40+strLen)})
			}
			// bytes +32..+39 must be zero.
			if offset+40 <= len(after) {
				for i := offset + 32; i < offset+40; i++ {
					if after[i] != 0 {
						viol = append(viol, Violation{Offset: offset, Chunk: "mhoh", Message: fmt.Sprintf("type 0x%X byte +%d=0x%02X, want 0x00 (reserved tail)", hohmType, i-offset, after[i])})
						break
					}
				}
			}
		}
	})

	return GuardResult{Guard: name, Violations: viol}
}

// mhohNonStandardLen reports whether a hohmType is one of the three corpus types
// (0x15, 0x69, 0x6C) documented in ITunesMhohEncoding as having a non-standard
// length layout (totalLen != 40+strLen, tail not zeroed). These are exempt from
// the length-arithmetic and tail-zero checks but not from headerLen/+24/+27.
func mhohNonStandardLen(hohmType uint32) bool {
	switch hohmType {
	case 0x15, 0x69, 0x6C:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Guard: location-form  (catches K4 + staging-dir leak — the CRIT-2 class)
// ---------------------------------------------------------------------------

// guardLocationForm enforces the NORMATIVE Location field contract (SPEC §1b),
// per-track, on DECODED strings (0x0D may be UTF-16-encoded — 1,736 of golden's
// 93,014 are):
//
//   - if a track has a 0x0D, it must decode to a Windows absolute path
//     (drive letter + ':\', backslashes, NO "file://", NO "%"-escapes), and its
//     sibling 0x0B must be a "file://localhost/" URL that round-trips to the same
//     path (\\→/, RFC-3986 escaping).
//   - tracks WITHOUT a 0x0D (podcast/stream entries — 1,187 in golden) may carry
//     any http(s):// URL in 0x0B and are exempt from the pairing rule.
//   - NO value (0x0B or 0x0D) may contain ".itunes-writeback/" or other staging
//     markers (damaged-4 leaked staging-dir paths).
//
// Catches: K4 (URL written into 0x0D) and the staging-path leak.
func guardLocationForm(before, after []byte, _ *hdfmHeader, _ ContractConfig) GuardResult {
	const name = "location-form"
	var viol []Violation

	// Pair-consistency (0x0B decodes to the same path as 0x0D) is a NO-NEW
	// check: the golden corpus proves iTunes itself leaves stale pairs behind
	// (e.g. a 0x0D missing "iTunes Media" while its 0x0B still has it), so an
	// absolute assertion fails known-good libraries forever (K16 follow-up).
	// Format checks below remain absolute. In audit mode (before == nil)
	// pre-existing mismatches are iTunes-authored warts, not our corruption.
	preMismatch := collectPairMismatchPaths(before)

	forEachTrackLocations(after, func(trackOffset int, loc0D, loc0B string, has0D, has0B bool) {
		// Staging-dir leak: applies to either field, present or not.
		for _, pair := range []struct {
			present bool
			val     string
			field   string
		}{{has0D, loc0D, "0x0D"}, {has0B, loc0B, "0x0B"}} {
			if pair.present && strings.Contains(pair.val, ".itunes-writeback/") {
				viol = append(viol, Violation{Offset: trackOffset, Chunk: "mhoh", Message: fmt.Sprintf("%s contains staging marker '.itunes-writeback/': %q", pair.field, truncStr(pair.val))})
			}
		}

		if !has0D {
			// Podcast/stream: 0x0B may hold http(s):// — no pairing requirement.
			return
		}

		// 0x0D must be a native Windows absolute path.
		if !isWindowsAbsPath(loc0D) {
			viol = append(viol, Violation{Offset: trackOffset, Chunk: "mhoh", Message: fmt.Sprintf("0x0D Location is not a Windows absolute path: %q (K4)", truncStr(loc0D))})
		}
		if strings.Contains(strings.ToLower(loc0D), "file://") {
			viol = append(viol, Violation{Offset: trackOffset, Chunk: "mhoh", Message: fmt.Sprintf("0x0D Location contains 'file://' — iTunes stores a native path here (K4): %q", truncStr(loc0D))})
		}
		// Only an actual %XX escape TRIPLE marks an escaped path leaking into
		// 0x0D
		// — real filenames legitimately contain bare '%' ("Still 100% Human",
		// "1% Lifesteal"; both present in the golden corpus).
		if containsEscapeTriple(loc0D) {
			viol = append(viol, Violation{Offset: trackOffset, Chunk: "mhoh", Message: fmt.Sprintf("0x0D Location contains '%%XX'-escape — native path must be unescaped (K4): %q", truncStr(loc0D))})
		}

		// Sibling 0x0B must be a round-tripping file://localhost/ URL.
		if !has0B {
			viol = append(viol, Violation{Offset: trackOffset, Chunk: "mhoh", Message: "track with 0x0D Location is missing its sibling 0x0B LocalURL"})
			return
		}
		if !strings.HasPrefix(loc0B, "file://localhost/") {
			viol = append(viol, Violation{Offset: trackOffset, Chunk: "mhoh", Message: fmt.Sprintf("0x0B LocalURL is not a 'file://localhost/' URL: %q", truncStr(loc0B))})
			return
		}
		// Round-trip comparison is SEMANTIC (percent-decoded), not byte-exact:
		// iTunes leaves RFC-3986 sub-delims like '(' ')' ',' unescaped in 0x0B
		// while winPathToLocalURL escapes them — both render the same path
		// (K16 follow-up: 22,902 golden blocks differ only in escape strictness).
		// And it is a NO-NEW check: mismatched pairs already in `before` (or, in
		// audit mode, in the library itself) are iTunes-authored, not ours.
		if want := winPathToLocalURL(loc0D); want != loc0B && !localURLsEquivalent(want, loc0B) {
			if _, preexisting := preMismatch[loc0D]; !preexisting && before != nil {
				viol = append(viol, Violation{Offset: trackOffset, Chunk: "mhoh", Message: fmt.Sprintf("0x0B LocalURL does not round-trip 0x0D path (newly introduced): got %q, want %q", truncStr(loc0B), truncStr(want))})
			}
		}
	})

	return GuardResult{Guard: name, Violations: viol}
}

// localURLsEquivalent reports whether two file://localhost/ URLs decode to the
// same path — equal after percent-decoding. WHY: escape STRICTNESS varies
// (iTunes leaves '(' ')' ',' etc. raw; RFC-3986 encoders escape them) but the
// decoded path is the identity that matters. Malformed escapes fail closed.
func localURLsEquivalent(a, b string) bool {
	da, errA := url.PathUnescape(a)
	db, errB := url.PathUnescape(b)
	if errA != nil || errB != nil {
		return false
	}
	return da == db
}

// containsEscapeTriple reports whether s contains a %XX percent-escape triple —
// the K4 signature of an escaped URL leaking into the native-path 0x0D field.
// A bare '%' is NOT flagged: real filenames contain it ("Still 100% Human").
func containsEscapeTriple(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '%' && isHexDigit(s[i+1]) && isHexDigit(s[i+2]) {
			return true
		}
	}
	return false
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// collectPairMismatchPaths returns the set of 0x0D paths in `data` whose 0x0B
// sibling does not (semantically) round-trip. nil input → empty set.
func collectPairMismatchPaths(data []byte) map[string]struct{} {
	out := make(map[string]struct{})
	if data == nil {
		return out
	}
	forEachTrackLocations(data, func(_ int, loc0D, loc0B string, has0D, has0B bool) {
		if !has0D || !has0B {
			return
		}
		if want := winPathToLocalURL(loc0D); want != loc0B && !localURLsEquivalent(want, loc0B) {
			out[loc0D] = struct{}{}
		}
	})
	return out
}

// ---------------------------------------------------------------------------
// Guard: tid-pid-sanity  (catches K6, K9, K11)
// ---------------------------------------------------------------------------

// guardTidPidSanity asserts the master track list has strictly ascending,
// unique TrackIDs and unique, nonzero persistent IDs (PIDs). golden is
// TID-sorted and duplicate-free; this is cheap to keep as a primary assertion.
//
// Catches: K6 (TID gaps/duplicates), K9 (PID collisions), K11 (mith ordering).
func guardTidPidSanity(_, after []byte, _ *hdfmHeader, _ ContractConfig) GuardResult {
	const name = "tid-pid-sanity"
	var viol []Violation

	tids, pids := collectMithTidsPids(after)

	prev := int64(-1)
	for _, t := range tids {
		if int64(t) <= prev {
			viol = append(viol, Violation{Offset: -1, Chunk: "mith", Message: fmt.Sprintf("TrackIDs not strictly ascending/unique: %d follows %d (K6/K11)", t, prev)})
			break
		}
		prev = int64(t)
	}

	seen := make(map[string]struct{}, len(pids))
	for _, p := range pids {
		if p == "0000000000000000" {
			viol = append(viol, Violation{Offset: -1, Chunk: "mith", Message: "track has zero persistent ID (K9)"})
			break
		}
		if _, dup := seen[p]; dup {
			viol = append(viol, Violation{Offset: -1, Chunk: "mith", Message: fmt.Sprintf("duplicate persistent ID %s (K9)", p)})
			break
		}
		seen[p] = struct{}{}
	}

	return GuardResult{Guard: name, Violations: viol}
}

// ---------------------------------------------------------------------------
// Guard: expected-magnitude  (K14 — external truth anchor for counts)
// ---------------------------------------------------------------------------

// guardExpectedMagnitude asserts the `after` track count is within
// cfg.MagnitudeTolerancePct of cfg.ExpectedTrackCount. Unlike count-coherence
// (which is self-referential — the header is regenerated from the same payload
// it is compared to), the expected count comes from OUTSIDE the file: the DB's
// synced-book census or the identity sidecar. Disarmed (vacuous pass) when
// ExpectedTrackCount <= 0.
//
// Catches: K14 — a structurally-perfect library that is simply the wrong size
// (rebuild from an under-populated DB; iTunes-authored fresh library; the
// July 2026 374-track cloud stub, a 99.6% shrink that passed all 8 guards).
func guardExpectedMagnitude(_, after []byte, _ *hdfmHeader, cfg ContractConfig) GuardResult {
	const name = "expected-magnitude"
	if cfg.ExpectedTrackCount <= 0 {
		return pass(name)
	}
	_, actual := countMasterTracks(after)
	deviation := actual - cfg.ExpectedTrackCount
	if deviation < 0 {
		deviation = -deviation
	}
	if deviation*100 > cfg.ExpectedTrackCount*cfg.MagnitudeTolerancePct {
		return GuardResult{Guard: name, Violations: []Violation{{
			Offset: -1, Chunk: "msdh",
			Message: fmt.Sprintf("track count %d deviates from expected %d by more than %d%% (K14)",
				actual, cfg.ExpectedTrackCount, cfg.MagnitudeTolerancePct),
		}}}
	}
	return pass(name)
}

// ---------------------------------------------------------------------------
// Guard: library-identity  (K13 — continuity with the last known library)
// ---------------------------------------------------------------------------

// guardLibraryIdentity asserts the `after` payload is a continuation of the
// fingerprinted library in cfg.ExpectedIdentity: same Library Persistent ID
// in the proposed header, and at least cfg.IdentityMinOverlapPct of the
// fingerprint's track-PID sample still present. Disarmed when
// ExpectedIdentity is nil (first run, no sidecar) or cfg.AdoptLibrary is set
// (explicit operator baseline swap).
//
// Catches: K13 — "same path, different library": iTunes recreating a fresh
// library after a failed load (same Library PID is NOT sufficient — the July
// 2026 stub kept the Library PID with zero track-PID overlap), a wrong file
// copied over the staging target, or a mutation that silently replaced the
// track population. Structural guards cannot see this class at all.
func guardLibraryIdentity(_, after []byte, hdr *hdfmHeader, cfg ContractConfig) GuardResult {
	const name = "library-identity"
	if cfg.ExpectedIdentity == nil || cfg.AdoptLibrary {
		return pass(name)
	}
	var viol []Violation

	if want := cfg.ExpectedIdentity.LibraryPID; want != "" {
		if got := ExtractLibraryPIDHex(hdr); got != "" && got != want {
			viol = append(viol, Violation{
				Offset: -1, Chunk: "hdfm",
				Message: fmt.Sprintf("library PID changed: %s != expected %s (K13); use AdoptLibrary to bless a new library", got, want),
			})
		}
	}

	switch overlap := cfg.ExpectedIdentity.SampleOverlapPct(after); {
	case overlap < 0:
		// Cannot assess (empty sample or unlocatable master list). The master
		// list case is already a parse-roundtrip failure; an empty sample means
		// the fingerprint itself is degenerate — fail closed rather than let
		// every future write "match" a vacuous anchor.
		viol = append(viol, Violation{
			Offset: -1, Chunk: "mith",
			Message: "identity fingerprint unassessable (empty PID sample or unlocatable master list) (K13)",
		})
	case overlap < cfg.IdentityMinOverlapPct:
		viol = append(viol, Violation{
			Offset: -1, Chunk: "mith",
			Message: fmt.Sprintf("track-PID overlap %d%% below required %d%%: library population replaced (K13); use AdoptLibrary to bless a new library", overlap, cfg.IdentityMinOverlapPct),
		})
	}

	return GuardResult{Guard: name, Violations: viol}
}

// ---------------------------------------------------------------------------
// Guard: bounded-delta  (blast-radius cap for HIGH-3-style bugs)
// ---------------------------------------------------------------------------

// guardBoundedDelta is the guardrail: a single writeback may not remove more
// than cfg.RemovedTracksMax tracks (default 5000) nor rewrite more than
// cfg.RewrittenMhohPctMax percent of mhoh blocks (default 20) unless cfg.Force.
// In audit mode (before == nil) there is no delta to bound, so it passes.
//
// Catches: the blast-radius class behind HIGH-3 (writeback that re-stamps nearly
// the whole library every sync) — a runaway removal or rewrite is refused before
// it reaches disk.
func guardBoundedDelta(before, after []byte, _ *hdfmHeader, cfg ContractConfig) GuardResult {
	const name = "bounded-delta"
	if before == nil || cfg.Force {
		return pass(name)
	}
	var viol []Violation

	beforeTracks, _ := countMasterTracks(before)
	afterTracks, _ := countMasterTracks(after)
	removed := beforeTracks - afterTracks
	if removed > cfg.RemovedTracksMax {
		viol = append(viol, Violation{Offset: -1, Chunk: "mith", Message: fmt.Sprintf("writeback removes %d tracks > cap %d (set Force to override)", removed, cfg.RemovedTracksMax)})
	}

	beforeMhoh := countMhohBlocks(before)
	if beforeMhoh > 0 {
		rewritten := mhohRewriteCount(before, after)
		pct := rewritten * 100 / beforeMhoh
		if pct > cfg.RewrittenMhohPctMax {
			viol = append(viol, Violation{Offset: -1, Chunk: "mhoh", Message: fmt.Sprintf("writeback rewrites %d%% of mhoh blocks > cap %d%% (set Force to override)", pct, cfg.RewrittenMhohPctMax)})
		}
	}

	return GuardResult{Guard: name, Violations: viol}
}

// ---------------------------------------------------------------------------
// Helpers — payload walking
// ---------------------------------------------------------------------------

// forEachMhoh invokes fn(offset, span) for every mhoh block in the payload,
// descending into msdh containers (types 1, 2, 9, 11) and their mith/miah/miph
// children. span is the chunk's full byte length (totalLen for containers).
func forEachMhoh(data []byte, fn func(offset, span int)) {
	walkTopMsdh(data, func(msdhOffset, headerLen, totalLen, blockType int) {
		walkChunksForMhoh(data, msdhOffset+headerLen, msdhOffset+totalLen, fn)
	})
}

// walkChunksForMhoh recursively walks [start,end) emitting every mhoh and
// descending into container chunks that carry children.
func walkChunksForMhoh(data []byte, start, end int, fn func(offset, span int)) {
	offset := start
	for offset+12 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			return
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		isContainer := (tag == "mith" || tag == "mhoh" || tag == "miah" || tag == "miph") && totalLen > headerLen && offset+totalLen <= end
		if isContainer {
			span = totalLen
		}
		if span < 8 || offset+span > end {
			return
		}
		if tag == "mhoh" {
			fn(offset, span)
		} else if isContainer && headerLen >= 8 && headerLen < span {
			// Descend past the fixed header into children (mith→mhoh, miph→mhoh).
			walkChunksForMhoh(data, offset+headerLen, offset+span, fn)
		}
		offset += span
	}
}

// walkTopMsdh invokes fn for each top-level msdh container.
func walkTopMsdh(data []byte, fn func(offset, headerLen, totalLen, blockType int)) {
	offset := 0
	for offset+16 <= len(data) {
		if readTag(data, offset) != "msdh" {
			return
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		blockType := int(readUint32LE(data, offset+12))
		if totalLen < 16 || headerLen < 16 || headerLen > totalLen || offset+totalLen > len(data) {
			return
		}
		fn(offset, headerLen, totalLen, blockType)
		offset += totalLen
	}
}

// countMasterTracks returns (mlth declared count, actual mith block count).
func countMasterTracks(data []byte) (declared, actual int) {
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		return 0, 0
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen
	if contentEnd > len(data) {
		contentEnd = len(data)
	}
	offset := contentStart
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlth" {
		declared = int(readUint32LE(data, contentStart+8))
		offset = contentStart + int(readUint32LE(data, contentStart+4))
	}
	for offset+12 <= contentEnd {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah") && totalLen > headerLen && offset+totalLen <= contentEnd {
			span = totalLen
		}
		if span < 8 || offset+span > contentEnd {
			break
		}
		if tag == "mith" {
			actual++
		}
		offset += span
	}
	return declared, actual
}

// countPlaylistsAndCheckMiph counts miph blocks in the playlist msdh (type 2)
// and, for each, compares its declared item count (+16) to its actual mtph
// children — emitting a violation per mismatch (K8).
func countPlaylistsAndCheckMiph(data []byte) (playlists int, viol []Violation) {
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 2)
	if msdhOffset < 0 {
		return 0, nil
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen
	if contentEnd > len(data) {
		contentEnd = len(data)
	}
	offset := contentStart
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlph" {
		offset = contentStart + int(readUint32LE(data, contentStart+4))
	}
	for offset+12 <= contentEnd {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		if (tag == "miph" || tag == "mith" || tag == "mhoh" || tag == "miah") && totalLen > headerLen && offset+totalLen <= contentEnd {
			span = totalLen
		}
		if span < 8 || offset+span > contentEnd {
			break
		}
		if tag == "miph" {
			playlists++
			declared := int(readUint32LE(data, offset+16))
			actual := countMtphChildren(data, offset+headerLen, offset+span)
			if declared != actual {
				viol = append(viol, Violation{Offset: offset, Chunk: "miph", Message: fmt.Sprintf("miph declared item count %d != actual mtph children %d (K8)", declared, actual)})
			}
		}
		offset += span
	}
	return playlists, viol
}

// countMtphChildren counts mtph items directly under a miph in [start,end).
func countMtphChildren(data []byte, start, end int) int {
	n := 0
	offset := start
	for offset+12 <= end {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		if (tag == "mhoh" || tag == "miph") && totalLen > headerLen && offset+totalLen <= end {
			span = totalLen
		}
		if span < 8 || offset+span > end {
			break
		}
		if tag == "mtph" {
			n++
		}
		offset += span
	}
	return n
}

// countMsdhItems counts the top-level item chunks (itemTag) inside the msdh of
// the given blockType. Used for album (type 9, miah) and artist (type 11, miih)
// counts; returns 0 when the container is absent (older libraries lack them).
func countMsdhItems(data []byte, blockType int, itemTag string) int {
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, blockType)
	if msdhOffset < 0 {
		return 0
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen
	if contentEnd > len(data) {
		contentEnd = len(data)
	}
	n := 0
	offset := contentStart
	// Skip a leading list-header chunk if present (mlah/mlih etc.).
	if contentStart+12 <= contentEnd {
		if first := readTag(data, contentStart); first != "" && first != itemTag {
			offset = contentStart + int(readUint32LE(data, contentStart+4))
		}
	}
	for offset+12 <= contentEnd {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		if (tag == itemTag || tag == "mhoh") && totalLen > headerLen && offset+totalLen <= contentEnd {
			span = totalLen
		}
		if span < 8 || offset+span > contentEnd {
			break
		}
		if tag == itemTag {
			n++
		}
		offset += span
	}
	return n
}

// countMhohBlocks returns the total number of mhoh blocks in the payload.
func countMhohBlocks(data []byte) int {
	n := 0
	forEachMhoh(data, func(_, _ int) { n++ })
	return n
}

// mhohRewriteCount counts how many mhoh blocks were rewritten (content changed)
// between before and after, matched by IDENTITY — (enclosing track TID, hohmType)
// for track mhohs — so that merely removing or adding tracks does not count
// surviving blocks as rewrites. Blocks present in both with differing bytes are
// rewrites; blocks only in `after` (newly added) are also counted. This is a
// blast-radius proxy for the bounded-delta guardrail; exactness is not required.
func mhohRewriteCount(before, after []byte) int {
	b := mhohBlocksByIdentity(before)
	n := 0
	for key, ablk := range mhohBlocksByIdentity(after) {
		bblk, ok := b[key]
		if !ok || !bytesEqual(bblk, ablk) {
			n++
		}
	}
	return n
}

// mhohBlocksByIdentity returns track-mhoh blocks keyed by "TID:hohmType". Only
// mhohs nested under a mith carry a stable identity; container-level mhohs
// (playlist names etc.) are keyed by their walk index to stay deterministic.
func mhohBlocksByIdentity(data []byte) map[string][]byte {
	out := map[string][]byte{}
	idx := 0
	walkTopMsdh(data, func(msdhOffset, headerLen, totalLen, blockType int) {
		// Walk top children of this msdh; for mith, key its child mhohs by TID.
		offset := msdhOffset + headerLen
		end := msdhOffset + totalLen
		for offset+12 <= end {
			tag := readTag(data, offset)
			if tag == "" {
				break
			}
			hlen := int(readUint32LE(data, offset+4))
			tlen := int(readUint32LE(data, offset+8))
			span := hlen
			isContainer := (tag == "mith" || tag == "miph" || tag == "miah" || tag == "mhoh") && tlen > hlen && offset+tlen <= end
			if isContainer {
				span = tlen
			}
			if span < 8 || offset+span > end {
				break
			}
			switch {
			case tag == "mith" && isContainer:
				tid := readUint32LE(data, offset+16)
				walkChunksForMhoh(data, offset+hlen, offset+span, func(mhohOff, mhohSpan int) {
					ht := readUint32LE(data, mhohOff+12)
					key := fmtUint(tid) + ":" + fmtUint(ht)
					out[key] = data[mhohOff : mhohOff+mhohSpan]
				})
			case tag == "mhoh":
				out["idx:"+fmtUint(uint32(idx))] = data[offset : offset+span]
				idx++
			case isContainer:
				walkChunksForMhoh(data, offset+hlen, offset+span, func(mhohOff, mhohSpan int) {
					out["idx:"+fmtUint(uint32(idx))] = data[mhohOff : mhohOff+mhohSpan]
					idx++
				})
			}
			offset += span
		}
	})
	return out
}

func fmtUint(v uint32) string { return fmt.Sprintf("%d", v) }

// collectMithTidsPids returns the TrackIDs (in walk order) and hex PIDs of every
// mith in the master track list.
func collectMithTidsPids(data []byte) (tids []uint32, pids []string) {
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		return nil, nil
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen
	if contentEnd > len(data) {
		contentEnd = len(data)
	}
	offset := contentStart
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlth" {
		offset = contentStart + int(readUint32LE(data, contentStart+4))
	}
	for offset+12 <= contentEnd {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		if (tag == "mith" || tag == "mhoh" || tag == "miah") && totalLen > headerLen && offset+totalLen <= contentEnd {
			span = totalLen
		}
		if span < 8 || offset+span > contentEnd {
			break
		}
		if tag == "mith" {
			tids = append(tids, readUint32LE(data, offset+16))
			var pid [8]byte
			if offset+136 <= len(data) {
				for i := 0; i < 8; i++ {
					pid[i] = data[offset+135-i]
				}
			}
			pids = append(pids, pidToHex(pid))
		}
		offset += span
	}
	return tids, pids
}

// forEachTrackLocations invokes fn for every mith in the master list, passing
// the DECODED 0x0D Location and 0x0B LocalURL strings (and presence flags).
func forEachTrackLocations(data []byte, fn func(trackOffset int, loc0D, loc0B string, has0D, has0B bool)) {
	msdhOffset, msdhHeaderLen, msdhTotalLen := findMsdhByType(data, 1)
	if msdhOffset < 0 {
		return
	}
	contentStart := msdhOffset + msdhHeaderLen
	contentEnd := msdhOffset + msdhTotalLen
	if contentEnd > len(data) {
		contentEnd = len(data)
	}
	offset := contentStart
	if contentStart+12 <= contentEnd && readTag(data, contentStart) == "mlth" {
		offset = contentStart + int(readUint32LE(data, contentStart+4))
	}
	for offset+12 <= contentEnd {
		tag := readTag(data, offset)
		if tag == "" {
			break
		}
		headerLen := int(readUint32LE(data, offset+4))
		totalLen := int(readUint32LE(data, offset+8))
		span := headerLen
		isMithContainer := tag == "mith" && totalLen > headerLen && offset+totalLen <= contentEnd
		if (tag == "mith" || tag == "mhoh" || tag == "miah") && totalLen > headerLen && offset+totalLen <= contentEnd {
			span = totalLen
		}
		if span < 8 || offset+span > contentEnd {
			break
		}
		if tag == "mith" {
			var loc0D, loc0B string
			var has0D, has0B bool
			if isMithContainer {
				walkChunksForMhoh(data, offset+headerLen, offset+span, func(mhohOff, mhohSpan int) {
					t := readUint32LE(data, mhohOff+12)
					switch t {
					case 0x0D:
						loc0D, has0D = decodeMhohString(data, mhohOff, mhohSpan), true
					case 0x0B:
						loc0B, has0B = decodeMhohString(data, mhohOff, mhohSpan), true
					}
				})
			}
			fn(offset, loc0D, loc0B, has0D, has0B)
		}
		offset += span
	}
}

// decodeMhohString decodes the string carried by an mhoh block using the
// DUAL-convention decoder (TASK-005): +27!=0 → legacy flag at +27; +27==0 →
// corpus +24 indicator (1=latin1, 3=UTF-16LE). This makes location-form operate
// on the DECODED string regardless of how it was stamped (SPEC §2 "on decoded
// strings"), correctly handling iTunes-conformant UTF-16LE locations that the
// old +27-only reader would have mis-decoded as ASCII.
func decodeMhohString(data []byte, offset, span int) string {
	if span < 40 || offset+40 > len(data) {
		return ""
	}
	blockLen := span
	if offset+blockLen > len(data) {
		blockLen = len(data) - offset
	}
	s, err := decodeMhohBlock(data[offset : offset+blockLen])
	if err != nil {
		return ""
	}
	return s
}

// ---------------------------------------------------------------------------
// Helpers — location form
// ---------------------------------------------------------------------------

// isWindowsAbsPath reports whether s is a native Windows absolute path:
// drive letter, ':', backslash, no forward slashes (a single C:\ form). It does
// NOT permit "file://" or percent-escapes (those belong in 0x0B).
func isWindowsAbsPath(s string) bool {
	if len(s) < 3 {
		return false
	}
	c := s[0]
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
		return false
	}
	if s[1] != ':' || s[2] != '\\' {
		return false
	}
	if strings.Contains(s, "/") {
		return false
	}
	return true
}

// winPathToLocalURL renders a native Windows path into the 0x0B canonical form:
// "file://localhost/" + path with '\'→'/' and RFC-3986 percent-escaping of every
// byte outside the unreserved set (plus '/' and ':' which are kept literal,
// matching iTunes' 0x0B rendering, e.g. "file://localhost/W:/itunes/...").
func winPathToLocalURL(winPath string) string {
	slashed := strings.ReplaceAll(winPath, "\\", "/")
	var b strings.Builder
	b.WriteString("file://localhost/")
	for i := 0; i < len(slashed); i++ {
		c := slashed[i]
		if isURLKeepByte(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// isURLKeepByte reports whether c is left literal in the 0x0B URL rendering.
// Unreserved per RFC-3986 (ALPHA / DIGIT / '-' '.' '_' '~') plus the path
// separators iTunes keeps literal ('/' and ':').
func isURLKeepByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '-', '.', '_', '~', '/', ':':
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Helpers — header reconstruction / decode
// ---------------------------------------------------------------------------

// reconstructHdfmHeader rebuilds the full hdfm header bytes from a parsed header
// so the file-absolute BE count offsets 0x44/0x48/0x4C/0x54 used throughout the
// spec apply directly. The remainder already contains those count bytes; we just
// need the leading 17 + len(version) bytes in front of it.
func reconstructHdfmHeader(hdr *hdfmHeader) []byte {
	return buildHdfmHeader(hdr.version, hdr.headerRemainder, hdr.fileLen, hdr.unknown)
}

// decodeITLForContract parses, decrypts, and fail-closed-inflates a raw .itl
// file into (header, decompressed LE payload). It surfaces the T010 inflate
// error rather than silently treating an over-cap payload as uncompressed.
func decodeITLForContract(data []byte) (*hdfmHeader, []byte, error) {
	hdr, err := parseHdfmHeader(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parse hdfm: %w", err)
	}
	if int(hdr.headerLen) > len(data) {
		return nil, nil, fmt.Errorf("header length %d exceeds file size %d", hdr.headerLen, len(data))
	}
	decrypted := itlDecrypt(hdr, data[hdr.headerLen:])
	payload, _, err := itlInflate(decrypted)
	if err != nil {
		return nil, nil, fmt.Errorf("inflate payload: %w", err)
	}
	return hdr, payload, nil
}

// ---------------------------------------------------------------------------
// Small utilities
// ---------------------------------------------------------------------------

func normalizeConfig(cfg ContractConfig) ContractConfig {
	if cfg.RemovedTracksMax == 0 {
		cfg.RemovedTracksMax = 5000
	}
	if cfg.RewrittenMhohPctMax == 0 {
		cfg.RewrittenMhohPctMax = 20
	}
	if cfg.IdentityMinOverlapPct == 0 {
		cfg.IdentityMinOverlapPct = 90
	}
	if cfg.MagnitudeTolerancePct == 0 {
		cfg.MagnitudeTolerancePct = 10
	}
	return cfg
}

func summarize(before, after []byte) ContractSummary {
	var s ContractSummary
	if before != nil {
		s.BeforeTracks, _ = countMasterTracks(before)
	}
	if after != nil {
		_, s.AfterTracks = countMasterTracks(after)
		s.AfterPlaylists, _ = countPlaylistsAndCheckMiph(after)
		s.AfterMhohBlocks = countMhohBlocks(after)
	}
	return s
}

func pass(name string) GuardResult { return GuardResult{Guard: name} }

func fail(name string, offset int, chunk, msg string) GuardResult {
	return GuardResult{Guard: name, Violations: []Violation{{Offset: offset, Chunk: chunk, Message: msg}}}
}

func previewU32(s []uint32) []uint32 {
	const n = 5
	if len(s) > n {
		return s[:n]
	}
	return s
}

func truncStr(s string) string {
	const n = 120
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
