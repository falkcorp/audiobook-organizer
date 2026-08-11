// file: internal/plugins/maintenance/intro_migrate_single_file.go
// version: 1.2.0
// guid: 6b0d94e7-1c58-4a32-bf07-9e5d2a17c630
// last-edited: 2026-08-11

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Tier 0 of the per-file intro backfill: migrate the book-level transcript onto
// the file it came from, for books where "the file it came from" is knowable.
//
// Measured on the full library 2026-08-07 (all 44,875 books):
//
//	0 book_file rows   1,122   2.5%          0 files  <- unlinked, cannot migrate
//	1 file            33,780  75.3%     33,780 files  <- THIS TIER, zero GPU
//	2-5 files          2,884   6.4%      7,829 files
//	6-20 files         2,775   6.2%     34,601 files
//	21+ files          4,314   9.6%    228,681 files
//	                  ------                 -------
//	                  44,875                 304,891
//
// Three quarters of the library migrates here for free. The remaining tiers pay
// GPU; this one pays a Pebble write.
//
// 🔴 WHY SINGLE-FILE ONLY — this is a correctness constraint, not a scope cut.
//
// The book-level transcript does not record WHICH file produced it. The
// transcribe op retries a silent book against the SECOND audio file
// (intro_transcribe.go retry2), so for a multi-file book the stored transcript
// may have come from file 2 — and nothing distinguishes that from file 1.
// Copying it onto file 1 would assert a provenance the data cannot support.
//
// For a single-file book the ambiguity cannot arise: nthAudioFile returns ""
// once n >= len(audio) (intro_transcribe.go:64) and retry2 skips on an empty
// source (:644), so retry2 provably never fired. The transcript came from the
// one file, possibly via retry1's longer clip — same file either way.
//
// Books with ZERO book_file rows are skipped, not "migrated with no effect".
// Measured 2026-08-07 (todo.d dry-run arithmetic: 1,415 no-transcript − 147
// single-file − 145 multi-file ≈ 1,123): those ~1,122 books have NEITHER file
// rows NOR a book-level transcript. Relinking alone will not hand them a
// transcript — they need real GPU transcription after the relink. An earlier
// revision of this comment claimed they were "unlinked, not un-transcribed";
// the dry-run disproved that. migrateOneBook checks row shape BEFORE the
// transcript so they are reported as skip_no_book_file_rows, the more
// actionable of the two reasons.

// introMigrateParams controls the tier-0 migration.
type introMigrateParams struct {
	// DryRun reports what WOULD be written without writing it. Defaults to
	// TRUE: this op touches ~33,780 rows, and the counts per skip-reason are
	// the thing worth reading before any of that happens.
	DryRun *bool `json:"dry_run,omitempty"`
	// LastBookID resumes past a prior run's checkpoint.
	LastBookID string `json:"last_book_id,omitempty"`
	// Overwrite re-copies onto files that already carry a transcript. Off by
	// default so re-runs are cheap and idempotent.
	Overwrite *bool `json:"overwrite,omitempty"`
}

// migrate outcome reasons — every book lands in exactly one, and the totals are
// reported. A book that is skipped must say WHY; "processed 44,875, wrote 33,780"
// with no account of the other 11,095 is the shape of report that hides a bug.
const (
	migrateWrote          = "wrote"
	migrateDryRun         = "would_write"
	migrateNoRows         = "skip_no_book_file_rows"
	migrateMultiFile      = "skip_multi_file_provenance"
	migrateNoTranscript   = "skip_book_has_no_transcript"
	migrateAlreadyPresent = "skip_file_already_has_transcript"
	migrateNoAudio        = "skip_no_audio_file"
	migrateWriteFailed    = "write_failed"
)

const introMigratePageSize = 500

func (p *Plugin) introMigrateSingleFileDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.intro-migrate-single-file",
		Plugin:          "maintenance",
		DisplayName:     "Migrate intro transcripts to single-file books",
		Description:     "Tier 0 of the per-file intro backfill. Copies the book-level intro transcript onto the book's one BookFile row, for the ~33,780 single-file books (75.3% of the library). Zero GPU: it is a copy, not a transcription. Multi-file books are deliberately refused because the book-level transcript does not record which file produced it and the silence-retry path may have used file 2. Defaults to dry_run=true; pass dry_run=false to write.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.intro-migrate-single-file",
		// UpdateBookFile whole-row write-back on BookFile rows (reads Books).
		Writes:      []sdk.Resource{sdk.ResBookFiles},
		Reads:       []sdk.Resource{sdk.ResBooks},
		Cancellable: true,
		Isolate:     false,
		Timeout:     2 * time.Hour,
		// Pure DB work, no Whisper: pages complete in seconds, so the default
		// 5-minute no-progress watchdog is generous already. Left explicit so a
		// future change that adds I/O here has to think about it.
		ProgressTimeout: 5 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runIntroMigrateSingleFile,
	}
}

