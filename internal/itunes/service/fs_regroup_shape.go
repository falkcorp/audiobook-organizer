// file: internal/itunes/service/fs_regroup_shape.go
// version: 1.4.0
// guid: 1e7d4a92-3c85-4b60-9f21-6a8c0d5e2b47
// last-edited: 2026-07-26

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

	// DiscNumber / TrackNumber are OUTPUT fields, zero on the input view and
	// populated by classifyGroup (assignDiscTrack) for the members of a confident
	// multidisc collapse. They carry the per-file play-order the apply path
	// (ApplyMultidisc) writes onto the merged book's BookFile rows. Semantics:
	//   - a member living in a real "Disc N"/"CD N" subfolder gets DiscNumber = N
	//     (the true physical disc, e.g. a Star Wars boxed set) and a track rank
	//     WITHIN that disc;
	//   - a member that is just a sequentially-numbered chapter/flat file on ONE
	//     disc (e.g. "When We Were Sisters_1.mp3".."_6.mp3") gets DiscNumber = 0
	//     (no disc concept — do NOT spread fake disc numbers across chapters) and
	//     TrackNumber = its sequence position.
	// Both are collision-free within the group by construction (a running per-disc
	// counter), so the (disc, track, path) sort in GetBookFiles orders cleanly.
	DiscNumber  int
	TrackNumber int
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

