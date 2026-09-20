// file: internal/plugins/maintenance/book_shape_report.go
// version: 1.1.0
// guid: 3f0c5a71-8d4e-4a92-9b16-2c7e5d40ab31
// last-edited: 2026-09-20

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- book-shape-report ---
//
// 🔴 WHY THIS EXISTS. The 2026-09-19 read-only investigation
// (.claude/notes/oversized-and-split-books-2026-09-19.md) measured four
// distinct defects that all wear one signature — "two book rows at one path" —
// and one more that wears none: a single book that owns a whole author shelf.
// On prod at that time: 205 books held >200 book_file rows and carried 20.1% of
// every book_file row in the library; 526 two-member version groups shared a
// path; 2,409 directory paths carried more than one book row. NO existing op
// covers any of it — dedupe-book-file-rows is within-book, merge-same-path-dupes
// excludes directory books by construction, fs-regroup-xml's chapter_folder_layout
// apply is unimplemented, probe-directory-books only re-classifies.
//
// 🔴 REPORT-ONLY, DELIBERATELY, AND WITH NO APPLY MODE AT ALL. This is not a
// "dry run by default" op: there is no apply parameter to flip. It never
// requests CapLibraryWrite, and its store interface (bookShapeStore below)
// lists only two read methods, so a write cannot compile into this file without
// a reviewer having to widen that interface on purpose.
//
// What it DOES write, and the only thing it writes: one per-finding TSV report
// under {RootDir}/.reports/ (the report_path.go convention shared with
// mark-missing-files and its siblings). No book, book_file, version group, or
// file on disk is modified, created, or deleted.
//
// 🔴 WHY A REPORT AND NOT A REPAIR. The shapes need OPPOSITE treatments, and
// one of them must never be merged (see shapeDifferentContent). A blind
// same-path merge would attach an iTunes author shelf's 1,494 files to a
// single-title record. Every finding therefore carries a RECOMMENDED treatment
// in words, for a later repair op or a human to act on — this op implements
// none of them.
//
// The scanner rules that CREATE these shapes were fixed in #3481 and #3488, so
// a repair built on this report is no longer re-broken by the next scan.

// --- shapes ---
//
// The shape names are the report's vocabulary. They are deliberately stable
// strings: a later repair op is expected to select rows by shape.
const (
	// shapeOversized is one book owning more rows than the threshold. Per-book,
	// not per-group: the Gene Wolfe shelf is eight oversized books that are ALSO
	// one duplicate group, and both facts are reported.
	shapeOversized = "oversized-book"

	// shapeDuplicateRowsAtPath is the raw fact that N>1 book rows share one
	// grouping path, independent of what their file sets look like. Emitted for
	// every multi-member group so the census in the report matches the
	// investigation's "2,409 directory paths carry more than one book row".
	shapeDuplicateRowsAtPath = "duplicate-book-rows-at-path"

	// shapeContainment is A's file set ⊆ B's: the SAME physical files carry rows
	// under two book ids (Awaken Online, Nightflyers, Ben Hale in the notes).
	shapeContainment = "containment"

	// shapePartialOverlap is a non-empty intersection that is not a containment.
	// Neither a clean duplicate nor a clean split.
	shapePartialOverlap = "partial-overlap"

	// shapePartition is the only shape the notes found that is a TRUE split:
	// disjoint file sets whose union equals the audio files on disk in the
	// folder (Wind and Truth: 110 + 101 = exactly the 211 on disk). Neither
	// record is complete and both are user-visible.
	shapePartition = "partition"

	// shapeDisjointUnaccounted is disjoint file sets whose union does NOT equal
	// the disk count. The gate that makes a partition merge sound is missing, so
	// this is not a merge candidate.
	shapeDisjointUnaccounted = "disjoint-unaccounted"

	// shapeDifferentContent is the shape that must NEVER be merged: the book
	// rows share a path but their FILE rows point somewhere else — an iTunes
	// author shelf vs the organized folder, or a pile of collision-suffix dirs.
	shapeDifferentContent = "different-content"

	// shapeOneSideEmpty is a multi-member group where at least one member owns
	// zero rows (98 of the 526 same-path groups). "Disjoint" is technically true
	// of ∅ and B, which is exactly why this is branched out before the ladder
	// can recommend merging into an empty record.
	shapeOneSideEmpty = "one-side-empty"

	// shapeAllMembersEmpty is a multi-member group where NO member owns a row
	// (285 of the 526).
	shapeAllMembersEmpty = "all-members-empty"

	// shapeOrphanedFiles is files on disk under the group directory that no book
	// row owns (Foundation: 498 owned rows against 668 files on disk). NOT
	// mutually exclusive with the shapes above — emitted as an extra finding.
	shapeOrphanedFiles = "orphaned-files"

	// shapeConcatenatedPath is a stored path containing a second absolute path
	// appended to the first.
	shapeConcatenatedPath = "concatenated-absolute-path"

	// shapeCollisionSuffixExplosion is N sibling "<prefix> - <n>" directories
	// whose rows all name ONE basename — a rename/organize retry loop that
	// re-suffixed its destination once per attempt (Magi'i of Cyador: 113 dirs,
	// all holding a file named 117.mp3).
	shapeCollisionSuffixExplosion = "collision-suffix-explosion"
)

