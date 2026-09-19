// file: internal/scanner/version_link.go
// version: 1.0.0
// guid: 3f4ff5e2-d0d7-45eb-ac42-18142fcf22cb
// last-edited: 2026-09-19

package scanner

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Same-folder version linking ("smart dedup").
//
// A version group holds different EDITIONS or COPIES of ONE book: the same
// content as an .m4b and as an .mp3, or a second copy of a file that landed
// beside the original. It does NOT hold the chapters of a single book. That
// distinction is not cosmetic: the web library list filters on
// is_primary_version=true (web/src/services/api.ts, memdb_reads.go:543/693),
// and the v2 bulk_metadata_fetch filter path always applies the same filter
// (server/handlers/metadata.go), so every non-primary member of a group is
// hidden from the library list and excluded from enrichment.
//
// Until 2026-09-19 the predicate was "same lowercased title in the same
// parent directory" and nothing else, which is chapter-invariant: 27 mp3
// chapter records of one book share a folder and a title, so they were
// linked as 27 "versions" of each other. Measured library-wide on
// 2026-09-19: 14,775 same-folder/same-title/SAME-format groups against 9
// same-folder/same-title/mixed-format groups, i.e. the feature's intended
// case was outnumbered 71:1 by its own false positives, and 2,586 groups
// had no primary at all. See
// .claude/notes/version-group-sibling-chapters-2026-09-19.md.
//
// The predicate below therefore requires positive evidence that two records
// are two copies of one WHOLE book, and refuses to link anything that reads
// as a positional part.

// versionLinkDurationTolerance is how far two copies' durations may differ
// and still be read as the same content (5%). Re-encodes and different rips
// of one release drift by a few seconds; they do not halve.
const versionLinkDurationTolerance = 0.05

// versionLinkIsPart reports whether a record reads as ONE POSITIONAL PART of
// a larger book -- a chapter, track, disc or part -- rather than a whole
// copy of one. A part is never a "version" of another record.
//
// It reuses the chapter-split detector's own markers rather than a second
// title parser: ParseSequenceMarker for the title and ParseFilenameSequence
// for the file stem (chapter_sequence.go), and seqCopySuffixRe
// (chapter_consolidator.go) to strip the " (1)" a file manager appends to a
// second copy -- that suffix marks a COPY, not a position, and must not be
// read as one.
//
// Deliberately looser than classifySeqCand, which additionally demands that
// the title say nothing the file does not, because the two callers want
// opposite errors: the consolidator must not MERGE records that are not
// chapters, while this gate must not LINK records that might be. A whole
// book whose file name happens to start with a number ("1984 - The
// Novel.mp3") is read as a part here and simply left ungrouped, which is
// the cheap error.
func versionLinkIsPart(title, filePath string) bool {
	if _, ok := ParseSequenceMarker(strings.TrimSpace(title)); ok {
		return true
	}
	_, ok := ParseFilenameSequence(versionLinkStem(filePath))
	return ok
}

