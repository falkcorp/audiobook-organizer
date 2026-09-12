// file: internal/plugins/maintenance/fs_regroup_xml.go
// version: 2.1.0
// guid: 7d2a9c14-3e86-4b50-9f71-2c8e0a6d4b95
// last-edited: 2026-09-12

// Package maintenance — op maintenance.fs-regroup-xml.
//
// Repairs the "one chapter per folder" layout (`<Book>/<Book> - N/<file>`, the
// shape internal/chaptershape recognises) in three categories. Spec and prod
// census: .claude/notes/fragment-merge-ops-2026-09-12.md.
//
//   - fragments: a real book imported as one single-file book row per chapter
//     folder. Apply moves every member's book_file rows onto one survivor,
//     creates a row only for a chapter path that has none, and soft-deletes the
//     emptied shells.
//   - duplicates: single-file rows whose files a multi-file book ALREADY owns
//     (the scan minted a second owner). Apply soft-deletes the duplicate shells
//     and leaves the owner and every book_file row as they are.
//   - chapter_folder_layout: ONE multi-file book whose files each sit in their
//     own `<Title> - N/` folder. Detection and dry-run reporting only; the apply
//     refuses (see fsLayoutApplyRefusal).
//
// Groups where some members are owned by another book and some are not
// ("mixed"), groups under the iTunes tree or a configured protected path
// ("protected"), and groups that would touch a book another group also touches
// ("overlap") are reported and never applied.
//
// Safety rules, each enforced in code below:
//   - No book_file row is ever deleted and no book is ever hard-deleted. Rows
//     are moved (MoveBookFilesToBookBulk) or created; shells go through
//     merge.SoftDeleteBook. The store slice this op uses has no delete method
//     for book_file rows at all.
//   - Default dryRun=true; an apply needs dryRun=false.
//   - Every mutation writes an OperationChange row under the op id, and an
//     apply without an op id refuses.
//   - The apply refuses while library.scan is queued or running, and holds the
//     scan stand-down for its whole write phase, aborting on a lost lease.
//   - A lookup error is never read as "no row": the group is skipped.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/chaptershape"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Repair categories. The first two can be applied; the rest are report-only.
const (
	fsCatFragments  = "fragments"
	fsCatDuplicates = "duplicates"
	fsCatLayout     = "chapter_folder_layout"
	fsCatMixed      = "mixed"
	fsCatProtected  = "protected"
	fsCatOverlap    = "overlap"
)

// fsCategoryOrder is the order categories are reported in.
var fsCategoryOrder = []string{fsCatFragments, fsCatDuplicates, fsCatLayout, fsCatMixed, fsCatProtected, fsCatOverlap}

// Ledger change types this op writes besides metadata_update (title). The
// Activity Log revert (audiobooks.RevertService) reverses the reassign, track,
// path and soft-delete rows: it moves a row back to the OldValue book, restores
// a track number or path, and clears a shell's deletion mark. The two marked
// record-only are reported as not undoable instead: undoing a created row
// would delete it, and the external-id row does not say which ids moved. See
// the constants' docs in internal/undo.
const (
	fsChangeFileReassign = undo.ChangeTypeBookFileReassign
	fsChangeFileTrack    = undo.ChangeTypeBookFileTrack
	fsChangeFileCreate   = undo.ChangeTypeBookFileCreate // record-only
	fsChangePathUpdate   = undo.ChangeTypeBookPathUpdate
	fsChangeSoftDelete   = undo.ChangeTypeBookSoftDelete
	fsChangeExtIDs       = undo.ChangeTypeExternalIDReassign // record-only
)

// fsLayoutApplyRefusal is returned when an apply asks for the layout category.
const fsLayoutApplyRefusal = "fs-regroup-xml: chapter_folder_layout apply is not implemented. " +
	"The repair moves audio on disk, and the spec (fragment-merge-ops 2026-09-12 §0, §3, §4) marks this " +
	"census PLAUSIBLE (checked file by file for one book), puts the layout repair out of this op's scope, " +
	"and asks for its own safety design (target naming and padding, book folders shared by more than one " +
	"book, undo of per-file moves). Use the dry-run report to size it"

type fsRegroupParams struct {
	// DryRun defaults true (safe). Set false to apply the plan and mutate the library.
	DryRun bool `json:"dryRun"`
	// Limit caps how many groups the apply path repairs in one run (0 = no cap).
	// Use limit=1 for the first canary apply before batching.
	Limit int `json:"limit"`
	// Categories restricts the apply to these categories. Empty means
	// fragments and duplicates. chapter_folder_layout makes the apply refuse.
	Categories []string `json:"categories"`
}

func (p fsRegroupParams) categorySet() (map[string]bool, error) {
	if len(p.Categories) == 0 {
		return map[string]bool{fsCatFragments: true, fsCatDuplicates: true}, nil
	}
	out := map[string]bool{}
	for _, c := range p.Categories {
		switch c {
		case fsCatFragments, fsCatDuplicates, fsCatLayout:
			out[c] = true
		default:
			return nil, fmt.Errorf("invalid category %q (want %s, %s or %s)", c, fsCatFragments, fsCatDuplicates, fsCatLayout)
		}
	}
	return out, nil
}

// fsRepairStore is what the planner and the apply need. It adds the ledger
// and field-lock reads to fsRegroupStore. It carries no book_file delete
// method, so the apply cannot remove a row even by mistake.
type fsRepairStore interface {
	fsRegroupStore
	regroupSnapshotReader
	CreateOperationChange(change *database.OperationChange) error
	database.MetadataFieldStateReader
}

