// file: internal/plugins/maintenance/bookfile_acoustid_index_repair.go
// version: 1.0.0
// guid: 8b4e1d27-5f90-4c63-a2d8-e07c91b3f5a4
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenance.bookfile-acoustid-index-repair deletes the book_file_acoustid:
// index keys that no book_file row holds a segment for.
//
// Until 2026-10-30 every BookFile create and update wrote one such key for each
// AcoustIDSeg0..6 value the caller's struct carried (the fingerprint backfill
// sets AcoustIDSeg0 on every row it fingerprints), although the stored row has
// dropped those columns since T020. Nothing could delete such a key later: the
// delete paths and dedup.bookfile-seg-drop find a row's segments in the STORED
// row, which has none. The writers are fixed (the index is now derived from the
// row as stored); this op removes what the old ones left behind.
//
// A key is an orphan when the row it names is gone, or exists and does not hold
// the key's segment. A key whose row still holds its segment is a live legacy
// key and is left for dedup.bookfile-seg-drop, which rewrites the row and
// removes the key together. A value that is not a "<bookID>:<fileID>" reference
// is reported and never deleted.
//
// WHAT IT COSTS. GetBookFileByAcoustID, and through it the exact-match tier of
// the dedup scan and the Deluge discovery check, answers from this index. A key
// the repair deletes stops answering. Preview first: the counts say how many
// keys that is.
//
// Modes:
//   - preview (default, owner rule 2026-09-25): judges every key and reports the
//     counts and a sample; deletes nothing.
//   - dry_run=false: deletes the orphans, in chunks. Each chunk judges its keys
//     AGAIN under the single-owner index mutex before deleting (see
//     DeleteOrphanBookFileAcoustIDEntries), so a key a retire, un-retire or
//     rehome repointed to a live row after the scan is kept.
//
// It never writes a book_file: row. Idempotent, so ResumeRestart. The index
// holds a whole segment string per key, so it is read in pages and each page is
// judged on a worker pool sized to the CPUs (one row read per key).
const bookFileAcoustIDIndexRepairID = "maintenance.bookfile-acoustid-index-repair"

const (
	// acoustIDRepairPage is how many index entries are scanned and judged at once.
	acoustIDRepairPage = 2048
	// acoustIDRepairDeleteChunk is how many orphans one delete commit judges
	// again and removes. The single-owner index mutex is held across it, and it
	// blocks every other writer of a single-owner key, so it stays small.
	acoustIDRepairDeleteChunk = 256
	// acoustIDRepairSamples is how many orphan keys the report names.
	acoustIDRepairSamples = 5
)

// bookFileAcoustIDIndexRepairer is the store capability the op needs; it is
// resolved through database.AsCapability because OpsStore() can be a decorator
// that hides *PebbleStore's methods.
type bookFileAcoustIDIndexRepairer interface {
	ScanBookFileAcoustIDIndex(after string, limit int) ([]database.BookFileAcoustIDEntry, string, error)
	JudgeBookFileAcoustIDEntry(e database.BookFileAcoustIDEntry) (database.BookFileAcoustIDVerdict, error)
	DeleteOrphanBookFileAcoustIDEntries(ctx context.Context, entries []database.BookFileAcoustIDEntry) (int, error)
}

func (p *Plugin) bookFileAcoustIDIndexRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          bookFileAcoustIDIndexRepairID,
		Preview:     sdk.PreviewSpec{Params: opmode.DryRunParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Book file AcoustID index repair",
		Description: "Deletes book_file_acoustid: index keys whose book_file row holds no matching " +
			"AcoustID segment: keys the old create/update writers left for segments the row never " +
			"stored. Preview by default: judges every key and reports counts, writes nothing. With " +
			"dry_run=false it deletes the orphans, each judged again under the single-owner index " +
			"lock before the delete. Keys whose row still stores the segment are left for " +
			"dedup.bookfile-seg-drop; malformed values are reported, never deleted. GetBookFileByAcoustID " +
			"stops answering for a deleted key. It never writes a book_file row.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  bookFileAcoustIDIndexRepairID,
		Writes:          []sdk.Resource{sdk.ResBookFiles},
		Reads:           []sdk.Resource{sdk.ResBookFiles},
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runBookFileAcoustIDIndexRepair,
	}
}

// acoustIDRepairTally is the run's counters. The worker goroutines judge
// concurrently, so every counter is atomic; the samples have their own mutex.
type acoustIDRepairTally struct {
	scanned, live, lacksSeg, rowGone, malformed atomic.Int64
	deleted, keptAtDelete                       atomic.Int64

	mu      sync.Mutex
	samples []string
}

func (t *acoustIDRepairTally) sample(e database.BookFileAcoustIDEntry, v database.BookFileAcoustIDVerdict) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.samples) < acoustIDRepairSamples {
		seg := e.Seg
		if len(seg) > 24 {
			seg = seg[:24] + "…"
		}
		t.samples = append(t.samples, fmt.Sprintf("%s (%s) -> %s", seg, v, e.Ref))
	}
}

func (t *acoustIDRepairTally) orphans() int64 { return t.lacksSeg.Load() + t.rowGone.Load() }

