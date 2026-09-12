// file: internal/plugins/maintenance/tag_backfill.go
// version: 2.1.0
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
// (and an empty Title) and leaves the order alone. When a book is accepted, EVERY
// file of it is moved to its tag position, siblings included, so a book is never
// left half in tag numbering and half in positional numbering.

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

// tagBackfillWriteBatchSize is the row count at which pending rows are flushed
// through one BatchUpsertBookFiles call. Batches end on book boundaries, so a
// batch may exceed this by at most one book. 250, not 1000: a full row can carry
// a ~230 KB AcoustIDFingerprint, so 1000 rows is ~230 MB in one batch. A var (not
// a const) so tests can shrink it to exercise the multi-batch path.
var tagBackfillWriteBatchSize = 250

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

// tagPlacement is one file's position as its tags state it. disc is 0 when the
// tag carries no disc number.
type tagPlacement struct{ track, trackTotal, disc, discTotal int }

type tagPositionKey struct{ disc, track int }

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
//
// On accept it returns every file's placement, keyed by file ID, so the caller
// can move the whole book — siblings included — to its tag positions.
func judgeTagPositions(bookID string, full []database.BookFile, readings map[string]metadata.Metadata) (tagVerdict, string, map[string]tagPlacement) {
	if bookID == "" {
		return tagRefuseMissing, "no book id", nil
	}
	placements := make(map[string]tagPlacement, len(full))
	seen := make(map[tagPositionKey]string, len(full))
	for i := range full {
		var p tagPlacement
		if m, ok := readings[full[i].ID]; ok {
			p = tagPlacement{track: m.TrackNumber, trackTotal: m.TrackTotal, disc: m.DiscNumber, discTotal: m.DiscTotal}
		} else {
			p.track, p.trackTotal, p.disc, p.discTotal = metadata.TrackDiscFromTags(full[i].RawTags)
		}
		if p.track <= 0 {
			return tagRefuseMissing, fmt.Sprintf("file %s has no tag track", full[i].ID), nil
		}
		key := tagPositionKey{disc: max(p.disc, 0), track: p.track}
		if other, dup := seen[key]; dup {
			return tagRefuseDuplicate, fmt.Sprintf("disc %d track %d on %s and %s", key.disc, key.track, other, full[i].ID), nil
		}
		seen[key] = full[i].ID
		placements[full[i].ID] = p
	}
	return tagTakePositions, "", placements
}

// applyTagPlacement moves a row to its tag position and reports whether any
// position field changed. TrackNumber and DiscNumber are set exactly as the tag
// states them (DiscNumber 0 when the tag has no disc) so the row sorts where the
// judgement placed it. A tag total replaces the stored count; with no disc, the
// disc count is cleared too, since a disc count without a disc is meaningless.
// A missing track total keeps the stored TrackCount, which does not affect order.
func applyTagPlacement(u *database.BookFile, p tagPlacement) bool {
	before := [4]int{u.TrackNumber, u.TrackCount, u.DiscNumber, u.DiscCount}
	u.TrackNumber = p.track
	if p.trackTotal > 0 {
		u.TrackCount = p.trackTotal
	}
	u.DiscNumber = max(p.disc, 0)
	switch {
	case p.discTotal > 0:
		u.DiscCount = p.discTotal
	case u.DiscNumber == 0:
		u.DiscCount = 0
	}
	return before != [4]int{u.TrackNumber, u.TrackCount, u.DiscNumber, u.DiscCount}
}

