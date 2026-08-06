// file: internal/plugins/maintenance/probe_directory_books.go
// version: 1.0.0
// guid: 6f2a90d4-3c81-4e57-b0a6-9d47e1c85b23
// last-edited: 2026-08-06

// Package maintenance — op maintenance.probe-directory-books.
//
// TIER 2 OF FIRST AID: re-decide, with measured evidence, the directory-shaped
// books that tier 1 could only park.
//
// 🔴 THE PROBLEM THIS EXISTS TO CLOSE. maintenance.relink-unlinked-books
// relinked 16.0k of 17,149 unlinked books. The 1,019 that went to review are NOT
// unknowable — they were never MEASURED. classifyUnlinked calls
// linkintegrity.ClassifyDir(names, subdirs, nil): durations are always nil,
// because a whole-library pass over 44,887 books can afford one DB read and one
// os.Stat per book and nothing more. ClassifyDir's series guard cannot fire
// without durations, so it refuses — correctly — and every multi-file folder
// lands in review carrying the reason "no durations are known — cannot rule out
// a series".
//
// That refusal is right at tier 1 and wrong as a final answer. Tier 2's whole
// premise is that the flagged set is small enough (~1,019 folders, not 44,887
// books) to afford what tier 1 could not: an ffprobe per audio file. This op
// spends exactly that, then re-runs THE SAME classifier with the durations
// filled in.
//
// SAME CLASSIFIER, NOT A SECOND ONE. Detection reuses classifyUnlinked from
// relink_unlinked.go and the verdict comes from linkintegrity.ClassifyDirProbed,
// which wraps ClassifyDir. Writing a parallel classifier here would let tier 1
// and tier 2 drift into disagreeing about the same folder, which is worse than
// either being wrong on its own.
//
// 🔴 ABSENT EVIDENCE MEANS "CANNOT VERIFY", NEVER "REFUTED". A probe that fails
// contributes NOTHING to the verdict — never a zero. A zero duration reads as
// "short file, therefore a chapter, therefore safe to merge", and that exact
// substitution disabled the regroup series guard across 97.5% of the review
// queue and nearly merged 41 of 43 distinct novels. The invariant is carried
// structurally by linkintegrity.ProbedDuration's OK flag rather than by
// convention, and a folder with ANY unprobeable file stays in review.
//
// The same rule governs a missing ffprobe: the op refuses to start rather than
// classifying all 1,019 folders as unknown-duration, which would look like a
// successful run that simply found nothing to do.
//
// 🔴 DATA-LOSS SAFETY. Like relink_unlinked.go, this file contains NO UpdateBook
// call — the defence is structural, not a promise. This repo's dominant incident
// class is the write-back wipe (a bare UpdateBook clearing AcoustIDFingerprint /
// Author / Series). The only write here is CreateBookFile, which is additive.
//
// DRY RUN BY DEFAULT, matching its tier-1 sibling: nothing is written unless the
// caller passes {"apply": true}.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// probeDirParams configures the op.
type probeDirParams struct {
	// Apply gates every write. False (the default) makes this a pure re-classifier
	// that reports what the measured durations would change.
	Apply bool `json:"apply"`
	// Limit caps how many folders are LINKED this run (0 = no cap). Probing and
	// re-classification always cover the whole flagged set, mirroring
	// relink-unlinked-books: a capped run must never look like a clean bill of
	// health, so the cap applies to the writes and never to the measurement.
	Limit int `json:"limit"`
}

// probeFileTimeout bounds one ffprobe invocation.
//
// This mirrors the value of internal/mediainfo's ffprobeDurationTimeout (20s),
// which is unexported and therefore cannot be imported. ffprobe only reads the
// container header, so 20s is generous even for a multi-GB audiobook; the
// timeout exists for a wedged mount, not for slow files.
const probeFileTimeout = 20 * time.Second

// ── injection seam for tests ─────────────────────────────────────────────────
//
// ProbeDurationSeconds and LookupFFprobe are package-level functions, so tests
// substitute them through these two vars rather than shelling out to a real
// ffprobe. Keeping the seam this narrow (two vars, default-wired to the real
// implementations) means production takes exactly the code path it always did.
//
// Tests that swap these must NOT run in parallel and must restore via
// t.Cleanup — they are process-global.
var (
	probeDurationSecondsFn = audioutil.ProbeDurationSeconds
	lookupFFprobeFn        = audioutil.LookupFFprobe
)