func (p *Plugin) runIntroMigrateSingleFile(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params introMigrateParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("maintenance.intro-migrate-single-file: decode params: %w", err)
		}
	}
	dryRun := params.DryRun == nil || *params.DryRun
	overwrite := params.Overwrite != nil && *params.Overwrite

	log := reporter.Logger()

	allIDs, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	startIdx := 0
	if params.LastBookID != "" {
		for i, id := range allIDs {
			if id == params.LastBookID {
				startIdx = i + 1
				break
			}
		}
	}
	total := len(allIDs)
	log.Info("intro-migrate-single-file: starting",
		"dry_run", dryRun, "overwrite", overwrite,
		"total_books", total, "start_index", startIdx)

	if startIdx >= total {
		_ = reporter.UpdateProgress(1, 1, "Done — nothing to migrate")
		return nil
	}

	pages := chunkIDs(allIDs[startIdx:], introMigratePageSize)

	var mu sync.Mutex
	counts := map[string]int{}
	record := func(reason string, n int) {
		mu.Lock()
		counts[reason] += n
		mu.Unlock()
	}

	// Pages partition the library by book ID into DISJOINT sets, so two workers
	// can never touch the same book_file row. That matters because UpdateBookFile
	// is a read-modify-write with no store-level lock: concurrent writers to the
	// SAME row could interleave. Disjoint partitioning is what makes this safe,
	// per the concurrency rules in CLAUDE.md — do not replace the page split with
	// anything that lets two workers share a book.
	err = registry.RunItems(ctx, reporter, pages, func(ctx context.Context, ids []string) error {
		for _, id := range ids {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			reason := p.migrateOneBook(store, log, id, dryRun, overwrite)
			record(reason, 1)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   8,
		ProgressTotal: len(pages),
		Label: func(i, t int) string {
			return fmt.Sprintf("Page %d/%d", i+1, t)
		},
	})

	mu.Lock()
	snapshot := make(map[string]int, len(counts))
	for k, v := range counts {
		snapshot[k] = v
	}
	mu.Unlock()

	// Report EVERY bucket, including the zeros: a reader must be able to add the
	// numbers up to the book count and find nothing unaccounted for.
	log.Info("intro-migrate-single-file: complete",
		"dry_run", dryRun,
		"wrote", snapshot[migrateWrote],
		"would_write", snapshot[migrateDryRun],
		"skip_no_book_file_rows", snapshot[migrateNoRows],
		"skip_multi_file_provenance", snapshot[migrateMultiFile],
		"skip_book_has_no_transcript", snapshot[migrateNoTranscript],
		"skip_file_already_has_transcript", snapshot[migrateAlreadyPresent],
		"skip_no_audio_file", snapshot[migrateNoAudio],
		"write_failed", snapshot[migrateWriteFailed],
		"total_books", total)

	verb := "Would migrate"
	n := snapshot[migrateDryRun]
	if !dryRun {
		verb = "Migrated"
		n = snapshot[migrateWrote]
	}
	_ = reporter.UpdateProgress(1, 1, fmt.Sprintf(
		"%s %d single-file books — skipped: %d unlinked, %d multi-file, %d no transcript, %d already done (of %d)",
		verb, n, snapshot[migrateNoRows], snapshot[migrateMultiFile],
		snapshot[migrateNoTranscript], snapshot[migrateAlreadyPresent], total))
	if err != nil {
		return err
	}
	return nil
}

