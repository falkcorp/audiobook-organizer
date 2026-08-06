// file: internal/plugins/maintenance/relink_unlinked.go
// version: 1.0.0
// guid: c17b493a-8d02-4f65-b9e1-604a8f2371cd
// last-edited: 2026-08-05

// Package maintenance — op maintenance.relink-unlinked-books.
//
// Detects and repairs the library's largest single integrity gap: a Book row
// that exists, whose FilePath RESOLVES on disk, but which owns ZERO book_file
// rows. A whole-library survey on 2026-08-05 found 17,149 of 44,886 books
// (38.2%) in this state.
//
// WHY NO EXISTING OP CATCHES THIS. maintenance.reconcile-scan flags a book only
// when os.Stat on its path FAILS (reconcile.go). These all stat fine, so it
// walks past every one of them and reports the library healthy.
// maintenance.orphan-book-files-cleanup handles the opposite direction
// (book_file rows with no parent Book), and file-integrity-check needs rows that
// by definition do not exist here.
//
// 🔴 WHY THIS MUST RUN BEFORE regroup-shattered-ai. That producer derives each
// candidate's DurationSec by SUMMING its book_file rows. Its series-guard
// membersAreBookLength — the check that stops distinct novels being merged into
// one book — cannot fire when that sum is zero. 97.5% of the review queue was
// made of zero-file books, so the guard was inert and the queue was built on
// blank evidence. Relinking restores the signal the classifier depends on.
//
// UNLINKED, NOT ORPHANED. Of 4,655 such paths checked on disk, 4,321 resolved to
// a real file, 332 to a directory, and 2 were missing. The remedy is therefore
// to CREATE the missing book_file row, never to delete the book.
//
// DRY RUN BY DEFAULT (owner decision D3). Nothing is written unless the caller
// passes {"apply": true}.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// relinkParams configures the op.
type relinkParams struct {
	// Apply gates every write. False (the default) makes this a pure detector.
	Apply bool `json:"apply"`
	// Limit caps how many books are REPAIRED this run (0 = no cap). Detection
	// always covers the whole library so the reported counts stay honest — a
	// capped run must never look like a clean bill of health.
	Limit int `json:"limit"`
	// IncludeDirectories opts into repairing the directory-shaped cases that
	// linkintegrity.ClassifyDir judged unambiguous. Off by default: the file
	// shape is 93.5% of the population and carries no over-merge risk, so the
	// two are staged separately.
	IncludeDirectories bool `json:"includeDirectories"`
}

func (p *Plugin) relinkUnlinkedBooksDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.relink-unlinked-books",
		Plugin:      "maintenance",
		DisplayName: "Relink unlinked books (detect · repair)",
		Description: "Finds Book rows that own ZERO book_file rows but whose file_path still resolves on disk, " +
			"and creates the missing book_file row. These books are unlinked, not orphaned — they are unplayable " +
			"and invisible to every duration-dependent check until relinked. reconcile-scan cannot see them because " +
			"it only flags books whose path FAILS to stat. DRY RUN unless {\"apply\": true}.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.relink-unlinked-books",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapLibraryWrite},
		Run:             p.runRelinkUnlinkedBooks,
	}
}