var _ fsRepairStore = (*database.PebbleStore)(nil)

func (p *Plugin) fsRegroupXMLDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.fs-regroup-xml",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Repair chapter-per-folder books (fragments, duplicates, layout)",
		Description: "Finds books laid out one chapter per folder (<Book>/<Book> - N/<file>) and sorts them into " +
			"fragments (one book row per chapter: merged onto one survivor, shells soft-deleted), duplicates " +
			"(rows whose files another book already owns: shells soft-deleted, owner untouched) and " +
			"chapter_folder_layout (one book whose files sit in per-chapter folders: reported only). Never " +
			"deletes a book_file row, skips the iTunes tree and protected paths, journals every change. " +
			"Default dry-run reports the per-group plan; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.fs-regroup-xml",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runFSRegroupXML,
	}
}

func (p *Plugin) runFSRegroupXML(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := fsRegroupParams{DryRun: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	if _, err := params.categorySet(); err != nil {
		return err
	}
	opsStore := p.deps.OpsStore()
	if opsStore == nil {
		return fmt.Errorf("database not initialized")
	}
	var anyStore any = opsStore
	store, ok := anyStore.(fsRepairStore)
	if !ok {
		return fmt.Errorf("fs-regroup-xml: store lacks the ledger or field-lock methods; refusing to run")
	}

	plan, err := planFSRepair(ctx, store, reporter)
	if err != nil {
		return err
	}
	reportFSRepairPlan(plan, params.DryRun, reporter)
	if params.DryRun {
		_ = reporter.UpdateProgress(3, 3, "DRY RUN — "+plan.summary())
		return nil
	}
	res, err := applyFSRepairPlan(ctx, store, p.deps, p.deps.OperationQueueStore(), plan, params, reporter)
	_ = reporter.UpdateProgress(3, 3, res.String())
	return err
}

// ---- plan -----------------------------------------------------------------

// fsRepairGroup is one classified item: a group of shattered rows, or one
// layout book. Every book it would mutate is in MemberIDs.
type fsRepairGroup struct {
	Category   string
	BookFolder string
	Title      string
	SurvivorID string                 // fragments: the kept book; layout: the book itself
	OwnerID    string                 // duplicates: the multi-file book that owns the files
	Members    []itunesservice.FSBook // fragments/duplicates/mixed, in chapter order
	MemberIDs  []string
	Moves      []fsLayoutMove // layout: the planned renames (never executed here)
	Rows       int            // book rows (fragments etc.) or files (layout) in the group
	Conflicts  int            // layout: planned targets already taken
	Reason     string
}

type fsLayoutMove struct{ FileID, From, To string }

type fsRepairPlan struct {
	Groups []fsRepairGroup
	Stats  itunesservice.GroupStats
}

func (pl *fsRepairPlan) counts() (groups, rows map[string]int) {
	groups, rows = map[string]int{}, map[string]int{}
	for _, g := range pl.Groups {
		groups[g.Category]++
		rows[g.Category] += g.Rows
	}
	return groups, rows
}

func (pl *fsRepairPlan) summary() string {
	groups, rows := pl.counts()
	conflicts := 0
	for _, g := range pl.Groups {
		conflicts += g.Conflicts
	}
	return fmt.Sprintf("fragments: groups=%d rows=%d | duplicates: groups=%d rows=%d | "+
		"chapter_folder_layout (detection only): books=%d files=%d target-conflicts=%d | "+
		"mixed (report only): groups=%d rows=%d | protected (refused): groups=%d rows=%d | overlap (refused): groups=%d rows=%d",
		groups[fsCatFragments], rows[fsCatFragments], groups[fsCatDuplicates], rows[fsCatDuplicates],
		groups[fsCatLayout], rows[fsCatLayout], conflicts,
		groups[fsCatMixed], rows[fsCatMixed], groups[fsCatProtected], rows[fsCatProtected],
		groups[fsCatOverlap], rows[fsCatOverlap])
}

type fsFileRef struct{ ID, Path string }

// fsFileIndex is the read-only snapshot the classifiers share. It is built
// before the worker pool starts and never written after, so concurrent reads
// need no lock.
type fsFileIndex struct {
	owners map[string][]string    // file path -> live book ids with a row at that path
	count  map[string]int         // live book id -> book_file rows
	files  map[string][]fsFileRef // live book id -> its rows
	paths  map[string]string      // live book id -> Book.FilePath
}

// fsRegroupProtectedPath reports whether p is in a tree this op must not read
// or write: the frozen iTunes tree, any iTunes folder, the configured iTunes
// library folders, or a configured protected path.
func fsRegroupProtectedPath(p string) bool {
	if p == "" {
		return false
	}
	if config.UnderFrozenITunesTree(p) {
		return true
	}
	clean := strings.ReplaceAll(p, "\\", "/")
	lower := strings.ToLower(clean)
	if strings.Contains(lower, "/itunes/") || strings.Contains(lower, "/itunes media/") {
		return true
	}
	under := func(root string) bool {
		root = strings.TrimSuffix(strings.ReplaceAll(root, "\\", "/"), "/")
		return root != "" && (clean == root || strings.HasPrefix(clean, root+"/"))
	}
	for _, pp := range config.AppConfig.ProtectedPaths {
		if under(pp) {
			return true
		}
	}
	for _, lib := range []string{config.AppConfig.ITunes.LibraryReadPath, config.AppConfig.ITunes.LibraryWritePath} {
		if lib != "" && under(filepath.Dir(lib)) {
			return true
		}
	}
	return false
}

// planFSRepair loads the library snapshot and classifies it. Read-only.
func planFSRepair(ctx context.Context, store regroupSnapshotReader, reporter sdk.Reporter) (*fsRepairPlan, error) {
	_ = reporter.UpdateProgress(0, 3, "Phase 1/3: scanning books…")
	bookMeta := make(map[string]*itunesservice.FSBook)
	titles := make(map[string]string)
	idx := &fsFileIndex{
		owners: map[string][]string{}, count: map[string]int{},
		files: map[string][]fsFileRef{}, paths: map[string]string{},
	}
	const page = 1000
	afterID := ""
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		books, err := store.GetAllBooksFullFrom(afterID, page)
		if err != nil {
			return nil, fmt.Errorf("GetAllBooksFullFrom afterID=%q: %w", afterID, err)
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			b := &books[i]
			if b.IsSoftDeleted() {
				continue // a retired row is never regrouped or counted as an owner
			}
			fsb := itunesservice.FSBook{
				ID:        b.ID,
				Title:     b.Title,
				FilePath:  b.FilePath,
				IsPrimary: b.IsPrimaryVersion == nil || *b.IsPrimaryVersion, // nil treated as primary
			}
			if b.AuthorID != nil {
				fsb.AuthorID = *b.AuthorID
			}
			if b.ASIN != nil {
				fsb.ASIN = strings.TrimSpace(*b.ASIN)
			}
			if b.Duration != nil {
				fsb.DurationSec = *b.Duration
			}
			fsb.EnrichScore = enrichScore(b)
			bookMeta[b.ID] = &fsb
			titles[b.ID] = b.Title
			idx.paths[b.ID] = b.FilePath
		}
		afterID = books[len(books)-1].ID
		_ = reporter.UpdateProgress(0, 3, fmt.Sprintf("Phase 1/3: scanned %d books…", len(bookMeta)))
		if len(books) < page {
			break
		}
	}

	_ = reporter.UpdateProgress(1, 3, "Phase 2/3: indexing book files…")
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	for i := range files {
		f := files[i]
		fsb := bookMeta[f.BookID]
		if fsb == nil {
			continue // rows under a soft-deleted or unknown book own nothing live
		}
		fsb.FileCount++
		idx.count[f.BookID]++
		idx.files[f.BookID] = append(idx.files[f.BookID], fsFileRef{ID: f.ID, Path: f.FilePath})
		if f.FilePath != "" && !slices.Contains(idx.owners[f.FilePath], f.BookID) {
			idx.owners[f.FilePath] = append(idx.owners[f.FilePath], f.BookID)
		}
	}

	all := make([]itunesservice.FSBook, 0, len(bookMeta))
	for _, fsb := range bookMeta {
		all = append(all, *fsb)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })

	_ = reporter.UpdateProgress(2, 3, "Phase 3/3: classifying…")
	targets, st := itunesservice.GroupShatteredBooks(all)

	// One job per shattered target and one per multi-file book. Targets are
	// disjoint by (book folder, prefix) and multi-file books are excluded from
	// targets, so every book lands in at most one job; each job writes only its
	// own slot of groups, and the index is read-only here.
	type job struct {
		i      int
		target *itunesservice.FSRegroupTarget
		bookID string
	}
	jobs := make([]job, 0, len(targets)+len(idx.count))
	for ti := range targets {
		jobs = append(jobs, job{i: len(jobs), target: &targets[ti]})
	}
	multi := make([]string, 0)
	for id, n := range idx.count {
		if n > 1 {
			multi = append(multi, id)
		}
	}
	sort.Strings(multi)
	for _, id := range multi {
		jobs = append(jobs, job{i: len(jobs), bookID: id})
	}
	groups := make([]fsRepairGroup, len(jobs))
	var done atomic.Int64
	err = registry.RunItems(ctx, reporter, jobs, func(_ context.Context, j job) error {
		if j.target != nil {
			groups[j.i] = classifyShatteredTarget(*j.target, idx)
		} else if g, ok := classifyLayoutBook(j.bookID, idx); ok {
			groups[j.i] = g
		}
		done.Add(1)
		return nil
	}, registry.RunItemsOptions{
		// CPU-bound map lookups over an immutable snapshot: one worker per core.
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label: func(_, total int) string {
			return fmt.Sprintf("Phase 3/3: classified %d/%d", done.Load(), total)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}

	plan := &fsRepairPlan{Stats: st}
	for _, g := range groups {
		if g.Category != "" {
			plan.Groups = append(plan.Groups, g)
		}
	}
	markLayoutConflicts(plan, idx)
	markOverlaps(plan)
	return plan, nil
}

// memberPaths is every path a member stands for: its Book.FilePath and the
// path of each row it owns.
func memberPaths(m itunesservice.FSBook, idx *fsFileIndex) []string {
	out := []string{}
	if m.FilePath != "" {
		out = append(out, m.FilePath)
	}
	for _, f := range idx.files[m.ID] {
		if f.Path != "" && !slices.Contains(out, f.Path) {
			out = append(out, f.Path)
		}
	}
	return out
}

// classifyShatteredTarget sorts one GroupShatteredBooks target into
// fragments, duplicates, mixed or protected.
func classifyShatteredTarget(t itunesservice.FSRegroupTarget, idx *fsFileIndex) fsRepairGroup {
	g := fsRepairGroup{BookFolder: t.BookFolder, Title: t.Title, SurvivorID: t.SurvivorID,
		Members: t.Members, Rows: len(t.Members)}
	members := make(map[string]bool, len(t.Members))
	for _, m := range t.Members {
		g.MemberIDs = append(g.MemberIDs, m.ID)
		members[m.ID] = true
	}
	// Protected first, from the DB paths alone, so nothing below (and nothing in
	// the apply) ever reads or writes that tree.
	if fsRegroupProtectedPath(t.BookFolder) {
		g.Category, g.Reason = fsCatProtected, "book folder is under the iTunes tree or a protected path"
		return g
	}
	for _, m := range t.Members {
		for _, p := range memberPaths(m, idx) {
			if fsRegroupProtectedPath(p) {
				g.Category, g.Reason = fsCatProtected, "a member path is under the iTunes tree or a protected path"
				return g
			}
		}
	}
	if t.SurvivorID == "" {
		g.Category, g.Reason = fsCatMixed, "planner chose no survivor"
		return g
	}

	owned := 0
	owners := map[string]bool{}
	for _, m := range t.Members {
		mOwned := false
		for _, p := range memberPaths(m, idx) {
			for _, o := range idx.owners[p] {
				if !members[o] {
					owners[o] = true
					mOwned = true
				}
			}
		}
		if mOwned {
			owned++
		}
	}
	switch {
	case owned == 0:
		g.Category = fsCatFragments
	case owned == len(t.Members) && len(owners) == 1:
		var owner string
		for o := range owners {
			owner = o
		}
		if idx.count[owner] < 2 {
			g.Category, g.Reason = fsCatMixed, "the outside owner "+owner+" is not a multi-file book"
			return g
		}
		for _, m := range t.Members {
			for _, p := range memberPaths(m, idx) {
				if !slices.Contains(idx.owners[p], owner) {
					g.Category, g.Reason = fsCatMixed, fmt.Sprintf("member %s has a file owner %s does not", m.ID, owner)
					return g
				}
			}
		}
		g.Category, g.OwnerID = fsCatDuplicates, owner
	default:
		g.Category = fsCatMixed
		g.Reason = fmt.Sprintf("%d of %d members' files are already owned by %d other book(s)", owned, len(t.Members), len(owners))
	}
	return g
}

// classifyLayoutBook reports whether a multi-file book has every file in its
// own `<prefix> - N` folder under one book folder (chaptershape's guard), and
// plans the flat names the repair would use. ok is false when it does not.
func classifyLayoutBook(bookID string, idx *fsFileIndex) (fsRepairGroup, bool) {
	files := idx.files[bookID]
	if len(files) < 2 {
		return fsRepairGroup{}, false
	}
	type part struct {
		ref fsFileRef
		num int
	}
	var parent, normPrefix, prefix string
	nums, dirs := map[int]bool{}, map[string]bool{}
	maxNum := 0
	parts := make([]part, 0, len(files))
	for _, f := range files {
		par, pre, n, ok := chaptershape.Parts(f.Path)
		if !ok || !chaptershape.PrefixInParent(par, pre) {
			return fsRepairGroup{}, false
		}
		np := chaptershape.NormPrefix(pre)
		if parent == "" {
			parent, normPrefix, prefix = par, np, pre
		} else if par != parent || np != normPrefix {
			return fsRepairGroup{}, false
		}
		dir := filepath.Dir(f.Path)
		if nums[n] || dirs[dir] {
			return fsRepairGroup{}, false // two files per chapter folder is not this layout
		}
		nums[n], dirs[dir] = true, true
		maxNum = max(maxNum, n)
		parts = append(parts, part{f, n})
	}
	g := fsRepairGroup{Category: fsCatLayout, BookFolder: parent, Title: prefix, SurvivorID: bookID,
		MemberIDs: []string{bookID}, Rows: len(files), Reason: "detection only"}
	for _, f := range files {
		if fsRegroupProtectedPath(f.Path) {
			g.Category, g.Reason = fsCatProtected, "a file is under the iTunes tree or a protected path"
			return g, true
		}
	}
	if fsRegroupProtectedPath(idx.paths[bookID]) {
		g.Category, g.Reason = fsCatProtected, "book path is under the iTunes tree or a protected path"
		return g, true
	}
	width := max(2, len(strconv.Itoa(maxNum)))
	sort.Slice(parts, func(i, j int) bool { return parts[i].num < parts[j].num })
	for _, x := range parts {
		to := filepath.Join(parent, fmt.Sprintf("%s - %0*d%s", prefix, width, x.num, filepath.Ext(x.ref.Path)))
		g.Moves = append(g.Moves, fsLayoutMove{FileID: x.ref.ID, From: x.ref.Path, To: to})
	}
	return g, true
}

// markLayoutConflicts counts, per layout book, planned targets that another
// row already uses or another layout book also plans (23 book folders are
// shared by more than one book in the census).
func markLayoutConflicts(plan *fsRepairPlan, idx *fsFileIndex) {
	claims := map[string]int{}
	for _, g := range plan.Groups {
		if g.Category == fsCatLayout {
			for _, mv := range g.Moves {
				claims[mv.To]++
			}
		}
	}
	for i := range plan.Groups {
		g := &plan.Groups[i]
		if g.Category != fsCatLayout {
			continue
		}
		for _, mv := range g.Moves {
			if claims[mv.To] > 1 || len(idx.owners[mv.To]) > 0 {
				g.Conflicts++
			}
		}
	}
}

// markOverlaps refuses any appliable group that shares a mutated book with
// another. GroupShatteredBooks keys are disjoint, so this should never fire;
// it is the guard that lets the apply run groups in parallel.
func markOverlaps(plan *fsRepairPlan) {
	seen := map[string]int{}
	for _, g := range plan.Groups {
		if g.Category == fsCatFragments || g.Category == fsCatDuplicates {
			for _, id := range g.MemberIDs {
				seen[id]++
			}
		}
	}
	for i := range plan.Groups {
		g := &plan.Groups[i]
		if g.Category != fsCatFragments && g.Category != fsCatDuplicates {
			continue
		}
		for _, id := range g.MemberIDs {
			if seen[id] > 1 {
				g.Category, g.Reason = fsCatOverlap, "book "+id+" is in more than one group"
				break
			}
		}
	}
}

// ---- report ---------------------------------------------------------------

func fsIDList(ids []string) string {
	const maxIDs = 12
	if len(ids) <= maxIDs {
		return "[" + strings.Join(ids, " ") + "]"
	}
	return fmt.Sprintf("[%s +%d more]", strings.Join(ids[:maxIDs], " "), len(ids)-maxIDs)
}

func (g fsRepairGroup) planLine() string {
	switch g.Category {
	case fsCatFragments:
		shells := slices.DeleteFunc(slices.Clone(g.MemberIDs), func(id string) bool { return id == g.SurvivorID })
		return fmt.Sprintf("PLAN fragments folder=%q title=%q survivor=%s shells=%d %s → move each shell's rows "+
			"onto the survivor, create a row for any chapter path with none, soft-delete the emptied shells",
			g.BookFolder, g.Title, g.SurvivorID, len(shells), fsIDList(shells))
	case fsCatDuplicates:
		return fmt.Sprintf("PLAN duplicates folder=%q owner=%s shells=%d %s → soft-delete each shell; "+
			"the owner and every book_file row stay as they are", g.BookFolder, g.OwnerID, len(g.MemberIDs), fsIDList(g.MemberIDs))
	case fsCatLayout:
		first := ""
		if len(g.Moves) > 0 {
			first = fmt.Sprintf(" e.g. %q → %q", g.Moves[0].From, g.Moves[0].To)
		}
		return fmt.Sprintf("PLAN chapter_folder_layout book=%s folder=%q files=%d target-conflicts=%d%s → detection only",
			g.SurvivorID, g.BookFolder, len(g.Moves), g.Conflicts, first)
	default:
		return fmt.Sprintf("PLAN %s folder=%q rows=%d %s reason=%q → no change", g.Category, g.BookFolder, g.Rows,
			fsIDList(g.MemberIDs), g.Reason)
	}
}

// reportFSRepairPlan logs the reconciliation, per-category counts and
// examples, and (dry-run) one plan line per group.
func reportFSRepairPlan(plan *fsRepairPlan, dryRun bool, reporter sdk.Reporter) {
	st := plan.Stats
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"RECONCILE: total=%d non-primary=%d multi-file=%d not-chapter-pattern=%d chapter-candidates=%d "+
			"→ singleton-groups=%d prefix-not-in-parent=%d grouped-records=%d",
		st.TotalBooks, st.NonPrimary, st.MultiFile, st.NotChapterPattern, st.ChapterCandidates,
		st.SingletonGroups, st.PrefixNotInParent, st.GroupedRecords))
	prefix := "APPLY PLAN: "
	if dryRun {
		prefix = "DRY RUN PLAN: "
	}
	_ = reporter.Log(slog.LevelInfo, prefix+plan.summary())
	for _, cat := range fsCategoryOrder {
		var ex []fsRepairGroup
		for _, g := range plan.Groups {
			if g.Category == cat {
				ex = append(ex, g)
			}
		}
		if len(ex) == 0 {
			continue
		}
		sort.SliceStable(ex, func(i, j int) bool { return ex[i].Rows > ex[j].Rows })
		parts := make([]string, 0, 5)
		for _, g := range ex[:min(5, len(ex))] {
			parts = append(parts, fmt.Sprintf("%q (%d)", g.Title, g.Rows))
		}
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("EXAMPLES %s: %s", cat, strings.Join(parts, " | ")))
	}
	if !dryRun {
		return
	}
	for _, cat := range fsCategoryOrder {
		for _, g := range plan.Groups {
			if g.Category == cat {
				_ = reporter.Log(slog.LevelInfo, g.planLine())
			}
		}
	}
}

