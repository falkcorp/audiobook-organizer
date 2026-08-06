// file: internal/itunes/service/fs_regroup_shape.go
// version: 1.6.0
// guid: 1e7d4a92-3c85-4b60-9f21-6a8c0d5e2b47
// last-edited: 2026-08-06

// Package service — deterministic (regex-only) shape classifier for the
// shattered-book REGROUP review producer (PR-B1).
//
// The broken iTunes import created one standalone single-file "book" per track
// (~44K records), each living under a per-book folder. This classifier re-derives
// candidate real audiobooks WITHOUT the iTunes XML, fingerprinting, or any AI: it
// groups single-file books by their **book folder** and classifies each folder's
// shape from the folder/track NAMES alone. The library folder is the only reliable
// identity signal here — the per-track tags are unreliable (see the plan
// docs/plans/2026-07-13-review-queue-and-regroup.md, decision #6: regex-only v1).
//
// This is a companion to GroupShatteredBooks (fs_regroup.go). That function is a
// high-precision auto-heal planner (prefix ⊆ parent, ASIN/author consensus) that
// only ever emits confident collapses; this one is BROADER — it recognizes disc
// sets, version groups (Abridged + Unabridged), anthologies, and ambiguous folders,
// and emits every candidate as a review HOLD (never an auto-apply). All four Kind
// strings are load-bearing: the A1 review-queue frontend maps them verbatim.
//
// Grouping key (deviation from a literal "grandparent folder"): a literal
// always-grandparent key over-merges flat multi-track (`<Book>/01.mp3` → grandparent
// is `<Author>`, collapsing the whole author). Instead we group by the BOOK FOLDER:
// the file's grandparent when its parent dir is a chapter (`<prefix> - N`) or disc
// (`Disc N`) subdir, else the file's parent dir. This is the correct generalization
// of "the book folder" and matches docs/dedup-import-pipeline-audit.md's
// (grandparent_dir, normalized-album) recommendation.
//
// Two identity signals (v1.1): grouping now folds in a book's ORIGINAL iTunes album
// folder (BookFile.ITunesPath, `W:\…`) IN ADDITION to its FilePath book-folder, via a
// small union-find — a book joins a group if EITHER key matches another member's. When
// only FilePath is present (the common non-iTunes case) this reduces exactly to the old
// map grouping. When the two signals disagree (a group spans several FilePath folders,
// glued only by a shared iTunes album), the group is held as ambiguous, not collapsed.
//
// Anthology precision (v1.1): the anthology marker is matched ONLY against the book
// folder's own name (never a parent series dir or a track's album tag), and a folder is
// classified as an anthology ONLY with strong evidence — a folder marker AND multiple
// genuinely-distinct title stems. Sequential chapters of one title (a novel split into
// N chapter files) collapse to multidisc; a marked-but-sequential folder is held as
// ambiguous. This fixes a prod false positive that counted 133 chapter FILES of one
// novel as 133 distinct WORKS.
//
// Over-merge guard (v1.2): the flat-multitrack branch now also requires
// !manyDistinctTitles. A plain AUTHOR/COLLECTION folder of distinct single-file books
// (e.g. `.../Terry Pratchett Discworld` = 70 different novels, or a flat
// `.../unsorted/books` dump) is flat-and-numbered too — most audiobook filenames carry
// SOME number — so numberedCount alone mistook 70 different books for 70 chapters of
// one. The distinct-title-stems signal (already the anthology discriminator) now also
// VETOES a confident collapse, dropping such folders to no-hold. A prod dry-run on
// 2026-07-14 flagged 24 of 196 confident-multidisc holds as this over-merge shape.
//
// Pure function over DB-derived metadata: no I/O, fully unit-testable.

package itunesservice

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Regroup Kind strings — LOAD-BEARING. The A1 review-queue frontend maps these
// verbatim; do not rename without updating the frontend map.
const (
	KindMultidisc    = "regroup.multidisc"     // confident collapse: N single-file books = 1 audiobook
	KindVersionGroup = "regroup.version-group" // Abridged + Unabridged editions in one folder
	KindAnthology    = "regroup.anthology"     // anthology/collection/omnibus = ONE book → combine (owner: single ISBN)
	KindAmbiguous    = "regroup.ambiguous"     // book-like folder, cannot confidently classify
)

// Recommended-action strings — the CLOSED vocabulary a review hold may recommend.
//
// These are deliberately SEPARATE from the Kind strings above and do not replace
// them. A Kind says what SHAPE the classifier saw ("these files live in Disc N
// folders"); an action says what a human should DO about it ("these are six
// different novels — leave them apart"). Prod showed the two can disagree: 3 of the
// 130 `regroup.multidisc` holds measured on 2026-08-06 hold members that are each
// book-length, because the disc and chapter/edition branches of classifyGroup never
// evaluate the series guard. Their Kind is still "multidisc" — the shape really is a
// disc set — but the correct action is `separate`, and approving `combine` would
// merge distinct novels through an apply path that hard-deletes absorbed Book rows.
//
// Exported because the approve handler (owner item 2) dispatches on the CHOSEN
// action across package boundaries.
const (
	// ActionCombine — the members are parts of one book; merge them.
	ActionCombine = "combine"
	// ActionSeparate — the members are already N distinct books; leave them apart.
	// Non-destructive: nothing to apply, only a status transition.
	ActionSeparate = "separate"
	// ActionDuplicateOf — the members are debris of a book that already exists
	// correctly elsewhere.
	//
	// 🔴 NOTHING IN THIS FILE EVER EMITS THIS. It is defined here so the action
	// vocabulary is closed and complete in one place, but deciding "this folder
	// duplicates an existing book" requires cross-book identity evidence
	// (fingerprints, ASIN/ISBN consensus) that classifyGroup does not have — it
	// reasons over ONE folder's names and runtimes in isolation. The
	// duplicate-detection track owns emitting it.
	ActionDuplicateOf = "duplicate-of"
	// ActionInsufficientEvidence — the classifier cannot tell. Emit-only: it is a
	// statement BY the machine, not a decision a human can pick.
	ActionInsufficientEvidence = "insufficient-evidence"
	// ActionVersionGroup — the members are DIFFERENT EDITIONS of one work (an
	// abridged and an unabridged recording, two narrators, a remaster). Link them
	// into a version group with one primary; keep every file.
	//
	// 🔴 WHY THIS EXISTS AS A FIFTH ACTION, AND WHY RUNTIME CANNOT DECIDE IT.
	// Two editions of the same novel are BOTH book-length by definition, so the
	// runtime rule below sees a majority over bookLengthSec and answers
	// ActionSeparate — technically true (they are distinct recordings) and
	// operationally wrong (leaving them unlinked is exactly the state the hold
	// exists to fix). Worse, once the approve handler dispatches on the CHOSEN
	// action rather than on Kind, a version-group hold recommending "separate"
	// makes ApplyVersionGroup permanently unreachable: the one apply path that
	// destroys nothing would become the one that never runs.
	//
	// So KindVersionGroup overrides the runtime recommendation. That is safe in a
	// way the reverse would not be — the classifier reached KindVersionGroup only
	// via explicit abridged/unabridged edition markers PLUS a dominant shared
	// title, which is positive identity evidence that runtime does not have.
	ActionVersionGroup = "version-group"
)

