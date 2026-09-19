// file: internal/scanner/chapter_consolidator.go
// version: 2.2.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-09-19

package scanner

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// chapterNumPrefixDetectRe matches a leading numeric chapter/track prefix:
// "01 - ", "02. ", "003:", "1 " etc. — must be at the start of the stem.
var chapterNumPrefixDetectRe = regexp.MustCompile(`^\d{1,3}[\s\-_\.\:]+`)

// Confidence levels a detected group carries.
const (
	ChapterConfidenceHigh   = "high"
	ChapterConfidenceMedium = "medium"
	ChapterConfidenceLow    = "low"
)

// ChapterGroup is a set of single-file book records that are one multi-file
// audiobook split into one record per chapter.
//
// BookIDs is in chapter order -- (disc, index), then file name, then ID -- so
// the order is deterministic whatever order the store listed the rows in.
// PrimaryBookID is BookIDs[0], the lowest-numbered chapter: a merge folds the
// others into it and plays its files first.
type ChapterGroup struct {
	PrimaryBookID string   `json:"primary_book_id"`
	BookIDs       []string `json:"book_ids"`
	// CommonTitle is the proposed title of the merged book: the track titles'
	// shared residual ("006_Head of the Wyrm" -> "Head of the Wyrm"), else the
	// folder's name ("Eldritch", never "157").
	CommonTitle   string  `json:"common_title"`
	TotalDuration float64 `json:"total_duration"` // sum of the KNOWN durations, seconds
	FileCount     int     `json:"file_count"`
	Directory     string  `json:"directory"`

	// IndexLabels is each member's position, parallel to BookIDs ("7", or
	// "2-05" for disc 2 track 5).
	IndexLabels []string `json:"index_labels"`
	// Gaps are the positions missing from the run ("7", "40..45", "2-3").
	Gaps []string `json:"gaps,omitempty"`
	// DeclaredTotal is M from "N of M" titles, when present.
	DeclaredTotal int `json:"declared_total,omitempty"`
	// DurationsKnown counts members with a known (> 0) duration. Durations
	// are advisory: unknown ones never keep a group out.
	DurationsKnown int      `json:"durations_known"`
	Confidence     string   `json:"confidence"`
	Reasons        []string `json:"reasons"`
	// MemberTitles, MemberFiles (base names) and MemberDurations (seconds,
	// 0 = unknown) describe each member, parallel to BookIDs, for review.
	MemberTitles    []string `json:"member_titles"`
	MemberFiles     []string `json:"member_files"`
	MemberDurations []int    `json:"member_durations"`
	// Blockers, when present, are why the group must not be merged; such a
	// group is returned in ChapterDetection.Blocked, never in Groups.
	Blockers []string `json:"blockers,omitempty"`
}

// ChapterDetectOptions tunes DetectChapterGroupsWithOptions.
type ChapterDetectOptions struct {
	// MinFiles is the smallest group reported. Defaults to 2 when <= 1.
	MinFiles int
	// MaxPerFileDuration (seconds) is advisory: members longer than it are
	// noted in the group's reasons. Defaults to 600 when <= 0. It no longer
	// keeps a group out.
	MaxPerFileDuration int
	// PathPrefix, when set, limits detection to books whose file path is the
	// prefix itself or lies beneath it (directory-boundary match, so "/a/b"
	// does not match "/a/bc").
	PathPrefix string
	// Exclude, when set, removes a book from detection entirely (counted in
	// SkippedExcluded). The chapter jobs use it for the owner's manual-only
	// libraries (Doctor Who / Big Finish / Torchwood).
	Exclude func(b *database.BookCore) bool
	// Protected, when set, returns a non-empty reason for a book a merge
	// must never touch (the active iTunes library). Its groups are still
	// detected and reported, as Blocked with that reason.
	Protected func(b *database.BookCore) string
}

// ChapterDetection is the full detector output.
type ChapterDetection struct {
	// Groups may be offered for a (reviewed) merge.
	Groups []ChapterGroup
	// Blocked are chapter-split groups found but not mergeable; each names
	// its Blockers (duplicate indices, non-primary versions, protected
	// library, disagreeing authors or totals, a too-sparse run...).
	Blocked []ChapterGroup
	// SkippedDuplicateCopies counts runs that are copies of one book, not
	// chapters: every member at the same position (not reported), or
	// full-length / mixed-container members (reported in Blocked).
	SkippedDuplicateCopies int
	// SkippedExcluded counts books removed by ChapterDetectOptions.Exclude.
	SkippedExcluded int
	// SkippedNotSingleFile counts sequence-titled records whose FilePath is
	// not an audio file (a folder: a multi-file book already).
	SkippedNotSingleFile int
	// SkippedNotSequence counts numbered runs rejected as not chapters
	// (bare years, a bare run that starts far from 1 with few members, a
	// too-sparse run of fewer than minSparseReport members).
	SkippedNotSequence int
}

// stripNumPrefix removes a leading numeric chapter/track number from a filename
// stem, e.g. "01 - My Book" → "My Book", "002. Foo" → "Foo".
func stripNumPrefix(title string) string {
	return strings.TrimSpace(chapterNumPrefixDetectRe.ReplaceAllString(title, ""))
}