// bookShapeRecommendations maps each shape to the treatment a repair op or a
// human should apply. The text is the deliverable: this op implements none of
// these.
var bookShapeRecommendations = map[string]string{
	shapeOversized: "REVIEW, do not auto-split. A directory book this large is usually a whole author " +
		"shelf recorded as one work. Re-run maintenance.probe-directory-books to re-classify the folder, " +
		"and split into per-work books only under human review. Note the per-book work skew: any per-book " +
		"worker pool gets one worker holding this book's entire inner loop.",
	shapeDuplicateRowsAtPath: "TRIAGE by the accompanying shape finding for this path — the treatment " +
		"differs per shape and three of them are not merges.",
	shapeContainment: "MERGE: keep the record owning the superset, soft-delete the subset twin and its " +
		"duplicate book_file rows, then recompute aggregates. This is merge-same-path-dupes semantics " +
		"widened from 'same audio file' to 'same directory AND subset file set'; the subset test is the " +
		"safety gate that replaces its hash gate.",
	shapePartialOverlap: "INVESTIGATE before any merge: the sets neither duplicate nor partition each " +
		"other, so neither the containment gate nor the partition gate holds.",
	shapePartition: "MERGE and re-own: move every row under one book, then recompute aggregates. Gate the " +
		"merge on all three facts this row already carries — disjoint sets, same directory, and an owned " +
		"path set that is EXACTLY the set of files present on disk (not merely the same count, which a " +
		"missing row can fake). Without the disk set the gate is unsound.",
	shapeDisjointUnaccounted: "DO NOT MERGE on the disjointness alone: the union does not account for the " +
		"folder. Re-scan the folder (the scanner rules were fixed in #3481/#3488) and re-report before " +
		"treating it as a partition.",
	shapeDifferentContent: "DO NOT MERGE. The rows point outside this book's own path — merging would " +
		"attach another folder's files to this record. Repoint or delete the stale/mangled side first, " +
		"then re-report.",
	shapeOneSideEmpty: "INVESTIGATE the zero-row member: it may be a phantom record to retire, or a row " +
		"whose files were repointed away. Never merge INTO the empty record.",
	shapeAllMembersEmpty: "INVESTIGATE: no member owns a file row, so there is nothing to merge. Likely " +
		"phantom records at a path whose rows were deleted or repointed.",
	shapeOrphanedFiles: "RE-OWN, not merge: files on disk under this folder belong to no book row and are " +
		"invisible to the app. Re-scan the folder after the #3481/#3488 scanner fix, or create the missing " +
		"book_file rows under the owning book.",
	shapeConcatenatedPath: "CORRUPT ROW: an absolute path was appended to another absolute path. Repoint " +
		"the row to its real path or delete it. Never use this row as evidence in a merge or dedup decision.",
	shapeCollisionSuffixExplosion: "COLLAPSE the '<prefix> - N' siblings: this is an organize/rename retry " +
		"loop that re-suffixed its destination once per attempt. Repoint the rows to one destination folder " +
		"and remove the empty suffixed directories. Not a merge candidate.",
}

// --- defaults ---
const (
	// bookShapeDefaultOversized is the row count above which a book is reported
	// as oversized. 200 is the investigation's own threshold (205 books above it,
	// holding 20.1% of all book_file rows).
	bookShapeDefaultOversized = 200

	// bookShapeDefaultCollisionMin is how many sibling "<prefix> - N" directories
	// sharing one basename it takes to call a collision-suffix explosion. The two
	// measured cases were 113 and 20 directories, so 3 is a wide margin; it is a
	// judgment call, hence a parameter.
	bookShapeDefaultCollisionMin = 3

	// bookShapeDefaultSampleLimit bounds how many findings are echoed to the log.
	// The TSV holds all of them.
	bookShapeDefaultSampleLimit = 50

	// bookShapeLivenessEvery stamps the liveness clock every N groups inside a
	// worker. RunItems only stamps BETWEEN items, and one item here is a shard of
	// groups that can contain a 1,494-entry directory on a slow mount — so
	// without this the 5-minute stuck-op watchdog could kill a healthy run. Same
	// shape as bookFileStatLivenessEvery in bookfile_batch_write.go.
	bookShapeLivenessEvery = 32
)

// collisionSuffixRe matches the "<prefix> - <n>" directory basename that an
// organize retry loop mints.
var collisionSuffixRe = regexp.MustCompile(`^(.*) - (\d+)$`)

// bookShapeReportParams are the JSON parameters accepted by the op. There is no
// apply parameter, by design — see the file comment.
type bookShapeReportParams struct {
	// OversizedThreshold is the rows-per-book count above which a book is
	// reported as oversized (0 = bookShapeDefaultOversized).
	OversizedThreshold int `json:"oversizedThreshold,omitempty"`

	// CollisionSuffixMin is how many sibling "<prefix> - N" directories sharing
	// one basename it takes to report a collision-suffix explosion
	// (0 = bookShapeDefaultCollisionMin).
	CollisionSuffixMin int `json:"collisionSuffixMin,omitempty"`

	// SampleLimit bounds how many findings are echoed to the log (0 = default).
	SampleLimit int `json:"sampleLimit,omitempty"`

	// ReportPath overrides where the per-finding TSV is written. Empty =
	// {RootDir}/.reports/book-shape-report-<opID>.tsv.
	ReportPath string `json:"reportPath,omitempty"`

	// Concurrency overrides the worker-pool size (0 = runtime.NumCPU()).
	Concurrency int `json:"concurrency,omitempty"`

	// SkipDiskStat turns off the per-directory readdir. The partition and
	// orphaned-files shapes REQUIRE the disk count, so with this set those two
	// findings cannot be produced and disk_files reads -1 everywhere; the run
	// says so in its summary rather than silently degrading.
	SkipDiskStat bool `json:"skipDiskStat,omitempty"`
}