// ShatterBook is the slim per-book view the classifier reasons over. Build it from
// a Book plus its BookFiles: FileCount comes from the BookFile rows, FilePath is the
// real BookFile path when the book owns exactly one file, else the virtual
// Book.FilePath.
type ShatterBook struct {
	BookID   string
	FilePath string // real BookFile path (preferred) or virtual Book.FilePath
	// ITunesPath is the ORIGINAL iTunes album-folder path (`W:\…`) captured at import,
	// when present. These shattered books are largely an iTunes-import artifact and the
	// original album folder is often a STRONGER identity signal than the reorganized
	// FilePath, so it is folded in as an ADDITIONAL grouping signal: a book joins a group
	// if its FilePath book-folder OR its ITunesPath book-folder matches another member's
	// (see ClassifyShatteredFolders). Empty for non-iTunes books (the common prod case),
	// where grouping falls back to FilePath alone with no behavior change.
	ITunesPath string
	FileCount  int    // BookFiles owned; > 1 means already a real multi-file book
	IsPrimary  bool   // non-primary version members are ignored
	Title      string // scanner-derived title (album tag); may be empty
	Author     string // display author when known; never used for the FOLDER key

	// DurationSec is the book's total runtime in SECONDS, 0 when unknown.
	//
	// 🔴 This is the signal that tells a SERIES apart from a CHAPTER SET, and its
	// absence caused the worst near-miss this classifier has had. See
	// membersAreBookLength.
	DurationSec int

	// DiscNumber / TrackNumber are OUTPUT fields, zero on the input view and
	// populated by classifyGroup (assignDiscTrack) for the members of a combine.
	// They carry the per-file play-order the apply path (ApplyMultidisc) writes onto
	// the merged book's BookFile rows. Owner decision (2026-07-26): discs are
	// flattened away — DiscNumber is ALWAYS 0 and TrackNumber is a single continuous
	// 1..N over play order, even across former disc boundaries (a real "Disc N" set
	// becomes one continuous track list). The physical disc only orders the files;
	// it is never persisted. Contiguous + unique by construction.
	DiscNumber  int
	TrackNumber int
}

// bookLengthSec is the runtime above which a single member is judged to be a
// whole book rather than a chapter or a disc.
//
// 90 minutes sits in a wide empty band: audiobook chapters are minutes long, and
// full novels are hours. A 90-minute *chapter* is vanishingly rare, and a novel
// shorter than 90 minutes would be a single-file book, never a member of an N-way
// group.
const bookLengthSec = 90 * 60

// membersAreBookLength reports whether most members are individually long enough
// to BE books, which means the group is a series and must never be collapsed.
//
// 🔴 THE NEAR-MISS THIS PREVENTS. The flat branch's over-merge guard keys on
// distinct title STEMS, and numbered sequels all share one stem:
//
//	Super Sales on Super Heroes.m4b
//	Super Sales on Super Heroes 2.m4b … 5.m4b
//
// strips to a single stem, so manyDistinctTitles stayed false and the collapse
// was judged "confident". A production dry-run on 2026-08-05 found 41 of 43
// multidisc candidates were this shape — every one an iTunes AUTHOR folder
// (`iTunes Media/Audiobooks/<Author>/`) holding that author's whole catalogue,
// because the classifier's founding assumption ("files in one folder are tracks
// of one book") is true of the organized tree and false of the iTunes tree.
// Approving them would have merged entire series into single books, and the apply
// path hard-deletes absorbed rows.
//
// Runtime is the discriminator stems cannot be: six two-hour files are six books,
// six three-minute files are six chapters. Requires a strict majority so one long
// member (an omnibus track, a mis-tagged file) cannot veto a real chapter set.
//
// Members with unknown duration are counted as NOT book-length: an absent value is
// not evidence of a series, and the guard must not fire on missing data.
func membersAreBookLength(members []memberInfo) bool {
	if len(members) == 0 {
		return false
	}
	long := 0
	for _, m := range members {
		if m.book.DurationSec >= bookLengthSec {
			long++
		}
	}
	return long*2 > len(members)
}

// RecommendationEvidence is the numeric case FOR a recommendation — every number
// the reason sentence quotes, so a reviewer can check the machine's arithmetic
// instead of trusting it.
//
// This exists because of what the queue looked like before it: 762 of 777 holds
// carried the SAME sentence ("review: flat folder shares a title but ordering is
// unclear"), which tells a reviewer nothing they could act on. A reason without
// numbers is just a nicer generic string; the numbers are the point.
//
// Every field is already computed by classifyGroup or trivially derived from the
// members — nothing here needs new I/O. JSON tags are lowerCamelCase to match the
// existing regroup payload style.
type RecommendationEvidence struct {
	// Members is the group's member count.
	Members int `json:"members"`
	// DurationsKnown is how many members have a non-zero DurationSec. The gap
	// between this and Members is the whole reason a recommendation can be
	// "insufficient-evidence": see recommendAction.
	DurationsKnown int `json:"durationsKnown"`
	// BookLengthMembers is how many members run >= bookLengthSec (90 min) and are
	// therefore individually long enough to BE books.
	BookLengthMembers int `json:"bookLengthMembers"`
	// MedianKnownSec is the median runtime over members with a KNOWN duration
	// (zeros excluded — including them would make the reason sentence state a
	// number no member actually has). Lower-middle on an even count. 0 when no
	// member has a known duration.
	MedianKnownSec int `json:"medianKnownSec"`
	// LongestKnownSec is the longest member runtime in seconds, 0 when unknown.
	// It is the single most legible number for the separate case: "the longest
	// member runs 15.8 h" reads as "that is a novel" without any further context.
	LongestKnownSec int `json:"longestKnownSec"`
	// DistinctStems is the number of distinct title stems among the members
	// (classifyGroup's distinctPrefixes) — the anthology / over-merge signal.
	DistinctStems int `json:"distinctStems"`
	// NumberedMembers is how many members carry a parseable chapter/track ordinal.
	NumberedMembers int `json:"numberedMembers"`
	// Structure is the group's dominant physical shape, mirroring
	// RegroupGroup.Structure ("disc" | "chapter" | "flat").
	Structure string `json:"structure"`
}

// gatherEvidence tallies the runtime facts about a group's members. The
// name-derived counts are passed in because classifyGroup already computed them.
func gatherEvidence(members []memberInfo, distinctStems, numberedMembers int, structure string) RecommendationEvidence {
	ev := RecommendationEvidence{
		Members:         len(members),
		DistinctStems:   distinctStems,
		NumberedMembers: numberedMembers,
		Structure:       structure,
	}
	known := make([]int, 0, len(members))
	for _, m := range members {
		d := m.book.DurationSec
		if d <= 0 {
			continue // unknown — deliberately NOT folded into any average
		}
		ev.DurationsKnown++
		known = append(known, d)
		if d >= bookLengthSec {
			ev.BookLengthMembers++
		}
		if d > ev.LongestKnownSec {
			ev.LongestKnownSec = d
		}
	}
	if len(known) > 0 {
		sort.Ints(known)
		ev.MedianKnownSec = known[(len(known)-1)/2] // lower middle on an even count
	}
	return ev
}

// humanRuntime renders a runtime the way a reviewer thinks about it: hours for a
// book, minutes for a chapter.
func humanRuntime(sec int) string {
	if sec >= 3600 {
		return fmt.Sprintf("%.1f h", float64(sec)/3600)
	}
	return fmt.Sprintf("%d min", (sec+30)/60)
}

