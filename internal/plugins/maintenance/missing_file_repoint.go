// file: internal/plugins/maintenance/missing_file_repoint.go
// version: 1.0.0
// guid: 9f4c1e02-7b56-4d38-a1c9-05e6b7d3428f
// last-edited: 2026-08-20

// Package maintenance — REPOINT repair for book_file rows whose FilePath no longer
// resolves but whose bytes are still on disk under a different name.
//
// 🔴 THIS OP NEVER DELETES A ROW. It only rewrites FilePath. That is the whole
// reason it exists as a separate op instead of an `apply` mode on
// maintenance.missing-file-repair: that op's delete path was removed on 2026-08-19
// (owner decision: never delete), and reviving an `apply` flag there would blur a
// deliberate boundary. Repointing is the opposite operation — it RESTORES a pointer
// rather than dropping one — so it gets its own def, its own capability, and its own
// audit trail.
//
// Measured on 2026-08-20: 71,954 of 532,296 book_file rows point at a path that does
// not exist, leaving 16,265 books with zero resolvable files (unplayable and
// undownloadable). The audit classified 35,296 of those rows (49.1%) "recoverable":
// they match the track-slash shape AND a derived candidate exists on disk. This op
// is what turns that classification into a fix.
//
// The shape, from the audit's own sample:
//
//	row says   /books/<Author>/<Book>/Corruption - 2/35.mp3   (does not exist)
//	bytes are  /books/<Author>/<Book>/Corruption - 02.mp3     (exists)
//
// i.e. a per-track subdirectory that was flattened into one padded file.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// missingFileRepointDefaultMax bounds how many rows one run will rewrite, so a first
// production run is a sample rather than a 35k-row leap. 0 in params means this.
const missingFileRepointDefaultMax = 500

type missingFileRepointParams struct {
	// Apply must be explicitly true to write. Default false = report only.
	Apply bool `json:"apply"`
	// PathPrefix scopes the sweep to one tree (e.g. only the organizer's tree).
	PathPrefix string `json:"pathPrefix"`
	// Max bounds rewrites per run. <=0 uses missingFileRepointDefaultMax.
	Max int `json:"max"`
	// RequireSizeMatch, default TRUE, refuses to repoint unless the candidate file's
	// size on disk equals the row's recorded FileSize. 100% of missing rows carry a
	// file_size (measured 2026-08-20), so this is a real check on every row, not an
	// aspiration. Set false only to recover rows whose size was never recorded.
	RequireSizeMatch *bool `json:"requireSizeMatch"`
}

func (p missingFileRepointParams) requireSize() bool {
	return p.RequireSizeMatch == nil || *p.RequireSizeMatch
}

// repointDecision is one row's outcome. Every missing row lands in exactly one
// bucket, and the buckets are reported, so a row that is NOT repointed is visible
// rather than silently dropped.
type repointDecision struct {
	FileID  string `json:"file_id"`
	BookID  string `json:"book_id"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path,omitempty"`
	Reason  string `json:"reason"`
}

type repointPlan struct {
	Apply            bool `json:"apply"`
	ScannedRows      int  `json:"scanned_rows"`
	MissingRows      int  `json:"missing_rows"`
	NoShape          int  `json:"no_shape"`
	NoCandidateBytes int  `json:"no_candidate_bytes"`
	SizeMismatch     int  `json:"size_mismatch"`
	TargetCollision  int  `json:"target_collision"`
	TargetClaimed    int  `json:"target_claimed"`
	Repointable      int  `json:"repointable"`
	Repointed        int  `json:"repointed"`
	UpdateErrs       int  `json:"update_errs"`
	CappedAt         int  `json:"capped_at,omitempty"`

	Samples []repointDecision `json:"samples,omitempty"`
}

func (p repointPlan) summary() string {
	mode := "DRY RUN"
	if p.Apply {
		mode = "APPLIED"
	}
	return fmt.Sprintf(
		"%s scanned=%d missing=%d repointable=%d repointed=%d | rejected: no-shape=%d no-bytes=%d size-mismatch=%d collision=%d already-claimed=%d | update_errs=%d",
		mode, p.ScannedRows, p.MissingRows, p.Repointable, p.Repointed,
		p.NoShape, p.NoCandidateBytes, p.SizeMismatch, p.TargetCollision, p.TargetClaimed, p.UpdateErrs)
}

func (p *Plugin) missingFileRepointDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.missing-file-repoint",
		DisplayName: "Repoint missing book files to their real path",
		Description: "Rewrites book_file.FilePath for rows whose recorded path is gone but whose bytes " +
			"exist on disk under the flattened/padded name. NEVER deletes a row. Default dry-run; " +
			"pass {\"apply\": true} to write. Refuses any row whose target is ambiguous or already " +
			"claimed by another row.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.missing-file-repoint",
		// CapLibraryWrite: this op mutates book_file rows (FilePath only).
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runMissingFileRepoint(ctx, raw, reporter)
		},
	}
}