// anthologyRe detects markers that a folder holds "multiple distinct works" —
// which regex cannot reliably split. It is now matched ONLY against the BOOK FOLDER
// NAME (never a parent dir or a track's album tag — see classifyGroup) AND is only
// promoted to KindAnthology when the members also show multiple distinct title stems
// (manyDistinctTitles). Because of that second gate, "collection"/"collected" are now
// SAFE to include: a genuine single "The Complete Collection" (sequential chapters,
// one stem) fails the distinct-titles gate and falls to an ambiguous review hold, not
// a wrong anthology split — so the term no longer risks flooding, and a real
// short-story "…Collection" folder is no longer missed. "complete" alone stays out
// (too weak — appears on ordinary unabridged titles).
var anthologyRe = regexp.MustCompile(`(?i)\b(antholog(?:y|ies)|omnibus|trilog(?:y|ies)|tetralog(?:y|ies)|quartet|boxed?\s*set|collect(?:ion|ions|ed))\b`)

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
	fileBase   string // filename without extension
	prefix     string // original-case title prefix (chapter prefix, or track prefix)
	normPrefix string // normalized prefix for consensus voting
	num        int    // chapter/track/disc number (0 when none)
	hasNum     bool
	fpKey      string // FilePath-derived book-folder key (always set)
	itKey      string // ITunesPath-derived book-folder key, namespaced; "" when no iTunes path
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
	mi.fileBase = fileBase

	switch {
	case discDirRe.MatchString(pbase):
		m := discDirRe.FindStringSubmatch(pbase)
		mi.structure = "disc"
		mi.num, _ = strconv.Atoi(m[1])
		mi.hasNum = true
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
		return filepath.Dir(parent), mi

	case editionMarkerRe.MatchString(pbase):
		// Parent dir is an edition sub-folder ("<Book> (Unabridged)"). Group by the
		// grandparent book folder; identity vote = the edition name minus its markers.
		mi.structure = "edition"
		mi.prefix = stripEditionMarkers(pbase)
		mi.normPrefix = normTitle(mi.prefix)
		mi.num, mi.hasNum = trackNum(fileBase)
		return filepath.Dir(parent), mi

	default:
		// Flat multi-track: the file sits directly in the book folder. Numbering comes
		// from the filename; the identity vote is the filename's title remainder (weak).
		mi.structure = "flat"
		mi.num, mi.hasNum = trackNum(fileBase)
		mi.prefix = titleRemainder(fileBase)
		mi.normPrefix = normTitle(mi.prefix)
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
	folderRef := dominantKey(fpVotes)
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
	hasFolderAnthologyMarker := anthologyRe.MatchString(folderName)

	// Structural tallies.
	var discCount, chapterCount, flatCount, numberedCount int
	prefixVotes := map[string]int{}
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
		}
	}
	dominantPrefix, dominantCount := topStr(prefixVotes)
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
	build := func(kind string, confident bool, action string, distinctWorks int) RegroupGroup {
		out := make([]ShatterBook, 0, n)
		for _, m := range members {
			out = append(out, m.book)
		}
		return RegroupGroup{
			FolderRef:      folderRef,
			Kind:           kind,
			Confident:      confident,
			SurvivorTitle:  deriveSurvivorTitle(folderName),
			ProposedAction: action,
			Members:        out,
			DistinctWorks:  distinctWorks,
			Structure:      structure,
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

	case hasFolderAnthologyMarker && manyDistinctTitles:
		// An anthology/omnibus/collection marker on the BOOK FOLDER itself AND multiple
		// distinct title stems → an anthology. Owner decision (2026-07-26): an anthology
		// is a SINGLE real book (one ISBN), not multiple works to split — so the action
		// is to COMBINE the files into one multi-file audiobook, exactly like a disc set.
		// DistinctWorks is still surfaced (it's the story/chapter count) for the label.
		return build(KindAnthology, false,
			"combine into one multi-file audiobook (anthology/collection)",
			distinctStems), true

	case hasFolderAnthologyMarker:
		// Anthology/trilogy/omnibus marker on the folder, but the members are sequential
		// / share one title stem (e.g. `Foundation Trilogy - 1/2/3` could be 3 chapters
		// OR 3 volumes). We cannot confirm distinct works, and a confident multi-disc
		// collapse would be wrong if they are separate volumes → hold as ambiguous
		// (maintainer rule: prefer ambiguous over confident-but-wrong).
		return build(KindAmbiguous, false,
			"review: collection/anthology marker on the folder, but work boundaries are unclear", 0), true

	case structure == "disc" && discCount*2 > n:
		// Majority of members live in Disc N / CD N subfolders → one multi-disc book.
		return build(KindMultidisc, true,
			"collapse disc set into 1 multi-file audiobook", 0), true

	case structure == "flat" && numberedCount*2 >= n && n >= flatMultitrackMin && !manyDistinctTitles:
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

// sortMembers orders members by chapter/track number, then BookID for stability.
func sortMembers(members []memberInfo) {
	sort.SliceStable(members, func(i, j int) bool {
		if members[i].num != members[j].num {
			return members[i].num < members[j].num
		}
		return members[i].book.BookID < members[j].book.BookID
	})
}

// assignDiscTrack stamps each member's book with the (DiscNumber, TrackNumber) the
// apply path will write onto the merged BookFile rows. It MUST run after sortMembers
// so the sequence follows play order. The rule mirrors the two real shapes the owner
// called out:
//
//   - A member in a genuine "Disc N"/"CD N" subfolder (structure=="disc") is a real
//     physical disc: DiscNumber = its disc-folder number, TrackNumber = its rank
//     within that disc. (A Star Wars boxed set with actual disc folders.)
//   - Any other member (flat / chapter / edition) is a sequential file on ONE disc:
//     DiscNumber = 0 (there is NO disc concept — never spread fake disc numbers 1..N
//     across chapters of a single recording), TrackNumber = its sequence position.
//
// TrackNumber is a running per-disc counter (disc 0 is its own bucket), so every
// (disc, track) pair in the group is unique — the (disc, track, path) ordering in
// GetBookFiles can never collide. We deliberately renumber to a contiguous 1..N per
// disc rather than trusting the parsed filename ordinal: the apply path only writes
// these when the file currently has NO disc/track metadata at all, so a clean
// contiguous sequence is the right default for an otherwise-unnumbered file.
//
// Assignment is by each member's OWN structure, not the group's classified Kind. This
// is intentional and correct: a file physically living in "Disc 2/" belongs to disc 2
// regardless of its neighbors, and a bare chapter file belongs to no disc. In the rare
// mixed folder (a stray loose file alongside real "Disc N" subfolders), the loose file
// gets disc 0 and the disc files get their true numbers — a sensible hybrid, never the
// failure the owner flagged (fake disc numbers 1..N spread across chapters of one
// recording), which only happens if you key off group order instead of real structure.
func assignDiscTrack(members []memberInfo) {
	trackByDisc := map[int]int{}
	for i := range members {
		disc := 0
		if members[i].structure == "disc" {
			disc = members[i].num
		}
		trackByDisc[disc]++
		members[i].book.DiscNumber = disc
		members[i].book.TrackNumber = trackByDisc[disc]
	}
}

// deriveSurvivorTitle turns a book-folder name into a clean survivor title: strip a
// leading "NN - " track prefix, a trailing "(era/year)" parenthetical, and a trailing
// " - N" number. Author is intentionally NOT derived from the path.
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