// recommendAction turns the runtime evidence into a recommended action and a
// one-sentence reason that quotes its own numbers.
//
// 🔴 THE ONE RULE THAT MATTERS: AN ABSENT DURATION IS NOT EVIDENCE.
//
// The classifier's founding assumption is "files in one folder are tracks of one
// book". That is true of the organized tree and FALSE of the iTunes tree, where
// `iTunes Media/Audiobooks/<Author>/` holds an author's whole catalogue. Runtime is
// the only signal that separates the two cases, because numbered SEQUELS share one
// title stem ("Super Sales on Super Heroes" / " 2" / " 3") and so slip past every
// name-based guard. A 2026-08-05 dry-run found 41 of 43 confident candidates were
// exactly that shape.
//
// Which means a group with NO runtime data is not "probably chapters" — it is
// UNKNOWN, and the same folder that looks like a chapter set with the durations
// missing looks like a six-novel series with them present. So the majority-known
// gate below is checked FIRST, unconditionally, with no structural exception: a
// zero-duration chapter shatter recommends insufficient-evidence, not combine. The
// asymmetry is deliberate and is the reason for the ordering — `combine` routes to
// an apply path that HARD-DELETES the absorbed Book rows, while `separate` and
// `insufficient-evidence` change nothing. Guessing wrong toward separate leaves a
// book shattered (recoverable any time); guessing wrong toward combine destroys
// rows. Only one of those is reversible.
//
// This is the same rule that made membersAreBookLength safe (it counts unknown
// members as NOT book-length so the guard cannot fire on missing data); here the
// symmetric half is enforced — missing data cannot fire a COLLAPSE either.
//
// Deliberately independent of Kind. A Kind describes the shape the classifier saw;
// this describes what the runtimes say to do about it. The 3 multidisc holds whose
// members are each book-length are exactly the case where the two disagree, and
// suppressing the disagreement is what would lose books.
//
// distinctPathFolders is how many distinct FilePath book-folders the group spans; >1
// means the membership itself is unconfirmed (see the gate below).
func recommendAction(ev RecommendationEvidence, members []memberInfo,
	distinctPathFolders int) (action, reason string) {
	n := ev.Members

	// (1) Not enough runtime evidence to say anything. A strict majority of members
	// must have a known duration before ANY decisive call, so a lone known runtime
	// among five unknowns cannot carry the group.
	if ev.DurationsKnown*2 <= n {
		if ev.DurationsKnown == 0 {
			return ActionInsufficientEvidence, fmt.Sprintf(
				"none of the %d members has a known runtime — an absent duration is not evidence, "+
					"so chapters of one book cannot be told apart from separate books", n)
		}
		return ActionInsufficientEvidence, fmt.Sprintf(
			"only %d of %d members has a known runtime — an absent duration is not evidence, "+
				"so chapters of one book cannot be told apart from separate books",
			ev.DurationsKnown, n)
	}

	// (1b) The group spans several FilePath folders and is held together ONLY by a
	// shared original iTunes album. classifyGroup's first switch case already refuses
	// to vouch for this grouping ("the two identity signals disagree"), and a
	// recommendation that reasons purely about RUNTIMES would happily answer
	// `combine` for it — recommending a destructive merge of a membership the
	// classifier just declined to confirm. Two 30-minute files under
	// `Author A/Book One` and `Author B/Book Two` are not two chapters of one book
	// merely because both runtimes are short; they are two different books whose
	// iTunes album tag collided.
	//
	// This is a GUARD on membership, not a second length threshold — bookLengthSec
	// remains the only runtime cut-off in this file.
	if distinctPathFolders > 1 {
		return ActionInsufficientEvidence, fmt.Sprintf(
			"these %d members span %d different file-path folders and are grouped only by a "+
				"shared original iTunes album — whether they are one book is unconfirmed",
			n, distinctPathFolders)
	}

	// (2) A strict majority of members are individually long enough to BE books, so
	// this folder holds separate books that were grouped by name alone. Reuses the
	// existing membersAreBookLength helper and its single bookLengthSec threshold —
	// a second threshold here would be a second thing to get wrong.
	if membersAreBookLength(members) {
		return ActionSeparate, fmt.Sprintf(
			"%d of %d members run 90 min or longer (longest %s) — each is book-length, "+
				"so these are separate books, not parts of one",
			ev.BookLengthMembers, n, humanRuntime(ev.LongestKnownSec))
	}

	// (3) Every member whose runtime we know is shorter than a book. Combined with
	// the majority-known gate above, that is positive evidence of genuine
	// disc/chapter fragments — the only branch that recommends a destructive merge.
	if ev.BookLengthMembers == 0 {
		return ActionCombine, fmt.Sprintf(
			"all %d members with a known runtime are under 90 min (median %s) — "+
				"chapter- or disc-length fragments of one book",
			ev.DurationsKnown, humanRuntime(ev.MedianKnownSec))
	}

	// (4) Mixed: some members are book-length and some are fragments, but neither
	// side is a majority. The members disagree about what they are, and a folder
	// that mixes whole books with loose fragments needs a human to look at it.
	return ActionInsufficientEvidence, fmt.Sprintf(
		"members disagree on runtime: %d of %d run 90 min or longer and %d are shorter — "+
			"neither a chapter set nor a clean set of separate books",
		ev.BookLengthMembers, n, ev.DurationsKnown-ev.BookLengthMembers)
}

// RegroupGroup is one book folder the classifier flagged for a review hold.
type RegroupGroup struct {
	FolderRef      string        // the book folder (grandparent for chapter/disc; parent for flat)
	Kind           string        // one of the Kind* constants
	Confident      bool          // true only for confident multidisc collapses
	SurvivorTitle  string        // derived: strip "NN - " prefix and "(era/year)" / " - N" suffix
	ProposedAction string        // human-readable proposed action
	Members        []ShatterBook // ordered by chapter/track number then BookID
	// DistinctWorks is the count of DISTINCT title stems in the folder — set ONLY for
	// KindAnthology, where the human-facing count must be the number of distinct WORKS,
	// not the raw file count (a novel split into 133 sequential chapter files is ONE
	// work, not 133). Zero for every other Kind (callers fall back to len(Members)).
	DistinctWorks int
	// Structure is the dominant physical shape of the group's members:
	// "disc" (real Disc N/CD N folders), "chapter" (`<Book> - N` subdirs),
	// "flat" (sequential files in one folder), or "edition". It lets the review
	// label distinguish a genuine multi-DISC set ("Multi-disc → 1 book") from
	// same-disc CHAPTERS ("Chapters → 1 book"), which read identically otherwise.
	Structure string

	// RecommendedAction is what a human should DO about this group: one of
	// ActionCombine, ActionSeparate, ActionVersionGroup, ActionDuplicateOf or
	// ActionInsufficientEvidence (this file never emits ActionDuplicateOf — see
	// its doc comment). It is computed from the members' RUNTIMES and is
	// deliberately independent of Kind, which describes only the folder's physical
	// SHAPE. The two can disagree, and the disagreement is the point: 3 prod
	// `regroup.multidisc` holds carry members that are each book-length, because
	// the disc and chapter/edition branches never evaluate the series guard.
	//
	// The ONE exception is KindVersionGroup, which overrides runtime with
	// ActionVersionGroup — two editions of one work are both book-length, so
	// runtime alone would say "separate" and strand ApplyVersionGroup behind an
	// action nothing dispatches to. See ActionVersionGroup.
	RecommendedAction string
	// RecommendationReason is one human-readable sentence explaining WHY, quoting
	// the numbers that produced it. Never a bare category name: the queue this
	// replaces had 762 of 777 holds carrying one identical generic sentence, which
	// is precisely what made it unworkable.
	RecommendationReason string
	// RecommendationEvidence is the arithmetic behind the reason, so a reviewer
	// can check it rather than trust it.
	RecommendationEvidence RecommendationEvidence
}