func (p *Plugin) runMissingFileRepoint(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params missingFileRepointParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("missing-file-repoint: decode params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	plan, err := planMissingFileRepoint(ctx, store, params, reporter)
	if err != nil {
		return err
	}
	log := reporter.Logger()
	if b, mErr := json.Marshal(plan); mErr == nil {
		log.Info("missing-file-repoint report (JSON)", "report", string(b))
	}
	if plan.TargetCollision > 0 {
		log.Warn("missing-file-repoint: rows REFUSED because several rows derive the same file — "+
			"this is the flattened-directory shape; repointing them all would leave N rows sharing one path",
			"rows", plan.TargetCollision)
	}
	if plan.CappedAt > 0 {
		log.Warn("missing-file-repoint: more repointable rows than the cap — run again to continue",
			"cap", plan.CappedAt, "repointable", plan.Repointable)
	}
	log.Info("missing-file-repoint complete", "summary", plan.summary())
	return nil
}

// repointStore is the narrow store this op needs: read every row, read one book's
// files (to rehydrate the full BookFile before writing), and write it back.
type repointStore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

func planMissingFileRepoint(ctx context.Context, store repointStore, params missingFileRepointParams, reporter sdk.Reporter) (repointPlan, error) {
	log := reporter.Logger()
	maxRewrites := params.Max
	if maxRewrites <= 0 {
		maxRewrites = missingFileRepointDefaultMax
	}
	log.Info("missing-file-repoint start",
		"apply", params.Apply, "path_prefix", params.PathPrefix,
		"max", maxRewrites, "require_size_match", params.requireSize())

	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return repointPlan{}, fmt.Errorf("load book files: %w", err)
	}
	plan := repointPlan{Apply: params.Apply, ScannedRows: len(files)}

	// claimed holds EVERY path any row currently points at, missing or not. A repoint
	// target that is already in here would create two rows pointing at one file, which
	// is the duplicate-row bug this op exists to avoid creating.
	claimed := make(map[string]string, len(files))
	for i := range files {
		if p := strings.TrimSpace(files[i].FilePath); p != "" {
			claimed[p] = files[i].ID
		}
	}

	type candidateItem struct {
		idx  int
		file database.BookFileCore
	}
	items := make([]candidateItem, 0, len(files))
	for i := range files {
		path := strings.TrimSpace(files[i].FilePath)
		if path == "" {
			continue
		}
		if params.PathPrefix != "" && !strings.HasPrefix(path, params.PathPrefix) {
			continue
		}
		items = append(items, candidateItem{idx: len(items), file: files[i]})
	}

	// Phase 1 — stat every row, and for the missing ones derive and stat candidates.
	// I/O bound over the whole library, so it runs on the same bounded worker pool the
	// audit's stat sweep uses rather than a serial loop (CLAUDE.md concurrency rule).
	type rowOutcome struct {
		missing bool
		target  string
		reason  string
		size    int64
	}
	outcomes := make([]rowOutcome, len(items))
	var missingCount atomic.Int64

	prog := sdk.NewProgress(reporter, len(items))
	prog.Start(fmt.Sprintf("Checking %d book_file path(s)…", len(items)))

	err = registry.RunItems(ctx, reporter, items, func(_ context.Context, it candidateItem) error {
		if _, serr := os.Stat(it.file.FilePath); serr == nil {
			return nil // present; nothing to do
		} else if !os.IsNotExist(serr) {
			outcomes[it.idx] = rowOutcome{reason: "unreadable"}
			return nil
		}
		missingCount.Add(1)
		out := rowOutcome{missing: true}

		cands, matched := deriveTrackSlashCandidates(it.file.FilePath)
		if !matched {
			out.reason = "no-shape"
			outcomes[it.idx] = out
			return nil
		}
		var found []string
		var foundSize int64
		for _, c := range cands {
			st, serr := os.Stat(c)
			if serr == nil && !st.IsDir() {
				found = append(found, c)
				foundSize = st.Size()
			}
		}
		if len(found) == 0 {
			out.reason = "no-candidate-bytes"
			outcomes[it.idx] = out
			return nil
		}
		// Two derived candidates both existing means padded AND unpadded files are
		// both on disk; which one this row meant is unknowable. Refuse.
		if len(found) > 1 {
			out.reason = "ambiguous-candidates"
			outcomes[it.idx] = out
			return nil
		}
		out.target = found[0]
		out.size = foundSize
		outcomes[it.idx] = out
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Checked %d/%d paths (missing=%d)", i+1, t, missingCount.Load())
		},
	})
	if err != nil {
		return repointPlan{}, fmt.Errorf("stat sweep: %w", err)
	}
	plan.MissingRows = int(missingCount.Load())

	// Phase 2 — collision detection ACROSS rows. Serial and deliberate: this is a
	// whole-set decision, not a per-row one. N rows deriving one target is the
	// expected shape when a directory of tracks collapsed into a single file, and
	// repointing all of them would leave N rows sharing one path.
	targetClaimants := map[string][]int{}
	for i := range items {
		if outcomes[i].missing && outcomes[i].target != "" {
			targetClaimants[outcomes[i].target] = append(targetClaimants[outcomes[i].target], i)
		}
	}

	type rewrite struct {
		item   candidateItem
		target string
	}
	var rewrites []rewrite
	for i, it := range items {
		o := outcomes[i]
		if !o.missing {
			continue
		}
		switch {
		case o.reason == "no-shape":
			plan.NoShape++
			continue
		case o.reason == "no-candidate-bytes", o.reason == "ambiguous-candidates":
			plan.NoCandidateBytes++
			continue
		case o.target == "":
			continue
		}
		if len(targetClaimants[o.target]) > 1 {
			plan.TargetCollision++
			plan.addSample(repointDecision{FileID: it.file.ID, BookID: it.file.BookID,
				OldPath: it.file.FilePath, NewPath: o.target,
				Reason: fmt.Sprintf("target-collision: %d rows derive this same file", len(targetClaimants[o.target]))})
			continue
		}
		if owner, taken := claimed[o.target]; taken && owner != it.file.ID {
			plan.TargetClaimed++
			plan.addSample(repointDecision{FileID: it.file.ID, BookID: it.file.BookID,
				OldPath: it.file.FilePath, NewPath: o.target,
				Reason: "target already claimed by book_file " + owner})
			continue
		}
		if params.requireSize() && it.file.FileSize > 0 && o.size != it.file.FileSize {
			plan.SizeMismatch++
			plan.addSample(repointDecision{FileID: it.file.ID, BookID: it.file.BookID,
				OldPath: it.file.FilePath, NewPath: o.target,
				Reason: fmt.Sprintf("size mismatch: row=%d disk=%d", it.file.FileSize, o.size)})
			continue
		}
		rewrites = append(rewrites, rewrite{item: it, target: o.target})
	}
	plan.Repointable = len(rewrites)

	// Deterministic order so a capped run takes a stable prefix across re-runs
	// instead of a different arbitrary slice each time.
	sort.Slice(rewrites, func(a, b int) bool { return rewrites[a].item.file.ID < rewrites[b].item.file.ID })
	if len(rewrites) > maxRewrites {
		plan.CappedAt = maxRewrites
		log.Warn("missing-file-repoint: more repointable rows than the cap — taking the first N by file ID",
			"repointable", len(rewrites), "cap", maxRewrites)
		rewrites = rewrites[:maxRewrites]
	}

	for _, rw := range rewrites {
		plan.addSample(repointDecision{FileID: rw.item.file.ID, BookID: rw.item.file.BookID,
			OldPath: rw.item.file.FilePath, NewPath: rw.target, Reason: "repointable"})
	}
	if !params.Apply {
		log.Info("missing-file-repoint: DRY RUN — no rows written", "would_repoint", len(rewrites))
		return plan, nil
	}

	// Phase 3 — write. Rehydrate the FULL BookFile and change only FilePath:
	// UpdateBookFile does a full-record replacement, so constructing a partial
	// BookFile here would wipe the fingerprint, transcript and tags that make these
	// rows worth recovering in the first place.
	var repointed, updateErrs atomic.Int64
	err = registry.RunItems(ctx, reporter, rewrites, func(_ context.Context, rw rewrite) error {
		siblings, gerr := store.GetBookFiles(rw.item.file.BookID)
		if gerr != nil {
			updateErrs.Add(1)
			log.Warn("missing-file-repoint: load book files", "book", rw.item.file.BookID, "err", gerr)
			return nil
		}
		var full *database.BookFile
		for i := range siblings {
			if siblings[i].ID == rw.item.file.ID {
				full = &siblings[i]
				break
			}
		}
		if full == nil {
			updateErrs.Add(1)
			log.Warn("missing-file-repoint: row vanished before write", "file", rw.item.file.ID)
			return nil
		}
		full.FilePath = rw.target
		if uerr := store.UpdateBookFile(full.ID, full); uerr != nil {
			updateErrs.Add(1)
			log.Warn("missing-file-repoint: update failed", "file", full.ID, "err", uerr)
			return nil
		}
		repointed.Add(1)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Repointed %d/%d rows (errs=%d)", i+1, t, updateErrs.Load())
		},
	})
	if err != nil {
		return plan, fmt.Errorf("repoint writes: %w", err)
	}
	plan.Repointed = int(repointed.Load())
	plan.UpdateErrs = int(updateErrs.Load())
	return plan, nil
}

// addSample keeps a bounded, representative sample rather than 35k rows of JSON.
func (p *repointPlan) addSample(d repointDecision) {
	const maxSamples = 40
	if len(p.Samples) < maxSamples {
		p.Samples = append(p.Samples, d)
	}
}
