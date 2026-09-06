// file: internal/plugins/maintenance/recover_missing_files.go
// version: 1.2.0
// guid: 4e8b1d27-9a3c-4f60-bb15-7c2e9d84a013
// last-edited: 2026-09-05

// Package maintenance — RECOVER missing book_file rows by matching their recorded
// FileSize to real files on disk, for the rows that maintenance.missing-file-repoint
// (name-shape derivation) could NOT recover.
//
// 🔴 RELATIONSHIP TO REPOINT. Run maintenance.missing-file-repoint FIRST. That op
// recovers a missing row when it can DERIVE the new path from the old one's shape
// (a per-track subdirectory flattened into one padded file, or the owning single-file
// book's own path). This op handles the RESIDUE: rows whose bytes were renamed to
// something the shape rules cannot guess. Repoint's fixes drop out of this op's
// population automatically — a repointed row is no longer missing, so it is not
// selected here.
//
// 🔴 WHY A NEW OP, NOT AN APPLY FLAG ON REPOINT. Different candidate discovery, with a
// different cost. Repoint stats one derived candidate per row inline. This op has to
// build a whole-tree inventory of every file on disk keyed by size, then match against
// it — an O(files-on-disk) directory walk that repoint's cheap per-row path must never
// pay. Folding one into the other would make the common case expensive.
//
// 🔴 WHY SIZE MATCHING IS SAFE HERE. Byte-size alone is weak identity — audio files
// collide on size readily — so this op NEVER repoints on size alone. It repoints a row
// only when the match is BIDIRECTIONALLY UNIQUE against UNCLAIMED files: exactly one
// file of that size (and extension) that NO book_file row currently points at, wanted
// by exactly one missing row. Anything else (two candidate files, two rows wanting one
// file, an extension mismatch) is refused and reported, never guessed. This generalizes
// repoint's `claimed`/collision guards from "derived target" to "size-keyed candidate".
//
// SCOPE (this op = Branch A + C):
//   - Branch A — IN-TREE REPOINT (DB-only, zero I/O): the unique unclaimed candidate is
//     under RootDir. Rewrite FilePath; never move bytes, never delete a row.
//   - Branch C — CENSUS: everything else is classified and reported, not acted on:
//     "outside"  — the only candidate(s) live under a source dir (SourceDirs), i.e. a
//     REFLINK candidate for the deferred Branch B, not an in-tree repoint.
//     "nowhere"  — no file of that size exists anywhere walked. Residue.
//     "ambiguous"/"size-collision"/"ext-mismatch"/"no-size" — refused, with the reason.
//   - Branch B — REFLINK from a source dir into the tree — is a SEPARATE follow-up op.
//     This op only CENSUSES its population ("outside").
//
// ⚠️ SCAN PRECONDITION (operational, not enforced), same as mark-missing/repoint: do
// NOT run apply=true while a library scan is active. This op matches against an
// inventory SNAPSHOT that may be minutes stale, and a scan may be moving files
// underneath it. The controls are the deploy boundary (the scan only resumes on the
// operator's deploy) and the write-time re-stat interlock below (each candidate is
// re-stat'd immediately before its row is written; a disagreement skips the row rather
// than writing it stale).
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// recoverDefaultMax bounds how many rows one run will repoint, so a first production run
// is a sample rather than a leap over the whole residue. 0 in params means this.
const recoverDefaultMax = 500

// recoverWalkProgressEvery emits a progress tick every N files during the inventory
// walk. The walk is a heavy pre-write phase; without an in-walk tick the watchdog counts
// stuck-time from StartedAt and kills the op as "never_reported" (watchdog.go). Sized so
// the interval between ticks stays well under the 5m ProgressTimeout even on a slow NAS.
const recoverWalkProgressEvery = 2000