// ShapeStats reconciles every input book so the record count ties out (no silent
// filtering), mirroring fs_regroup.go's GroupStats.
type ShapeStats struct {
	TotalBooks   int            // all books seen
	NonPrimary   int            // skipped: non-primary version member
	MultiFile    int            // skipped: already a real multi-file book
	NoPath       int            // skipped: no usable file path
	Singletons   int            // book folders with a single member (genuine single file)
	DistinctSkip int            // book folders skipped as distinct-book collections (no hold)
	Groups       int            // emitted review groups (== len(returned groups))
	ByKind       map[string]int // emitted-group count per Kind
}

// ─── shape regexes ───────────────────────────────────────────────────────────

// discDirRe matches a disc subfolder basename: "Disc 1", "CD03", "disc_2".
var discDirRe = regexp.MustCompile(`(?i)^(?:disc|cd)\s*_?\s*0*(\d+)$`)

// chapterSubdirRe matches a chapter subfolder basename "<prefix> - N" (the shatter
// shape): a title, a dash separator, then a number. Requiring the dash separates it
// from disc dirs and bare numbers. "Cage of Souls - 15" → ("Cage of Souls", 15).
var chapterSubdirRe = regexp.MustCompile(`^(.+?)\s*-\s*(\d{1,4})$`)

// editionMarkerRe detects an Abridged/Unabridged edition marker anywhere in a name.
var editionMarkerRe = regexp.MustCompile(`(?i)\b(?:un)?abridged\b`)

// leadNumRe / trailNumRe extract a track ordinal from a bare filename. A leading
// number ("01 - Chapter One" → 1) is preferred; else a trailing number ("Chapter 3"
// → 3, "05" → 5).
var (
	leadNumRe  = regexp.MustCompile(`^\s*(\d{1,4})\b`)
	trailNumRe = regexp.MustCompile(`(\d{1,4})\s*$`)
)

// flatMultitrackMin is the minimum member count for a FLAT numbered folder to be a
// CONFIDENT multi-disc collapse. Below it, a numbered flat folder is only ambiguous
// (2-3 loose numbered files could be distinct works stored under a shared dir).
const flatMultitrackMin = 4

// unabridgedRe / abridgedRe detect edition markers. Order matters: "unabridged"
// contains "abridged", so hasAbridged is only asserted after stripping every
// "unabridged" occurrence from the text.
var (
	unabridgedRe = regexp.MustCompile(`(?i)unabridged`)
	abridgedRe   = regexp.MustCompile(`(?i)abridged`)
)

// The folder-marker regexes are split by whether the marked thing is ONE book or
// potentially SEVERAL (owner decision 2026-07-26). Both are matched ONLY against the
// BOOK FOLDER NAME (never a parent dir or a track's album tag — see classifyGroup) and
// only act when the members also show multiple distinct title stems (manyDistinctTitles).
//
//   - singleBookMarkerRe: anthology / omnibus / collection = a SINGLE published book
//     with one ISBN (a short-story anthology, an omnibus). → COMBINE into one book.
//     A genuine single "The Complete Collection" (sequential chapters, one stem) fails
//     the distinct-titles gate and falls to an ambiguous hold, so "collection" is safe.
//   - multiBookMarkerRe: trilogy / tetralogy / quartet / boxed set = potentially
//     MULTIPLE books, each its own ISBN. → do NOT auto-combine; hold as ambiguous with
//     a "may be several books" note so the human decides (a boxed set is often 3 books,
//     not one). If both markers appear, the multi-book marker wins (safer).
//
// anthologyMarkerRe is the union, used for the sequential/one-stem fallback (either
// marker but unclear boundaries → ambiguous). "complete" alone stays out (too weak —
// appears on ordinary unabridged titles).
var (
	singleBookMarkerRe = regexp.MustCompile(`(?i)\b(antholog(?:y|ies)|omnibus|collect(?:ion|ions|ed))\b`)
	multiBookMarkerRe  = regexp.MustCompile(`(?i)\b(trilog(?:y|ies)|tetralog(?:y|ies)|quartet|boxed?\s*set)\b`)
	anthologyMarkerRe  = regexp.MustCompile(`(?i)\b(antholog(?:y|ies)|omnibus|trilog(?:y|ies)|tetralog(?:y|ies)|quartet|boxed?\s*set|collect(?:ion|ions|ed))\b`)
)

// leadingNumRe / trailingParenRe / trailingNumSuffixRe clean a derived survivor title.
var (
	leadingNumRe        = regexp.MustCompile(`^\s*\d+\s*[-.]\s*`)
	trailingParenRe     = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
	trailingNumSuffixRe = regexp.MustCompile(`\s*[-]\s*\d+\s*$`)
)

// normTitle lowercases and strips non-alphanumerics for substring/identity checks.
// (Shares the fs_regroup.go definition — declared there, reused here.)

// memberInfo is the per-member derived layout used during classification.
type memberInfo struct {
	book       ShatterBook
	structure  string // "disc" | "chapter" | "flat"
	parentBase string // basename of the file's immediate parent dir
	parentDir  string // FULL path of the file's immediate parent dir
	fileBase   string // filename without extension
	prefix     string // original-case title prefix (chapter prefix, or track prefix)
	normPrefix string // normalized prefix for consensus voting
	num        int    // chapter/track/disc number (0 when none)
	hasNum     bool
	fpKey      string // FilePath-derived book-folder key (always set)
	itKey      string // ITunesPath-derived book-folder key, namespaced; "" when no iTunes path
	// sortDisc / sortTrack are the PLAY-ORDER key. For a real "Disc N" member,
	// sortDisc = the physical disc number and sortTrack = the within-disc chapter
	// parsed from the filename; for flat/chapter/edition members, sortDisc = 0 and
	// sortTrack = the file's own track/chapter number. Ordering by (sortDisc,
	// sortTrack) yields D1C1, D1C2, D2C1, D2C2 — the sequence assignDiscTrack then
	// flattens into continuous track numbers (owner decision: discs don't exist).
	sortDisc  int
	sortTrack int
}

