// file: internal/plugins/maintenance/bookfile_acoustid_index_repair.go
// version: 1.2.1
// guid: 8b4e1d27-5f90-4c63-a2d8-e07c91b3f5a4
// last-edited: 2026-11-13

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

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
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
//
// RESUME. The index is paged by a key cursor, so after each page is judged (and,
// live, deleted) the run checkpoints the cursor and the running counts
// (acoustIDRepairParams). A restart merges that checkpoint into the params and
// continues at the next page instead of re-judging every key from the first one
// (one row read each); the final report still counts the whole run, not only the
// part after the restart. A page cut mid-way is judged again, which is safe: a
// delete judges its keys again under the index mutex and removes only orphans.
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

// t6g2KindBookFileAcoustIDIndexRepair is the native kind (F5-07, rule R3).
const t6g2KindBookFileAcoustIDIndexRepair tasks.Kind = "books." + bookFileAcoustIDIndexRepairID

// DisplayName and Description of the def, shared by HEAD's def and the native
// kind so the two cannot drift.
const (
	t6g2BookFileAcoustIDIndexRepairTitle = "Book file AcoustID index repair"
	t6g2BookFileAcoustIDIndexRepairHelp  = "Deletes book_file_acoustid: index keys whose book_file row holds no matching " +
		"AcoustID segment: keys the old create/update writers left for segments the row never " +
		"stored. Preview by default: judges every key and reports counts, writes nothing. With " +
		"dry_run=false it deletes the orphans, each judged again under the single-owner index " +
		"lock before the delete. Keys whose row still stores the segment are left for " +
		"dedup.bookfile-seg-drop; malformed values are reported, never deleted. GetBookFileByAcoustID " +
		"stops answering for a deleted key. It never writes a book_file row."
)