func (p *Plugin) tagBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.tag-backfill",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Backfill lossless file tags (RawTags + track/disc)",
		Description: "Reads each BookFile's audio tags and backfills the lossless RawTags map (and an " +
			"empty title) for rows imported before lossless capture. Tag track/disc numbers replace the " +
			"stored ones only when they are distinct across every file of the book, and then for every " +
			"file of that book. Files missing on disk are skipped. Default dry-run previews counts; set " +
			"dryRun=false to apply.",
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
		siblingsRenumbered         int
		refusedDup, refusedMissing int
		examples                   = make([]string, 0, 5)
		refusedExamples            = make([]string, 0, 5)
	)

	// Bounded writes: each finished book's rows are appended to pending as one
	// unit, and pending is flushed WHOLE once it reaches tagBackfillWriteBatchSize,
	// so every batch ends on a book boundary (exceeding the size by at most one
	// book) and memory holds about one batch instead of every hydrated row in the
	// library. wmu guards pending/written/writeErr AND serializes the
	// BatchUpsertBookFiles call, so the op never depends on that call being
	// goroutine-safe. It is separate from mu so a batch write never stalls counter
	// updates or progress labels. After the first failed write nothing more is
	// written: later books are refused with the same error.
	var (
		wmu      sync.Mutex
		pending  []*database.BookFile
		written  int
		writeErr error
	)
	flushLocked := func() error {
		if writeErr != nil {
			return writeErr
		}
		// pending holds only whole books, so writing all of it in one call is
		// what keeps every batch on a book boundary. (The loop runs once; it is a
		// loop so that a chunked variant would still write every row.)
		for len(pending) > 0 {
			n := len(pending)
			if err := store.BatchUpsertBookFiles(pending[:n]); err != nil {
				writeErr = fmt.Errorf("batch write of %d rows failed (%d rows written before it): %w", n, written, err)
				return writeErr
			}
			written += n
			pending = pending[n:]
		}
		pending = nil // release the flushed rows
		return nil
	}
	enqueue := func(rows []*database.BookFile) error {
		wmu.Lock()
		defer wmu.Unlock()
		if writeErr != nil {
			return writeErr
		}
		pending = append(pending, rows...)
		if len(pending) >= tagBackfillWriteBatchSize {
			return flushLocked()
		}
		return nil
	}

	// Work is partitioned by BookID: every candidate row of a book is in exactly
	// one tagBackfillBook, and RunItems hands each item to exactly one worker, so
	// two workers can never judge or write the same book (or the same row) —
	// including the sibling rows an accepted book renumbers.
	// Per-book work is I/O bound (stat + tag read per file), so the pool is sized
	// like path_repair_resolver.go: NumCPU()*4.
	runErr := registry.RunItems(ctx, reporter, books, func(ctx context.Context, b tagBackfillBook) error {
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

		verdict, why, placements := judgeTagPositions(b.bookID, full, readings)
		take := verdict == tagTakePositions

		updates := make([]*database.BookFile, 0, len(full))
		found, bSiblings := 0, 0
		for j := range full {
			meta, ok := readings[full[j].ID]
			if !ok {
				// A sibling not read this run. On accept it is moved to its tag
				// position too: leaving it on its old positional numbers while the
				// rows read this run take tag numbers would mix two numbering
				// schemes in one book (e.g. disc 0 tracks 1-10 next to a recovered
				// file at disc 1 track 3) and scramble the order on a later run.
				if take {
					sib := full[j]
					if applyTagPlacement(&sib, placements[sib.ID]) {
						bSiblings++
						updates = append(updates, &sib)
					}
				}
				continue
			}
			found++
			updated := full[j] // copy of the full hydrated row
			updated.RawTags = meta.AllTags
			if take {
				applyTagPlacement(&updated, placements[updated.ID])
			}
			if updated.Title == "" && meta.Title != "" {
				updated.Title = meta.Title
			}
			updates = append(updates, &updated)
		}
		// A candidate absent from its own book's hydrated list cannot be written
		// safely; count it as a read error, as before.
		bReadErr += len(readings) - found
		bRead := len(updates) - bSiblings

		mu.Lock()
		missing += bMissing
		readErr += bReadErr
		needed += bRead
		siblingsRenumbered += bSiblings
		if take {
			tookTracks += bRead
		} else {
			rawOnly += bRead
			if bRead > 0 {
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
			examples = append(examples, fmt.Sprintf("%s(%d tags,disc %d trk %d)", u.ID, len(u.RawTags), u.DiscNumber, u.TrackNumber))
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
			return fmt.Sprintf("book %d/%d (%d files examined) — %d to backfill (%d tag tracks, %d RawTags-only, %d siblings renumbered), books refused %d dup / %d missing-number, %d missing, %d read-err",
				i, t, examined, needed, tookTracks, rawOnly, siblingsRenumbered, refusedDup, refusedMissing, missing, readErr)
		},
	})

	// RunItems has returned, so every worker has finished (run_items.go waits on
	// its WaitGroup) and pending holds only complete books. Flush them even when
	// the run was canceled or failed: those books were judged in full, and
	// dropping them would only force a re-read next run. After a failed write
	// nothing more is written and the rows still pending are reported dropped.
	stopNote := ""
	if !params.DryRun {
		wmu.Lock()
		n := len(pending)
		ferr := flushLocked()
		writtenNow := written
		wmu.Unlock()
		if runErr != nil || ferr != nil {
			flushed, dropped := n, 0
			if ferr != nil {
				flushed, dropped = 0, n
			}
			stopNote = fmt.Sprintf(" | stopped early: flushed %d pending rows of complete books, dropped %d; %d rows written in total", flushed, dropped, writtenNow)
			_ = reporter.Log(slog.LevelWarn, "tag backfill stopping early:"+stopNote)
		}
		if runErr == nil && ferr != nil {
			runErr = ferr
		}
	}

	wmu.Lock()
	writtenTotal := written
	wmu.Unlock()
	mu.Lock()
	verb := "would backfill"
	if !params.DryRun {
		verb = fmt.Sprintf("wrote %d rows;", writtenTotal)
	}
	summary := fmt.Sprintf("examined=%d books=%d %s needed=%d took-tag-tracks=%d rawtags-only=%d siblings-renumbered=%d "+
		"books-refused-duplicate=%d books-refused-missing=%d missing-on-disk=%d read-errors=%d | e.g. %s | refused e.g. %s%s",
		examined, len(books), verb, needed, tookTracks, rawOnly, siblingsRenumbered, refusedDup, refusedMissing, missing, readErr,
		strings.Join(examples, ", "), strings.Join(refusedExamples, ", "), stopNote)
	mu.Unlock()
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(books), len(books), summary)
	if runErr != nil {
		return fmt.Errorf("parallel tag backfill: %w", runErr)
	}
	return nil
}