// folderKeyOf computes the BOOK FOLDER key and per-member layout for one file path.
// The key is the file's grandparent when its parent dir is a sub-container of the
// book folder (a chapter `<prefix> - N` dir, a `Disc N` dir, or an edition
// `... (Unabridged)` dir), and the parent dir itself otherwise (flat multi-track).
// See the package doc for why this generalizes "the book folder".
func folderKeyOf(fp string) (key string, mi memberInfo) {
	parent := filepath.Dir(fp)
	pbase := filepath.Base(parent)
	fileBase := strings.TrimSuffix(filepath.Base(fp), filepath.Ext(filepath.Base(fp)))
	mi.parentBase = pbase
	mi.parentDir = parent
	mi.fileBase = fileBase

	switch {
	case discDirRe.MatchString(pbase):
		m := discDirRe.FindStringSubmatch(pbase)
		mi.structure = "disc"
		mi.num, _ = strconv.Atoi(m[1])
		mi.hasNum = true
		// Play order: physical disc first, then the within-disc chapter from the
		// filename (so Disc 1's chapters precede Disc 2's when we flatten to tracks).
		mi.sortDisc = mi.num
		mi.sortTrack, _ = trackNum(fileBase)
		return filepath.Dir(parent), mi

	case chapterSubdirRe.MatchString(pbase):
		// Parent dir is a "<prefix> - N" chapter dir (the shatter shape). Group by the
		// grandparent (the book folder that holds all the chapter dirs); the prefix is
		// the per-member identity vote.
		m := chapterSubdirRe.FindStringSubmatch(pbase)
		mi.structure = "chapter"
		mi.prefix = strings.TrimSpace(m[1])
		mi.normPrefix = normTitle(mi.prefix)
		mi.num, _ = strconv.Atoi(m[2])
		mi.hasNum = true
		mi.sortTrack = mi.num // no disc; order by chapter number
		return filepath.Dir(parent), mi

	case editionMarkerRe.MatchString(pbase):
		// Parent dir is an edition sub-folder ("<Book> (Unabridged)"). Group by the
		// grandparent book folder; identity vote = the edition name minus its markers.
		mi.structure = "edition"
		mi.prefix = stripEditionMarkers(pbase)
		mi.normPrefix = normTitle(mi.prefix)
		mi.num, mi.hasNum = trackNum(fileBase)
		mi.sortTrack = mi.num // no disc; order by filename track
		return filepath.Dir(parent), mi

	default:
		// Flat multi-track: the file sits directly in the book folder. Numbering comes
		// from the filename; the identity vote is the filename's title remainder (weak).
		mi.structure = "flat"
		mi.num, mi.hasNum = trackNum(fileBase)
		mi.prefix = titleRemainder(fileBase)
		mi.normPrefix = normTitle(mi.prefix)
		mi.sortTrack = mi.num // no disc; order by filename track
		return parent, mi
	}
}

// trackNum extracts a track ordinal from a bare filename: a leading number is
// preferred ("01 - Chapter One" → 1), else a trailing number ("Chapter 3" → 3).
func trackNum(fileBase string) (int, bool) {
	if m := leadNumRe.FindStringSubmatch(fileBase); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if m := trailNumRe.FindStringSubmatch(fileBase); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	return 0, false
}

// titleRemainder strips a leading "NN[ -._]" ordinal and a trailing number from a
// filename, leaving the title-ish remainder used as a weak flat-folder identity vote.
func titleRemainder(fileBase string) string {
	t := leadNumRe.ReplaceAllString(fileBase, "")
	t = strings.TrimLeft(t, " -_.")
	t = trailNumRe.ReplaceAllString(t, "")
	return strings.TrimRight(strings.TrimSpace(t), " -_.")
}

// stripEditionMarkers removes Abridged/Unabridged markers and any parenthetical
// groups from an edition folder name, leaving the book title ("Dune (Unabridged)" →
// "Dune").
func stripEditionMarkers(name string) string {
	t := editionMarkerRe.ReplaceAllString(name, " ")
	for {
		stripped := trailingParenRe.ReplaceAllString(t, "")
		if stripped == t {
			break
		}
		t = stripped
	}
	// Drop any now-empty "()" left behind and collapse whitespace.
	t = strings.ReplaceAll(t, "()", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(t), " "))
}

