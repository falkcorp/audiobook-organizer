// file: internal/server/library_writeback_op.go
// version: 1.9.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-11-18

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// bulkWriteBackOpParams is the JSON params for the library.bulk-write-back op.
//
// It doubles as the checkpoint payload: resumeRestart overlays the checkpoint
// onto the row's params, so BookIDs carries NO omitempty — an empty remaining
// set must replace the original list in the overlay rather than let it show
// through and re-write every file (see batch_apply_op.go for that incident).
type bulkWriteBackOpParams struct {
	BookIDs []string `json:"book_ids"`
	Rename  bool     `json:"rename"`
}

// bulkWriteBackCheckpointEvery is how many examined books sit between
// checkpoints. A write-back is a file rewrite per book, so an interrupt costs
// at most this many idempotent re-writes.
const bulkWriteBackCheckpointEvery = 25

func mergeBulkWriteBackQueuedParams(existing, incoming json.RawMessage) (json.RawMessage, bool, error) {
	var current, next bulkWriteBackOpParams
	if err := json.Unmarshal(existing, &current); err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(incoming, &next); err != nil {
		return nil, false, err
	}
	if current.Rename != next.Rename {
		return nil, false, nil
	}
	merged, err := json.Marshal(bulkWriteBackOpParams{
		BookIDs: mergeUniqueBookIDs(current.BookIDs, next.BookIDs),
		Rename:  current.Rename,
	})
	return merged, err == nil, err
}

// summarizeBulkWriteBackQueued reports the size of a queued bulk write-back.
// This op has no OriginalTotal to carry prior work across a resume, so the
// completed half is always zero and the total is simply what is queued.
func summarizeBulkWriteBackQueued(params json.RawMessage) (int, int, string) {
	p, ok := decodeQueuedParams[bulkWriteBackOpParams](params)
	if !ok {
		return 0, 0, ""
	}
	return formatQueuedBookSummary(0, len(p.BookIDs), "write back")
}

// bulkWriteBackOpID is HEAD's def ID for the bulk tag write-back.
const bulkWriteBackOpID = "library.bulk-write-back"

// bulkWriteBackWriter is the writes of the bulk write-back: the per-book tag
// rewrite pass (the file rewrite and the book rows it stamps), and the two
// activity-log rows the run closes with.
type bulkWriteBackWriter interface {
	// WriteBackBooks rewrites the tags of bookIDs, calling onDone after every
	// examined book (written, failed or skipped).
	WriteBackBooks(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter, onDone func(bookID string)) error
	t7ActivityWriter
}

// bulkWriteBackEnv is what the shared core takes from its shell (rules R2, R10).
type bulkWriteBackEnv struct {
	t7Env
	Writer    bulkWriteBackWriter
	StandDown t7StandDown
}

// bulkWriteBackCore is the body of the op, shared by HEAD's shell and the native
// kind. Params are decoded and, on a resumed native run, overlaid with the
// checkpoint before this is called.
func bulkWriteBackCore(ctx context.Context, p bulkWriteBackOpParams, env bulkWriteBackEnv) (retErr error) {
	if len(p.BookIDs) == 0 {
		return nil
	}
	// Metadata is never applied during a library scan: hold the scan stand-down
	// before the first write (fails the op if the scan does not park).
	hold, sdErr := env.StandDown(ctx, "library.bulk-write-back apply")
	if sdErr != nil {
		return sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	ctx, env.t7Env = t7Rebind(ctx, env.t7Env, hold)

	opID := ulid.Make().String()

	// CHECKPOINTING. onDone fires after every examined book (written, failed
	// or skipped — see runBulkWriteBack), so the remaining set is exactly the
	// books this attempt has not yet looked at. ckptMu serialises the
	// Checkpoint calls so two workers crossing a cadence boundary together
	// cannot write an older remaining set over a newer one.
	done := newDoneSet(bulkWriteBackCheckpointEvery)
	var ckptMu sync.Mutex
	writeCheckpoint := func() {
		ckptMu.Lock()
		defer ckptMu.Unlock()
		if err := env.Checkpoint(bulkWriteBackOpParams{BookIDs: done.remaining(p.BookIDs), Rename: p.Rename}); err != nil {
			slog.Warn("bulk-write-back checkpoint failed", "opID", opID, "err", err)
		}
	}
	onDone := func(bookID string) {
		if done.mark(bookID) {
			writeCheckpoint()
		}
	}

	runErr := env.Writer.WriteBackBooks(ctx, opID, p.BookIDs, p.Rename, 0, env.reporter(), onDone)
	if ctx.Err() != nil {
		// Final checkpoint on the way out, so the books examined since the last
		// periodic one are not re-owed by the resumed run. The store does not
		// take ctx, so this write succeeds under a cancelled context.
		writeCheckpoint()
	}
	env.Writer.FlushActivityOp(opID)
	summary := fmt.Sprintf("Bulk tag write-back completed for %d books", len(p.BookIDs))
	if runErr != nil {
		summary = fmt.Sprintf("Bulk tag write-back failed: %v", runErr)
	}
	env.Writer.EmitActivityInfo(opID, "library.bulk-write-back", "library", summary, activity.AlwaysShow)
	return runErr
}

// headBulkWriteBackWriter is HEAD's write side: the server's runBulkWriteBack and
// activity writer, each called as HEAD's body called them.
type headBulkWriteBackWriter struct {
	headT7Activity
	s *Server
}

func (w headBulkWriteBackWriter) WriteBackBooks(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter, onDone func(bookID string)) error {
	return w.s.runBulkWriteBack(ctx, opID, bookIDs, doRename, startIdx, progress, onDone)
}

// bulkWriteBackDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) bulkWriteBackDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              bulkWriteBackOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "library",
		DisplayName:     "Bulk Tag Write-back",
		Description:     "Write metadata from the database back to audio file tags for a set of audiobooks.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		// RESUME AUDIT 2026-09-11 (a): keep ResumeRestart, and back it with a
		// real checkpoint. Until now the only checkpoint on this path was the v1
		// SaveCheckpoint inside runBulkWriteBack, keyed on a ULID minted per
		// attempt and never read back, so every restart rewrote every file from
		// the top of a six-hour run. Run now persists the remaining book ids as a
		// done-set (workers finish out of order, so a count or cursor would skip
		// stragglers) every bulkWriteBackCheckpointEvery books and once more on
		// cancel; resumeRestart overlays it onto params, and MergeQueuedParams
		// unions any newly queued books into that reduced set.
		ResumePolicy:      opsregistry.ResumeRestart,
		ConcurrencyKey:    bulkWriteBackOpID,
		MergeQueuedParams: mergeBulkWriteBackQueuedParams,
		SummarizeQueued:   summarizeBulkWriteBackQueued,
		Permissions:       []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:      []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run:               s.runBulkWriteBackOp,
	}
}