func (p *Plugin) runRelinkUnlinkedBooks(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := relinkParams{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	_ = reporter.UpdateProgress(0, 0, "Phase 1/2: enumerating library…")
	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("ListBookIDs: %w", err)
	}

	// CONCURRENCY (CLAUDE.md mandate): a serial os.Stat sweep over 44,886 books on
	// network/ZFS storage is precisely the hotspot shape that hung dedup.full-scan
	// for 3+ hours on one core (2026-07-05 audit). Detection is read-only, so fan
	// it across a bounded pool; the per-book results are collected under a mutex
	// and every WRITE happens single-threaded afterwards, so two workers can never
	// touch the same row.
	var mu sync.Mutex
	findings := make([]linkintegrity.Finding, 0, 1024)
	var examined, skipped int

	scanErr := registry.RunItems(ctx, reporter, ids, func(ctx context.Context, id string) error {
		b, err := store.GetBookByID(id)
		if err != nil || b == nil {
			mu.Lock()
			skipped++
			mu.Unlock()
			return nil // non-fatal: a book that vanished mid-scan
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
			return nil // healthy — already linked
		}
		path := strings.TrimSpace(b.FilePath)
		if path == "" {
			return nil // nothing to resolve; not this op's problem
		}

		f := classifyUnlinked(b, path)
		mu.Lock()
		findings = append(findings, f)
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   runtime.NumCPU(),
		ProgressTotal: len(ids),
		ErrMode:       registry.ErrModeCollect,
		Label: func(i, total int) string {
			return fmt.Sprintf("Phase 1/2: checking book %d/%d…", i+1, total)
		},
	})
	if scanErr != nil {
		return fmt.Errorf("library scan: %w", scanErr)
	}

	// Deterministic order so two dry runs diff cleanly and a capped repair run
	// always picks the same prefix.
	sortFindingsByID(findings)

	phase := linkintegrity.PhaseResult{
		Name:     "unlinked-books",
		Examined: len(findings),
		Applied:  params.Apply,
		Findings: findings,
	}

	// ── Phase 2: repair (only with apply) ───────────────────────────────────
	repaired, repairErrs := 0, 0
	if params.Apply {
		_ = reporter.UpdateProgress(len(ids), len(ids)+1, "Phase 2/2: creating book_file rows…")
		for i := range findings {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if params.Limit > 0 && repaired >= params.Limit {
				break
			}
			f := findings[i]
			if f.Action != linkintegrity.DispositionLink {
				continue
			}
			if f.Shape == linkintegrity.ShapeDirectory && !params.IncludeDirectories {
				continue
			}
			n, err := relinkOne(store, f)
			if err != nil {
				_ = reporter.Log(slog.LevelWarn,
					fmt.Sprintf("relink %s (%s): %v", f.BookID, f.FilePath, err))
				repairErrs++
				continue
			}
			if n > 0 {
				repaired++
			}
		}
	}

	// Every finding is accounted for: linked, or left for review/report.
	phase.Actioned = repaired
	phase.Errors = repairErrs
	phase.Skipped = len(findings) - repaired - repairErrs

	report := linkintegrity.Report{
		LibraryTotal: len(ids),
		DryRun:       !params.Apply,
		Phases:       []linkintegrity.PhaseResult{phase},
	}

	// RECONCILE: tie every book back to the library total, mirroring the existing
	// producers. A scan that silently lost books must be visible, not inferred.
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"RECONCILE: library=%d examined=%d scan-skipped=%d unlinked=%d",
		len(ids), examined, skipped, len(findings)))
	_ = reporter.Log(slog.LevelInfo, report.Summary())
	_ = reporter.UpdateProgress(len(ids)+1, len(ids)+1, strings.TrimSpace(report.Summary()))

	if bad := report.UnreconciledPhases(); len(bad) > 0 {
		return fmt.Errorf("phase(s) did not reconcile: %s", strings.Join(bad, ", "))
	}
	return nil
}

// classifyUnlinked resolves one unlinked book's FilePath on disk and decides what
// should happen to it. Read-only.
func classifyUnlinked(b *database.Book, path string) linkintegrity.Finding {
	f := linkintegrity.Finding{
		BookID:   b.ID,
		Title:    b.Title,
		FilePath: path,
	}
	st, err := os.Stat(path)
	switch {
	case err != nil:
		// Owner decision D4: report only. An offline mount is indistinguishable
		// from deleted audio, and reconcile-scan already owns this case.
		f.Shape = linkintegrity.ShapeMissing
		f.Action = linkintegrity.DispositionReportOnly
		f.Reason = "file_path does not resolve on disk — could be deleted audio or an offline mount; reconcile-scan owns this case"
		return f

	case st.IsDir():
		f.Shape = linkintegrity.ShapeDirectory
		names, subdirs := readDirNames(path)
		// Durations of the folder's files are unknown without probing each one,
		// which a whole-library scan cannot afford. ClassifyDir treats absent
		// durations as "refuse to auto-link", which is the safe direction: a
		// multi-file folder goes to review rather than being guessed at.
		v := linkintegrity.ClassifyDir(names, subdirs, nil)
		f.AudioFileCount = v.AudioCount
		f.SubdirCount = subdirs
		f.Reason = v.Reason
		if v.OneBook {
			f.Action = linkintegrity.DispositionLink
		} else {
			f.Action = linkintegrity.DispositionReview
		}
		return f

	default:
		f.Shape = linkintegrity.ShapeFile
		f.Action = linkintegrity.DispositionLink
		f.Reason = fmt.Sprintf("file_path resolves to a %d-byte audio file but the book owns no book_file row", st.Size())
		return f
	}
}