// itunesFolderKey derives a stable book-folder key from a raw iTunes path (`W:\…`).
// It normalizes backslashes to forward slashes so folderKeyOf's filepath logic works
// on a non-native path, then namespaces the result ("itunes\x00…") so an iTunes key can
// never collide with a Linux FilePath key — the two only ever match keys of their own
// kind. Returns "" for a blank/degenerate path.
func itunesFolderKey(raw string) string {
	norm := strings.ReplaceAll(strings.TrimSpace(raw), `\`, "/")
	if norm == "" {
		return ""
	}
	key, _ := folderKeyOf(norm)
	if strings.TrimSpace(key) == "" || key == "." || key == "/" {
		return ""
	}
	return "itunes\x00" + key
}

// unionFind is a tiny disjoint-set over grouping-connector strings. It lets a book
// join a group through EITHER its FilePath book-folder OR its ITunesPath book-folder:
// each book unions its two connector keys, so any two books that share either key land
// in the same set. With no iTunes path a book contributes only its FilePath key, which
// reproduces the old plain-map grouping exactly (no regression).
type unionFind struct{ parent map[string]string }

func newUnionFind() *unionFind { return &unionFind{parent: map[string]string{}} }

func (u *unionFind) add(x string) {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
	}
}

func (u *unionFind) find(x string) string {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]] // path halving
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// dominantKey returns the highest-voted key, breaking ties by smallest string so the
// choice is deterministic (map iteration order is not).
func dominantKey(votes map[string]int) string {
	best, bestCount := "", -1
	for k, v := range votes {
		if v > bestCount || (v == bestCount && k < best) {
			best, bestCount = k, v
		}
	}
	return best
}

// ClassifyShatteredFolders groups single-file books by their book folder and
// classifies each folder's shape. It emits ONE RegroupGroup per candidate folder
// that plausibly represents a single book needing review; folders that are clearly
// collections of DISTINCT books (author dirs, correctly-stored series volumes, flat
// dumps) are skipped (DistinctSkip) so the review queue is not flooded, and genuine
// single-file books (group size 1) are left alone (Singletons).
//
// It performs NO writes and NO I/O — the caller (the dry-run op) turns each group
// into a review-queue hold.
func ClassifyShatteredFolders(books []ShatterBook) ([]RegroupGroup, ShapeStats) {
	st := ShapeStats{TotalBooks: len(books), ByKind: map[string]int{}}

	// Two-pass grouping via union-find so a book can join a group through EITHER its
	// FilePath book-folder OR its ITunesPath book-folder (Bug 2). Pass 1 builds each
	// member and unions its connector keys; pass 2 buckets members by set root. With no
	// iTunes paths this is identical to the old grouping (each set == one FilePath key).
	uf := newUnionFind()
	kept := make([]memberInfo, 0, len(books))
	for _, b := range books {
		if !b.IsPrimary {
			st.NonPrimary++
			continue
		}
		if b.FileCount > 1 {
			st.MultiFile++
			continue
		}
		if strings.TrimSpace(b.FilePath) == "" {
			st.NoPath++
			continue
		}
		fpKey, mi := folderKeyOf(b.FilePath)
		mi.book = b
		mi.fpKey = fpKey
		uf.add(fpKey)
		if itKey := itunesFolderKey(b.ITunesPath); itKey != "" {
			mi.itKey = itKey
			uf.add(itKey)
			uf.union(fpKey, itKey) // this book's two locations are the same book
		}
		kept = append(kept, mi)
	}

	byRoot := make(map[string][]memberInfo)
	for _, mi := range kept {
		root := uf.find(mi.fpKey)
		byRoot[root] = append(byRoot[root], mi)
	}

	groups := make([]RegroupGroup, 0, len(byRoot))
	for _, members := range byRoot {
		if len(members) < 2 {
			st.Singletons++ // genuine single-file book — leave alone
			continue
		}
		g, emit := classifyGroup(members)
		if !emit {
			st.DistinctSkip++
			continue
		}
		st.ByKind[g.Kind]++
		groups = append(groups, g)
	}
	st.Groups = len(groups)

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Kind != groups[j].Kind {
			return groups[i].Kind < groups[j].Kind
		}
		return groups[i].FolderRef < groups[j].FolderRef
	})
	return groups, st
}

// versionMarkers reports whether text carries both an Unabridged and an Abridged
// marker (case-insensitive). Because "unabridged" contains "abridged", the abridged
// check runs against text with every "unabridged" occurrence removed.
func versionMarkers(text string) (hasUnabridged, hasAbridged bool) {
	hasUnabridged = unabridgedRe.MatchString(text)
	stripped := unabridgedRe.ReplaceAllString(text, " ")
	hasAbridged = abridgedRe.MatchString(stripped)
	return hasUnabridged, hasAbridged
}

// classifyGroup assigns a shape Kind to one book-folder group. emit=false means the
// folder is not a single-book candidate (a collection of distinct books) and gets no
// review hold. Thresholds are documented inline; the classifier biases conservative
// (ambiguous, not confident) whenever the evidence is weak — every group is a
// human-reviewed hold, so the human is the backstop.
// displayFolderRef picks the folder a HUMAN should be shown for this group.
//
// The grouping key deliberately climbs to the grandparent for the chapter/disc/
// edition shapes, because `<Book>/<Book> - 01/file` needs every chapter shell to
// land in one group. But the grandparent is only the BOOK when the group really
// spans sibling shells. When every member sits in the SAME directory, that
// directory is the book and the grandparent is one level too high.
//
// 🔴 WHY THIS MATTERS FOR REVIEW. A production group of 17 tracks — all of
// "Rysa Walker - The Delphi Effect ... (Unabridged)" — was labelled
// `/abooks/imported/Rysa Walker`, because the parent carried an edition marker and
// the edition branch returns the grandparent. The grouping was correct; the label
// named the AUTHOR. A reviewer reading "Rysa Walker" would reasonably reject it as
// an author-folder merge and throw away a good regroup — and with ~900 holds to get
// through, a label that misrepresents the group is a correctness problem in its own
// right.
//
// Display only: the grouping key is untouched, so which books belong together does
// not change.
func displayFolderRef(members []memberInfo, groupKey string) string {
	if len(members) == 0 {
		return groupKey
	}
	first := members[0].parentDir
	if first == "" {
		return groupKey
	}
	for _, m := range members[1:] {
		if m.parentDir != first {
			// Members really do span sibling shells — the grandparent IS the book.
			return groupKey
		}
	}
	return first
}

func classifyGroup(members []memberInfo) (RegroupGroup, bool) {
	n := len(members)

	// FolderRef = the dominant FilePath book-folder among the members (deterministic).
	// mergedViaItunes is true when the set spans MORE THAN ONE distinct FilePath folder,
	// which can only happen when an ITunesPath album glued otherwise-separate FilePath
	// folders together (Bug 2). That is the "the two identity signals disagree" case, so
	// the maintainer's rule applies: lean ambiguous (grouped, but hold for review).
	fpVotes := map[string]int{}
	for _, m := range members {
		fpVotes[m.fpKey]++
	}
	folderRef := displayFolderRef(members, dominantKey(fpVotes))
	mergedViaItunes := len(fpVotes) > 1
	folderName := filepath.Base(folderRef)
	normFolder := normTitle(folderName)

	// Version/edition markers may live on any member (parent-dir basename, filename, or
	// album tag), so the version check scans the whole text blob. The ANTHOLOGY marker,
	// by contrast, is matched ONLY against the book-folder name below — a parent series
	// dir or a track's album tag ("The Traitor Spy Trilogy") must NOT reclassify a child
	// single-book folder as an anthology (Bug 1).
	var sb strings.Builder
	sb.WriteString(folderName)
	for _, m := range members {
		sb.WriteByte(' ')
		sb.WriteString(m.parentBase)
		sb.WriteByte(' ')
		sb.WriteString(m.fileBase)
		sb.WriteByte(' ')
		sb.WriteString(m.book.Title)
	}
	text := sb.String()
	hasUnab, hasAb := versionMarkers(text)
	hasSingleBookMarker := singleBookMarkerRe.MatchString(folderName)
	hasMultiBookMarker := multiBookMarkerRe.MatchString(folderName)
	hasFolderAnthologyMarker := anthologyMarkerRe.MatchString(folderName)

	// Structural tallies.
	var discCount, chapterCount, flatCount, numberedCount int
	prefixVotes := map[string]int{}
	// prefixDisplay maps each NORMALIZED prefix back to the first original-case form
	// seen for it. prefixVotes is keyed by normPrefix (lowercased, non-alphanumerics
	// stripped), so the winning key reads "therisingvol9" — unusable as a title. The
	// survivor-title selector needs the original casing and punctuation.
	prefixDisplay := map[string]string{}
	// authorVotes tallies the members' display authors (normalized) so the survivor
	// title can REJECT a candidate that is merely the author's name — the observed
	// "C. T. Phipps" failure. Only a majority author is trusted; below that the
	// guard stays inert rather than guessing.
	authorVotes := map[string]int{}
	for _, m := range members {
		switch m.structure {
		case "disc":
			discCount++
		case "chapter":
			chapterCount++
		default:
			flatCount++
		}
		if m.hasNum {
			numberedCount++
		}
		if m.normPrefix != "" {
			prefixVotes[m.normPrefix]++
			if _, seen := prefixDisplay[m.normPrefix]; !seen {
				prefixDisplay[m.normPrefix] = m.prefix
			}
		}
		if na := normTitle(m.book.Author); na != "" {
			authorVotes[na]++
		}
	}
	dominantPrefix, dominantCount := topStr(prefixVotes)
	dominantAuthorNorm, dominantAuthorCount := topStr(authorVotes)
	if dominantAuthorCount*2 <= n {
		dominantAuthorNorm = "" // no majority author — leave the author guard inert
	}
	distinctPrefixes := len(prefixVotes)
	structure := majorityStructure(discCount, chapterCount, flatCount)

	// distinctStems / manyDistinctTitles are the CORE anthology signal (Bug 1): the
	// per-file title stems, ordinals already stripped when the prefix was derived. A
	// novel split into 133 sequential chapter files shares ONE stem ("Chapter", or the
	// book title) → distinctStems==1 → NOT an anthology, just a multi-track book. A real
	// SF anthology carries a DIFFERENT short-story title per track → distinctStems large.
	// We require a strong majority of members to carry their own stem to call it "many".
	distinctStems := distinctPrefixes
	manyDistinctTitles := distinctStems >= 3 && distinctStems*2 > n

	// folderNamedAfterBook: the members' dominant title prefix is a majority AND is a
	// substring of the book-folder name. This is the high-precision "these chapters
	// belong to THIS book" guard (mirrors fs_regroup.go's prefix ⊆ parent). It is
	// FALSE for correctly-stored series volumes (`Author/Series - N/file`: grandparent
	// = author, not named after the series), which keeps them out of the queue.
	folderNamedAfterBook := dominantPrefix != "" &&
		strings.Contains(normFolder, dominantPrefix) &&
		dominantCount*2 >= n

	sortMembers(members)
	assignDiscTrack(members)

	// The recommendation is computed ONCE, before the Kind switch, and closed over by
	// build — it depends only on the members' runtimes, never on which branch fires.
	// Keeping it out of build's signature keeps all nine build(...) call sites
	// byte-identical, so this change cannot perturb the Kind switch by accident.
	evidence := gatherEvidence(members, distinctPrefixes, numberedCount, structure)
	recAction, recReason := recommendAction(evidence, members, len(fpVotes))
	survivorTitle := survivorTitleFor(folderName, folderNamedAfterBook,
		prefixDisplay[dominantPrefix], dominantCount*2 >= n, dominantAuthorNorm)

	build := func(kind string, confident bool, action string, distinctWorks int) RegroupGroup {
		out := make([]ShatterBook, 0, n)
		for _, m := range members {
			out = append(out, m.book)
		}

		// KindVersionGroup overrides the runtime recommendation. Both editions of
		// one work are book-length, so recommendAction would answer ActionSeparate
		// and — once approve dispatches on the chosen action — make
		// ApplyVersionGroup unreachable. See ActionVersionGroup's comment for the
		// full argument. The evidence block is still carried verbatim so the queue
		// shows the runtimes the human is judging.
		kindAction, kindReason := recAction, recReason
		if kind == KindVersionGroup {
			kindAction = ActionVersionGroup
			kindReason = fmt.Sprintf(
				"abridged and unabridged edition markers on a shared title across %d members "+
					"(longest %s) — different recordings of one work, so link them as versions "+
					"rather than merging or separating them",
				evidence.Members, humanRuntime(evidence.LongestKnownSec))
		}

		return RegroupGroup{
			FolderRef:              folderRef,
			Kind:                   kind,
			Confident:              confident,
			SurvivorTitle:          survivorTitle,
			ProposedAction:         action,
			Members:                out,
			DistinctWorks:          distinctWorks,
			Structure:              structure,
			RecommendedAction:      kindAction,
			RecommendationReason:   kindReason,
			RecommendationEvidence: evidence,
		}
	}

	switch {
	case mergedViaItunes:
		// The set spans multiple FilePath folders, glued only by a shared iTunes album
		// (Bug 2). The two identity signals disagree, so we hold rather than guess a
		// confident collapse — the human confirms whether these are one book.
		return build(KindAmbiguous, false,
			"review: grouped by a shared original iTunes album, but the file paths differ", 0), true

	case hasUnab && hasAb && dominantCount*2 >= n:
		// Abridged + Unabridged editions of the SAME book share a folder → 2-book
		// version group (decision #8). The dominant-prefix guard (a majority of members
		// share one book title after markers are stripped) prevents a false positive on
		// an AUTHOR folder that merely happens to hold one abridged + one unabridged of
		// two DIFFERENT books. Held for review; the human confirms the primary edition.
		return build(KindVersionGroup, false,
			"create a version group (Abridged + Unabridged), Unabridged primary", 0), true

	case hasMultiBookMarker && manyDistinctTitles:
		// A MULTI-book marker (trilogy/tetralogy/quartet/boxed set) with distinct title
		// stems → this is very likely SEVERAL books, each its own ISBN, NOT one book
		// (owner decision 2026-07-26). Combining would be wrong, so hold as ambiguous
		// with a clear note and let the human decide (keep separate, or combine a subset).
		// Checked BEFORE the single-book case so "Foundation Trilogy Collection" (both
		// markers) leans safe. DistinctWorks = the likely volume count, for the label.
		return build(KindAmbiguous, false,
			"review: looks like a multi-book set (trilogy/boxed set) — may be several separate books, not one",
			distinctStems), true

	case hasSingleBookMarker && manyDistinctTitles:
		// A SINGLE-book marker (anthology/omnibus/collection) on the BOOK FOLDER itself
		// AND multiple distinct title stems → a real anthology, which is ONE published
		// book (one ISBN), not multiple works to split (owner decision 2026-07-26). The
		// action is to COMBINE the files into one multi-file audiobook, like a disc set.
		// DistinctWorks is the story count, surfaced for the label.
		return build(KindAnthology, false,
			"combine into one multi-file audiobook (anthology/collection)",
			distinctStems), true

	case hasFolderAnthologyMarker:
		// Either marker on the folder, but the members are sequential / share one title
		// stem (e.g. `Foundation Trilogy - 1/2/3` could be 3 chapters OR 3 volumes). We
		// cannot confirm the boundaries, and a confident collapse would be wrong if they
		// are separate volumes → hold as ambiguous (prefer ambiguous over wrong).
		return build(KindAmbiguous, false,
			"review: collection/anthology marker on the folder, but work boundaries are unclear", 0), true

	case structure == "disc" && discCount*2 > n:
		// Majority of members live in Disc N / CD N subfolders → one multi-disc book.
		return build(KindMultidisc, true,
			"collapse disc set into 1 multi-file audiobook", 0), true

	case structure == "flat" && numberedCount*2 >= n && n >= flatMultitrackMin &&
		!manyDistinctTitles && !membersAreBookLength(members):
		// Many members sit directly in ONE book folder and are sequentially numbered →
		// flat multi-track collapse. The shared parent folder IS the book identity.
		//
		// OVER-MERGE GUARD (!manyDistinctTitles): a plain AUTHOR or COLLECTION folder
		// holding N *distinct* single-file books (e.g. `.../Audiobooks/Terry Pratchett
		// Discworld` = 70 different novels, or a flat `.../unsorted/books` dump) also
		// looks flat-and-numbered — most audiobook filenames carry SOME number (series
		// #, year, bitrate), so numberedCount alone can't tell 70 chapters of one book
		// from 70 different books. `manyDistinctTitles` (a strong majority of members
		// carry their OWN distinct title stem) is exactly that discriminator, and it
		// was already the anthology signal — here we also use it to REFUSE a confident
		// collapse. Such folders fall through to the flat-ambiguous / default cases
		// (no confident merge). This errs toward NOT grouping: a real book with
		// per-chapter descriptive titles is left shattered (recoverable later) rather
		// than N distinct books being wrongly merged into one (corruption). A dry-run
		// on 2026-07-14 flagged 24/196 confident-multidisc holds as this shape.
		//
		// SERIES GUARD (!membersAreBookLength): stems alone are not enough, because
		// numbered SEQUELS share one stem ("Super Sales on Super Heroes" / " 2" / " 3").
		// A 2026-08-05 dry-run found 41 of 43 candidates were exactly that — iTunes
		// AUTHOR folders holding a whole catalogue — and collapsing them would have
		// merged entire series into one book. Runtime separates the two cases where the
		// name cannot. See membersAreBookLength.
		return build(KindMultidisc, true,
			"collapse flat multi-track folder into 1 multi-file audiobook", 0), true

	case (structure == "chapter" || structure == "edition") && folderNamedAfterBook && distinctPrefixes <= 1:
		// Classic shatter (`<Book>/<Book> - N/file`) or a single edition folder
		// (`<Book>/<Book> (Unabridged)/file`), one consistent identity matching the
		// book folder → confident collapse.
		return build(KindMultidisc, true,
			"collapse chapter/edition shells into 1 multi-file audiobook", 0), true

	case (structure == "chapter" || structure == "edition") && folderNamedAfterBook && distinctPrefixes >= 2:
		// Book folder, but the sub-dirs carry ≥2 distinct title prefixes — mixed
		// identity. Confident collapse is unsafe; hold for review.
		return build(KindAmbiguous, false,
			"review: folder with mixed identities", 0), true

	case structure == "flat" && dominantPrefix != "" && dominantCount*2 >= n:
		// Flat folder whose members share a dominant title but are NOT cleanly
		// numbered (or too few to be confident) — book-like but uncertain. Hold.
		return build(KindAmbiguous, false,
			"review: flat folder shares a title but ordering is unclear", 0), true

	default:
		// No positive single-book evidence: an author folder, correctly-stored series
		// volumes, or a flat dump of distinct files. Emit no hold (avoids flooding).
		return RegroupGroup{}, false
	}
}