func (p *Plugin) probeDirectoryBooksDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.probe-directory-books",
		Plugin:      "maintenance",
		DisplayName: "Probe directory-shaped books (tier 2 · re-classify)",
		Description: "Tier-2 escalation over the directory-shaped unlinked books that relink-unlinked-books could only " +
			"send to review. Tier 1 passes nil durations, so ClassifyDir's series guard cannot fire and every multi-file " +
			"folder is parked. This op probes each audio file's real duration with ffprobe and re-runs the SAME " +
			"classifier with that evidence, resolving folders that were un-probed rather than unknowable. Files that " +
			"cannot be probed are excluded, never counted as zero, and a folder with any unprobeable file stays in " +
			"review. Refuses to run when ffprobe is unavailable. DRY RUN unless {\"apply\": true}.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		// Shares tier 1's key: both ops create book_file rows for the same
		// unlinked population, and running them concurrently would let each see
		// a half-repaired library.
		ConcurrencyKey: "maintenance.relink-unlinked-books",
		Cancellable:    true,
		Isolate:        false,
		Timeout:        120 * time.Minute,
		Capabilities:   []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapLibraryWrite},
		Run:            p.runProbeDirectoryBooks,
	}
}

// probeCandidate pairs a tier-1 finding with the folder contents tier 2 measured.
type probeCandidate struct {
	finding linkintegrity.Finding
	// beforeReason is tier 1's verdict text, kept so the report can show what
	// measurement actually changed rather than only the new state.
	beforeReason string
	probes       []linkintegrity.ProbedDuration
	verdict      linkintegrity.DirVerdict
}