func (p bookShapeReportParams) oversizedThreshold() int {
	if p.OversizedThreshold > 0 {
		return p.OversizedThreshold
	}
	return bookShapeDefaultOversized
}

func (p bookShapeReportParams) collisionSuffixMin() int {
	if p.CollisionSuffixMin > 0 {
		return p.CollisionSuffixMin
	}
	return bookShapeDefaultCollisionMin
}

func (p bookShapeReportParams) sampleLimit() int {
	if p.SampleLimit > 0 {
		return p.SampleLimit
	}
	return bookShapeDefaultSampleLimit
}

// bookShapeStore is this op's ENTIRE store surface. Both methods are reads;
// there is no write method to call, which is the compile-time half of the
// read-only guarantee (the other half is the capability list on the def).
type bookShapeStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
}

// bookShapeFinding is one row of the report. One finding per (shape, subject),
// NOT one per book: a book can be oversized AND a member of a duplicate group
// AND hold a corrupt path, and all three are reported.
type bookShapeFinding struct {
	Shape string `json:"shape"`
	// Dir is the grouping path: the book's own FilePath when it is
	// directory-shaped, otherwise the directory holding its audio file.
	Dir string `json:"dir"`
	// BookIDs is the subject: one id for a per-book shape, every member for a
	// group shape.
	BookIDs []string `json:"book_ids"`
	// PathKind is what the filesystem says the grouping path is: dir, file,
	// missing, unreadable, or not-statted. REPORTED, never an input to the
	// grouping — see bookShapeGroupKey.
	PathKind string `json:"path_kind"`
	// LibraryState and IsPrimary are the book's own fields for a per-book shape,
	// and a comma-joined list in member order for a group shape.
	LibraryState string `json:"library_state"`
	IsPrimary    string `json:"is_primary_version"`
	// OwnedRows is how many book_file rows the subject owns, DiskFiles how many
	// audio files the grouping directory holds (-1 when unknown), so
	// "owned vs on disk" is visible on every row.
	OwnedRows int `json:"owned_rows"`
	DiskFiles int `json:"disk_files"`
	// Detail is the shape-specific evidence; Recommendation is the treatment.
	Detail         string `json:"detail"`
	Recommendation string `json:"recommendation"`
}

// bookShapeReport is the outcome of one sweep.
type bookShapeReport struct {
	TotalBooks     int
	TotalFileRows  int
	EmptyFilePath  int
	Groups         int
	MultiBookPaths int
	// ShapeCounts is findings per shape, the headline census.
	ShapeCounts map[string]int
	// Findings holds every finding, sorted for a deterministic, diffable report.
	Findings []bookShapeFinding
	// MergedAway counts records excluded from grouping because they are already
	// absorbed into another book.
	MergedAway int
	// UnreadableDirs counts grouping directories the op could not read. These
	// are REPORTED (DiskFiles = -1, PathKind = unreadable), never fatal.
	UnreadableDirs int
	// DiskStatSkipped records that the disk pass was turned off, so a reader
	// knows why no partition/orphan findings appear.
	DiskStatSkipped bool
	ReportPath      string
}

func (r bookShapeReport) summary() string {
	shapes := make([]string, 0, len(r.ShapeCounts))
	for s := range r.ShapeCounts {
		shapes = append(shapes, s)
	}
	sort.Strings(shapes)
	parts := make([]string, 0, len(shapes))
	for _, s := range shapes {
		parts = append(parts, fmt.Sprintf("%s=%d", s, r.ShapeCounts[s]))
	}
	skipped := ""
	if r.DiskStatSkipped {
		skipped = " disk_stat=SKIPPED(no partition/orphan findings possible)"
	}
	return fmt.Sprintf("books=%d file_rows=%d empty_path=%d merged_away=%d groups=%d multi_book_paths=%d unreadable_dirs=%d%s findings=%d [%s]",
		r.TotalBooks, r.TotalFileRows, r.EmptyFilePath, r.MergedAway, r.Groups, r.MultiBookPaths,
		r.UnreadableDirs, skipped, len(r.Findings), strings.Join(parts, " "))
}

func (p *Plugin) bookShapeReportDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.book-shape-report",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Book shape report",
		Description: "Classifies oversized books, duplicate book rows at one path, the four same-path " +
			"split shapes (partition, containment, different-content, orphaned files) and corrupt paths " +
			"(concatenated absolutes, collision-suffix explosions), and names a recommended treatment for " +
			"each. REPORT-ONLY: there is no apply mode; the only output is a TSV under {root}/.reports/.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.book-shape-report",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		// 🔴 READ ONLY. CapLibraryWrite is never requested, so a write cannot be
		// authorized even if a future edit tried to make one.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runBookShapeReport,
	}
}

