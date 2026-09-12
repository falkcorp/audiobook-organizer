// file: internal/plugins/maintenance/tag_backfill.go
// version: 2.0.0
// guid: 1f6b3d28-9a47-4c50-8e21-7b0c4a9d6e35
// last-edited: 2026-09-12

// Package maintenance — op maintenance.tag-backfill.
//
// Lossless tag capture for EXISTING BookFiles. New imports now record every tag
// (Metadata.AllTags -> BookFile.RawTags) and the real track/disc position, but rows
// imported before that change carry nil RawTags and often a positional-only
// TrackNumber. This op reads each file's tags and backfills RawTags (+ Title when
// empty) so the full provenance is recoverable everywhere — the "eventually for
// everything" half of the lossless-capture design. Dry-run by default; reads files
// only and writes DB rows only.
//
// Track/disc guard: a row's positional TrackNumber is usually right, and many
// audiobook rips tag every file "1", "1/1", or repeat numbers. Tag track/disc
// numbers therefore replace the stored ones only when they are distinct across
// EVERY file of the book (see judgeTagPositions); otherwise the op fills RawTags
// (and an empty Title) and leaves the order alone.

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// tagBackfillWriteBatchSize is how many rows accumulate before one
// BatchUpsertBookFiles call. A var (not a const) so tests can shrink it to
// exercise the multi-batch path.
var tagBackfillWriteBatchSize = 1000

type tagBackfillParams struct {
	DryRun bool `json:"dryRun"`
	// Force re-reads tags even for files that already have RawTags (e.g. after a
	// capture-logic change). Default false: only files missing RawTags are touched.
	Force bool `json:"force"`
	// Limit caps the number of files processed in one run (0 = no cap). Useful for
	// a bounded first pass over a large library.
	Limit int `json:"limit"`
}

// tagBackfillBook is one unit of work: a book and the rows of it (from the
// examined slice) that need a tag backfill.
type tagBackfillBook struct {
	bookID string
	rows   []database.BookFileCore
}

// tagVerdict is the per-book decision on whether tag track/disc numbers may
// replace the stored ones.
type tagVerdict int

const (
	tagTakePositions   tagVerdict = iota // tag numbers distinct across the whole book
	tagRefuseDuplicate                   // two files share a (disc, track) pair
	tagRefuseMissing                     // some file has no known tag track number
)

type tagPosition struct{ disc, track int }

// judgeTagPositions decides whether a book's tag track/disc numbers may be
// trusted. It looks at EVERY file of the book (full, from GetBookFiles), not just
// the rows being fixed: a file freshly read this run contributes its tag reading
// (readings[ID]); any other file contributes the position parsed from its stored
// RawTags. A file with no known tag track number — missing on disk, unreadable,
// untagged, never captured, or outside a Limit window — makes the book "not
// distinct" and is refused. This fails closed on purpose: refusing costs only the
// track numbers (RawTags still land), while a wrong accept scrambles chapter order.
//
// Positions are compared as (disc, track) pairs, absent disc = 0, so a multi-disc
// book with track 1 on each disc passes and two files both at (0, 1) do not. A
// single-file book passes iff its tag carries a track number. Rows with no BookID
// have no book to be ordered within and are refused as "missing".
func judgeTagPositions(bookID string, full []database.BookFile, readings map[string]metadata.Metadata) (tagVerdict, string) {
	if bookID == "" {
		return tagRefuseMissing, "no book id"
	}
	seen := make(map[tagPosition]string, len(full))
	for i := range full {
		var pos tagPosition
		if m, ok := readings[full[i].ID]; ok {
			pos = tagPosition{disc: m.DiscNumber, track: m.TrackNumber}
		} else {
			track, _, disc, _ := metadata.TrackDiscFromTags(full[i].RawTags)
			pos = tagPosition{disc: disc, track: track}
		}
		if pos.track <= 0 {
			return tagRefuseMissing, fmt.Sprintf("file %s has no tag track", full[i].ID)
		}
		if other, dup := seen[pos]; dup {
			return tagRefuseDuplicate, fmt.Sprintf("disc %d track %d on %s and %s", pos.disc, pos.track, other, full[i].ID)
		}
		seen[pos] = full[i].ID
	}
	return tagTakePositions, ""
}