func (p *Plugin) runProbeDirectoryBooks(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := probeDirParams{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}

	// ── Phase 0: refuse to run without ffprobe ───────────────────────────────
	//
	// This check comes FIRST — before the store is even fetched — because the
	// only honest outcome of a missing ffprobe is a hard failure. Every probe
	// would fail, every folder would be classified unknown-duration, and the op
	// would report the same "nothing resolved" summary it reports when the
	// library is genuinely healthy. Those two states must not look alike.
	ffprobePath, err := lookupFFprobeFn()
	if err != nil {
		return fmt.Errorf("cannot run: this op measures real durations and has no fallback — %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("using ffprobe at %s", ffprobePath))

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if !params.Apply {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no book_file rows will be created")
	}

	// ── Phase 1: find the directory-shaped books held for review ─────────────
	_ = reporter.UpdateProgress(0, 0, "Phase 1/3: enumerating library…")
	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("ListBookIDs: %w", err)
	}

	// CONCURRENCY (CLAUDE.md mandate): identical shape to tier 1's detection
	// sweep — a serial os.Stat pass over 44,887 books on ZFS is the hotspot that
	// hung dedup.full-scan for 3+ hours on one core. Detection is read-only, so
	// results are collected under a mutex and every write happens later.
	var mu sync.Mutex
	candidates := make([]probeCandidate, 0, 1024)
	var examined, skipped, dirAlreadyLinkable int

	scanErr := registry.RunItems(ctx, reporter, ids, func(_ context.Context, id string) error {
		b, err := store.GetBookByID(id)
		if err != nil || b == nil {
			mu.Lock()
			skipped++
			mu.Unlock()
			return nil // a book that vanished mid-scan is not this op's problem
		}
		files, ferr := store.GetBookFiles(id)
		if ferr != nil {
			mu.Lock()
			skipped++
			mu.Unlock()
			return nil
		}
		mu.Lock()
		examined++
		mu.Unlock()

		if len(files) > 0 {
			return nil // already linked
		}
		path := strings.TrimSpace(b.FilePath)
		if path == "" {
			return nil
		}

		// REUSE, don't reimplement: this is the identical call tier 1 makes, so
		// the two ops cannot disagree about which books are in scope.
		f := classifyUnlinked(b, path)
		if f.Shape != linkintegrity.ShapeDirectory {
			return nil
		}
		if f.Action != linkintegrity.DispositionReview {
			// A directory tier 1 could already resolve (the single-audio-file
			// case). Counted, not re-probed — tier 1 owns it.
			mu.Lock()
			dirAlreadyLinkable++
			mu.Unlock()
			return nil
		}
		mu.Lock()
		candidates = append(candidates, probeCandidate{finding: f, beforeReason: f.Reason})
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   runtime.NumCPU(),
		ProgressTotal: len(ids),
		ErrMode:       registry.ErrModeCollect,
		Label: func(i, total int) string {
			return fmt.Sprintf("Phase 1/3: checking book %d/%d…", i+1, total)
		},
	})
	if scanErr != nil {
		return fmt.Errorf("library scan: %w", scanErr)
	}

	// Deterministic order so two dry runs diff cleanly and a capped apply always
	// picks the same prefix.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].finding.BookID < candidates[j].finding.BookID
	})

	// ── Phase 2: probe every folder, re-classify with real durations ─────────
	//
	// CONCURRENCY: one bounded pool at the FOLDER level, sized to
	// runtime.NumCPU(). ffprobe is a subprocess doing CPU+IO work, so NumCPU is
	// the right width. Files WITHIN a folder are probed sequentially and
	// deliberately: a nested pool would put NumCPU² ffprobe processes on the box
	// at once, which on a 32-core server is a fork bomb dressed as parallelism.
	var doneFolders atomic.Int64
	totalFolders := len(candidates)

	if err := probeAll(ctx, reporter, ffprobePath, candidates, &doneFolders, totalFolders); err != nil {
		return fmt.Errorf("duration probe: %w", err)
	}

	// Fold the new verdict back into each finding. Reason carries the EVIDENCE,
	// per linkintegrity's report contract — a queue of rows all saying
	// "ambiguous" is the failure this whole effort exists to correct.
	// transitionCap bounds the worked example list: enough to sanity-check a dry
	// run by eye, not so many that the log becomes the report.
	const transitionCap = 10
	var nowOneBook, stillReview, withFailedProbes int
	transitions := make([]string, 0, transitionCap)
	for i := range candidates {
		c := &candidates[i]
		c.finding.Reason = c.verdict.Reason
		if c.verdict.OneBook {
			c.finding.Action = linkintegrity.DispositionLink
			nowOneBook++
			// Show BEFORE → AFTER for the folders measurement actually moved.
			// A count alone ("812 resolved") is not reviewable; the owner has to
			// be able to see that the evidence justifies the change before
			// authorising an apply.
			if len(transitions) < transitionCap {
				transitions = append(transitions, fmt.Sprintf("%s [%s]: %q → %q",
					c.finding.BookID, linkintegrity.DirNameOf(c.finding.FilePath),
					c.beforeReason, c.verdict.Reason))
			}
		} else {
			c.finding.Action = linkintegrity.DispositionReview
			stillReview++
		}
		if c.verdict.ProbesFailed > 0 {
			withFailedProbes++
		}
	}

	// ── Phase 3: apply (only with apply=true) ────────────────────────────────
	linked, linkErrs := 0, 0
	if params.Apply {
		_ = reporter.UpdateProgress(totalFolders, totalFolders+1, "Phase 3/3: creating book_file rows…")
		for i := range candidates {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if params.Limit > 0 && linked >= params.Limit {
				break
			}
			c := candidates[i]
			if c.finding.Action != linkintegrity.DispositionLink {
				continue
			}
			n, err := linkProbedFolder(store, c)
			if err != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"link %s (%s): %v", c.finding.BookID, c.finding.FilePath, err))
				linkErrs++
				continue
			}
			if n > 0 {
				linked++
			}
		}
	}

	findings := make([]linkintegrity.Finding, 0, len(candidates))
	for i := range candidates {
		findings = append(findings, candidates[i].finding)
	}

	phase := linkintegrity.PhaseResult{
		Name:     "probe-directory-books",
		Examined: len(candidates),
		Applied:  params.Apply,
		Actioned: linked,
		Errors:   linkErrs,
		// Every examined folder lands in exactly one bucket, so the phase
		// reconciles. A phase that does not reconcile fails the op.
		Skipped:  len(candidates) - linked - linkErrs,
		Findings: findings,
	}

	report := linkintegrity.Report{
		LibraryTotal: len(ids),
		DryRun:       !params.Apply,
		Phases:       []linkintegrity.PhaseResult{phase},
	}

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"RECONCILE: library=%d examined=%d scan-skipped=%d dir-review=%d dir-already-linkable=%d",
		len(ids), examined, skipped, len(candidates), dirAlreadyLinkable))
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"RECLASSIFIED with measured durations: now-one-book=%d still-review=%d folders-with-unprobeable-files=%d",
		nowOneBook, stillReview, withFailedProbes))
	for _, tr := range transitions {
		_ = reporter.Log(slog.LevelInfo, "resolved by measurement: "+tr)
	}
	_ = reporter.Log(slog.LevelInfo, report.Summary())
	_ = reporter.UpdateProgress(totalFolders+1, totalFolders+1, strings.TrimSpace(report.Summary()))

	if bad := report.UnreconciledPhases(); len(bad) > 0 {
		return fmt.Errorf("phase(s) did not reconcile: %s", strings.Join(bad, ", "))
	}
	return nil
}