func (p *Plugin) runBookShapeReport(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params bookShapeReportParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	// Resolve the report path BEFORE any work: a run that could not record its
	// findings should fail with nothing done rather than spend an hour walking
	// the library and then lose the answer.
	reportPath, err := p.resolveReportPath(params.ReportPath, opReportFileName(reporter, "book-shape-report"))
	if err != nil {
		return err
	}

	report, err := buildBookShapeReport(ctx, store, params, reporter)
	if err != nil {
		return err
	}

	log := reporter.Logger()
	if wErr := writeBookShapeReport(reportPath, report.Findings); wErr != nil {
		log.Error("book-shape-report: FAILED to write the per-finding report",
			"path", reportPath, "err", wErr, "findings", len(report.Findings))
	} else {
		report.ReportPath = reportPath
		log.Info("book-shape-report: per-finding report written", "path", reportPath, "findings", len(report.Findings))
	}

	limit := params.sampleLimit()
	for i, f := range report.Findings {
		if i >= limit {
			break
		}
		log.Info("book-shape-report: finding", "shape", f.Shape, "dir", f.Dir, "book_ids", f.BookIDs,
			"path_kind", f.PathKind, "owned_rows", f.OwnedRows, "disk_files", f.DiskFiles, "detail", f.Detail)
	}
	log.Info("book-shape-report complete (REPORT ONLY, nothing modified)", "summary", report.summary())
	return nil
}

// bookShapeGroupKey is the grouping key for a book, and it is a PURE function of
// the stored path — no filesystem call.
//
// 🔴 A stat must never decide the grouping. On prod many of these paths are
// missing or on a slow mount; letting a stat failure reshuffle a book into a
// different group would make the report non-deterministic between runs, which
// is exactly what the sorted output exists to prevent. Whether the path is a
// directory or a file is a REPORTED column (bookShapeFinding.PathKind), derived
// from the stat afterwards.
func bookShapeGroupKey(filePath string) string {
	p := strings.TrimSpace(filePath)
	if p == "" {
		return ""
	}
	if linkintegrity.IsAudioFile(p) {
		return filepath.Dir(p)
	}
	return p
}

// hasConcatenatedAbsolutePath reports the concatenation defect: an absolute path
// appended to another absolute path, e.g.
// "/<root>/A/B/The Sword of the Lictor" + "/<root>/A/The Sword of the Lictor".
//
// The test is that the path's OWN first segment reappears later with at least
// one segment after it. Deriving the marker from the path itself keeps every
// site-specific root string out of this file.
//
// It is a heuristic and will fire on a legitimate path that contains a directory
// named the same as its own root segment. That is the right trade for a report:
// a false positive costs a human one glance, and tightening it would miss real
// corruption.
func hasConcatenatedAbsolutePath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	segs := strings.Split(strings.Trim(filepath.Clean(p), "/"), "/")
	if len(segs) < 3 {
		return false
	}
	root := segs[0]
	if root == "" {
		return false
	}
	for i := 1; i < len(segs)-1; i++ {
		if segs[i] == root {
			return true
		}
	}
	return false
}

// bookShapeDirStat is what one readdir of a grouping directory yields. It is
// taken ONCE PER GROUP and shared by every member: the Gene Wolfe folder holds
// eight books over 1,494 files, and reading it eight times would be eight times
// the I/O and could give two members different denominators.
type bookShapeDirStat struct {
	Kind       string // dir | file | missing | unreadable | not-statted
	AudioFiles int    // -1 when unknown
	// Present is the SET of audio file paths actually in the directory. The
	// orphan and partition tests compare SETS against it, never two integers:
	// the library holds ~66,753 rows whose file no longer exists, so a folder
	// can hold more owned rows than files on disk while still having files
	// nobody owns. An integer comparison hides exactly the Foundation shape
	// this op exists to find. Nil when the directory could not be read.
	Present map[string]struct{}
}

// statGroupDir stats the grouping path and counts the audio files directly
// inside it. Non-recursive on purpose: the loose-file folder in the
// investigation holds 7,179 subdirectories, and the disk counts this report is
// compared against were `find -maxdepth 1` counts.
//
// An unreadable directory is REPORTED (Kind=unreadable, AudioFiles=-1), never
// fatal: one bad mount point must not cost the whole library sweep.
func statGroupDir(dir string) bookShapeDirStat {
	info, err := os.Stat(dir)
	switch {
	case err != nil && os.IsNotExist(err):
		return bookShapeDirStat{Kind: "missing", AudioFiles: -1}
	case err != nil:
		return bookShapeDirStat{Kind: "unreadable", AudioFiles: -1}
	case !info.IsDir():
		return bookShapeDirStat{Kind: "file", AudioFiles: -1}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return bookShapeDirStat{Kind: "unreadable", AudioFiles: -1}
	}
	present := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if linkintegrity.IsAudioFile(e.Name()) {
			present[filepath.Join(dir, e.Name())] = struct{}{}
		}
	}
	return bookShapeDirStat{Kind: "dir", AudioFiles: len(present), Present: present}
}

// bookShapeMember is one book row inside a group, with the file paths it owns.
type bookShapeMember struct {
	book  database.BookCore
	paths []string
	set   map[string]struct{}
}

// bookShapeGroup is every book row sharing one grouping path.
type bookShapeGroup struct {
	dir     string
	members []bookShapeMember
}

func boolField(b *bool) string {
	if b == nil {
		return "unset"
	}
	return strconv.FormatBool(*b)
}

func strField(s *string) string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return "unset"
	}
	return *s
}

func bookShapeConcurrency(override int) int {
	if override > 0 {
		return override
	}
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	return n
}