// normForCompare lowercases, replaces non-alphanumeric chars with spaces, and
// collapses whitespace for a stable comparison key.
func normForCompare(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// chapterTokenRe matches a within-book position token and its number:
// "Chapter 3", "Part 2", "Track 07", "Disc 1", "CD2", "pt.3". These are the
// ONLY words allowed to differ between two files of one chapter group. Series
// and episode numbers ("Book 2", "Volume 3", "Episode 4") are deliberately
// absent: they name different books.
var chapterTokenRe = regexp.MustCompile(`(?i)[\s\-_.,:]*\b(chapter|chap|ch|track|trk|part|pt|disc|disk|cd|section)[\s\-_.]*\d+\b`)

// minChapterKeyLen is the shortest non-empty normalised residual that may
// group. A one/two-character residual ("01 - It") carries no evidence that
// two files belong together; an EMPTY residual (a bare number) is a
// different case, grouped by folder.
const minChapterKeyLen = 3

// fullBookSeconds is the per-file length above which a member reads as a
// whole book rather than a chapter, for the duplicate-copies check.
const fullBookSeconds = 3600

// maxSparseFraction is the largest share of missing positions in a run's
// span a group may have and still be offered for merge.
const maxSparseFraction = 0.20

// minSparseReport is the smallest too-sparse run still reported (as
// Blocked); a smaller one is dropped as not a sequence.
const minSparseReport = 5

// audioExts are the single-file audio containers a chapter record can be.
var audioExts = map[string]bool{
	".mp3": true, ".m4b": true, ".m4a": true, ".aac": true, ".flac": true,
	".ogg": true, ".oga": true, ".opus": true, ".wma": true, ".wav": true,
	".aif": true, ".aiff": true, ".ape": true, ".mp4": true, ".alac": true,
}

// looksLikeDuplicateCopies reports whether members with one identical,
// non-empty residual are several copies of a book: each a full book long and
// within 10% of each other. It is false when any duration is unknown -- no
// evidence either way, and the review sees the run.
func looksLikeDuplicateCopies(members []database.BookCore) bool {
	lo, hi := -1, 0
	for _, m := range members {
		if m.Duration == nil || *m.Duration <= 0 {
			return false
		}
		d := *m.Duration
		if d < fullBookSeconds {
			return false
		}
		if lo < 0 || d < lo {
			lo = d
		}
		if d > hi {
			hi = d
		}
	}
	return lo > 0 && float64(hi) <= float64(lo)*1.10
}

// DetectChapterGroups is DetectChapterGroupsWithOptions without a path
// filter, returning only the mergeable groups.
func DetectChapterGroups(books []database.BookCore, minFiles, maxPerFileDuration int) []ChapterGroup {
	return DetectChapterGroupsWithOptions(books, ChapterDetectOptions{
		MinFiles:           minFiles,
		MaxPerFileDuration: maxPerFileDuration,
	}).Groups
}

// pathUnderPrefix reports whether p is prefix or lies beneath it.
func pathUnderPrefix(p, prefix string) bool {
	prefix = filepath.Clean(prefix)
	p = filepath.Clean(p)
	if p == prefix {
		return true
	}
	if strings.HasSuffix(prefix, string(filepath.Separator)) {
		return strings.HasPrefix(p, prefix) // prefix is the root
	}
	return strings.HasPrefix(p, prefix+string(filepath.Separator))
}

// chapterBookLive reports whether a book may take part in detection at all.
// Books already absorbed by an earlier merge (MergedIntoBookID) and
// soft-deleted books (a chapter merge soft-deletes its sources) are out, so a
// re-run can never regroup or re-merge what a previous run already merged.
// Non-primary versions ARE detected, but only ever reported as Blocked.
func chapterBookLive(b *database.BookCore) bool {
	if b.MergedIntoBookID != nil && *b.MergedIntoBookID != "" {
		return false
	}
	return !b.IsSoftDeleted()
}

// seqCand is one record read as a chapter.
type seqCand struct {
	book  database.BookCore
	stem  string
	ext   string
	shape string
	disc  int
	index int
	total int
	// titleMarker is true when the TITLE (not just the file) is a position;
	// titleBare when it is ONLY a position.
	titleMarker, titleBare bool
	// key is the normalised residual the record groups by; key2 the part of
	// it before a chapter token (the book's name when the rest is a
	// chapter's own name), used only when key alone groups nothing.
	key, key2 string
	// titleDisplay is the residual from the TITLE, for the proposed title;
	// fileDisplay the residual from the file stem.
	titleDisplay, fileDisplay string
}

func (c seqCand) label() string {
	if c.disc > 0 {
		return fmt.Sprintf("%d-%02d", c.disc, c.index)
	}
	return fmt.Sprint(c.index)
}

// classifySeqCand reads one record. ok=false when neither its title nor its
// file stem is a sequence marker, or its residual is too short to mean
// anything.
func classifySeqCand(b database.BookCore) (seqCand, bool) {
	base := filepath.Base(b.FilePath)
	ext := strings.ToLower(filepath.Ext(base))
	c := seqCand{book: b, ext: ext, stem: strings.TrimSuffix(base, filepath.Ext(base))}
	tm, tok := ParseSequenceMarker(b.Title)
	fm, fok := ParseFilenameSequence(c.stem)
	if !tok && !fok {
		return c, false
	}
	if !tok && !seqTitleCarriesNothing(b.Title, b.FilePath, fm) {
		// A numbered FILE under a real title ("The Saga - 01.m4b" titled
		// "Leviathan Rising", "Dunes_2.m4b" titled "Book 2") is a volume of
		// a series, not a chapter: the file name alone never makes a
		// candidate. The title must be a position, or say nothing the file
		// does not (empty, the file's own name, or its residual).
		return c, false
	}
	c.titleMarker = tok
	// A bare title ("157", "Part 3") says nothing but a position; it needs
	// corroboration whatever key the file supplies.
	c.titleBare = tok && sequenceResidualKey(tm.Residual) == ""
	var residual string
	if tok {
		c.shape, c.disc, c.index, c.total = tm.Shape, tm.Disc, tm.Index, tm.Total
		if tm.Shape == SeqShapeBare && fok && fm.Disc > 0 && fm.Index == tm.Index {
			c.disc = fm.Disc // "5" in "2-05 The Yard.mp3" is disc 2 track 5
		}
		residual = tm.Residual
		if sequenceResidualKey(tm.Residual) != "" {
			c.titleDisplay = sequenceResidualDisplay(tm.Residual)
		} else if fok {
			// A bare title ("157") takes its grouping key from the file
			// ("Eldritch - 157.mp3"), which separates distinct works that
			// share a folder.
			residual = fm.Residual
			if c.total == 0 && fm.Index == c.index {
				c.total = fm.Total
			}
		}
	} else {
		c.shape, c.disc, c.index, c.total = fm.Shape, fm.Disc, fm.Index, fm.Total
		residual = fm.Residual
	}
	if fok {
		c.fileDisplay = sequenceResidualDisplay(fm.Residual)
	}
	c.key = sequenceResidualKey(residual)
	if c.key != "" && len(strings.ReplaceAll(c.key, " ", "")) < minChapterKeyLen {
		return c, false
	}
	c.key2 = c.key
	if b, found := sequenceResidualBase(residual); found {
		c.key2 = sequenceResidualKey(b)
	}
	return c, true
}

// seqTitleCarriesNothing reports whether a non-position title adds nothing
// to what the file stem says: empty, the stem itself (with or without its
// number), or exactly the stem's residual.
func seqTitleCarriesNothing(title, filePath string, fm SequenceMarker) bool {
	if normForCompare(title) == "" || ChapterTitleIsFilenameDerived(title, filePath) {
		return true
	}
	fk := sequenceResidualKey(fm.Residual)
	return fk != "" && sequenceResidualKey(title) == fk
}

// versionIndex maps a version group to the directories of its primary
// copies, built from every live book so a non-primary member can say where
// its primary lives.
type versionIndex map[string][]string

func buildVersionIndex(books []database.BookCore) versionIndex {
	vi := versionIndex{}
	for i := range books {
		b := &books[i]
		if b.VersionGroupID == nil || *b.VersionGroupID == "" || !chapterBookLive(b) {
			continue
		}
		if database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
			vi[*b.VersionGroupID] = append(vi[*b.VersionGroupID], filepath.Dir(b.FilePath))
		}
	}
	return vi
}