// probeAll runs the folder-level worker pool, writing each folder's probes and
// verdict back into candidates[i] in place.
//
// Writing through the index (rather than RunItems' by-value item) is why this is
// a separate function: each worker owns exactly one index and no two workers
// ever touch the same element, so the in-place writes need no lock. That
// partitioning is the same discipline CLAUDE.md requires of any parallel apply
// path — disjoint sets, stated explicitly.
func probeAll(
	ctx context.Context,
	reporter sdk.Reporter,
	ffprobePath string,
	candidates []probeCandidate,
	doneFolders *atomic.Int64,
	totalFolders int,
) error {
	idx := make([]int, len(candidates))
	for i := range idx {
		idx[i] = i
	}

	return registry.RunItems(ctx, reporter, idx, func(ctx context.Context, i int) error {
		c := &candidates[i]
		names, subdirs := readDirNames(c.finding.FilePath)
		audio := folderAudioNames(names)

		// 🔴 PROGRESS MUST BE STAMPED MID-FOLDER. The registry watchdog cancels
		// any op whose gap between UpdateProgress calls exceeds ProgressTimeout
		// (default 5 minutes). RunItems only stamps BETWEEN items, and a folder
		// of 23 files each hitting the 20s ffprobe timeout takes ~7.7 minutes —
		// so a single slow folder would get the op killed as stuck. That is not
		// hypothetical: it is exactly how maintenance.dedupe-book-file-rows died
		// at book 19/194.
		//
		// The reported `current` is the completed-FOLDER count, never the file
		// index, so the number stays monotonic while workers finish out of
		// order; the message carries the live per-file detail. SetCurrentItem is
		// NOT sufficient here — it is in-memory/SSE only and never touches the
		// clock the watchdog reads.
		onFile := func(fileIdx int, name string) {
			_ = reporter.UpdateProgress(int(doneFolders.Load()), totalFolders, fmt.Sprintf(
				"Phase 2/3: probing %s — file %d/%d (%s)",
				linkintegrity.DirNameOf(c.finding.FilePath), fileIdx+1, len(audio), name))
		}

		c.probes = probeFolderDurations(ctx, ffprobePath, c.finding.FilePath, audio, onFile)
		c.verdict = linkintegrity.ClassifyDirProbed(names, subdirs, c.probes)

		doneFolders.Add(1)
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   runtime.NumCPU(),
		ProgressTotal: totalFolders,
		ErrMode:       registry.ErrModeCollect,
		// PerItemTimeout is deliberately unset: it would cap a whole FOLDER,
		// and a legitimately large folder would be truncated mid-measurement,
		// producing exactly the partial evidence this op refuses to act on.
		// The bound that matters is per-FILE, inside probeFolderDurations.
		Label: func(i, total int) string {
			return fmt.Sprintf("Phase 2/3: probing folder %d/%d…", i+1, total)
		},
	})
}

// folderAudioNames returns a directory's audio basenames in deterministic order.
//
// Both the probe pass and the apply pass call this, so the file list they act on
// cannot disagree. ClassifyDir applies the same IsAudioFile filter internally;
// building the list here through the same predicate keeps the durations aligned
// with the files the classifier counted.
func folderAudioNames(names []string) []string {
	audio := make([]string, 0, len(names))
	for _, n := range names {
		if linkintegrity.IsAudioFile(n) {
			audio = append(audio, n)
		}
	}
	sort.Strings(audio)
	return audio
}