type recoverMissingParams struct {
	// Apply must be explicitly true to write. Default false = report only.
	Apply bool `json:"apply"`
	// SourceDirs are extra roots walked ONLY for the "outside" census — the reflink
	// candidates the deferred Branch B will act on (e.g. an ingest/newbooks tree, an
	// iTunes tree). Bytes found only under one of these land in bucket "outside".
	// Empty means only RootDir is walked, so "outside" and "nowhere" cannot be
	// distinguished and a size found nowhere in-tree is reported as "nowhere".
	SourceDirs []string `json:"sourceDirs,omitempty"`
	// Max bounds in-tree repoints per run. <=0 uses recoverDefaultMax.
	Max int `json:"max"`
	// RequireExtMatch, default TRUE, refuses to match a candidate whose file extension
	// differs from the missing row's recorded path — a .jpg the same byte size as a
	// .mp3 is never the row's audio. Set false only to recover rows whose extension is
	// known to have changed.
	RequireExtMatch *bool `json:"requireExtMatch"`
	// ReportPath overrides where the full per-row TSV lands. Empty derives a path under
	// reports/. Written on EVERY run — a dry run whose decisions are unreadable cannot
	// inform the apply it exists to inform.
	ReportPath string `json:"reportPath,omitempty"`
}

func (p recoverMissingParams) requireExt() bool {
	return p.RequireExtMatch == nil || *p.RequireExtMatch
}

// recoverDecision is one missing row's outcome. Every missing row lands in exactly one
// bucket and every bucket is reported, so a row that is NOT repointed is visible.
type recoverDecision struct {
	FileID string `json:"file_id"`
	BookID string `json:"book_id"`
	// Bucket is the coarse outcome; Reason carries specifics. Buckets: "repointable"
	// (unique in-tree candidate → would rewrite FilePath), "outside" (candidate only
	// under a source dir → reflink later), "nowhere" (no candidate of that size),
	// "ambiguous" (>1 in-tree candidate), "size-collision" (>1 missing row wants the one
	// candidate), "ext-mismatch" (a same-size candidate exists but the extension differs
	// and RequireExtMatch is on), "no-size" (row has no recorded size to match on).
	Bucket   string `json:"bucket"`
	Size     int64  `json:"size"`
	OldPath  string `json:"old_path"`
	NewPath  string `json:"new_path,omitempty"`
	CandSeen int    `json:"cand_seen"`
	Reason   string `json:"reason"`
}

type recoverPlan struct {
	Apply         bool `json:"apply"`
	ScannedRows   int  `json:"scanned_rows"`
	MissingRows   int  `json:"missing_rows"`
	UnclaimedSeen int  `json:"unclaimed_files_seen"`

	Repointable   int `json:"repointable"`
	Outside       int `json:"outside"`
	Nowhere       int `json:"nowhere"`
	Ambiguous     int `json:"ambiguous"`
	SizeCollision int `json:"size_collision"`
	ExtMismatch   int `json:"ext_mismatch"`
	NoSize        int `json:"no_size"`

	InventoryKeys int `json:"inventory_keys"` // distinct sizes seen — a degenerate (near-zero) value warns the inventory is empty
	WalkErrors    int `json:"walk_errors"`    // per-entry walk failures skipped; >0 means the inventory is partial

	Repointed      int `json:"repointed"`
	SkippedChanged int `json:"skipped_changed"`
	UpdateErrs     int `json:"update_errs"`
	CappedAt       int `json:"capped_at,omitempty"`

	ReportPath string `json:"report_path,omitempty"`

	// Samples is a per-bucket-capped subset for the JSON log line; all holds every
	// decision for the TSV (not serialised — the residue is tens of thousands of rows).
	Samples       []recoverDecision `json:"samples,omitempty"`
	all           []recoverDecision
	bucketSampled map[string]int
}

func (p *recoverPlan) record(d recoverDecision) {
	p.all = append(p.all, d)
	const samplesPerBucket = 8
	if p.bucketSampled == nil {
		p.bucketSampled = map[string]int{}
	}
	if p.bucketSampled[d.Bucket] < samplesPerBucket {
		p.bucketSampled[d.Bucket]++
		p.Samples = append(p.Samples, d)
	}
}

func (p recoverPlan) summary() string {
	mode := "DRY RUN"
	if p.Apply {
		mode = "APPLIED"
	}
	return fmt.Sprintf(
		"%s scanned=%d missing=%d | in-tree repointable=%d repointed=%d | census: outside=%d nowhere=%d | "+
			"refused: ambiguous=%d size-collision=%d ext-mismatch=%d no-size=%d | skipped-changed=%d update_errs=%d",
		mode, p.ScannedRows, p.MissingRows, p.Repointable, p.Repointed,
		p.Outside, p.Nowhere, p.Ambiguous, p.SizeCollision, p.ExtMismatch, p.NoSize,
		p.SkippedChanged, p.UpdateErrs)
}