// RegisterBulkWriteBackOp registers the "library.bulk-write-back" v2 OperationDef.
// The HTTP handler handleBulkWriteBack pre-filters books and passes the resulting
// book IDs as params; the Run func executes the actual tag-write work.
func (s *Server) RegisterBulkWriteBackOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.bulkWriteBackDef())
}

// runBulkWriteBackOp is the Run body of library.bulk-write-back. It is a named
// method rather than a closure so the resume test can drive it with a
// recording reporter and a cancelled context. It is the v2 shell over
// bulkWriteBackCore.
func (s *Server) runBulkWriteBackOp(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
	// No backup opt-out here: runBulkWriteBack applies tagger.WithoutBackup
	// itself (owner decision D69), so every caller of it gets it.
	var p bulkWriteBackOpParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &p); err != nil {
			return fmt.Errorf("bulk-write-back: decode params: %w", err)
		}
	}
	return bulkWriteBackCore(ctx, p, bulkWriteBackEnv{
		t7Env:     t7HeadCheckpointing(t7HeadEnv(reporter, true), reporter),
		Writer:    headBulkWriteBackWriter{headT7Activity: headT7Activity{w: s.activityWriter}, s: s},
		StandDown: s.t7HeadMetadataStandDown(reporter),
	})
}

// bulkWriteBackNative is the native books.library.bulk-write-back kind. HEAD runs
// it only live, so a Preview run is refused. A resumed run reads HEAD's
// checkpoint struct (the remaining book ids) back, so it continues rather than
// starting over (ResumeContinue).
func (s *Server) bulkWriteBackNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(bulkWriteBackOpID)
	c := t7Common(s.bulkWriteBackDef(), sched)
	c.Resume = t7Resume(opsregistry.ResumeRestart, true)
	// The write set is the whole row of WriteBackBooks, which is one gated call:
	// the tag and cover write (files, books), the rename-only apply and the
	// library copy it can create (book_files, authors, metadata_cache, indexes,
	// field_states, embeddings, user_state, sync_identity, external_ids,
	// operations). It was books, book_files and files, which the first
	// ModifyBook of the run would have been refused outside (its row stamps
	// metadata_cache and indexes too). booksT7WriteBackBooksStoreWrites lists the
	// store writes with where each is made.
	c.Effects = t7Writes(booksT7WriteBackBooksRow()...)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return struct{}{}, err
			}
			p, err := t7DecodeNative[bulkWriteBackOpParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			var ck bulkWriteBackOpParams
			if ok, err := rc.Restore(&ck); err != nil {
				return struct{}{}, fmt.Errorf("%s: restore checkpoint: %w", kind, err)
			} else if ok {
				p = ck
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, bulkWriteBackCore(rc.Context(), p, bulkWriteBackEnv{
				t7Env:     t7NativeEnv(rc),
				Writer:    w,
				StandDown: t7NativeStandDown(rc, deps.StandDownKey),
			})
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBulkWriteBackOp(reg) })
}