// probeFolderDurations probes each audio file in one folder, sequentially.
//
// onFile, when non-nil, is called before each probe so the caller can stamp
// progress — see the watchdog note at the call site.
func probeFolderDurations(
	ctx context.Context,
	ffprobePath, dir string,
	audio []string,
	onFile func(fileIdx int, name string),
) []linkintegrity.ProbedDuration {
	out := make([]linkintegrity.ProbedDuration, 0, len(audio))
	for i, name := range audio {
		if onFile != nil {
			onFile(i, name)
		}
		if ctx.Err() != nil {
			// Cancelled: record the remainder as UNMEASURED rather than
			// dropping them. Dropping would shrink the probe list and make a
			// truncated run look like a fully-measured one.
			out = append(out, linkintegrity.ProbedDuration{Name: name})
			continue
		}
		out = append(out, probeOne(ctx, ffprobePath, filepath.Join(dir, name), name))
	}
	return out
}

// probeOne measures a single file. It is the ONE place where an ffprobe result
// becomes evidence, so it is the one place the OK invariant has to hold.
func probeOne(ctx context.Context, ffprobePath, fullPath, name string) linkintegrity.ProbedDuration {
	fctx, cancel := context.WithTimeout(ctx, probeFileTimeout)
	defer cancel()

	secs, err := probeDurationSecondsFn(fctx, ffprobePath, fullPath)
	if err != nil {
		return linkintegrity.ProbedDuration{Name: name} // OK stays false
	}
	// 🔴 A SUCCESSFUL EXIT REPORTING ZERO IS STILL A FAILED MEASUREMENT.
	// ProbeDurationSeconds does not validate its result ("callers apply their
	// own validity rules") and ffprobe can exit 0 with a zero or negative
	// duration for a truncated or header-only container. Admitting that value
	// would hand the series guard a zero — the precise failure this op exists
	// to avoid — so it is treated as unmeasured.
	if secs <= 0 {
		return linkintegrity.ProbedDuration{Name: name}
	}
	// Round rather than truncate, matching duration_reextract.go's handling of
	// the same float→int conversion.
	return linkintegrity.ProbedDuration{Name: name, Sec: int(secs + 0.5), OK: true}
}

// linkProbedFolder creates the book_file rows for a folder that measurement has
// confirmed to be one book, and returns how many it created.
//
// 🔴 NO UpdateBook CALL. Like relinkOne, this touches only book_file rows via
// the additive CreateBookFile. The Book row is never rewritten, so the
// write-back wipe class (AcoustIDFingerprint / Author / Series cleared by a bare
// UpdateBook) is structurally unreachable from here.
//
// Unlike relinkOne — which must seed 0 for directory members because tier 1 has
// no per-file durations — this path writes the MEASURED duration onto each row.
// That is the point of having probed: a zero-duration book_file row leaves the
// regroup series guard just as inert as no row at all.
func linkProbedFolder(store database.Store, c probeCandidate) (int, error) {
	b, err := store.GetBookByID(c.finding.BookID)
	if err != nil {
		return 0, fmt.Errorf("refetch book: %w", err)
	}
	if b == nil {
		return 0, nil // vanished since detection — not an error
	}
	// Re-check under the write path: a concurrent import may have linked this
	// book between detection and apply. Never create a second row for a file
	// that is already owned.
	existing, err := store.GetBookFiles(c.finding.BookID)
	if err != nil {
		return 0, fmt.Errorf("recheck files: %w", err)
	}
	if len(existing) > 0 {
		return 0, nil
	}

	// Re-read the directory rather than trusting the probe-time snapshot: the
	// folder may have changed, and a row must never be created for a file that
	// is no longer there.
	names, _ := readDirNames(c.finding.FilePath)
	audio := folderAudioNames(names)
	if len(audio) == 0 {
		return 0, nil
	}

	// Index the measured durations by name so a folder that changed between
	// probe and apply still pairs each row with ITS OWN measurement, never a
	// neighbour's by position.
	bySec := make(map[string]int, len(c.probes))
	for _, p := range c.probes {
		if p.OK && p.Sec > 0 {
			bySec[p.Name] = p.Sec
		}
	}

	created := 0
	for i, n := range audio {
		// A name with no measurement seeds 0 — the existing convention for
		// "unknown", which maintenance.duration-backfill later fills in. For a
		// multi-file folder this is unreachable: the coverage guard means a
		// folder with any unprobeable file never reaches OneBook. It is
		// reachable only for the single-file folder, whose verdict never
		// depended on duration.
		if err := createBookFileFor(store, b, filepath.Join(c.finding.FilePath, n), bySec[n], i+1); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