func (p *Plugin) recoverMissingFilesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.recover-missing-files",
		DisplayName: "Recover missing book files by size match",
		Description: "For book_file rows whose bytes are gone and whose path cannot be derived by " +
			"missing-file-repoint, matches the row's recorded file size to a real file on disk. Repoints " +
			"IN-TREE (rewrites FilePath, never moves or deletes) only when the match is bidirectionally " +
			"unique — exactly one unclaimed file of that size and extension, wanted by exactly one row. " +
			"Censuses the rest (outside=reflink-later, nowhere=residue). Run missing-file-repoint FIRST. " +
			"Default dry-run; pass {\"apply\": true} to write. Do NOT run apply=true while a library scan " +
			"is active (the op re-stats each candidate before writing, so concurrent changes are skipped).",
		DefaultPriority: sdk.PriorityLow,
		// Its OWN ConcurrencyKey, like every maintenance op. It declares no Writes for the
		// same reason mark-missing/repoint do: library.scan declares no Writes either, so a
		// Writes conflict-set gates against nothing (Gate 3b is Writes∩Writes). The
		// scan/apply interlock is operational — see the SCAN PRECONDITION note up top.
		ConcurrencyKey: "maintenance.recover-missing-files",
		// ResumeDrop, matching the other missing-file ops: this WRITES, and an apply
		// interrupted midway must not silently resume. Re-running is cheap and safe (a
		// repointed row is no longer missing, so it is simply not selected again).
		ResumePolicy: sdk.ResumeDrop,
		// LivenessRunItems: the stat sweep and the write phase both go through
		// registry.RunItems, which stamps progress per item. The inventory walk BETWEEN
		// them is not a RunItems phase, so it emits reporter.UpdateProgress itself every
		// recoverWalkProgressEvery files — the watchdog reads lastProgressAt regardless of
		// which phase stamped it, so that keeps a heavy walk from tripping never_reported.
		Liveness:     sdk.LivenessRunItems,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runRecoverMissingFiles(ctx, raw, reporter)
		},
	}
}