// shardGroups splits groups into up to n contiguous shards. A group is never
// split across shards: every classification is group-local, so a worker owning a
// whole group needs no shared state beyond the findings reduce.
func shardGroups(groups []bookShapeGroup, n int) [][]bookShapeGroup {
	if n < 1 {
		n = 1
	}
	if len(groups) == 0 {
		return nil
	}
	if n > len(groups) {
		n = len(groups)
	}
	shards := make([][]bookShapeGroup, 0, n)
	base := len(groups) / n
	rem := len(groups) % n
	start := 0
	for i := 0; i < n; i++ {
		size := base
		if i < rem {
			size++
		}
		if size == 0 {
			continue
		}
		shards = append(shards, groups[start:start+size])
		start += size
	}
	return shards
}

// buildBookShapeReport performs the sweep and RETURNS the report, so the counts
// and findings can be asserted as values rather than scraped from log lines.
func buildBookShapeReport(ctx context.Context, store bookShapeStore, params bookShapeReportParams, reporter sdk.Reporter) (bookShapeReport, error) {
	log := reporter.Logger()
	log.Info("book-shape-report start",
		"oversized_threshold", params.oversizedThreshold(),
		"collision_suffix_min", params.collisionSuffixMin(),
		"skip_disk_stat", params.SkipDiskStat)

	// One limit-0 call = one consistent snapshot; paging with offset across
	// calls can skip or repeat rows if the memdb snapshot swaps between pages.
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return bookShapeReport{}, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	fileRows, err := store.GetAllBookFilesCore()
	if err != nil {
		return bookShapeReport{}, fmt.Errorf("GetAllBookFilesCore: %w", err)
	}

	// Reduce the file rows to bookID -> paths immediately and drop the slice.
	// BookFileCore carries RawTags, the heavy field, and the library holds
	// ~550k rows; nothing below needs more than the path.
	totalFileRows := len(fileRows)
	pathsByBook := make(map[string][]string, len(books))
	for i := range fileRows {
		pathsByBook[fileRows[i].BookID] = append(pathsByBook[fileRows[i].BookID], fileRows[i].FilePath)
	}
	fileRows = nil

	// Group by the PURE path key (no filesystem call — see bookShapeGroupKey).
	// This pass is in-memory string work with no per-item I/O, so it stays
	// sequential; the parallel pass below is the one that stats directories.
	byDir := make(map[string][]bookShapeMember, len(books))
	emptyPath := 0
	merged := 0
	for _, b := range books {
		// A record already absorbed into another is retired: grouping it would
		// report work that is already done and could earn a merge recommendation
		// for a row nothing should touch. (Soft-deleted rows are already excluded
		// upstream -- GetAllBooksCore filters MarkedForDeletion by default.)
		if b.MergedIntoBookID != nil && strings.TrimSpace(*b.MergedIntoBookID) != "" {
			merged++
			continue
		}
		key := bookShapeGroupKey(b.FilePath)
		if key == "" {
			emptyPath++
			continue
		}
		paths := pathsByBook[b.ID]
		set := make(map[string]struct{}, len(paths))
		for _, p := range paths {
			set[p] = struct{}{}
		}
		byDir[key] = append(byDir[key], bookShapeMember{book: b, paths: paths, set: set})
	}

	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	groups := make([]bookShapeGroup, 0, len(dirs))
	multiBook := 0
	for _, d := range dirs {
		members := byDir[d]
		sort.Slice(members, func(i, j int) bool { return members[i].book.ID < members[j].book.ID })
		if len(members) > 1 {
			multiBook++
		}
		groups = append(groups, bookShapeGroup{dir: d, members: members})
	}

	report := bookShapeReport{
		TotalBooks:      len(books),
		MergedAway:      merged,
		TotalFileRows:   totalFileRows,
		EmptyFilePath:   emptyPath,
		Groups:          len(groups),
		MultiBookPaths:  multiBook,
		ShapeCounts:     map[string]int{},
		DiskStatSkipped: params.SkipDiskStat,
	}

	shards := shardGroups(groups, bookShapeConcurrency(params.Concurrency))
	if len(shards) == 0 {
		_ = reporter.UpdateProgress(0, 0, "REPORT ONLY (nothing modified) — "+report.summary())
		return report, nil
	}

	var mu sync.Mutex
	var findings []bookShapeFinding
	unreadable := 0

	prog := sdk.NewProgress(reporter, len(shards))
	prog.Start(fmt.Sprintf("Classifying %d book(s) across %d path group(s) in %d shard(s)…", len(books), len(groups), len(shards)))

	err = registry.RunItems(ctx, reporter, shards, func(itemCtx context.Context, shard []bookShapeGroup) error {
		local := make([]bookShapeFinding, 0, 16)
		localUnreadable := 0
		for i, g := range shard {
			if err := itemCtx.Err(); err != nil {
				return err
			}
			// RunItems stamps liveness only BETWEEN items, and one item here is a
			// whole shard that can hold a 1,494-entry directory on a slow mount.
			if i%bookShapeLivenessEvery == 0 {
				registry.TouchLiveness(reporter)
			}
			stat := bookShapeDirStat{Kind: "not-statted", AudioFiles: -1}
			if !params.SkipDiskStat {
				// Stamp before the readdir too: a single large or slow directory
				// can outlast the watchdog window on its own.
				registry.TouchLiveness(reporter)
				stat = statGroupDir(g.dir)
				if stat.Kind == "unreadable" {
					localUnreadable++
				}
			}
			local = append(local, classifyGroup(g, stat, params)...)
		}
		mu.Lock()
		findings = append(findings, local...)
		unreadable += localUnreadable
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency: bookShapeConcurrency(params.Concurrency),
		ErrMode:     registry.ErrModeCollect,
		// Label depends only on its arguments. It runs INSIDE each worker
		// goroutine, so reading a shared counter here would be a data race.
		Label: func(i, t int) string { return fmt.Sprintf("Classified shard %d/%d", i+1, t) },
	})
	if err != nil {
		return bookShapeReport{}, fmt.Errorf("shape sweep: %w", err)
	}

	report.UnreadableDirs = unreadable
	sortBookShapeFindings(findings)
	report.Findings = findings
	for _, f := range findings {
		report.ShapeCounts[f.Shape]++
	}

	sdk.NewProgress(reporter, len(shards)).Done("REPORT ONLY (nothing modified) — " + report.summary())
	return report, nil
}

