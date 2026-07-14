// file: internal/itunes/service/fs_regroup_shape.go
// version: 1.0.0
// guid: 1e7d4a92-3c85-4b60-9f21-6a8c0d5e2b47
// last-edited: 2026-07-13

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
	KindAnthology    = "regroup.anthology"     // multiple distinct works (anthology/trilogy/omnibus)
	KindAmbiguous    = "regroup.ambiguous"     // book-like folder, cannot confidently classify
)

// ShatterBook is the slim per-book view the classifier reasons over. Build it from
// a Book plus its BookFiles: FileCount comes from the BookFile rows, FilePath is the
// real BookFile path when the book owns exactly one file, else the virtual
// Book.FilePath.
type ShatterBook struct {
	BookID    string
	FilePath  string // real BookFile path (preferred) or virtual Book.FilePath
	FileCount int    // BookFiles owned; > 1 means already a real multi-file book
	IsPrimary bool   // non-primary version members are ignored
	Title     string // scanner-derived title (album tag); may be empty
	Author    string // display author when known; never used for the FOLDER key
}

// RegroupGroup is one book folder the classifier flagged for a review hold.
type RegroupGroup struct {
	FolderRef      string        // the book folder (grandparent for chapter/disc; parent for flat)
	Kind           string        // one of the Kind* constants
	Confident      bool          // true only for confident multidisc collapses
	SurvivorTitle  string        // derived: strip "NN - " prefix and "(era/year)" / " - N" suffix
	ProposedAction string        // human-readable proposed action
	Members        []ShatterBook // ordered by chapter/track number then BookID
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

// anthologyRe detects STRONG markers that a folder holds "multiple distinct works"
// — which regex cannot reliably split, so these are held for review. Deliberately
// excludes weak terms like "collection"/"complete" that appear on legitimate single
// audiobooks ("The Complete Collection", unabridged) to avoid mislabeling them.
var anthologyRe = regexp.MustCompile(`(?i)\b(antholog(?:y|ies)|omnibus|trilog(?:y|ies)|tetralog(?:y|ies)|quartet|boxed?\s*set)\b`)

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

	byKey := make(map[string][]memberInfo)
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
		key, mi := folderKeyOf(b.FilePath)
		mi.book = b
		byKey[key] = append(byKey[key], mi)
	}

	groups := make([]RegroupGroup, 0, len(byKey))
	for key, members := range byKey {
		if len(members) < 2 {
			st.Singletons++ // genuine single-file book — leave alone
			continue
		}
		g, emit := classifyGroup(key, members)
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
func classifyGroup(key string, members []memberInfo) (RegroupGroup, bool) {
	n := len(members)
	folderName := filepath.Base(key)
	normFolder := normTitle(folderName)

	// Marker text = the folder name + every member's parent-dir basename, filename,
	// and title. Editions/anthology markers can live on any of them.
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
	hasAnthology := anthologyRe.MatchString(text)

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

	// folderNamedAfterBook: the members' dominant title prefix is a majority AND is a
	// substring of the book-folder name. This is the high-precision "these chapters
	// belong to THIS book" guard (mirrors fs_regroup.go's prefix ⊆ parent). It is
	// FALSE for correctly-stored series volumes (`Author/Series - N/file`: grandparent
	// = author, not named after the series), which keeps them out of the queue.
	folderNamedAfterBook := dominantPrefix != "" &&
		strings.Contains(normFolder, dominantPrefix) &&
		dominantCount*2 >= n

	sortMembers(members)
	build := func(kind string, confident bool, action string) RegroupGroup {
		out := make([]ShatterBook, 0, n)
		for _, m := range members {
			out = append(out, m.book)
		}
		return RegroupGroup{
			FolderRef:      key,
			Kind:           kind,
			Confident:      confident,
			SurvivorTitle:  deriveSurvivorTitle(folderName),
			ProposedAction: action,
			Members:        out,
		}
	}

	switch {
	case hasUnab && hasAb && dominantCount*2 >= n:
		// Abridged + Unabridged editions of the SAME book share a folder → 2-book
		// version group (decision #8). The dominant-prefix guard (a majority of members
		// share one book title after markers are stripped) prevents a false positive on
		// an AUTHOR folder that merely happens to hold one abridged + one unabridged of
		// two DIFFERENT books. Held for review; the human confirms the primary edition.
		return build(KindVersionGroup, false,
			"create a version group (Abridged + Unabridged), Unabridged primary"), true

	case hasAnthology:
		// Anthology/trilogy/omnibus → multiple distinct works; regex cannot split
		// them (decision #9), so hold for review.
		return build(KindAnthology, false,
			"split into separate works (held — needs review to identify boundaries)"), true

	case structure == "disc" && discCount*2 > n:
		// Majority of members live in Disc N / CD N subfolders → one multi-disc book.
		return build(KindMultidisc, true,
			"collapse disc set into 1 multi-file audiobook"), true

	case structure == "flat" && numberedCount*2 >= n && n >= flatMultitrackMin:
		// Many members sit directly in ONE book folder and are sequentially numbered →
		// flat multi-track collapse. The shared parent folder IS the book identity.
		return build(KindMultidisc, true,
			"collapse flat multi-track folder into 1 multi-file audiobook"), true

	case (structure == "chapter" || structure == "edition") && folderNamedAfterBook && distinctPrefixes <= 1:
		// Classic shatter (`<Book>/<Book> - N/file`) or a single edition folder
		// (`<Book>/<Book> (Unabridged)/file`), one consistent identity matching the
		// book folder → confident collapse.
		return build(KindMultidisc, true,
			"collapse chapter/edition shells into 1 multi-file audiobook"), true

	case (structure == "chapter" || structure == "edition") && folderNamedAfterBook && distinctPrefixes >= 2:
		// Book folder, but the sub-dirs carry ≥2 distinct title prefixes — mixed
		// identity. Confident collapse is unsafe; hold for review.
		return build(KindAmbiguous, false,
			"review: folder with mixed identities"), true

	case structure == "flat" && dominantPrefix != "" && dominantCount*2 >= n:
		// Flat folder whose members share a dominant title but are NOT cleanly
		// numbered (or too few to be confident) — book-like but uncertain. Hold.
		return build(KindAmbiguous, false,
			"review: flat folder shares a title but ordering is unclear"), true

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