func (p *Plugin) bookFileAcoustIDIndexRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              bookFileAcoustIDIndexRepairID,
		Preview:         sdk.PreviewSpec{Params: opmode.DryRunParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     t6g2BookFileAcoustIDIndexRepairTitle,
		Description:     t6g2BookFileAcoustIDIndexRepairHelp,
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

// acoustIDRepairParams is the op's params and its checkpoint. The registry
// merges a checkpoint's JSON over the params on resume, so a checkpoint carries
// the mode too (dry_run) and a resumed run cannot fall back to the preview
// default or to the other mode.
type acoustIDRepairParams struct {
	opmode.DryRunParams
	// ResumeAfter is the scan cursor of the first page not yet handled; empty
	// starts at the first key.
	ResumeAfter string `json:"resume_after,omitempty"`
	// Done is what the pages before ResumeAfter found, so the report counts the
	// whole run across restarts.
	Done *acoustIDRepairDone `json:"done,omitempty"`
}

// acoustIDRepairDone is the tally of the pages a checkpoint covers.
type acoustIDRepairDone struct {
	Scanned      int64    `json:"scanned"`
	Live         int64    `json:"live"`
	LacksSeg     int64    `json:"lacks_seg"`
	RowGone      int64    `json:"row_gone"`
	Malformed    int64    `json:"malformed"`
	Deleted      int64    `json:"deleted"`
	KeptAtDelete int64    `json:"kept_at_delete"`
	Samples      []string `json:"samples,omitempty"`
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

// done snapshots the tally for a checkpoint. It is called between pages, when
// no worker is running, but takes the samples lock anyway.
func (t *acoustIDRepairTally) done() *acoustIDRepairDone {
	t.mu.Lock()
	defer t.mu.Unlock()
	return &acoustIDRepairDone{
		Scanned: t.scanned.Load(), Live: t.live.Load(), LacksSeg: t.lacksSeg.Load(),
		RowGone: t.rowGone.Load(), Malformed: t.malformed.Load(), Deleted: t.deleted.Load(),
		KeptAtDelete: t.keptAtDelete.Load(), Samples: append([]string(nil), t.samples...),
	}
}

// restore seeds the tally from a checkpoint's counts.
func (t *acoustIDRepairTally) restore(d *acoustIDRepairDone) {
	if d == nil {
		return
	}
	t.scanned.Store(d.Scanned)
	t.live.Store(d.Live)
	t.lacksSeg.Store(d.LacksSeg)
	t.rowGone.Store(d.RowGone)
	t.malformed.Store(d.Malformed)
	t.deleted.Store(d.Deleted)
	t.keptAtDelete.Store(d.KeptAtDelete)
	t.mu.Lock()
	t.samples = append([]string(nil), d.Samples...)
	t.mu.Unlock()
}

func (t *acoustIDRepairTally) orphans() int64 { return t.lacksSeg.Load() + t.rowGone.Load() }

func (p *Plugin) runBookFileAcoustIDIndexRepair(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params acoustIDRepairParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: invalid params: %w", bookFileAcoustIDIndexRepairID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(bookFileAcoustIDIndexRepairID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	env := headT6G2Wire(headT6G2Env(p, reporter), p, store)
	env.Checkpoint = reporter.Checkpoint
	env.Items = headT6G2Items(reporter, runtime.NumCPU())
	_, err = runBookFileAcoustIDIndexRepairCore(ctx, t6g2AcoustIDRepairRun{Params: params, DryRun: dryRun}, env)
	return err
}

// t6g2AcoustIDRepairRun is the core's input: the params as the run started
// (checkpoints save them verbatim, so a restored run keeps its spelling of the
// mode) and the mode already resolved.
type t6g2AcoustIDRepairRun struct {
	Params acoustIDRepairParams
	DryRun bool
}

// runBookFileAcoustIDIndexRepairCore is the body shared by HEAD's shell and the
// native kind. It scans and judges through env.Store, deletes through env.Writer
// and judges each page on env.Items.
func runBookFileAcoustIDIndexRepairCore(ctx context.Context, run t6g2AcoustIDRepairRun, env t6g2Env) (t6g2NoResult, error) {
	params, dryRun := run.Params, run.DryRun
	if !env.Store.Supports(t6g2CapAcoustIDRepair) {
		return t6g2NoResult{}, fmt.Errorf("%s: store is %s, which has no book_file_acoustid: index to repair", bookFileAcoustIDIndexRepairID, env.Store.Describe())
	}

	verb := "deleting orphans"
	if dryRun {
		verb = "checking (preview, deletes nothing)"
	}
	env.Progress(0, 0, "book_file_acoustid: index: "+verb)

	var tally acoustIDRepairTally
	tally.restore(params.Done)
	after := params.ResumeAfter
	if after != "" {
		env.Log(slog.LevelInfo, fmt.Sprintf("resuming after a restart: %d keys already handled", tally.scanned.Load()))
	}
	for {
		page, next, err := env.Store.ScanBookFileAcoustIDIndex(after, acoustIDRepairPage)
		if err != nil {
			return t6g2NoResult{}, fmt.Errorf("%s: scan: %w", bookFileAcoustIDIndexRepairID, err)
		}
		if len(page) > 0 {
			orphans, err := judgeAcoustIDPage(ctx, env, page, &tally)
			if err != nil {
				return t6g2NoResult{}, fmt.Errorf("%s: %w", bookFileAcoustIDIndexRepairID, err)
			}
			if !dryRun {
				if err := deleteAcoustIDOrphans(ctx, env.Writer, orphans, &tally); err != nil {
					return t6g2NoResult{}, fmt.Errorf("%s: %w", bookFileAcoustIDIndexRepairID, err)
				}
			}
		}
		if next == "" {
			break
		}
		after = next
		// The page is fully judged (and deleted, live). Persist the cursor and
		// the counts; a failed save costs only a re-judged page on restart.
		params.ResumeAfter, params.Done = after, tally.done()
		if err := env.Checkpoint(params); err != nil {
			env.Log(slog.LevelWarn, "Could not save the resume point; a restart would redo more",
				slog.String("error", err.Error()))
		}
	}

	for _, s := range tally.samples {
		env.Log(slog.LevelInfo, "orphan: "+s)
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
	env.Log(level, msg)
	env.Progress(1, 1, msg)
	return t6g2NoResult{}, nil
}

// judgeAcoustIDPage judges every entry of one scanned page on the env's pool and
// returns the orphans. Judging is read-only, so the workers share no state but
// the tally and the orphan list.
func judgeAcoustIDPage(ctx context.Context, env t6g2Env,
	page []database.BookFileAcoustIDEntry, tally *acoustIDRepairTally) ([]database.BookFileAcoustIDEntry, error) {
	var (
		mu      sync.Mutex
		orphans []database.BookFileAcoustIDEntry
	)
	base := int(tally.scanned.Load())
	err := env.Items(ctx, len(page), t6g2Loop{
		// Label runs inside the worker goroutines: it reads no counter.
		Label: func(i, total int) string {
			return fmt.Sprintf("book_file_acoustid: key %d/%d of this page", i+1, total)
		},
		ProgressOffset: base,
		ProgressTotal:  base + len(page),
	}, func(_ context.Context, i int) error {
		e := page[i]
		v, err := env.Store.JudgeBookFileAcoustIDEntry(e)
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
	})
	return orphans, err
}

// deleteAcoustIDOrphans deletes the orphans of one page in small chunks. A chunk
// judges its keys again under the lock, so a key that stopped being an orphan
// since the page was judged is counted as kept, not deleted.
func deleteAcoustIDOrphans(ctx context.Context, repairer t6g2IndexWriter,
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

// t6g2BookFileAcoustIDIndexRepairParallelism is HEAD's page-judging pool: one
// worker per CPU (one row read per key).
func t6g2BookFileAcoustIDIndexRepairParallelism() tasks.Parallelism { return tasks.CPU() }

func t6g2BookFileAcoustIDIndexRepairMeta() t6g2Meta {
	return t6g2Meta{
		Kind:     t6g2KindBookFileAcoustIDIndexRepair,
		Title:    t6g2BookFileAcoustIDIndexRepairTitle,
		Help:     t6g2BookFileAcoustIDIndexRepairHelp,
		Effects:  tasks.Writes(t6g2Res("book_files")...).AlsoReads(t6g2Res("book_files")...),
		Resume:   tasks.ResumeContinue,
		Priority: tasks.PriorityLow,
		Timeout:  60 * time.Minute,
		Key:      bookFileAcoustIDIndexRepairID,
	}
}

func t6g2BookFileAcoustIDIndexRepairKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	m := t6g2BookFileAcoustIDIndexRepairMeta()
	par := t6g2BookFileAcoustIDIndexRepairParallelism()
	run := func(rc *tasks.RC, raw json.RawMessage) (t6g2NoResult, error) {
		if err := t6g2RefuseModeKeys(m.Kind, raw); err != nil {
			return t6g2NoResult{}, err
		}
		dryRun := !t6g2Live(rc)
		// A checkpoint is the run's params with the cursor and counts updated.
		run := t6g2AcoustIDRepairRun{DryRun: dryRun}
		var saved acoustIDRepairParams
		ok, err := rc.Restore(&saved)
		if err != nil {
			return t6g2NoResult{}, fmt.Errorf("%s: restore checkpoint: %w", m.Kind, err)
		}
		if ok {
			run.Params = saved
		}
		run.Params.DryRun, run.Params.DryRunCamel = &dryRun, nil
		env, err := t6g2NativeEnv(rc, deps, par)
		if err != nil {
			return t6g2NoResult{}, err
		}
		return runBookFileAcoustIDIndexRepairCore(rc.Context(), run, env)
	}
	return tasks.Task(m.Kind, tasks.TaskSpec[json.RawMessage, t6g2NoResult]{Common: m.common(sched), Run: run})
}