// ---- apply ----------------------------------------------------------------

type fsRepairResult struct {
	Groups, RowsMoved, RowsCreated, ShellsSoftDeleted, ShellsKept, TitleKept, LedgerRows, Skipped, Errors int
	StandDownLost                                                                                         bool
}

func (r fsRepairResult) String() string {
	return fmt.Sprintf("APPLIED — groups=%d rows-moved=%d rows-created=%d shells-soft-deleted=%d shells-kept=%d "+
		"title-kept=%d ledger-rows=%d skipped=%d errors=%d stand-down-lost=%v",
		r.Groups, r.RowsMoved, r.RowsCreated, r.ShellsSoftDeleted, r.ShellsKept, r.TitleKept, r.LedgerRows,
		r.Skipped, r.Errors, r.StandDownLost)
}

// refuseWhileLibraryScanActive fails closed unless the queue shows no
// library.scan queued or running. The stand-down alone does not cover a scan
// that resumed after a restart, so this point-in-time check comes first (same
// shape as dedupe_book_file_rows.go).
func refuseWhileLibraryScanActive(queue OpQueueReader) error {
	if queue == nil {
		return fmt.Errorf("fs-regroup-xml: cannot verify no library.scan is active; refusing to apply")
	}
	active, err := queue.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("fs-regroup-xml: cannot list active operations; refusing to apply: %w", err)
	}
	for _, op := range active {
		if op.DefID == "library.scan" && (op.Status == "running" || op.Status == "queued") {
			return fmt.Errorf("fs-regroup-xml: library.scan is %s (op %s); refusing to apply — a scan rewrites the "+
				"rows this op moves. If that row is a stale zombie, clear it rather than bypassing this check", op.Status, op.ID)
		}
	}
	return nil
}