// sortBookShapeFindings makes the report deterministic and diffable across runs
// regardless of which worker finished first.
func sortBookShapeFindings(f []bookShapeFinding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Dir != f[j].Dir {
			return f[i].Dir < f[j].Dir
		}
		if f[i].Shape != f[j].Shape {
			return f[i].Shape < f[j].Shape
		}
		return strings.Join(f[i].BookIDs, ",") < strings.Join(f[j].BookIDs, ",")
	})
}

// classifyGroup produces every finding for one grouping path.
//
// Per-book shapes (oversized, the two corrupt-path shapes) are independent of
// the group shape and are emitted alongside it: a Gene Wolfe twin is oversized
// AND a duplicate-group member, and suppressing either fact would hide half the
// problem.
func classifyGroup(g bookShapeGroup, stat bookShapeDirStat, params bookShapeReportParams) []bookShapeFinding {
	var out []bookShapeFinding

	corruptBooks := map[string]bool{}
	for _, m := range g.members {
		kind := stat.Kind
		if kind == "dir" && m.book.FilePath != g.dir {
			// The group directory is a directory and this member's own path is an
			// audio file inside it.
			kind = "file"
		}
		if len(m.paths) > params.oversizedThreshold() {
			out = append(out, bookShapeFinding{
				Shape: shapeOversized, Dir: g.dir, BookIDs: []string{m.book.ID}, PathKind: kind,
				LibraryState: strField(m.book.LibraryState), IsPrimary: boolField(m.book.IsPrimaryVersion),
				OwnedRows: len(m.paths), DiskFiles: stat.AudioFiles,
				Detail: fmt.Sprintf("title=%q owns %d book_file row(s) at this path (threshold %d)",
					m.book.Title, len(m.paths), params.oversizedThreshold()),
				Recommendation: bookShapeRecommendations[shapeOversized],
			})
		}
		// Corrupt path: the book's own path or any row it owns.
		if bad := concatenatedPaths(m); len(bad) > 0 {
			corruptBooks[m.book.ID] = true
			out = append(out, bookShapeFinding{
				Shape: shapeConcatenatedPath, Dir: g.dir, BookIDs: []string{m.book.ID}, PathKind: kind,
				LibraryState: strField(m.book.LibraryState), IsPrimary: boolField(m.book.IsPrimaryVersion),
				OwnedRows: len(m.paths), DiskFiles: stat.AudioFiles,
				Detail:         fmt.Sprintf("%d path(s) contain a second absolute path, e.g. %q", len(bad), bad[0]),
				Recommendation: bookShapeRecommendations[shapeConcatenatedPath],
			})
		}
		if prefix, base, n := collisionSuffixExplosion(m, params.collisionSuffixMin()); n > 0 {
			corruptBooks[m.book.ID] = true
			out = append(out, bookShapeFinding{
				Shape: shapeCollisionSuffixExplosion, Dir: g.dir, BookIDs: []string{m.book.ID}, PathKind: kind,
				LibraryState: strField(m.book.LibraryState), IsPrimary: boolField(m.book.IsPrimaryVersion),
				OwnedRows: len(m.paths), DiskFiles: stat.AudioFiles,
				Detail:         fmt.Sprintf("%d sibling %q - N director(ies) all naming one basename %q", n, prefix, base),
				Recommendation: bookShapeRecommendations[shapeCollisionSuffixExplosion],
			})
		}
	}

	// Orphaned files is NOT mutually exclusive with the shape ladder: Foundation
	// is simultaneously disjoint-unaccounted and ~170 files short.
	if stat.Present != nil {
		owned := ownedInDir(g)
		var orphans []string
		for p := range stat.Present {
			if _, ok := owned[p]; !ok {
				orphans = append(orphans, p)
			}
		}
		if len(orphans) > 0 {
			sort.Strings(orphans)
			out = append(out, bookShapeFinding{
				Shape: shapeOrphanedFiles, Dir: g.dir, BookIDs: memberIDs(g), PathKind: stat.Kind,
				LibraryState: joinStates(g), IsPrimary: joinPrimary(g),
				OwnedRows: len(owned), DiskFiles: stat.AudioFiles,
				Detail: fmt.Sprintf("%d audio file(s) on disk are owned by no book row (%d row(s) own a path in this folder, %d file(s) present), e.g. %q",
					len(orphans), len(owned), stat.AudioFiles, filepath.Base(orphans[0])),
				Recommendation: bookShapeRecommendations[shapeOrphanedFiles],
			})
		}
	}

	if len(g.members) < 2 {
		return out
	}

	shape, detail := groupShape(g, stat, corruptBooks)
	totalOwned := 0
	for _, m := range g.members {
		totalOwned += len(m.paths)
	}
	out = append(out, bookShapeFinding{
		Shape: shapeDuplicateRowsAtPath, Dir: g.dir, BookIDs: memberIDs(g), PathKind: stat.Kind,
		LibraryState: joinStates(g), IsPrimary: joinPrimary(g),
		OwnedRows: totalOwned, DiskFiles: stat.AudioFiles,
		Detail:         fmt.Sprintf("%d book rows at this path; %s; file-set shape=%s", len(g.members), overlapWord(g), shape),
		Recommendation: bookShapeRecommendations[shapeDuplicateRowsAtPath],
	})
	out = append(out, bookShapeFinding{
		Shape: shape, Dir: g.dir, BookIDs: memberIDs(g), PathKind: stat.Kind,
		LibraryState: joinStates(g), IsPrimary: joinPrimary(g),
		OwnedRows: totalOwned, DiskFiles: stat.AudioFiles,
		Detail:         detail,
		Recommendation: bookShapeRecommendations[shape],
	})
	return out
}