// dirResult is one folder's detection output.
type dirResult struct {
	groups, blocked                       []ChapterGroup
	dupCopies, notSingleFile, notSequence int
}

// DetectChapterGroupsWithOptions inspects already-scanned records and
// returns the single-file records that are one multi-file book split into a
// record per chapter.
//
// A record is a candidate when its TITLE (or, failing that, its file stem)
// is a sequence marker (ParseSequenceMarker / ParseFilenameSequence) and its
// FilePath is an audio file. Candidates group when they share:
//
//  1. the parent folder, and
//  2. the normalised residual (what is left once the position is removed).
//     A bare title ("157") takes its residual from the file ("Eldritch -
//     157.mp3"). Bare residuals group with the one residual that equals the
//     folder's name. Records whose residuals differ are different books and
//     never group, which is what keeps a folder of organized series singles
//     ("09 - Ruins", "10 - Rise") and year-prefixed books apart.
//
// A group is then checked, and any failure makes it Blocked (reported with
// the reason, never mergeable): repeated positions (two copies), "N of M"
// totals that disagree, a run with more than maxSparseFraction missing,
// different known authors, mixed containers, full-length copies, a
// non-primary member (names where its primary lives), or a Protected member.
// Durations are advisory: summed when known, outliers noted, never required.
//
// Folders are processed in parallel (errgroup, NumCPU workers). Each folder is
// independent -- groups never span folders -- and each worker writes only its
// own slot, so there is no shared mutable state; results are assembled in
// sorted folder order, so output is deterministic.
func DetectChapterGroupsWithOptions(books []database.BookCore, opts ChapterDetectOptions) ChapterDetection {
	var out ChapterDetection
	if len(books) == 0 {
		return out
	}
	if opts.MinFiles < 2 {
		opts.MinFiles = 2
	}
	if opts.MaxPerFileDuration <= 0 {
		opts.MaxPerFileDuration = 600
	}
	vi := buildVersionIndex(books)

	byDir := make(map[string][]database.BookCore)
	for i := range books {
		b := books[i]
		if !chapterBookLive(&b) {
			continue
		}
		if opts.Exclude != nil && opts.Exclude(&b) {
			out.SkippedExcluded++
			continue
		}
		if opts.PathPrefix != "" && !pathUnderPrefix(b.FilePath, opts.PathPrefix) {
			continue
		}
		if b.FilePath == "" {
			continue
		}
		dir := filepath.Dir(b.FilePath)
		byDir[dir] = append(byDir[dir], b)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	results := make([]dirResult, len(dirs))
	var eg errgroup.Group
	eg.SetLimit(runtime.NumCPU())
	for i, dir := range dirs {
		eg.Go(func() error {
			results[i] = detectInDir(dir, byDir[dir], opts, vi)
			return nil
		})
	}
	_ = eg.Wait() // workers never return an error

	for _, r := range results {
		out.Groups = append(out.Groups, r.groups...)
		out.Blocked = append(out.Blocked, r.blocked...)
		out.SkippedDuplicateCopies += r.dupCopies
		out.SkippedNotSingleFile += r.notSingleFile
		out.SkippedNotSequence += r.notSequence
	}
	return out
}

// detectInDir groups one folder's candidates.
func detectInDir(dir string, books []database.BookCore, opts ChapterDetectOptions, vi versionIndex) dirResult {
	var res dirResult
	buckets := map[string][]seqCand{}
	for _, b := range books {
		c, ok := classifySeqCand(b)
		if !ok {
			continue
		}
		if !audioExts[c.ext] {
			res.notSingleFile++
			continue
		}
		buckets[c.key] = append(buckets[c.key], c)
	}
	if len(buckets) == 0 {
		return res
	}
	// A record alone under its full residual whose residual is "book name +
	// chapter name" regroups by the book name alone.
	for _, k := range sortedBucketKeys(buckets) {
		cs := buckets[k]
		if len(cs) != 1 || cs[0].key2 == cs[0].key {
			continue
		}
		// Only with corroboration: a known chapter-length duration. A
		// book-length record whose name contains "Part 2 - Subtitle" is a
		// volume, and must never regroup by the part before the token.
		if d := cs[0].book.Duration; d == nil || *d <= 0 || *d >= fullBookSeconds {
			continue
		}
		delete(buckets, k)
		buckets[cs[0].key2] = append(buckets[cs[0].key2], cs[0])
	}
	// Bare residuals join the one residual that IS the folder's name, but
	// only with corroboration (known durations in the run's range, same
	// container and codec). Without it they are reported apart, for review.
	folderKey := chapterFolderKey(dir)
	bareReview := ""
	if bare, ok := buckets[""]; ok && folderKey != "" {
		if named, ok := buckets[folderKey]; ok {
			if seqBareJoinCorroborated(named, bare) {
				buckets[folderKey] = append(named, bare...)
				delete(buckets, "")
			} else {
				bareReview = fmt.Sprintf("needs review: %d bare-titled record(s) beside the %q run, without matching durations/format to show they belong to it", len(bare), named[0].fileDisplayOr(named[0].titleDisplay))
			}
		}
	}
	for _, k := range sortedBucketKeys(buckets) {
		cs := buckets[k]
		if len(cs) < opts.MinFiles {
			continue
		}
		review := ""
		if k == "" {
			review = bareReview
		}
		g, verdict := evaluateSeqBucket(dir, k, folderKey, review, cs, opts, vi)
		switch verdict {
		case seqVerdictNotSequence:
			res.notSequence++
		case seqVerdictSamePosition:
			res.dupCopies++
		case seqVerdictDupCopies:
			res.dupCopies++
			res.blocked = append(res.blocked, g)
		default:
			if len(g.Blockers) > 0 {
				res.blocked = append(res.blocked, g)
			} else {
				res.groups = append(res.groups, g)
			}
		}
	}
	return res
}

func (c seqCand) fileDisplayOr(alt string) string {
	if alt != "" {
		return alt
	}
	return c.fileDisplay
}

// seqBareJoinCorroborated reports whether bare-titled records may join a
// named run: every bare member's duration is known and within 4x of the
// run's known durations, and container and codec match the run's.
func seqBareJoinCorroborated(named, bare []seqCand) bool {
	lo, hi := 0, 0
	exts := map[string]bool{}
	codecs := map[string]bool{}
	for _, c := range named {
		exts[c.ext] = true
		if c.book.Codec != nil && *c.book.Codec != "" {
			codecs[strings.ToLower(*c.book.Codec)] = true
		}
		if c.book.Duration != nil && *c.book.Duration > 0 {
			d := *c.book.Duration
			if lo == 0 || d < lo {
				lo = d
			}
			hi = max(hi, d)
		}
	}
	if lo == 0 || len(exts) != 1 {
		return false
	}
	for _, c := range bare {
		if !exts[c.ext] || c.book.Duration == nil || *c.book.Duration <= 0 {
			return false
		}
		if d := *c.book.Duration; d*4 < lo || d > hi*4 {
			return false
		}
		if c.book.Codec != nil && *c.book.Codec != "" && len(codecs) > 0 && !codecs[strings.ToLower(*c.book.Codec)] {
			return false
		}
	}
	return true
}

// catchAllFolders are folder names that say nothing about which book a
// record belongs to; a bare-numbered run in one is never offered, and the
// name is never proposed as a title.
var catchAllFolders = map[string]bool{
	"unknown": true, "unknown author": true, "unknown artist": true, "various": true,
	"various artists": true, "audiobooks": true, "audiobook": true, "books": true,
	"misc": true, "miscellaneous": true, "unsorted": true, "imported": true,
	"incoming": true, "downloads": true, "download": true, "new": true,
	"voice memos": true, "itunes media": true, "media": true, "music": true,
	"library": true, "abooks": true, "newbooks": true, "temp": true, "tmp": true,
	"moved": true, "incomplete": true, "complete": true,
}

func isCatchAllFolder(dir string) bool {
	return catchAllFolders[normForCompare(filepath.Base(dir))]
}

func sortedBucketKeys(m map[string][]seqCand) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type seqVerdict int

const (
	seqVerdictGroup seqVerdict = iota
	seqVerdictNotSequence
	seqVerdictDupCopies
	seqVerdictSamePosition
)

// evaluateSeqBucket checks one same-folder, same-residual bucket and builds
// its group.
func evaluateSeqBucket(dir, key, folderKey, review string, cs []seqCand, opts ChapterDetectOptions, vi versionIndex) (ChapterGroup, seqVerdict) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.disc != b.disc {
			return a.disc < b.disc
		}
		if a.index != b.index {
			return a.index < b.index
		}
		if a.stem != b.stem {
			return a.stem < b.stem
		}
		return a.book.ID < b.book.ID
	})
	n := len(cs)
	lo, hi := cs[0].index, cs[0].index
	allYears := true
	for _, c := range cs {
		lo, hi = min(lo, c.index), max(hi, c.index)
		if c.index < 1800 || c.index > 2100 {
			allYears = false
		}
	}
	if key == "" && review == "" && (allYears || (lo > 2 && n < 5)) {
		return ChapterGroup{}, seqVerdictNotSequence
	}
	// Every member at ONE position ("31 - Title" twice in a series folder)
	// is copies of one book, not chapters of it: dedup's job, not reported.
	samePos := true
	for _, c := range cs[1:] {
		if c.disc != cs[0].disc || c.index != cs[0].index {
			samePos = false
			break
		}
	}
	// Two records pointing at ONE file are duplicate records of that file,
	// not two chapters.
	paths := map[string]int{}
	for _, c := range cs {
		paths[c.book.FilePath]++
	}
	if samePos || len(paths) < 2 {
		return ChapterGroup{}, seqVerdictSamePosition
	}

	g := ChapterGroup{Directory: dir, FileCount: n, Confidence: ChapterConfidenceHigh}
	demote := func(to string) {
		if to == ChapterConfidenceLow || g.Confidence == ChapterConfidenceHigh {
			g.Confidence = to
		}
	}
	// missingEv lists the evidence a "high" group needs and this one lacks:
	// position TITLES, a contiguous run, known chapter-length durations.
	var missingEv []string
	if review != "" {
		g.Blockers = append(g.Blockers, review)
	}
	shapes := map[string]int{}
	nonMarkerTitles := 0
	for _, c := range cs {
		g.BookIDs = append(g.BookIDs, c.book.ID)
		g.IndexLabels = append(g.IndexLabels, c.label())
		g.MemberTitles = append(g.MemberTitles, c.book.Title)
		g.MemberFiles = append(g.MemberFiles, filepath.Base(c.book.FilePath))
		d := 0
		if c.book.Duration != nil && *c.book.Duration > 0 {
			d = *c.book.Duration
		}
		g.MemberDurations = append(g.MemberDurations, d)
		shapes[c.shape]++
		if !c.titleMarker {
			nonMarkerTitles++
		}
	}
	if nonMarkerTitles > 0 {
		missingEv = append(missingEv, fmt.Sprintf("%d track title(s) are not positions (only the file name is numbered)", nonMarkerTitles))
	}
	g.PrimaryBookID = g.BookIDs[0]
	g.Reasons = append(g.Reasons, fmt.Sprintf("%d single-file records in one folder titled as positions (%s)", n, seqShapeSummary(shapes)))

	// Positions: duplicates, gaps, totals.
	seen := map[[2]int]int{}
	byDisc := map[int][]int{}
	totals := map[int]bool{}
	for _, c := range cs {
		seen[[2]int{c.disc, c.index}]++
		byDisc[c.disc] = append(byDisc[c.disc], c.index)
		if c.total > 0 {
			totals[c.total] = true
		}
	}
	var dups []string
	for _, c := range cs {
		k := [2]int{c.disc, c.index}
		if seen[k] > 1 {
			dups = append(dups, fmt.Sprintf("%s (x%d)", c.label(), seen[k]))
			seen[k] = 0
		}
	}
	if len(dups) > 0 {
		g.Blockers = append(g.Blockers, fmt.Sprintf("duplicate index %s: two copies of the book in one folder?", seqJoinCapped(dups, 8)))
	}
	span, missing := 0, 0
	discs := make([]int, 0, len(byDisc))
	for d := range byDisc {
		discs = append(discs, d)
	}
	sort.Ints(discs)
	for _, d := range discs {
		idx := byDisc[d]
		sort.Ints(idx)
		dlo, dhi := idx[0], idx[len(idx)-1]
		span += dhi - dlo + 1
		have := map[int]bool{}
		for _, i := range idx {
			have[i] = true
		}
		for _, r := range seqMissingRanges(dlo, dhi, have) {
			missing += r[1] - r[0] + 1
			g.Gaps = append(g.Gaps, seqRangeLabel(d, r))
		}
	}
	switch {
	case len(totals) > 1:
		var ts []string
		for t := range totals {
			ts = append(ts, fmt.Sprint(t))
		}
		sort.Strings(ts)
		g.Blockers = append(g.Blockers, "N of M totals disagree ("+strings.Join(ts, ", ")+"): more than one set")
	case len(totals) == 1:
		for t := range totals {
			g.DeclaredTotal = t
		}
		g.Reasons = append(g.Reasons, fmt.Sprintf("titles declare %d parts; %d present", g.DeclaredTotal, n))
	}
	if span > 0 && float64(missing)/float64(span) > maxSparseFraction && n < minSparseReport && review == "" {
		// A handful of scattered positions ("23 - Title" and "29 - Title"
		// in one series folder) is not a split book worth reporting.
		return ChapterGroup{}, seqVerdictNotSequence
	}
	if shared := n - len(paths); shared > 0 {
		g.Blockers = append(g.Blockers, fmt.Sprintf("%d member(s) share a file path with another member: duplicate records of one file", shared))
	}
	if span > 0 && float64(missing)/float64(span) > maxSparseFraction {
		g.Blockers = append(g.Blockers, fmt.Sprintf("index run too sparse: %d of %d positions missing between %d and %d", missing, span, lo, hi))
	} else if missing > 0 {
		g.Reasons = append(g.Reasons, fmt.Sprintf("positions %d..%d with %d missing (see gaps)", lo, hi, missing))
		missingEv = append(missingEv, fmt.Sprintf("gaps in the run (%d missing)", missing))
	} else {
		g.Reasons = append(g.Reasons, fmt.Sprintf("positions %d..%d contiguous", lo, hi))
	}
	if lo > 1 && len(discs) == 1 {
		g.Reasons = append(g.Reasons, fmt.Sprintf("run starts at %d: earlier parts may be titled differently", lo))
	}

	// Plain numbering beside disc-track numbering is the same book twice
	// ("03 Title" and "1-03 Title"), not one book of twice the length.
	if _, plain := byDisc[0]; plain && len(byDisc) > 1 {
		g.Blockers = append(g.Blockers, "mixes plain and disc-track numbering: two copies of the book in one folder?")
	}

	// Authors: unknown is fine, two different known authors is not.
	authors := map[int]bool{}
	for _, c := range cs {
		if c.book.AuthorID != nil {
			authors[*c.book.AuthorID] = true
		}
	}
	if len(authors) > 1 {
		g.Blockers = append(g.Blockers, fmt.Sprintf("members have %d different authors", len(authors)))
	}

	// Containers and full-length copies.
	exts := map[string]bool{}
	members := make([]database.BookCore, n)
	for i, c := range cs {
		exts[c.ext] = true
		members[i] = c.book
	}
	verdict := seqVerdictGroup
	if len(exts) > 1 {
		g.Blockers = append(g.Blockers, "mixed audio containers ("+strings.Join(sortedSetKeys(exts), ", ")+"): probably two copies")
		verdict = seqVerdictDupCopies
	} else if key != "" && looksLikeDuplicateCopies(members) {
		g.Blockers = append(g.Blockers, "members are each a full book long and nearly equal: copies of one book (dedup's job)")
		verdict = seqVerdictDupCopies
	}

	// Versions: a non-primary member's primary lives elsewhere.
	var nonPrimary []seqCand
	inGroup := map[string]bool{}
	for _, c := range cs {
		inGroup[filepath.Dir(c.book.FilePath)] = true
		if !database.EffectiveIsPrimaryVersion(c.book.IsPrimaryVersion) {
			nonPrimary = append(nonPrimary, c)
		}
	}
	if len(nonPrimary) > 0 {
		where := map[string]int{}
		orphans := 0
		for _, c := range nonPrimary {
			vg := ""
			if c.book.VersionGroupID != nil {
				vg = *c.book.VersionGroupID
			}
			dirs := vi[vg]
			if vg == "" || len(dirs) == 0 {
				orphans++
				continue
			}
			for _, d := range dirs {
				where[d]++
			}
		}
		msg := fmt.Sprintf("%d of %d members are non-primary versions", len(nonPrimary), n)
		if len(nonPrimary) == n {
			msg = fmt.Sprintf("all %d members are non-primary versions: review the primary copies instead", n)
		}
		if len(where) > 0 {
			msg += "; primaries live in " + seqTopDirs(where, 3)
		}
		if orphans > 0 {
			msg += fmt.Sprintf("; %d have no primary in their version group", orphans)
		}
		g.Blockers = append(g.Blockers, msg)
	}
	if opts.Protected != nil {
		for _, c := range cs {
			b := c.book
			if why := opts.Protected(&b); why != "" {
				g.Blockers = append(g.Blockers, why+": the merge refuses to touch these records")
				break
			}
		}
	}

	// Durations: advisory for a titled run, corroborating evidence for a
	// bare one.
	var known []int
	long, bookLength := 0, 0
	for _, c := range cs {
		if c.book.Duration != nil && *c.book.Duration > 0 {
			d := *c.book.Duration
			known = append(known, d)
			g.TotalDuration += float64(d)
			if d >= opts.MaxPerFileDuration {
				long++
			}
			if d >= fullBookSeconds {
				bookLength++
			}
		}
	}
	g.DurationsKnown = len(known)
	g.Reasons = append(g.Reasons, fmt.Sprintf("durations known for %d of %d", len(known), n))
	if len(known) < n {
		missingEv = append(missingEv, fmt.Sprintf("durations unknown for %d of %d", n-len(known), n))
	}
	if bookLength > 0 {
		missingEv = append(missingEv, fmt.Sprintf("%d member(s) are book-length (>= %d s)", bookLength, fullBookSeconds))
	}
	// Every member with a known duration book-length is a set of volumes,
	// not chapters -- unless the titles declare "N of M" parts.
	declared := g.DeclaredTotal > 0
	knownAllBook := len(known) >= 2 && bookLength == len(known)
	if knownAllBook && !declared {
		g.Blockers = append(g.Blockers, fmt.Sprintf("every member with a known duration (%d) is book-length and no \"N of M\" total is declared: separate volumes, not chapters", len(known)))
	}
	bareTitles := 0
	for _, c := range cs {
		if c.titleBare {
			bareTitles++
		}
	}
	if key == "" && isCatchAllFolder(dir) {
		g.Blockers = append(g.Blockers, fmt.Sprintf("folder %q is a catch-all: nothing shows these bare-numbered records are one book", filepath.Base(dir)))
	}
	if key == "" || bareTitles > 0 {
		// A title that is only a position ("157") says nothing about which
		// book it is, whatever key the file supplied; it needs
		// corroboration.
		if len(known) < n {
			g.Blockers = append(g.Blockers, fmt.Sprintf("needs durations: bare position titles are corroborated only by known chapter-length durations (%d of %d known)", len(known), n))
		}
		if len(authors) == 0 {
			g.Blockers = append(g.Blockers, "no author known on any member: nothing ties the bare-numbered records together")
		}
	}
	// More than one book-length member never reaches medium; a run whose
	// positions come only from trailing file numbers under non-position
	// titles ("Lore - 001" episodes) is indistinguishable from episodes or
	// volumes and is low at most.
	forceLow := ""
	if bookLength > 1 {
		forceLow = fmt.Sprintf("%d members are book-length", bookLength)
	}
	trailingOnly := true
	for _, c := range cs {
		if c.shape != SeqShapeTrailing || c.titleMarker {
			trailingOnly = false
			break
		}
	}
	if trailingOnly {
		forceLow = "positions come only from trailing file numbers under non-position titles (episodes and volumes look the same)"
	}
	if long > 0 {
		g.Reasons = append(g.Reasons, fmt.Sprintf("%d member(s) at least %d s long", long, opts.MaxPerFileDuration))
	}
	if len(known) >= 3 {
		sorted := append([]int(nil), known...)
		sort.Ints(sorted)
		med := sorted[len(sorted)/2]
		var outliers []string
		for _, c := range cs {
			if c.book.Duration == nil || *c.book.Duration <= 0 {
				continue
			}
			if d := *c.book.Duration; d > 4*med || d*4 < med {
				outliers = append(outliers, fmt.Sprintf("%s (%ds)", c.label(), d))
			}
		}
		if len(outliers) > 0 {
			g.Reasons = append(g.Reasons, fmt.Sprintf("duration outliers vs median %ds: %s", med, seqJoinCapped(outliers, 6)))
			missingEv = append(missingEv, "duration outliers")
		}
	}

	// Proposed title.
	g.CommonTitle = seqProposeTitle(dir, key, folderKey, cs, &g, demote)

	// Confidence: high only with every piece of evidence; each missing
	// piece is named.
	if forceLow != "" {
		missingEv = append(missingEv, forceLow)
	}
	switch {
	case forceLow != "" || len(missingEv) >= 2:
		demote(ChapterConfidenceLow)
	case len(missingEv) == 1:
		demote(ChapterConfidenceMedium)
	}
	if len(missingEv) > 0 {
		g.Reasons = append(g.Reasons, "missing evidence: "+strings.Join(missingEv, "; "))
	}
	return g, verdict
}