type fsApplier struct {
	store    fsRepairStore
	opID     string
	reporter sdk.Reporter
	logMu    sync.Mutex // reporters are not required to be goroutine-safe

	groups, moved, created, softDeleted, kept, titleKept, ledger, skipped, errs atomic.Int64
}

func (a *fsApplier) log(level slog.Level, format string, args ...any) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	_ = a.reporter.Log(level, fmt.Sprintf(format, args...))
}

func (a *fsApplier) journal(bookID, changeType, field, oldV, newV string) {
	err := a.store.CreateOperationChange(&database.OperationChange{
		OperationID: a.opID, BookID: bookID, ChangeType: changeType,
		FieldName: field, OldValue: oldV, NewValue: newV,
	})
	if err != nil {
		a.errs.Add(1)
		a.log(slog.LevelWarn, "ledger row %s for book %s failed: %v", changeType, bookID, err)
		return
	}
	a.ledger.Add(1)
}

// applyFSRepairPlan applies the fragments and duplicates groups. It never
// runs a layout move: asking for that category returns fsLayoutApplyRefusal.
func applyFSRepairPlan(ctx context.Context, store fsRepairStore, scan ScanController, queue OpQueueReader,
	plan *fsRepairPlan, params fsRegroupParams, reporter sdk.Reporter,
) (fsRepairResult, error) {
	var res fsRepairResult
	want, err := params.categorySet()
	if err != nil {
		return res, err
	}
	if want[fsCatLayout] {
		return res, errors.New(fsLayoutApplyRefusal)
	}
	opID := registry.ReporterOpID(reporter)
	if opID == "" {
		return res, fmt.Errorf("fs-regroup-xml: no operation id to record the undo ledger under; refusing to apply")
	}
	var work []fsRepairGroup
	for _, g := range plan.Groups {
		if (g.Category == fsCatFragments || g.Category == fsCatDuplicates) && want[g.Category] {
			work = append(work, g)
		}
	}
	if params.Limit > 0 && len(work) > params.Limit {
		work = work[:params.Limit]
	}
	if len(work) == 0 {
		// No gate taken: parking a days-long scan for zero writes costs for nothing.
		_ = reporter.Log(slog.LevelInfo, "fs-regroup-xml: nothing to apply")
		return res, nil
	}
	if err := refuseWhileLibraryScanActive(queue); err != nil {
		return res, err
	}
	holderID, held, release, err := acquireScanStandDownForApply(ctx, scan, reporter, "fs-regroup-xml apply")
	if err != nil {
		return res, fmt.Errorf("fs-regroup-xml: acquire scan stand-down: %w", err)
	}
	defer release()

	a := &fsApplier{store: store, opID: opID, reporter: reporter}
	var lost atomic.Bool
	var done atomic.Int64
	// Groups are disjoint by book id (markOverlaps refuses any that are not), so
	// two workers never touch the same book. The book-row read-modify-write in
	// each group runs under the global merge.LockMergeRMW, the lock every merge
	// path shares, so that section is serial; the per-group reads and the ledger
	// writes outside it run in parallel. Making the RMW itself parallel needs
	// per-group locking in internal/merge, which is a separate design decision.
	err = registry.RunItems(ctx, reporter, work, func(_ context.Context, g fsRepairGroup) error {
		defer done.Add(1)
		if lost.Load() {
			return nil
		}
		if scanStandDownLostForApply(scan, holderID, held) {
			lost.Store(true)
			return nil
		}
		switch g.Category {
		case fsCatFragments:
			a.applyFragments(g)
		case fsCatDuplicates:
			a.applyDuplicates(g)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label: func(_, total int) string {
			return fmt.Sprintf("repaired %d/%d groups (errors=%d)", done.Load(), total, a.errs.Load())
		},
	})
	res = fsRepairResult{
		Groups: int(a.groups.Load()), RowsMoved: int(a.moved.Load()), RowsCreated: int(a.created.Load()),
		ShellsSoftDeleted: int(a.softDeleted.Load()), ShellsKept: int(a.kept.Load()), TitleKept: int(a.titleKept.Load()),
		LedgerRows: int(a.ledger.Load()), Skipped: int(a.skipped.Load()), Errors: int(a.errs.Load()),
		StandDownLost: lost.Load(),
	}
	_ = reporter.Log(slog.LevelInfo, res.String())
	switch {
	case err != nil:
		return res, fmt.Errorf("fs-regroup-xml apply: %w", err)
	case res.StandDownLost:
		return res, fmt.Errorf("fs-regroup-xml: scan stand-down lease lapsed after %d groups — aborted (re-run when the scan is idle)", res.Groups)
	case res.Errors > 0:
		return res, fmt.Errorf("%d errors during fs-regroup apply (see op log)", res.Errors)
	}
	return res, nil
}

// applyFragments merges one fragments group onto its survivor.
func (a *fsApplier) applyFragments(g fsRepairGroup) {
	members := make(map[string]bool, len(g.MemberIDs))
	for _, id := range g.MemberIDs {
		members[id] = true
	}
	track := map[string]int{}
	var moves []database.BookFileMove
	var creates []itunesservice.FSBook

	// Resolve every member first; any read error skips the whole group before
	// anything is written. An unreadable lookup is not proof there is no row,
	// and treating it as one would create a second row for the same path.
	for i, m := range g.Members {
		track[m.FilePath] = i + 1
		rows, err := a.store.GetBookFiles(m.ID)
		if err != nil {
			a.errs.Add(1)
			a.log(slog.LevelWarn, "fragments %q: read rows of %s: %v — group skipped", g.BookFolder, m.ID, err)
			return
		}
		hasOwnPath := false
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			if r.FilePath == m.FilePath {
				hasOwnPath = true
			}
			ids = append(ids, r.ID)
		}
		if m.ID != g.SurvivorID && len(ids) > 0 {
			moves = append(moves, database.BookFileMove{FileIDs: ids, SourceBookID: m.ID})
		}
		if hasOwnPath || m.FilePath == "" {
			continue
		}
		existing, err := a.store.GetBookFileByPath(m.FilePath)
		if err != nil {
			a.errs.Add(1)
			a.log(slog.LevelWarn, "fragments %q: look up %q: %v — group skipped", g.BookFolder, m.FilePath, err)
			return
		}
		switch {
		case existing == nil:
			creates = append(creates, m)
		case !members[existing.BookID]:
			a.skipped.Add(1)
			a.log(slog.LevelWarn, "fragments %q: %q is owned by %s outside the group (plan is stale) — group skipped",
				g.BookFolder, m.FilePath, existing.BookID)
			return
		}
	}

	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	if len(moves) > 0 {
		if err := a.store.MoveBookFilesToBookBulk(moves, g.SurvivorID); err != nil {
			a.errs.Add(1)
			a.log(slog.LevelWarn, "fragments %q: move rows onto %s: %v — nothing moved, group skipped", g.BookFolder, g.SurvivorID, err)
			return
		}
		for _, mv := range moves {
			for _, id := range mv.FileIDs {
				a.moved.Add(1)
				a.journal(g.SurvivorID, fsChangeFileReassign, "book_file:"+id, mv.SourceBookID, g.SurvivorID)
			}
		}
	}
	createFailed := map[string]bool{}
	for _, m := range creates {
		bf := &database.BookFile{
			ID:          ulid.Make().String(),
			BookID:      g.SurvivorID,
			FilePath:    m.FilePath,
			Format:      strings.TrimPrefix(strings.ToLower(filepath.Ext(m.FilePath)), "."),
			Duration:    m.DurationSec,
			TrackNumber: track[m.FilePath],
		}
		if err := a.store.CreateBookFile(bf); err != nil {
			createFailed[m.ID] = true
			a.errs.Add(1)
			a.log(slog.LevelWarn, "fragments %q: create row for %q: %v", g.BookFolder, m.FilePath, err)
			continue
		}
		a.created.Add(1)
		a.journal(g.SurvivorID, fsChangeFileCreate, "book_file:"+bf.ID, "", m.FilePath)
	}

	// Chapter order from the folder number, the only place it lives.
	if rows, err := a.store.GetBookFiles(g.SurvivorID); err != nil {
		a.errs.Add(1)
		a.log(slog.LevelWarn, "fragments %q: re-read survivor rows: %v", g.BookFolder, err)
	} else {
		for i := range rows {
			f := &rows[i]
			if n, ok := track[f.FilePath]; ok && f.TrackNumber != n {
				old := f.TrackNumber
				f.TrackNumber = n
				if err := a.store.UpdateBookFile(f.ID, f); err != nil {
					a.errs.Add(1)
					a.log(slog.LevelWarn, "fragments %q: set track %d on %s: %v", g.BookFolder, n, f.ID, err)
					continue
				}
				a.journal(g.SurvivorID, fsChangeFileTrack, "book_file:"+f.ID, strconv.Itoa(old), strconv.Itoa(n))
			}
		}
	}

	a.updateSurvivor(g)

	for _, m := range g.Members {
		if m.ID == g.SurvivorID {
			continue
		}
		if createFailed[m.ID] {
			a.kept.Add(1) // its chapter path has no row yet; the shell is the only pointer to it
			continue
		}
		rows, err := a.store.GetBookFiles(m.ID)
		if err != nil || len(rows) > 0 {
			// A read error counts as "still owns files": never retire a shell we
			// cannot prove is empty.
			a.kept.Add(1)
			a.log(slog.LevelWarn, "fragments %q: shell %s kept (rows=%d err=%v)", g.BookFolder, m.ID, len(rows), err)
			continue
		}
		if err := merge.SoftDeleteBook(a.store, m.ID); err != nil {
			a.errs.Add(1)
			a.log(slog.LevelWarn, "fragments %q: soft-delete %s: %v", g.BookFolder, m.ID, err)
			continue
		}
		a.softDeleted.Add(1)
		a.journal(m.ID, fsChangeSoftDelete, "marked_for_deletion", "", "merged into "+g.SurvivorID)
		if exts, err := a.store.GetExternalIDsForBook(m.ID); err != nil || len(exts) > 0 {
			if rerr := a.store.ReassignExternalIDs(m.ID, g.SurvivorID); rerr != nil {
				// The ids stay on the soft-deleted shell, which is recoverable.
				a.errs.Add(1)
				a.log(slog.LevelWarn, "fragments %q: reassign external ids %s → %s: %v", g.BookFolder, m.ID, g.SurvivorID, rerr)
			} else {
				a.journal(m.ID, fsChangeExtIDs, "external_ids", m.ID, g.SurvivorID)
			}
		}
	}
	if err := a.store.RecomputeBookAggregates(g.SurvivorID); err != nil {
		a.errs.Add(1)
		a.log(slog.LevelWarn, "fragments %q: recompute %s: %v", g.BookFolder, g.SurvivorID, err)
	}
	a.groups.Add(1)
}