// groupShape is the ORDERED ladder that names a multi-member group's shape.
//
// 🔴 The order is load-bearing, and different-content sits above the merge
// shapes on purpose. A group where one member is an iTunes author shelf and the
// other is the organized folder is disjoint, and if the disk count happened to
// line up it would otherwise be called a partition and recommended for a merge.
// Checking "do the rows even point at this folder?" first makes that impossible.
//
//  1. any member carries a corrupt path        -> different-content (never merge)
//  2. any member owns rows outside this folder -> different-content (never merge)
//  3. no member owns a row                     -> all-members-empty
//  4. some member owns no row                  -> one-side-empty
//  5. non-empty intersection                   -> containment | partial-overlap
//  6. disjoint AND union == files on disk      -> partition
//  7. disjoint otherwise                       -> disjoint-unaccounted
func groupShape(g bookShapeGroup, stat bookShapeDirStat, corruptBooks map[string]bool) (string, string) {
	for _, m := range g.members {
		if corruptBooks[m.book.ID] {
			return shapeDifferentContent, fmt.Sprintf(
				"member %s carries corrupt path rows, so this group's file sets are not comparable evidence", m.book.ID)
		}
	}
	for _, m := range g.members {
		outside := 0
		for p := range m.set {
			if filepath.Dir(p) != g.dir {
				outside++
			}
		}
		if outside > 0 {
			return shapeDifferentContent, fmt.Sprintf(
				"member %s owns %d of %d row(s) OUTSIDE this path; the members describe different content",
				m.book.ID, outside, len(m.set))
		}
	}

	empties := 0
	for _, m := range g.members {
		if len(m.set) == 0 {
			empties++
		}
	}
	if empties == len(g.members) {
		return shapeAllMembersEmpty, fmt.Sprintf("all %d member(s) own zero book_file rows", len(g.members))
	}
	if empties > 0 {
		return shapeOneSideEmpty, fmt.Sprintf("%d of %d member(s) own zero book_file rows", empties, len(g.members))
	}

	shared := sharedPathCount(g)
	if shared > 0 {
		if a, b, ok := subsetPair(g); ok {
			na, nb := len(memberByID(g, a).set), len(memberByID(g, b).set)
			if na == nb {
				return shapeContainment, fmt.Sprintf(
					"members %s and %s own IDENTICAL file sets (%d path(s) each): each holds a full private row set over the same physical files",
					a, b, na)
			}
			return shapeContainment, fmt.Sprintf(
				"member %s's file set (%d) is a subset of member %s's (%d): the same physical files carry rows under both ids",
				a, na, b, nb)
		}
		return shapePartialOverlap, fmt.Sprintf("%d path(s) are owned by more than one member, but no member's set is a subset of another's", shared)
	}

	union := 0
	for _, m := range g.members {
		union += len(m.set)
	}
	// Set equality against the files actually PRESENT, not len == len: a union
	// that reaches the right size while containing rows for files that no longer
	// exist is not a partition of this folder, and a merge gated on it would be
	// a merge gated on missing rows. The library held ~66,753 such rows in
	// 2026-09.
	if stat.Present != nil && sameSet(ownedInDir(g), stat.Present) {
		return shapePartition, fmt.Sprintf(
			"disjoint file sets whose union (%d) is EXACTLY the %d audio file(s) present on disk: one folder's files really are split across %d records, and NEITHER is complete",
			union, stat.AudioFiles, len(g.members))
	}
	disk := "unknown"
	if stat.AudioFiles >= 0 {
		disk = strconv.Itoa(stat.AudioFiles)
	}
	return shapeDisjointUnaccounted, fmt.Sprintf(
		"disjoint file sets, union %d, files on disk %s: the partition gate does not hold", union, disk)
}

// concatenatedPaths returns the member's paths (its own and its rows') that
// carry the concatenation defect.
func concatenatedPaths(m bookShapeMember) []string {
	var bad []string
	if hasConcatenatedAbsolutePath(m.book.FilePath) {
		bad = append(bad, m.book.FilePath)
	}
	paths := append([]string(nil), m.paths...)
	sort.Strings(paths)
	for _, p := range paths {
		if hasConcatenatedAbsolutePath(p) {
			bad = append(bad, p)
		}
	}
	return bad
}