// seqProposeTitle picks the merged book's title: the track titles' shared
// residual; else the file stems' residual when it agrees with the folder;
// else the folder's name.
func seqProposeTitle(dir, key, folderKey string, cs []seqCand, g *ChapterGroup, demote func(string)) string {
	var fromTitles, fromFiles []string
	for _, c := range cs {
		if c.titleDisplay != "" && sequenceResidualKey(c.titleDisplay) == key {
			fromTitles = append(fromTitles, c.titleDisplay)
		}
		// Only file residuals that ARE the group's key: a chapter-named
		// file ("01 Chapter 1 - The Hunter") grouped by its book-name part
		// says nothing about the book's title.
		if key != "" && c.fileDisplay != "" && sequenceResidualKey(c.fileDisplay) == key {
			fromFiles = append(fromFiles, c.fileDisplay)
		}
	}
	if t := seqMostCommon(fromTitles); t != "" && key != "" {
		if key == folderKey {
			g.Reasons = append(g.Reasons, "track titles' residual matches the folder name")
		} else {
			g.Reasons = append(g.Reasons, "title from the track titles' shared residual")
		}
		return t
	}
	folder := chapterFolderDisplay(dir)
	if isCatchAllFolder(dir) {
		folder = "" // "Unknown Author" is never a book title
	}
	file := seqMostCommon(fromFiles)
	fileKey := sequenceResidualKey(file)
	switch {
	case file != "" && (folderKey == "" || fileKey == folderKey):
		g.Reasons = append(g.Reasons, "title from the file names (bare track titles)")
		return file
	case folder != "":
		if file != "" && fileKey != "" {
			if strings.Contains(fileKey, folderKey) {
				g.Reasons = append(g.Reasons, fmt.Sprintf("file names say %q; proposed the folder name it contains", file))
				demote(ChapterConfidenceMedium)
			} else {
				g.Reasons = append(g.Reasons, fmt.Sprintf("file names say %q but the folder says %q: check the title", file, folder))
				demote(ChapterConfidenceLow)
			}
		} else {
			g.Reasons = append(g.Reasons, "bare track titles: title from the folder name")
			demote(ChapterConfidenceMedium)
		}
		return folder
	default:
		demote(ChapterConfidenceLow)
		return file
	}
}