// migrateOneBook evaluates and (unless dry-run) performs the tier-0 copy for a
// single book, returning the outcome reason.
func (p *Plugin) migrateOneBook(store database.Store, log interface {
	Info(string, ...any)
	Warn(string, ...any)
}, bookID string, dryRun, overwrite bool) string {
	b, err := store.GetBookByID(bookID)
	if err != nil || b == nil {
		return migrateNoRows
	}

	// GetBookFiles is the Pebble-direct read: it returns FULL rows with
	// IntroTranscription intact. Reading through a memdb path here would hand
	// back a slim row whose transcript is nil, and writing that back would blank
	// the very field this op exists to populate.
	//
	// Row shape is checked BEFORE the transcript on purpose: the ~1,122 zero-row
	// books also lack a transcript, and when both reasons apply the row shape is
	// the actionable one (relink, then transcribe). The original ordering put
	// them all in skip_book_has_no_transcript, which reported
	// skip_no_book_file_rows as 0 and hid the unlinked population entirely.
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		return migrateNoRows
	}
	if len(files) == 0 {
		return migrateNoRows
	}
	if b.IntroTranscription == nil || strings.TrimSpace(*b.IntroTranscription) == "" {
		return migrateNoTranscript
	}

	reason, target := classifyMigrateCandidate(*b, files, overwrite)
	if reason != "" {
		return reason
	}
	if dryRun {
		return migrateDryRun
	}

	applyBookIntroFieldsToFile(target, *b)
	if err := store.UpdateBookFile(target.ID, target); err != nil {
		log.Warn("intro-migrate: update failed", "book_id", bookID, "file_id", target.ID, "err", err)
		return migrateWriteFailed
	}
	return migrateWrote
}

// classifyMigrateCandidate decides whether a book is eligible for the tier-0
// copy, and if so which file receives it. It is pure — no store, no clock — so
// the eligibility rules (which are the whole correctness story of this tier)
// are testable directly.
//
// Returns ("", target) when eligible, or (reason, nil) when not.
func classifyMigrateCandidate(b database.Book, files []database.BookFile, overwrite bool) (string, *database.BookFile) {
	var audio []database.BookFile
	for _, f := range files {
		if f.FilePath != "" && audioExtSet[strings.ToLower(filepath.Ext(f.FilePath))] {
			audio = append(audio, f)
		}
	}
	switch {
	case len(audio) == 0:
		return migrateNoAudio, nil
	case len(audio) > 1:
		// See the provenance note at the top of this file. Refusing here is the
		// POINT of the tier, not a limitation of it: the book-level transcript
		// does not record which file produced it, and retry2 may have used file 2.
		return migrateMultiFile, nil
	}

	target := audio[0]
	if !overwrite && target.IntroTranscription != nil && strings.TrimSpace(*target.IntroTranscription) != "" {
		return migrateAlreadyPresent, nil
	}
	return "", &target
}

// introMigratedFields is the exact set of BookFile fields this migration is
// permitted to touch. It is declared as data, not just as code, so a test can
// assert reflectively that applyBookIntroFieldsToFile modifies these and
// NOTHING else — the guard against a full-row write-back quietly clobbering an
// unrelated column, which is this repo's dominant data-loss shape.
var introMigratedFields = []string{
	"IntroTranscription",
	"TranscribedTitle",
	"TranscribedAuthor",
	"TranscribedNarrator",
	"TranscribedTranslator",
	"TranscribedCoverArtist",
	"IntroTranscribedAt",
	"TranscribeStatus",
	"TranscribeError",
	"TranscribeAttemptedAt",
}

// applyBookIntroFieldsToFile copies the ten-field intro-transcription group
// from a Book onto one of its BookFiles. It is the ONLY place this migration
// mutates a BookFile, so the blast radius is one small function with a
// reflective test pinning it.
//
// dst must be a FULL Pebble row (from GetBookFiles), never a memdb projection:
// the caller writes dst back wholesale, so every field it does not set must
// already hold the stored value.
func applyBookIntroFieldsToFile(dst *database.BookFile, src database.Book) {
	dst.IntroTranscription = src.IntroTranscription
	dst.TranscribedTitle = src.TranscribedTitle
	dst.TranscribedAuthor = src.TranscribedAuthor
	dst.TranscribedNarrator = src.TranscribedNarrator
	dst.TranscribedTranslator = src.TranscribedTranslator
	dst.TranscribedCoverArtist = src.TranscribedCoverArtist
	dst.IntroTranscribedAt = src.IntroTranscribedAt
	dst.TranscribeStatus = src.TranscribeStatus
	dst.TranscribeError = src.TranscribeError
	dst.TranscribeAttemptedAt = src.TranscribeAttemptedAt
}