func (p *Plugin) runBookFileAcoustIDIndexRepair(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	dryRun, err := opmode.ParseDryRun(bookFileAcoustIDIndexRepairID, raw)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	repairer, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](store)
	if !ok {
		return fmt.Errorf("%s: store is %T, which has no book_file_acoustid: index to repair", bookFileAcoustIDIndexRepairID, store)
	}

	verb := "deleting orphans"
	if dryRun {
		verb = "checking (preview, deletes nothing)"
	}
	_ = reporter.UpdateProgress(0, 0, "book_file_acoustid: index: "+verb)

	var tally acoustIDRepairTally
	after := ""
	for {
		page, next, err := repairer.ScanBookFileAcoustIDIndex(after, acoustIDRepairPage)
		if err != nil {
			return fmt.Errorf("%s: scan: %w", bookFileAcoustIDIndexRepairID, err)
		}
		if len(page) > 0 {
			orphans, err := judgeAcoustIDPage(ctx, reporter, repairer, page, &tally)
			if err != nil {
				return fmt.Errorf("%s: %w", bookFileAcoustIDIndexRepairID, err)
			}
			if !dryRun {
				if err := deleteAcoustIDOrphans(ctx, repairer, orphans, &tally); err != nil {
					return fmt.Errorf("%s: %w", bookFileAcoustIDIndexRepairID, err)
				}
			}
		}
		if next == "" {
			break
		}
		after = next
	}

	for _, s := range tally.samples {
		_ = reporter.Log(slog.LevelInfo, "orphan: "+s)
	}
	msg := fmt.Sprintf("book_file_acoustid: index: %d keys; %d orphans (%d row lacks the segment, %d row gone); "+
		"%d live legacy (left for dedup.bookfile-seg-drop); %d malformed (never deleted)",
		tally.scanned.Load(), tally.orphans(), tally.lacksSeg.Load(), tally.rowGone.Load(),
		tally.live.Load(), tally.malformed.Load())
	if dryRun {
		msg += "; preview, nothing deleted (run with dry_run=false to delete the orphans)"
	} else {
		msg += fmt.Sprintf("; %d deleted, %d kept (changed since judged)", tally.deleted.Load(), tally.keptAtDelete.Load())
	}
	level := slog.LevelInfo
	if tally.malformed.Load() > 0 {
		level = slog.LevelWarn
	}
	_ = reporter.Log(level, msg)
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

// judgeAcoustIDPage judges every entry of one scanned page on a worker pool and
// returns the orphans. Judging is read-only, so the workers share no state but
// the tally and the orphan list.
func judgeAcoustIDPage(ctx context.Context, reporter sdk.Reporter, repairer bookFileAcoustIDIndexRepairer,
	page []database.BookFileAcoustIDEntry, tally *acoustIDRepairTally) ([]database.BookFileAcoustIDEntry, error) {
	var (
		mu      sync.Mutex
		orphans []database.BookFileAcoustIDEntry
	)
	base := int(tally.scanned.Load())
	err := registry.RunItems(ctx, reporter, page, func(_ context.Context, e database.BookFileAcoustIDEntry) error {
		v, err := repairer.JudgeBookFileAcoustIDEntry(e)
		if err != nil {
			return err
		}
		tally.scanned.Add(1)
		switch v {
		case database.AcoustIDEntryLive:
			tally.live.Add(1)
		case database.AcoustIDEntryMalformed:
			tally.malformed.Add(1)
		case database.AcoustIDEntryRowLacksSeg, database.AcoustIDEntryRowGone:
			if v == database.AcoustIDEntryRowLacksSeg {
				tally.lacksSeg.Add(1)
			} else {
				tally.rowGone.Add(1)
			}
			tally.sample(e, v)
			mu.Lock()
			orphans = append(orphans, e)
			mu.Unlock()
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		// Label runs inside the worker goroutines: it reads no counter.
		Label: func(i, total int) string {
			return fmt.Sprintf("book_file_acoustid: key %d/%d of this page", i+1, total)
		},
		ProgressOffset: base,
		ProgressTotal:  base + len(page),
	})
	return orphans, err
}

// deleteAcoustIDOrphans deletes the orphans of one page in small chunks. A chunk
// judges its keys again under the lock, so a key that stopped being an orphan
// since the page was judged is counted as kept, not deleted.
func deleteAcoustIDOrphans(ctx context.Context, repairer bookFileAcoustIDIndexRepairer,
	orphans []database.BookFileAcoustIDEntry, tally *acoustIDRepairTally) error {
	for start := 0; start < len(orphans); start += acoustIDRepairDeleteChunk {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := orphans[start:min(start+acoustIDRepairDeleteChunk, len(orphans))]
		n, err := repairer.DeleteOrphanBookFileAcoustIDEntries(ctx, chunk)
		// n counts what the commit applied even when only its fsync failed
		// (err non-nil), so the deleted tally stays true before the error is
		// returned. A refused commit applied nothing: n is 0 and the chunk is not
		// "kept", it is unfinished.
		tally.deleted.Add(int64(n))
		if err != nil {
			return fmt.Errorf("delete orphans: %w", err)
		}
		tally.keptAtDelete.Add(int64(len(chunk) - n))
	}
	return nil
}