// versionLinkStem is a file's name without its extension and without a
// trailing " (N)" copy suffix.
func versionLinkStem(filePath string) string {
	base := filepath.Base(filePath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if m := seqCopySuffixRe.FindStringSubmatchIndex(stem); m != nil {
		stem = stem[:m[0]]
	}
	return stem
}

// versionLinkHasCopySuffix reports whether a file name carries the " (N)"
// suffix a file manager gives a second copy dropped beside the original.
func versionLinkHasCopySuffix(filePath string) bool {
	base := filepath.Base(filePath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return seqCopySuffixRe.MatchString(stem)
}

// versionLinkDurationsAgree reports whether two known durations are close
// enough to be the same content. Unknown (nil or <=0) never agrees.
func versionLinkDurationsAgree(a, b *int) bool {
	if a == nil || b == nil || *a <= 0 || *b <= 0 {
		return false
	}
	hi, lo := *a, *b
	if lo > hi {
		hi, lo = lo, hi
	}
	return float64(hi-lo) <= float64(hi)*versionLinkDurationTolerance
}

// versionLinkDurationsConflict reports whether two KNOWN durations are too
// far apart to be the same content. Unknown durations never conflict.
func versionLinkDurationsConflict(a, b *int) bool {
	if a == nil || b == nil || *a <= 0 || *b <= 0 {
		return false
	}
	return !versionLinkDurationsAgree(a, b)
}

// versionLinkIsSameWork is the grouping predicate: are these two
// same-folder, same-title records two copies of one whole book?
//
//  1. Neither may read as a positional part (versionLinkIsPart).
//  2. Then one of:
//     a. The formats DIFFER -- the feature's stated purpose, an .m4b and an
//     .mp3 of one book, and the shape the 2026-09-19 census found to be
//     essentially always correct (9 groups, no observed false positive).
//     b. The formats MATCH and one file name carries the " (N)" copy suffix
//     over the other's stem -- the canonical shape of a second copy landing
//     beside the original.
//     Same-format records with no copy suffix are NOT grouped even when
//     their durations agree: two adjacent chapters of one book are the most
//     likely thing in a library to have similar durations, so duration
//     similarity is not evidence of being copies. It is only ever used as a
//     veto below.
//  3. Known durations must not conflict either way. Two records whose
//     durations differ by more than the tolerance are not the same content
//     however their formats or names read.
func versionLinkIsSameWork(newBook *database.Book, sib *database.Book) bool {
	if versionLinkIsPart(newBook.Title, newBook.FilePath) ||
		versionLinkIsPart(sib.Title, sib.FilePath) {
		return false
	}
	if versionLinkDurationsConflict(newBook.Duration, sib.Duration) {
		return false
	}
	newFmt, sibFmt := strings.TrimSpace(newBook.Format), strings.TrimSpace(sib.Format)
	if newFmt != "" && sibFmt != "" && !strings.EqualFold(newFmt, sibFmt) {
		return true
	}
	// Same (or unknown) format: require the copy-suffix shape over one
	// shared stem.
	newStem, sibStem := versionLinkStem(newBook.FilePath), versionLinkStem(sib.FilePath)
	if normForCompare(newStem) != normForCompare(sibStem) {
		return false
	}
	return versionLinkHasCopySuffix(newBook.FilePath) != versionLinkHasCopySuffix(sib.FilePath)
}

// versionLinkLive reports whether a sibling row may take part in a version
// group at all. GetBooksByTitleInDir filters neither soft-deleted nor
// merged-away rows, and electing one of those as primary would leave the
// group with no VISIBLE primary -- the very defect this file exists to stop.
func versionLinkLive(b *database.Book) bool {
	if b.MergedIntoBookID != nil && *b.MergedIntoBookID != "" {
		return false
	}
	return !b.IsSoftDeleted()
}

// versionLinkBeats reports whether candidate a should be primary over b.
//
// THE ELECTION RULE, in order:
//
//  1. an .m4b wins (unchanged from the pre-2026-09-19 behaviour, which
//     preferred the m4b -- it is the chaptered container);
//  2. the longer KNOWN duration wins (unknown ranks last);
//  3. the larger KNOWN file size wins (unknown ranks last);
//  4. an existing row wins over the row being created (the new row has no
//     ULID yet -- CreateBook mints it below -- so it cannot be compared by
//     ID, and preferring the incumbent also keeps a rescan from moving the
//     flag);
//  5. the lower ID wins -- ULIDs sort lexically by mint time, so this is
//     "oldest row".
//
// aNew/bNew mark the row being created.
func versionLinkBeats(a *database.Book, aNew bool, b *database.Book, bNew bool) bool {
	aM4B, bM4B := strings.EqualFold(a.Format, "m4b"), strings.EqualFold(b.Format, "m4b")
	if aM4B != bM4B {
		return aM4B
	}
	aDur, bDur := versionLinkInt(a.Duration), versionLinkInt(b.Duration)
	if aDur != bDur {
		return aDur > bDur
	}
	aSize, bSize := versionLinkInt64(a.FileSize), versionLinkInt64(b.FileSize)
	if aSize != bSize {
		return aSize > bSize
	}
	if aNew != bNew {
		return bNew // the existing row wins
	}
	return a.ID < b.ID
}

func versionLinkInt(p *int) int {
	if p == nil || *p < 0 {
		return 0
	}
	return *p
}

func versionLinkInt64(p *int64) int64 {
	if p == nil || *p < 0 {
		return 0
	}
	return *p
}

// applySmartVersionLink decides whether the row about to be created belongs
// in a version group with any of its same-title, same-folder siblings, and
// if so links them and elects exactly one primary.
//
// It writes dbBook.VersionGroupID / dbBook.IsPrimaryVersion in place, and
// leaves both nil when nothing genuinely groups. Idempotent by
// construction: it only ever links a sibling that carries NO group yet
// (linkVersionGroup skips the rest), so a rescan of an already-grouped
// folder writes nothing.
//
// Primacy is written EXPLICITLY -- true on the elected primary, false on
// the others -- rather than left nil. Nil reads as primary through
// database.EffectiveIsPrimaryVersion, so nil on a loser would mint a second
// primary; and reconcile/elect_primaries.go's countsAsPrimary tests
// `primary != nil && *primary`, so a nil PRIMARY would make the group look
// zero-primary to the repair pass and invite it to crown a second member
// (the #2668 double-primary defect). Explicit on both sides is the only
// form both readers agree on.
func applySmartVersionLink(dbBook *database.Book, siblings []database.Book, parentDir string) {
	// Candidates: live siblings that read as another copy of this same book.
	cands := make([]*database.Book, 0, len(siblings))
	for i := range siblings {
		sib := &siblings[i]
		if !versionLinkLive(sib) || !versionLinkIsSameWork(dbBook, sib) {
			continue
		}
		cands = append(cands, sib)
	}
	if len(cands) == 0 {
		defaultLog.Debug("No version link for %q in %s: %d same-title sibling(s), none reads as another copy of the same book",
			dbBook.Title, parentDir, len(siblings))
		return
	}

	// Join a candidate's existing group when there is one; otherwise mint
	// the same deterministic ID as before (the scheme is unchanged).
	var groupID string
	for _, sib := range cands {
		if sib.VersionGroupID != nil && *sib.VersionGroupID != "" {
			groupID = *sib.VersionGroupID
			break
		}
	}
	joinedExisting := groupID != ""
	if groupID == "" {
		h := sha256.Sum256([]byte(parentDir + "/" + strings.ToLower(dbBook.Title)))
		groupID = fmt.Sprintf("vg-%x", h[:8])
	}

	// An incumbent primary already in the joined group settles primacy: a
	// second one would be the double-primary defect.
	incumbent := false
	var unlinked []*database.Book
	for _, sib := range cands {
		if sib.VersionGroupID != nil && *sib.VersionGroupID != "" {
			if *sib.VersionGroupID == groupID && database.EffectiveIsPrimaryVersion(sib.IsPrimaryVersion) {
				incumbent = true
			}
			continue
		}
		unlinked = append(unlinked, sib)
	}

	// Elect among the new row and the siblings this call will link.
	var elected *database.Book // nil means the new row wins
	if !incumbent {
		for _, sib := range unlinked {
			best, bestNew := dbBook, true
			if elected != nil {
				best, bestNew = elected, false
			}
			if versionLinkBeats(sib, false, best, bestNew) {
				elected = sib
			}
		}
	}

	linked := 0
	electedLanded := elected == nil
	for _, sib := range unlinked {
		want := !incumbent && elected == sib
		sibGroup, ok := linkVersionGroup(sib.ID, groupID, want)
		if !ok {
			defaultLog.Warn("Sibling %s left out of version group %s: the group write did not land",
				sib.FilePath, groupID)
			continue
		}
		if sibGroup != groupID {
			defaultLog.Warn("Sibling %s is in version group %s, not %s: the group for %q in %s is split",
				sib.FilePath, sibGroup, groupID, dbBook.Title, parentDir)
			continue
		}
		linked++
		if want {
			electedLanded = true
		}
	}

	if linked == 0 && !joinedExisting {
		// Every link failed and there was no group to join, so this row
		// would carry a group ID no other row belongs to -- an orphan group
		// of one, which reads downstream as "this book has other versions"
		// and lists none. Same rule the hash-duplicate branches apply.
		defaultLog.Warn("Not version-linking %s: no sibling link landed, so there is no group to join",
			dbBook.FilePath)
		return
	}

	// The elected primary's write did not land (the row was gone, or another
	// writer had already put it in a different group), so the new row takes
	// primacy rather than leaving the group without one.
	isPrimary := !incumbent && (elected == nil || !electedLanded)
	dbBook.VersionGroupID = &groupID
	dbBook.IsPrimaryVersion = &isPrimary
	defaultLog.Info("Auto-linked version group %s for %q in %s (%d sibling(s) linked, new row primary=%t)",
		groupID, dbBook.Title, parentDir, linked, isPrimary)
}