// updateSurvivor sets the survivor's title (unless the user locked it; fails
// closed on an unreadable lock set) and its folder path, and journals both.
func (a *fsApplier) updateSurvivor(g fsRepairGroup) {
	b, err := a.store.GetBookByID(g.SurvivorID)
	if err != nil || b == nil {
		a.errs.Add(1)
		a.log(slog.LevelWarn, "fragments %q: read survivor %s: %v", g.BookFolder, g.SurvivorID, err)
		return
	}
	oldTitle, oldPath := b.Title, b.FilePath
	if g.Title != "" && g.Title != b.Title {
		locks, lerr := database.LoadFieldLocks(a.store, b.ID)
		switch {
		case lerr != nil:
			a.titleKept.Add(1)
			a.log(slog.LevelWarn, "fragments %q: title not changed, field locks unreadable: %v", g.BookFolder, lerr)
		case locks.Locked(database.FieldKeyTitle):
			a.titleKept.Add(1)
		default:
			b.Title = g.Title
		}
	}
	if g.BookFolder != "" {
		b.FilePath = g.BookFolder
	}
	if b.Title == oldTitle && b.FilePath == oldPath {
		return
	}
	if _, err := a.store.UpdateBook(b.ID, b); err != nil {
		a.errs.Add(1)
		a.log(slog.LevelWarn, "fragments %q: update survivor %s: %v", g.BookFolder, b.ID, err)
		return
	}
	if b.Title != oldTitle {
		a.journal(b.ID, "metadata_update", "title", oldTitle, b.Title)
	}
	if b.FilePath != oldPath {
		a.journal(b.ID, fsChangePathUpdate, "file_path", oldPath, b.FilePath)
	}
}