func (p *Plugin) tagBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.tag-backfill",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Backfill lossless file tags (RawTags + track/disc)",
		Description: "Reads each BookFile's audio tags and backfills the lossless RawTags map (and an " +
			"empty title) for rows imported before lossless capture. Tag track/disc numbers replace the " +
			"stored ones only when they are distinct across every file of the book. Files missing on " +
			"disk are skipped. Default dry-run previews counts; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.tag-backfill",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runTagBackfill,
	}
}

func (p *Plugin) runTagBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := tagBackfillParams{DryRun: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	total := len(files)

	// Limit caps how many files are EXAMINED: only the first Limit files (in
	// GetAllBookFilesCore order) are candidates at all. It never narrows the
	// distinctness denominator — each book is still judged against its full
	// GetBookFiles list, so a book cut by the window is refused rather than
	// judged on a partial view.
	items := files
	if params.Limit > 0 && params.Limit < total {
		items = files[:params.Limit]
	}
	examined := len(items)

	// Group candidate rows by book so a whole book is judged before any of its
	// rows is written. The skip checks here need no I/O.
	var books []tagBackfillBook
	bookIdx := make(map[string]int)
	for _, f := range items {
		if len(f.RawTags) > 0 && !params.Force {
			continue
		}
		if f.FilePath == "" {
			continue
		}
		i, ok := bookIdx[f.BookID]
		if !ok {
			i = len(books)
			bookIdx[f.BookID] = i
			books = append(books, tagBackfillBook{bookID: f.BookID})
		}
		books[i].rows = append(books[i].rows, f)
	}
	_ = reporter.UpdateProgress(0, len(books), fmt.Sprintf("Reading tags: %d files examined, %d books with rows to check…", examined, len(books)))

	// Counters and examples: written by every worker and read by Label (which
	// run_items.go calls inside worker goroutines), so all access holds mu.
	var (
		mu                         sync.Mutex
		needed, missing, readErr   int
		tookTracks, rawOnly        int
		refusedDup, refusedMissing int
		examples                   = make([]string, 0, 5)
		refusedExamples            = make([]string, 0, 5)
	)

	// Bounded writes: rows accumulate in pending and are flushed in batches of
	// tagBackfillWriteBatchSize as books finish, so memory holds at most one
	// batch plus one book instead of every hydrated row in the library. wmu
	// guards pending/written AND serializes the BatchUpsertBookFiles call, so the
	// op never depends on that call being goroutine-safe. It is separate from mu
	// so a batch write never stalls counter updates or progress labels.
	var (
		wmu     sync.Mutex
		pending []*database.BookFile
		written int
	)
	flushLocked := func(all bool) error {
		size := tagBackfillWriteBatchSize
		if size < 1 {
			size = 1
		}
		for len(pending) >= size || (all && len(pending) > 0) {
			n := min(size, len(pending))
			if err := store.BatchUpsertBookFiles(pending[:n]); err != nil {
				return fmt.Errorf("batch write after %d rows: %w", written, err)
			}
			written += n
			pending = append([]*database.BookFile(nil), pending[n:]...)
		}
		return nil
	}
	enqueue := func(rows []*database.BookFile) error {
		wmu.Lock()
		defer wmu.Unlock()
		pending = append(pending, rows...)
		return flushLocked(false)
	}

	// Work is partitioned by BookID: every candidate row of a book is in exactly
	// one tagBackfillBook, and RunItems hands each item to exactly one worker, so
	// two workers can never judge or write the same book (or the same row).
	// Per-book work is I/O bound (stat + tag read per file), so the pool is sized
	// like path_repair_resolver.go: NumCPU()*4.
	err = registry.RunItems(ctx, reporter, books, func(ctx context.Context, b tagBackfillBook) error {
		readings := make(map[string]metadata.Metadata, len(b.rows))
		var bMissing, bReadErr int
		for _, f := range b.rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, statErr := os.Stat(f.FilePath); statErr != nil {
				bMissing++
				continue
			}
			meta, merr := metadata.ExtractMetadata(f.FilePath, nil)
			if merr != nil {
				bReadErr++
				continue
			}
			if len(meta.AllTags) == 0 {
				continue // nothing capturable
			}
			readings[f.ID] = meta
		}
		if len(readings) == 0 {
			mu.Lock()
			missing += bMissing
			readErr += bReadErr
			mu.Unlock()
			return nil
		}

		// Hydrate the full rows once per book and mutate/write THOSE — never a
		// hand-built BookFile{} from Core fields, which would wipe the stored
		// fingerprint. BatchUpsertBookFiles' preserve-on-empty guard also covers
		// this, but hydrating keeps the write self-contained rather than relying
		// on that guard for correctness. See
		// docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md.
		// The same list is the distinctness denominator: every file of the book.
		full, herr := store.GetBookFiles(b.bookID)
		if herr != nil {
			mu.Lock()
			missing += bMissing
			readErr += bReadErr + len(readings)
			mu.Unlock()
			return nil
		}

		verdict, why := judgeTagPositions(b.bookID, full, readings)
		take := verdict == tagTakePositions

		updates := make([]*database.BookFile, 0, len(readings))
		found := 0
		for j := range full {
			meta, ok := readings[full[j].ID]
			if !ok {
				continue
			}
			found++
			updated := full[j] // copy of the full hydrated row
			updated.RawTags = meta.AllTags
			if take {
				if meta.TrackNumber > 0 {
					updated.TrackNumber = meta.TrackNumber
				}
				if meta.TrackTotal > 0 {
					updated.TrackCount = meta.TrackTotal
				}
				if meta.DiscNumber > 0 {
					updated.DiscNumber = meta.DiscNumber
				}
				if meta.DiscTotal > 0 {
					updated.DiscCount = meta.DiscTotal
				}
			}
			if updated.Title == "" && meta.Title != "" {
				updated.Title = meta.Title
			}
			updates = append(updates, &updated)
		}
		// A candidate absent from its own book's hydrated list cannot be written
		// safely; count it as a read error, as before.
		bReadErr += len(readings) - found

		mu.Lock()
		missing += bMissing
		readErr += bReadErr
		needed += len(updates)
		if take {
			tookTracks += len(updates)
		} else {
			rawOnly += len(updates)
			if len(updates) > 0 {
				if verdict == tagRefuseDuplicate {
					refusedDup++
				} else {
					refusedMissing++
				}
				if len(refusedExamples) < 5 {
					refusedExamples = append(refusedExamples, fmt.Sprintf("book %s (%s)", b.bookID, why))
				}
			}
		}
		for _, u := range updates {
			if len(examples) >= 5 {
				break
			}
			examples = append(examples, fmt.Sprintf("%s(%d tags,trk %d)", u.ID, len(u.RawTags), u.TrackNumber))
		}
		mu.Unlock()

		if params.DryRun || len(updates) == 0 {
			return nil
		}
		return enqueue(updates)
	}, registry.RunItemsOptions{
		Concurrency:   runtime.NumCPU() * 4,
		ProgressTotal: len(books),
		Label: func(i, t int) string {
			mu.Lock()
			defer mu.Unlock()
			return fmt.Sprintf("book %d/%d (%d files examined) — %d to backfill (%d tag tracks, %d RawTags-only), books refused %d dup / %d missing-number, %d missing, %d read-err",
				i, t, examined, needed, tookTracks, rawOnly, refusedDup, refusedMissing, missing, readErr)
		},
	})
	if err != nil {
		return fmt.Errorf("parallel tag backfill: %w", err)
	}
	if !params.DryRun {
		wmu.Lock()
		ferr := flushLocked(true)
		wmu.Unlock()
		if ferr != nil {
			return fmt.Errorf("final batch write: %w", ferr)
		}
	}

	verb := "would backfill"
	if !params.DryRun {
		verb = fmt.Sprintf("backfilled %d;", written)
	}
	summary := fmt.Sprintf("examined=%d books=%d %s needed=%d took-tag-tracks=%d rawtags-only=%d "+
		"books-refused-duplicate=%d books-refused-missing=%d missing-on-disk=%d read-errors=%d | e.g. %s | refused e.g. %s",
		examined, len(books), verb, needed, tookTracks, rawOnly, refusedDup, refusedMissing, missing, readErr,
		strings.Join(examples, ", "), strings.Join(refusedExamples, ", "))
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(books), len(books), summary)
	return nil
}