// collisionSuffixExplosion reports the rename/organize retry loop: N sibling
// "<prefix> - <n>" directories whose rows all name ONE basename. Returns the
// prefix, the basename, and how many suffixed directories were found (0 = not
// this shape).
func collisionSuffixExplosion(m bookShapeMember, minDirs int) (string, string, int) {
	byPrefix := map[string]map[string]struct{}{}
	baseByPrefix := map[string]map[string]struct{}{}
	for _, p := range m.paths {
		dir := filepath.Dir(p)
		mm := collisionSuffixRe.FindStringSubmatch(filepath.Base(dir))
		if mm == nil {
			continue
		}
		prefix := filepath.Join(filepath.Dir(dir), mm[1])
		if byPrefix[prefix] == nil {
			byPrefix[prefix] = map[string]struct{}{}
			baseByPrefix[prefix] = map[string]struct{}{}
		}
		byPrefix[prefix][dir] = struct{}{}
		baseByPrefix[prefix][filepath.Base(p)] = struct{}{}
	}
	prefixes := make([]string, 0, len(byPrefix))
	for k := range byPrefix {
		prefixes = append(prefixes, k)
	}
	sort.Strings(prefixes)
	for _, prefix := range prefixes {
		if len(byPrefix[prefix]) < minDirs || len(baseByPrefix[prefix]) != 1 {
			continue
		}
		base := ""
		for b := range baseByPrefix[prefix] {
			base = b
		}
		return prefix, base, len(byPrefix[prefix])
	}
	return "", "", 0
}

// ownedInDir is every path inside the group directory that some member owns.
func ownedInDir(g bookShapeGroup) map[string]struct{} {
	owned := map[string]struct{}{}
	for _, m := range g.members {
		for p := range m.set {
			if filepath.Dir(p) == g.dir {
				owned[p] = struct{}{}
			}
		}
	}
	return owned
}

// sameSet reports exact set equality.
func sameSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for p := range a {
		if _, ok := b[p]; !ok {
			return false
		}
	}
	return true
}

func sharedPathCount(g bookShapeGroup) int {
	seen := map[string]int{}
	for _, m := range g.members {
		for p := range m.set {
			seen[p]++
		}
	}
	n := 0
	for _, c := range seen {
		if c > 1 {
			n++
		}
	}
	return n
}

// subsetPair returns the ids of a member whose set is contained in another's,
// smaller first, if any.
//
// Containment here includes EQUALITY, deliberately: the eight Gene Wolfe twins
// each own a full private copy of the same 1,494 paths, which is the most
// extreme duplication in the library and must not fall through to
// "partial-overlap" just because neither side is strictly smaller.
func subsetPair(g bookShapeGroup) (string, string, bool) {
	for i := range g.members {
		for j := range g.members {
			if i >= j {
				continue
			}
			a, b := g.members[i], g.members[j]
			if len(a.set) > len(b.set) {
				a, b = b, a
			}
			if len(a.set) == 0 {
				continue
			}
			subset := true
			for p := range a.set {
				if _, ok := b.set[p]; !ok {
					subset = false
					break
				}
			}
			if subset {
				return a.book.ID, b.book.ID, true
			}
		}
	}
	return "", "", false
}

func memberByID(g bookShapeGroup, id string) bookShapeMember {
	for _, m := range g.members {
		if m.book.ID == id {
			return m
		}
	}
	return bookShapeMember{}
}

// overlapWord answers the report's "fully, partially or not at all" question
// about the members' file sets. FULLY means IDENTICAL sets -- a strict subset
// is a partial overlap, however clearly it is also a containment. The shape and
// the overlap word answer different questions and are deliberately not the same
// test.
func overlapWord(g bookShapeGroup) string {
	shared := sharedPathCount(g)
	if shared == 0 {
		return "file sets do not overlap"
	}
	if allSetsIdentical(g) {
		return fmt.Sprintf("file sets overlap fully (identical, %d path(s) each)", shared)
	}
	return fmt.Sprintf("file sets overlap partially (%d shared path(s))", shared)
}

// allSetsIdentical reports whether every member owns exactly the same paths.
func allSetsIdentical(g bookShapeGroup) bool {
	if len(g.members) < 2 {
		return false
	}
	first := g.members[0].set
	if len(first) == 0 {
		return false
	}
	for _, m := range g.members[1:] {
		if !sameSet(first, m.set) {
			return false
		}
	}
	return true
}

func memberIDs(g bookShapeGroup) []string {
	ids := make([]string, 0, len(g.members))
	for _, m := range g.members {
		ids = append(ids, m.book.ID)
	}
	return ids
}

func joinStates(g bookShapeGroup) string {
	parts := make([]string, 0, len(g.members))
	for _, m := range g.members {
		parts = append(parts, strField(m.book.LibraryState))
	}
	return strings.Join(parts, ",")
}

func joinPrimary(g bookShapeGroup) string {
	parts := make([]string, 0, len(g.members))
	for _, m := range g.members {
		parts = append(parts, boolField(m.book.IsPrimaryVersion))
	}
	return strings.Join(parts, ",")
}

// writeBookShapeReport writes the per-finding TSV. This is the op's ONLY write
// of any kind, and it lands in the app-owned {RootDir}/.reports directory.
func writeBookShapeReport(path string, findings []bookShapeFinding) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("shape\tdir\tbook_ids\tpath_kind\tlibrary_state\tis_primary_version\towned_rows\tdisk_files\tdetail\trecommendation\n")
	for _, f := range findings {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			f.Shape, clean(f.Dir), strings.Join(f.BookIDs, ","), f.PathKind,
			clean(f.LibraryState), f.IsPrimary, f.OwnedRows, f.DiskFiles,
			clean(f.Detail), clean(f.Recommendation))
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}