func (p *Plugin) runRecoverMissingFiles(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params recoverMissingParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("recover-missing-files: decode params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	rootDir := p.deps.RootDir()
	if strings.TrimSpace(rootDir) == "" {
		return fmt.Errorf("recover-missing-files: RootDir is empty; cannot walk the library tree")
	}
	log := reporter.Logger()
	if params.Apply {
		// Log the precondition at apply-start so the journal records that the operator
		// was told. A documented hazard is not a control; the deploy boundary and the
		// write-time re-stat are the controls.
		log.Warn("recover-missing-files: APPLY — rewriting FilePath on bidirectionally-unique rows. " +
			"Precondition: no library scan should be active. Each candidate is re-stat'd before its row " +
			"is written, so concurrent changes are skipped.")
	}

	plan, err := planRecoverMissingFiles(ctx, store, rootDir, params, reporter)
	if err != nil {
		return err
	}

	// Write the report BEFORE the summary log lines, so a run killed mid-summary still
	// leaves the artifact behind.
	reportPath := params.ReportPath
	if reportPath == "" {
		name := registry.ReporterOpID(reporter)
		if name == "" {
			name = "unknown-op"
		}
		reportPath = filepath.Join("reports", "recover-missing-files-"+name+".tsv")
	}
	if wErr := writeRecoverReport(reportPath, plan.all); wErr != nil {
		log.Error("recover-missing-files: FAILED to write the per-row report",
			"path", reportPath, "err", wErr, "rows", len(plan.all))
	} else {
		plan.ReportPath = reportPath
		log.Info("recover-missing-files: per-row report written", "path", reportPath, "rows", len(plan.all))
	}

	if b, mErr := json.Marshal(plan); mErr == nil {
		log.Info("recover-missing-files report (JSON)", "report", string(b))
	}
	if plan.SizeCollision > 0 {
		// Same cause repoint measured: rows sharing a size that resolves to one file are
		// usually DUPLICATE BOOK RECORDS. Say what was counted; let the report say which.
		log.Warn("recover-missing-files: rows REFUSED because more than one missing row wants the one "+
			"unclaimed file of their size (repointing them all would leave rows sharing one path). Group "+
			"the report's size-collision rows by new_path; duplicate book records are a known cause.",
			"rows", plan.SizeCollision, "report", reportPath)
	}
	if plan.CappedAt > 0 {
		log.Warn("recover-missing-files: more repointable rows than the cap — run again to continue",
			"cap", plan.CappedAt, "repointable", plan.Repointable)
	}
	log.Info("recover-missing-files complete", "summary", plan.summary())
	return nil
}

// recoverStore is the narrow store this op needs: read every row's core projection (to
// find the missing ones and their sizes), rehydrate one book's files to write the full
// record back, and write it. It deliberately does NOT read books — recover discovers
// candidates from the disk inventory, not from book paths, so it needs no book table.
type recoverStore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// invFile is one unclaimed file found on disk during the inventory walk.
type invFile struct {
	path   string
	ext    string // lower-cased extension, for the RequireExtMatch filter
	inTree bool   // under RootDir (a Branch-A repoint target) vs a SourceDir (reflink-later)
}

// rewriteRow is one row the apply phase will repoint, with the target it matched.
type rewriteRow struct {
	file   database.BookFileCore
	target string
}

func extOf(path string) string { return strings.ToLower(filepath.Ext(path)) }

func planRecoverMissingFiles(ctx context.Context, store recoverStore, rootDir string, params recoverMissingParams, reporter sdk.Reporter) (recoverPlan, error) {
	log := reporter.Logger()
	maxRewrites := params.Max
	if maxRewrites <= 0 {
		maxRewrites = recoverDefaultMax
	}
	requireExt := params.requireExt()
	log.Info("recover-missing-files start",
		"apply", params.Apply, "root_dir", rootDir, "source_dirs", params.SourceDirs,
		"max", maxRewrites, "require_ext_match", requireExt)

	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return recoverPlan{}, fmt.Errorf("load book files: %w", err)
	}
	plan := recoverPlan{Apply: params.Apply, ScannedRows: len(files)}

	// claimed holds EVERY path any row currently points at, missing or not. A candidate
	// already claimed by some row must never become a second row's target — that is the
	// duplicate-row bug this op exists to avoid creating. Built once, read by the walk.
	claimed := make(map[string]struct{}, len(files))
	for i := range files {
		if pth := strings.TrimSpace(files[i].FilePath); pth != "" {
			claimed[pth] = struct{}{}
		}
	}

	// --- Phase 1 — stat every row to find the MISSING ones. The stored Missing flag is
	// not trusted (it has no reliable live writer — the reason mark-missing exists), so
	// disk is the source of truth. I/O bound over the whole library, on the same bounded
	// pool the audit/repoint/mark-missing sweeps use. ---
	type item struct {
		idx  int
		file database.BookFileCore
	}
	items := make([]item, 0, len(files))
	for i := range files {
		if strings.TrimSpace(files[i].FilePath) == "" {
			continue // a pathless row is a different defect; nothing to match on disk
		}
		items = append(items, item{idx: len(items), file: files[i]})
	}
	gone := make([]bool, len(items))
	var missingCount atomic.Int64

	prog := sdk.NewProgress(reporter, len(items))
	prog.Start(fmt.Sprintf("Checking %d book_file path(s)…", len(items)))
	err = registry.RunItems(ctx, reporter, items, func(_ context.Context, it item) error {
		if _, serr := os.Stat(it.file.FilePath); os.IsNotExist(serr) {
			gone[it.idx] = true
			missingCount.Add(1)
		}
		// A stat error other than not-exist (permission, I/O) is treated as present:
		// this op only recovers rows whose bytes are provably gone, never one it merely
		// could not read.
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Checked %d/%d paths (missing=%d)", i+1, t, missingCount.Load())
		},
	})
	if err != nil {
		return recoverPlan{}, fmt.Errorf("stat sweep: %w", err)
	}
	plan.MissingRows = int(missingCount.Load())

	// --- Phase 2 — inventory walk. Build size -> unclaimed files on disk, over RootDir
	// and any SourceDirs. Keyed by SIZE ALONE (extension is stored per file and filtered
	// at classify time) so a "same size, different extension" candidate is still visible
	// — that distinction is what separates the recoverable "ext-mismatch" bucket from a
	// true "nowhere". Single-threaded on purpose: a directory walk is sequential readdir,
	// and the per-file work is a map membership check plus a size read from the DirEntry
	// (no extra syscall) — not the "meaningful per-item work" the concurrency rule
	// targets. The heavy per-item work (the stat sweep above and the writes below) is
	// what runs on the worker pool. Only UNCLAIMED files are kept, so the map is bounded
	// by the orphan count, not the whole library. ---
	unclaimedBySize := make(map[int64][]invFile)
	seen := make(map[string]struct{}) // dedupe overlapping roots
	var walked, walkErrs int64
	walkRoot := func(root string, inTree bool) error {
		root = strings.TrimSpace(root)
		if root == "" {
			return nil
		}
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if werr != nil {
				// A single unreadable subtree must not abort the whole walk — skip it and
				// keep going, so one bad mount point does not sink the census. But COUNT it:
				// a walk that skipped a large subtree yields a partial inventory, so rows
				// that live there would wrongly classify "nowhere". WalkErrors surfaces that
				// so a degraded run is not mistaken for a clean one.
				walkErrs++
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			walked++
			if walked%recoverWalkProgressEvery == 0 {
				// Keep the watchdog's lastProgressAt fresh through the walk. Total is
				// unknown mid-walk, so report count with total 0.
				_ = reporter.UpdateProgress(int(walked), 0,
					fmt.Sprintf("Building disk inventory… %d files (%d unclaimed)", walked, plan.UnclaimedSeen))
			}
			if _, taken := claimed[path]; taken {
				return nil // a live row already points here; never a recovery target
			}
			if _, dup := seen[path]; dup {
				return nil
			}
			seen[path] = struct{}{}
			info, ierr := d.Info()
			if ierr != nil || info.Size() <= 0 {
				return nil
			}
			unclaimedBySize[info.Size()] = append(unclaimedBySize[info.Size()],
				invFile{path: path, ext: extOf(path), inTree: inTree})
			plan.UnclaimedSeen++
			return nil
		})
	}
	// The RootDir walk is FATAL if the root itself is unreadable. filepath.WalkDir hands
	// a stat failure on the root to the callback with d==nil, where the skip-and-continue
	// branch above swallows it and WalkDir returns nil — so an unmounted NAS or a wrong
	// --dir would otherwise leave an EMPTY inventory and classify every missing row as
	// "nowhere", reporting a confident-but-wrong census (worse than a crash, because we'd
	// act on it). Precheck the root explicitly so that case is a hard error, not silence.
	if st, serr := os.Stat(rootDir); serr != nil || !st.IsDir() {
		return recoverPlan{}, fmt.Errorf("recover-missing-files: RootDir %q is not a readable directory "+
			"(unmounted or wrong --dir?): %v — refusing to build an empty inventory", rootDir, serr)
	}
	if werr := walkRoot(rootDir, true); werr != nil && ctx.Err() != nil {
		return recoverPlan{}, fmt.Errorf("inventory walk canceled: %w", werr)
	}
	// A bad SourceDir, unlike a bad RootDir, is NOT fatal: it only degrades the
	// outside/nowhere split (a reflink census for the deferred Branch B), so warn and
	// carry on rather than abort the in-tree repoint the run exists to do.
	for _, sd := range params.SourceDirs {
		if st, serr := os.Stat(sd); serr != nil || !st.IsDir() {
			log.Warn("recover-missing-files: skipping unreadable source dir (outside/nowhere split degraded)",
				"dir", sd, "err", serr)
			continue
		}
		if werr := walkRoot(sd, false); werr != nil && ctx.Err() != nil {
			return recoverPlan{}, fmt.Errorf("inventory walk canceled: %w", werr)
		}
	}
	plan.InventoryKeys = len(unclaimedBySize)
	plan.WalkErrors = int(walkErrs)
	if walkErrs > 0 {
		log.Warn("recover-missing-files: inventory walk skipped unreadable entries — inventory is PARTIAL, so "+
			"some 'nowhere' rows may have bytes in a subtree that could not be read",
			"walk_errors", walkErrs, "unclaimed_seen", plan.UnclaimedSeen)
	}

	// --- Phase 3, pass A — resolve each missing row's candidate set (size, then the ext
	// filter) and register which target path each single-candidate row wants, so a target
	// wanted by two rows is a collision rather than a race won by iteration order. This
	// mirrors repoint's targetClaimants, generalized from a derived target to a size
	// match. ---
	type resolved struct {
		row     database.BookFileCore
		inTree  []invFile
		outside []invFile
		anySize int // same-size candidates BEFORE the ext filter (to explain ext-mismatch)
	}
	var res []resolved
	targetClaimants := make(map[string]int)
	for i := range items {
		if !gone[i] {
			continue
		}
		f := items[i].file
		if f.FileSize <= 0 {
			res = append(res, resolved{row: f}) // classified "no-size" in pass B
			continue
		}
		all := unclaimedBySize[f.FileSize]
		rowExt := extOf(f.FilePath)
		r := resolved{row: f, anySize: len(all)}
		for _, c := range all {
			if requireExt && c.ext != rowExt {
				continue
			}
			if c.inTree {
				r.inTree = append(r.inTree, c)
			} else {
				r.outside = append(r.outside, c)
			}
		}
		if len(r.inTree) == 1 {
			targetClaimants[r.inTree[0].path]++
		}
		res = append(res, r)
	}

	// --- Phase 3, pass B — classify. ---
	var rewrites []rewriteRow
	for _, r := range res {
		f := r.row
		switch {
		case f.FileSize <= 0:
			plan.NoSize++
			plan.record(recoverDecision{FileID: f.ID, BookID: f.BookID, Bucket: "no-size",
				Size: f.FileSize, OldPath: f.FilePath, Reason: "row has no recorded file size to match on"})
		case len(r.inTree) == 0 && len(r.outside) == 0:
			// Nothing matched after the ext filter. If a same-size file existed but the
			// extension differed, say so — that is recoverable by re-running with
			// requireExtMatch=false; a true nowhere is not.
			if requireExt && r.anySize > 0 {
				plan.ExtMismatch++
				plan.record(recoverDecision{FileID: f.ID, BookID: f.BookID, Bucket: "ext-mismatch",
					Size: f.FileSize, OldPath: f.FilePath, CandSeen: r.anySize,
					Reason: "a same-size file exists but its extension differs (requireExtMatch on)"})
			} else {
				plan.Nowhere++
				plan.record(recoverDecision{FileID: f.ID, BookID: f.BookID, Bucket: "nowhere",
					Size: f.FileSize, OldPath: f.FilePath, Reason: "no file of this size found in any walked tree"})
			}
		case len(r.inTree) == 0:
			// Candidate(s) exist only under a SourceDir → a reflink target for Branch B,
			// not an in-tree repoint. Census only. NewPath reports r.outside[0] as a SAMPLE,
			// not a chosen target: it is intentionally NOT deduped or uniqueness-checked here.
			// ⚠️ Branch B MUST apply the same bidirectional-uniqueness gate the in-tree path
			// uses (one unclaimed candidate, wanted by one row) before it reflinks — it must
			// NOT adopt "first outside candidate wins" from this census line.
			plan.Outside++
			plan.record(recoverDecision{FileID: f.ID, BookID: f.BookID, Bucket: "outside",
				Size: f.FileSize, OldPath: f.FilePath, NewPath: r.outside[0].path, CandSeen: len(r.outside),
				Reason: "bytes of this size exist only outside RootDir (reflink candidate — Branch B)"})
		case len(r.inTree) > 1:
			plan.Ambiguous++
			plan.record(recoverDecision{FileID: f.ID, BookID: f.BookID, Bucket: "ambiguous",
				Size: f.FileSize, OldPath: f.FilePath, CandSeen: len(r.inTree),
				Reason: fmt.Sprintf("%d unclaimed in-tree files share this size; which one is unknowable", len(r.inTree))})
		case targetClaimants[r.inTree[0].path] > 1:
			plan.SizeCollision++
			plan.record(recoverDecision{FileID: f.ID, BookID: f.BookID, Bucket: "size-collision",
				Size: f.FileSize, OldPath: f.FilePath, NewPath: r.inTree[0].path, CandSeen: 1,
				Reason: fmt.Sprintf("%d missing rows want this one file; assigning it is unknowable", targetClaimants[r.inTree[0].path])})
		default:
			// Bidirectionally unique: exactly one unclaimed in-tree file of this size(+ext),
			// wanted by exactly one missing row. Safe to repoint.
			plan.Repointable++
			rewrites = append(rewrites, rewriteRow{file: f, target: r.inTree[0].path})
		}
	}

	// Deterministic order so a capped run takes a stable prefix across re-runs.
	sort.Slice(rewrites, func(a, b int) bool { return rewrites[a].file.ID < rewrites[b].file.ID })
	if len(rewrites) > maxRewrites {
		plan.CappedAt = maxRewrites
		log.Warn("recover-missing-files: more repointable rows than the cap — taking the first N by file ID",
			"repointable", len(rewrites), "cap", maxRewrites)
		rewrites = rewrites[:maxRewrites]
	}
	for _, rw := range rewrites {
		plan.record(recoverDecision{FileID: rw.file.ID, BookID: rw.file.BookID, Bucket: "repointable",
			Size: rw.file.FileSize, OldPath: rw.file.FilePath, NewPath: rw.target, CandSeen: 1,
			Reason: "unique in-tree size match — would repoint"})
	}

	if !params.Apply {
		log.Info("recover-missing-files: DRY RUN — no rows written", "would_repoint", len(rewrites))
		return plan, nil
	}

	// --- Phase 4 — write (Branch A only). Rehydrate the FULL BookFile and change only
	// FilePath: UpdateBookFile is a full-record replacement, so a partial record would
	// wipe the fingerprint/transcript/tags that make these rows worth recovering. Re-stat
	// the target immediately before writing (the interlock): if it is gone or its size no
	// longer matches, skip rather than write a value a concurrent scan just invalidated. ---
	var repointed, skippedChanged, updateErrs atomic.Int64
	var mu sync.Mutex // guards plan.record (RunItems runs the callback concurrently)
	err = registry.RunItems(ctx, reporter, rewrites, func(_ context.Context, rw rewriteRow) error {
		st, serr := os.Stat(rw.target)
		if serr != nil || st.IsDir() || st.Size() != rw.file.FileSize {
			skippedChanged.Add(1)
			mu.Lock()
			plan.record(recoverDecision{FileID: rw.file.ID, BookID: rw.file.BookID, Bucket: "skipped-changed",
				Size: rw.file.FileSize, OldPath: rw.file.FilePath, NewPath: rw.target,
				Reason: "target changed between plan and write (gone, directory, or size differs) — skipped"})
			mu.Unlock()
			return nil
		}
		siblings, gerr := store.GetBookFiles(rw.file.BookID)
		if gerr != nil {
			updateErrs.Add(1)
			log.Warn("recover-missing-files: load book files", "book", rw.file.BookID, "err", gerr)
			return nil
		}
		var full *database.BookFile
		for i := range siblings {
			if siblings[i].ID == rw.file.ID {
				full = &siblings[i]
				break
			}
		}
		if full == nil {
			updateErrs.Add(1)
			log.Warn("recover-missing-files: row vanished before write", "file", rw.file.ID)
			return nil
		}
		full.FilePath = rw.target
		if uerr := store.UpdateBookFile(full.ID, full); uerr != nil {
			updateErrs.Add(1)
			log.Warn("recover-missing-files: update failed", "file", full.ID, "err", uerr)
			return nil
		}
		repointed.Add(1)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Repointed %d/%d rows (skipped=%d errs=%d)", i+1, t, skippedChanged.Load(), updateErrs.Load())
		},
	})
	if err != nil {
		return plan, fmt.Errorf("repoint writes: %w", err)
	}
	plan.Repointed = int(repointed.Load())
	plan.SkippedChanged = int(skippedChanged.Load())
	plan.UpdateErrs = int(updateErrs.Load())
	return plan, nil
}

// writeRecoverReport dumps every missing row's decision, TSV, so a person deciding
// whether to apply can read and grep it by bucket — including the rows that were NOT
// repointed, which is the population the "run the apply?" decision actually turns on.
func writeRecoverReport(path string, decisions []recoverDecision) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("bucket\tfile_id\tbook_id\tsize\told_path\tnew_path\tcand_seen\treason\n")
	for _, d := range decisions {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%d\t%s\t%s\t%d\t%s\n",
			d.Bucket, d.FileID, d.BookID, d.Size, clean(d.OldPath), clean(d.NewPath), d.CandSeen, clean(d.Reason))
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}