// majorityStructure returns the dominant structure among the three tallies. Ties
// break disc > chapter > flat (disc/chapter carry stronger single-book signal).
func majorityStructure(disc, chapter, flat int) string {
	if disc >= chapter && disc >= flat && disc > 0 {
		return "disc"
	}
	if chapter >= flat && chapter > 0 {
		return "chapter"
	}
	return "flat"
}

// topStr returns the most-voted key and its count.
func topStr(votes map[string]int) (string, int) {
	bestKey, best := "", 0
	for k, v := range votes {
		if v > best {
			bestKey, best = k, v
		}
	}
	return bestKey, best
}

// sortMembers orders members into play order: by physical disc, then within-disc
// chapter, then filename, then BookID for stability. For non-disc groups sortDisc is
// 0 for all, so this reduces to track-then-filename order.
func sortMembers(members []memberInfo) {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if a.sortDisc != b.sortDisc {
			return a.sortDisc < b.sortDisc
		}
		if a.sortTrack != b.sortTrack {
			return a.sortTrack < b.sortTrack
		}
		if a.fileBase != b.fileBase {
			return a.fileBase < b.fileBase
		}
		return a.book.BookID < b.book.BookID
	})
}

// assignDiscTrack stamps each member's book with the (DiscNumber, TrackNumber) the
// apply path writes onto the merged BookFile rows. It MUST run after sortMembers so
// the sequence follows play order.
//
// Owner decision (2026-07-26): discs don't exist — a combined book is ONE continuous
// track list. So DiscNumber is ALWAYS 0 and TrackNumber runs 1..N over the play-order
// sort, INCLUDING across former disc boundaries:
//
//	Disc 1/Ch1 → t1   Disc 1/Ch2 → t2   Disc 2/Ch1 → t3   Disc 2/Ch2 → t4
//
// The physical disc is used only to ORDER the files (via sortMembers' sortDisc key),
// never persisted. Numbers are contiguous and unique by construction, so the
// (disc, track, path) sort in GetBookFiles can never collide. A same-disc chapter set
// (e.g. "When We Were Sisters_1.mp3".."_6.mp3") is the identical rule: disc 0, tracks
// 1..N.
func assignDiscTrack(members []memberInfo) {
	for i := range members {
		members[i].book.DiscNumber = 0
		members[i].book.TrackNumber = i + 1
	}
}