// readDirNames returns the immediate entry names of a directory plus how many of
// them are themselves directories. Errors degrade to empty, which ClassifyDir
// reads as "no audio" and therefore routes to review.
func readDirNames(dir string) (names []string, subdirs int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	names = make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		if e.IsDir() {
			subdirs++
		}
	}
	return names, subdirs
}

// relinkOne creates the missing book_file row(s) for one finding and returns how
// many it created.
//
// 🔴 DATA-LOSS SAFETY. This op NEVER touches the Book row. This repo's dominant
// incident class is write-back wipes (a bare UpdateBook clearing
// AcoustIDFingerprint / Author / Series), and the defence here is structural:
// there is no UpdateBook call in this file at all. The only write is
// CreateBookFile, which is additive.
//
// Duration is seeded from the Book's own snapshot rather than probed. For the
// file shape the book owns exactly one file, so Book.Duration IS that file's
// runtime — and seeding it is the entire point, since a zero duration leaves the
// regroup series-guard just as inert as before. It is passed through
// NormalizeDurationSec because ~1.9% of historical rows stored milliseconds, and
// a 1000x-inflated value would read as book-length to that guard.
func relinkOne(store database.Store, f linkintegrity.Finding) (int, error) {
	b, err := store.GetBookByID(f.BookID)
	if err != nil {
		return 0, fmt.Errorf("refetch book: %w", err)
	}
	if b == nil {
		return 0, nil // vanished since detection — not an error
	}
	// Re-check under the write path: a concurrent import may have linked it
	// between detection and repair. Never create a second row for a file that is
	// already owned.
	existing, err := store.GetBookFiles(f.BookID)
	if err != nil {
		return 0, fmt.Errorf("recheck files: %w", err)
	}
	if len(existing) > 0 {
		return 0, nil
	}

	switch f.Shape {
	case linkintegrity.ShapeFile:
		// Book.Duration is a nullable snapshot; a nil one simply seeds 0 and lets
		// maintenance.duration-backfill fill it in later.
		dur := 0
		if b.Duration != nil {
			dur = *b.Duration
		}
		if err := createBookFileFor(store, b, f.FilePath, dur, 1); err != nil {
			return 0, err
		}
		return 1, nil

	case linkintegrity.ShapeDirectory:
		names, _ := readDirNames(f.FilePath)
		audio := make([]string, 0, len(names))
		for _, n := range names {
			if linkintegrity.IsAudioFile(n) {
				audio = append(audio, n)
			}
		}
		if len(audio) == 0 {
			return 0, nil
		}
		sort.Strings(audio)
		created := 0
		for i, n := range audio {
			// Per-file duration is unknown here; leave it 0 and let
			// maintenance.duration-backfill fill it. Seeding the BOOK's total
			// onto every track would be actively wrong (it would multiply the
			// runtime by the track count).
			if err := createBookFileFor(store, b, filepath.Join(f.FilePath, n), 0, i+1); err != nil {
				return created, err
			}
			created++
		}
		return created, nil
	}
	return 0, nil
}

// createBookFileFor inserts one book_file row. Fields are limited to what can be
// known without probing the file; everything else is left for the existing
// backfill ops, which are idempotent.
func createBookFileFor(store database.Store, b *database.Book, path string, durationSec, track int) error {
	var size int64
	if st, err := os.Stat(path); err == nil {
		size = st.Size()
	}
	bf := &database.BookFile{
		BookID:           b.ID,
		FilePath:         path,
		OriginalFilename: filepath.Base(path),
		TrackNumber:      track,
		Duration:         database.NormalizeDurationSec(size, durationSec),
		FileSize:         size,
		Format:           strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
	}
	if err := store.CreateBookFile(bf); err != nil {
		return fmt.Errorf("CreateBookFile %q: %w", path, err)
	}
	return nil
}

// sortFindingsByID keeps output deterministic across runs.
func sortFindingsByID(f []linkintegrity.Finding) {
	sort.SliceStable(f, func(i, j int) bool { return f[i].BookID < f[j].BookID })
}