// applyDuplicates retires the duplicate shells of one duplicates group. The
// owner and every book_file row (the shells' included) are left in place.
func (a *fsApplier) applyDuplicates(g fsRepairGroup) {
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	owner, err := a.store.GetBookByID(g.OwnerID)
	if err != nil || owner == nil || owner.IsSoftDeleted() {
		a.skipped.Add(1)
		a.log(slog.LevelWarn, "duplicates %q: owner %s unreadable or gone (err=%v) — group skipped", g.BookFolder, g.OwnerID, err)
		return
	}
	ownerRows, err := a.store.GetBookFiles(owner.ID)
	if err != nil {
		a.errs.Add(1)
		a.log(slog.LevelWarn, "duplicates %q: read owner rows: %v — group skipped", g.BookFolder, err)
		return
	}
	ownerPaths := make(map[string]bool, len(ownerRows))
	for _, r := range ownerRows {
		ownerPaths[r.FilePath] = true
	}
	for _, m := range g.Members {
		shell, err := a.store.GetBookByID(m.ID)
		if err != nil || shell == nil {
			a.kept.Add(1)
			continue
		}
		if shell.IsSoftDeleted() {
			continue
		}
		rows, err := a.store.GetBookFiles(m.ID)
		if err != nil {
			a.kept.Add(1)
			a.log(slog.LevelWarn, "duplicates %q: read rows of %s: %v — shell kept", g.BookFolder, m.ID, err)
			continue
		}
		paths := []string{shell.FilePath}
		for _, r := range rows {
			paths = append(paths, r.FilePath)
		}
		dup := true
		for _, p := range paths {
			if p != "" && !ownerPaths[p] {
				dup = false
				break
			}
		}
		if !dup {
			a.kept.Add(1)
			a.log(slog.LevelWarn, "duplicates %q: shell %s has a file owner %s does not — kept", g.BookFolder, m.ID, owner.ID)
			continue
		}
		if err := merge.SoftDeleteBook(a.store, m.ID); err != nil {
			a.errs.Add(1)
			a.log(slog.LevelWarn, "duplicates %q: soft-delete %s: %v", g.BookFolder, m.ID, err)
			continue
		}
		a.softDeleted.Add(1)
		a.journal(m.ID, fsChangeSoftDelete, "marked_for_deletion", "", "duplicate of "+owner.ID)
	}
	a.groups.Add(1)
}