// genericTitleRe matches a candidate title that names a POSITION rather than a work:
// "Volume 1", "Chapter", "Disc 3", "Part 2", "Untitled", or a bare "01". Such a
// string is a container label, never a book title.
//
// Anchored at both ends and matched against the WHOLE cleaned candidate, never as a
// substring — a substring match would eat legitimate titles like "The Rising Vol. 9"
// or "Book of the New Sun".
var genericTitleRe = regexp.MustCompile(
	`(?i)^(?:\d+|(?:vol(?:ume)?|bk|books?|parts?|pts?|discs?|cds?|chapters?|tracks?|sides?|sections?|untitled)\.?\s*\d*)$`)

// survivorTitleFor picks WHICH string should become the survivor title, then cleans
// it with deriveSurvivorTitle. It exists because reading the folder name alone —
// what this code did until 2026-08-06 — produced author names ("C. T. Phipps"), bare
// ordinals ("Volume 1"), and WRONG volume numbers (a folder named "… Vol. 01" whose
// member files all say "Vol. 9").
//
// The folder name and the members' dominant title stem are two independent claims
// about what this book is called, and the choice between them is evidence-ranked:
//
//  1. folderNamedAfterBook — the members' own stem is a majority AND appears inside
//     the folder name, so the two signals AGREE. The folder name is then the better
//     of the two: it is the full human title, while the stem has had its ordinals
//     stripped. This is also the case that keeps a correct existing behaviour.
//  2. Otherwise the two DISAGREE, and the members win. The files are closer to the
//     content than the directory that happens to hold them — this is what fixes the
//     "Vol. 01 folder / Vol. 9 files" case, where the folder carries a stale number.
//     Only a MAJORITY stem is trusted; a 1-of-6 plurality in an author folder is not
//     a title, it is whichever book sorted first.
//  3. If the members have nothing trustworthy to say, fall back to the folder name
//     anyway — but only after the disqualification guards. This step is why a flat
//     folder "Coraline" holding "01 - Chapter.mp3" … "08 - Chapter.mp3" still yields
//     "Coraline": its dominant stem is the generic word "Chapter", which step 2
//     rejects. Without this fallback, the fix would introduce a fresh instance of
//     the very bug it exists to remove.
//  4. If NEITHER source survives its guards, return "".
//
// 🔴 EMPTY IS BETTER THAN WRONG. A blank survivor title renders as "needs a title"
// in the review queue — visibly incomplete, and a reviewer supplies one. A WRONG
// title reads as authoritative and silently mislabels a book. SurvivorTitle is
// display-only today (regroup_apply.go calls CombineBooks with a nil override, so
// nothing is written to any Book row), but the label is what a reviewer decides on:
// a hold labelled with the author's name is one a reasonable reviewer rejects,
// throwing away a good regroup.
//
// dominantAuthorNorm is the NORMALIZED majority author, or "" when no author holds a
// majority (guard inert).
func survivorTitleFor(folderName string, folderNamedAfterBook bool,
	dominantStem string, dominantIsMajority bool, dominantAuthorNorm string) string {
	// acceptable applies the disqualification guards to an already-cleaned candidate.
	acceptable := func(cand string) bool {
		if cand == "" {
			return false
		}
		if genericTitleRe.MatchString(cand) {
			return false // "Volume 1", "Chapter", "01" — a position, not a work
		}
		if dominantAuthorNorm != "" && normTitle(cand) == dominantAuthorNorm {
			return false // the author's name, not the book's — the "C. T. Phipps" case
		}
		return true
	}

	fromFolder := deriveSurvivorTitle(folderName)
	if folderNamedAfterBook && acceptable(fromFolder) {
		return fromFolder
	}
	if dominantIsMajority {
		if fromMembers := deriveSurvivorTitle(dominantStem); acceptable(fromMembers) {
			return fromMembers
		}
	}
	if acceptable(fromFolder) {
		return fromFolder
	}
	return ""
}

// deriveSurvivorTitle CLEANS one candidate string into a survivor title: strip a
// leading "NN - " track prefix, a trailing "(era/year)" parenthetical, and a trailing
// " - N" number. Author is intentionally NOT derived from the path.
//
// This is the pure string transform only. WHICH string reaches it — the book-folder
// name or the members' dominant title stem — is survivorTitleFor's decision.
func deriveSurvivorTitle(folderName string) string {
	t := folderName
	t = leadingNumRe.ReplaceAllString(t, "")
	// Strip trailing parentheticals repeatedly, e.g. "Book (Unabridged) (2019)".
	for {
		stripped := trailingParenRe.ReplaceAllString(t, "")
		if stripped == t {
			break
		}
		t = stripped
	}
	t = trailingNumSuffixRe.ReplaceAllString(t, "")
	return strings.TrimSpace(t)
}