func seqShapeSummary(shapes map[string]int) string {
	keys := sortedSetKeys(shapes)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, shapes[k])
	}
	return strings.Join(parts, ", ")
}

func sortedSetKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// seqMissingRanges returns the [from, to] runs in lo..hi absent from have.
func seqMissingRanges(lo, hi int, have map[int]bool) [][2]int {
	var out [][2]int
	for i := lo; i <= hi; i++ {
		if have[i] {
			continue
		}
		if len(out) > 0 && out[len(out)-1][1] == i-1 {
			out[len(out)-1][1] = i
		} else {
			out = append(out, [2]int{i, i})
		}
	}
	return out
}

func seqRangeLabel(disc int, r [2]int) string {
	pre := ""
	if disc > 0 {
		pre = fmt.Sprintf("%d-", disc)
	}
	if r[0] == r[1] {
		return fmt.Sprintf("%s%d", pre, r[0])
	}
	return fmt.Sprintf("%s%d..%s%d", pre, r[0], pre, r[1])
}

func seqJoinCapped(ss []string, limit int) string {
	if len(ss) <= limit {
		return strings.Join(ss, ", ")
	}
	return strings.Join(ss[:limit], ", ") + fmt.Sprintf(" and %d more", len(ss)-limit)
}

// seqTopDirs names up to limit directories by count, most first.
func seqTopDirs(where map[string]int, limit int) string {
	dirs := sortedSetKeys(where)
	sort.SliceStable(dirs, func(i, j int) bool { return where[dirs[i]] > where[dirs[j]] })
	parts := make([]string, 0, limit)
	for i, d := range dirs {
		if i == limit {
			parts = append(parts, fmt.Sprintf("%d more", len(dirs)-limit))
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", d, where[d]))
	}
	return strings.Join(parts, ", ")
}

// seqMostCommon returns the most frequent non-empty string, ties broken
// lexically.
func seqMostCommon(ss []string) string {
	counts := map[string]int{}
	for _, s := range ss {
		if s != "" {
			counts[s]++
		}
	}
	best, bestN := "", 0
	for _, s := range sortedSetKeys(counts) {
		if counts[s] > bestN {
			best, bestN = s, counts[s]
		}
	}
	return best
}

// ChapterTitleIsFilenameDerived reports whether title is still the
// scanner's filename-derived title for filePath: empty, or equal (after
// normalisation) to the file's stem with or without its numeric chapter
// prefix. A chapter merge may replace such a title with the group's common
// title; any other title was curated (by a metadata apply or by hand) and a
// merge must leave it alone.
func ChapterTitleIsFilenameDerived(title, filePath string) bool {
	nt := normForCompare(title)
	if nt == "" {
		return true
	}
	stem := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	return nt == normForCompare(stem) || nt == normForCompare(stripNumPrefix(stem))
}

// ChapterTitleIsReplaceable reports whether a chapter merge may replace the
// primary's title with the group's proposed title: the title is still
// filename-derived, or it is nothing but a position ("157", "108 of 310",
// "Part 3", "02.Prologue") -- a title no one curated.
func ChapterTitleIsReplaceable(title, filePath string) bool {
	if ChapterTitleIsFilenameDerived(title, filePath) {
		return true
	}
	m, ok := ParseSequenceMarker(title)
	return ok && sequenceResidualKey(m.Residual) == ""
}
