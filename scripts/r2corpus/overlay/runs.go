// file: scripts/r2corpus/overlay/runs.go
// version: 1.0.1
// guid: 3e8b2c5f-7a14-4d96-a0c3-5b9e1f6d2a47
// last-edited: 2026-11-10

//go:build r2corpus_overlay

package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Runs R0 cannot leave at a clean stop (05-tasks §5T.8.3): Registry.Shutdown
// cancels every running op, so running and stopping exist on disk only after a
// crash. They are driven through the tag's own state machine (InsertRunV3 and
// TransitionRunV3, which write the run record, its indexes, its audit trail and
// the opv2 mirror row in one batch), never written field by field.

var systemActor = database.Actor{UserID: "", AuthMethod: "system"}

// runKeys are the primary keys of one run: its record and its progress half.
// The run's opv2 mirror row, written in the same batch by the tag's
// InsertRunV3/TransitionRunV3, is not listed: main's
// TestOpsV2RowWritesGoThroughStageOpRow keeps every mention of that row key
// in the module reviewed, and this program never touches the row itself (the
// manifest's stage2.keys says so).
func runKeys(id string) []string {
	return []string{"opv3:run:" + id, "opv3:prog:" + id}
}

// newRun inserts a queued run of defID through InsertRunV3.
func (w *writer) newRun(defID string, params any, waitReason string) string {
	id := newID()
	raw, err := json.Marshal(params)
	must(err, "encode params")
	must(w.db.InsertRunV3(database.RunRecord{
		SchemaVersion: 3, ID: id, DefID: defID, State: state.V3Queued, WaitReason: waitReason,
		Params: raw, Priority: 1, Actor: systemActor, Trigger: "manual",
	}), "insert run "+defID)
	return id
}

// step applies one event to run id, reading its current (state, attempt,
// fence) first, as every TransitionRunV3 caller does.
func (w *writer) step(id string, ev state.Event, req database.TransitionRequest) *database.RunRecord {
	cur, err := w.db.GetRunV3(id)
	must(err, "read run "+id)
	if cur == nil {
		must(fmt.Errorf("run %s vanished", id), "read run")
	}
	req.ID, req.Event = id, ev
	req.ExpectState, req.ExpectAttempt, req.ExpectFenceEpoch = cur.State, cur.Attempt, cur.FenceEpoch
	if req.Actor == nil {
		a := systemActor
		req.Actor = &a
	}
	out, err := w.db.TransitionRunV3(req)
	must(err, fmt.Sprintf("run %s %s -> %s", id, cur.State, ev))
	return out
}

// running is a dispatched run with the per-run state a crash leaves: a
// checkpoint, a chunk ledger with marks, an open intent, item outcomes and log
// lines.
func (w *writer) running(defID string, params any) string {
	id := w.newRun(defID, params, "")
	w.step(id, state.EvDispatch, database.TransitionRequest{})
	now := time.Now().UTC()
	must(w.db.SaveCheckpointV3(id, database.Checkpoint{
		SchemaVersion: 1, Phase: "sweep", State: json.RawMessage(`{"cursor":"Knightfall","done":3}`), SavedAt: now,
	}), "checkpoint "+id)
	total := int64(8)
	must(w.db.CommitLedgerV3(id, database.LedgerBatch{
		Ledger: database.Ledger{
			SchemaVersion: 1, ChunkSize: 2, ChunkCount: 4, Done: []byte{0x03},
			Items: database.LedgerItems{Done: 3, Failed: 1}, Counters: map[string]int64{"ok": 3, "failed": 1},
			ChunksDone: 2, UpdatedAt: now,
		},
		Leases: []database.Lease{{Chunk: 2, Worker: 0, LeasedAt: now, Heartbeat: now, ItemIndex: 1, Item: "Hush"}},
		Progress: database.ProgressSnapshot{
			Phase: "sweep", Phases: []string{"sweep"}, Done: 4, Total: &total, Unit: "books",
			Failed: 1, Counters: map[string]int64{"ok": 3, "failed": 1},
			Chunks: &database.ChunkProgress{Done: 2, Total: 4, Size: 2}, Current: "Hush", UpdatedAt: now,
		},
		ItemRows: []database.ItemOutcome{{Key: "Year One", Outcome: "failed", Error: "file is locked by another writer", Chunk: 1}},
		Marks:    map[string][]string{"touched": {"book:year-one", "book:the-long-halloween"}},
	}), "ledger "+id)
	must(w.db.PutIntentV3(id, database.Intent{
		Seq: 1, Subject: "book:hush", Kind: "modify", Planned: json.RawMessage(`{"title":"Hush"}`), WrittenAt: now,
	}), "intent "+id)
	must(w.db.AppendLogV3(id, database.LogLine{At: now, Level: "info", Message: "sweeping the Gotham archive"}), "log "+id)
	must(w.db.AppendLogV3(id, database.LogLine{At: now, Level: "warn", Message: "Year One is locked; skipped"}), "log "+id)
	return id
}

func (w *writer) runKeysFull(id string) []string {
	return append(runKeys(id), "opv3:ckpt:"+id, "opv3:ledger:"+id)
}

// emptyParams is the params of a maintenance op that takes none.
var emptyParams = map[string]any{}

func writeRunRunning(w *writer, label string) {
	id := w.running("maintenance.temp-file-cleanup", emptyParams)
	w.record(label, w.runKeysFull(id)...)
}

// stoppingCases are the stopping reasons (state.StopReasons), one run each.
var stoppingCases = []struct{ label, defID, reason string }{
	{"run-stopping-shutdown", "maintenance.transcode-temp-cleanup", state.StopShutdown},
	{"run-stopping-user", "maintenance.purge-old-logs", state.StopUser},
	{"run-stopping-quiesce", "library.scan", state.StopQuiesce},
	{"run-stopping-timeout", "maintenance.opchange-book-index-rebuild", state.StopTimeout},
	{"run-stopping-watchdog", "maintenance.book-hash-index-repair", state.StopWatchdog},
}

func scanParams() map[string]any { return map[string]any{"folder_path": storeRoot + "/import"} }

func writeRunStopping(w *writer, label string) {
	for _, c := range stoppingCases {
		if c.label != label {
			continue
		}
		var params any = emptyParams
		if c.defID == "library.scan" {
			params = scanParams()
		}
		id := w.running(c.defID, params)
		w.step(id, state.EvStopRequested, database.TransitionRequest{StopReason: c.reason})
		w.record(label, w.runKeysFull(id)...)
	}
}

func writeRunInterruptedCrash(w *writer, label string) {
	id := w.running("maintenance.activity-filter-index-backfill", emptyParams)
	w.step(id, state.EvBootSweep, database.TransitionRequest{InterruptedWhy: state.WhyCrash, Error: "interrupted: the process exited mid-run"})
	w.record(label, w.runKeysFull(id)...)
}

func writeRunInterruptedQuiesced(w *writer, label string) {
	id := w.running("library.scan", scanParams())
	w.step(id, state.EvStopRequested, database.TransitionRequest{StopReason: state.StopQuiesce})
	w.step(id, state.EvGoroutineReturned, database.TransitionRequest{
		StopReason: state.StopQuiesce, InterruptedWhy: state.WhyQuiesced,
		Error: "quiesced: stood down for a maintenance op holding the scan gate",
	})
	w.record(label, w.runKeysFull(id)...)
}

func writeRunQueuedDeps(w *writer, label string) {
	book := firstBookID(w)
	id := w.newRun("dedup.check-book", map[string]any{"subject": map[string]string{"type": "book", "id": book}}, state.WaitDeps)
	w.record(label, runKeys(id)...)
}

func writeRunDropped(w *writer, label string) {
	id := w.running("maintenance.purge-deleted", emptyParams)
	w.step(id, state.EvBootSweep, database.TransitionRequest{InterruptedWhy: state.WhyCrash})
	w.step(id, state.EvDecideDrop, database.TransitionRequest{Error: "dropped by the owner after a crash"})
	w.record(label, runKeys(id)...)
}

// writeRunAwaitingDecision is a repairs plan (a Fixer preview) waiting for its
// approver, with its plan rows stored beside it by HEAD's repairs.SavePlan
// (opv2:row: and opv2:rowix:).
func writeRunAwaitingDecision(w *writer, label string) {
	fixer := "fix-read-by-narrator"
	id := w.running(repairs.PlanOpID, repairs.PlanParams{FixerID: fixer})
	w.step(id, state.EvBootSweep, database.TransitionRequest{InterruptedWhy: state.WhyCrash})
	plan := &repairs.PlanResult{
		FixerID: fixer, PlannedAt: time.Now().UTC(), Total: 2, Applicable: 1,
		SkippedByKind: map[string]int{"owner_manual": 1},
		Rows: []repairs.Row{
			{
				RowID: "row-hush", BookIDs: []string{firstBookID(w)}, Title: "Hush", Author: "Selina Kyle",
				Current: map[string]string{"narrator": "read by Alfred Pennyworth"}, Proposed: map[string]string{"narrator": "Alfred Pennyworth"},
				Reason: "the narrator field carries a 'read by' prefix", Risk: "low", Fingerprint: "r2corpus-hush-1",
			},
			{
				RowID: "row-year-one", Title: "Year One", Author: "Barbara Gordon",
				Current: map[string]string{"narrator": "read by Jim Gordon"}, Proposed: map[string]string{"narrator": "Jim Gordon"},
				Reason: "the narrator field carries a 'read by' prefix", Risk: "low", Fingerprint: "r2corpus-year-one-1",
				Skipped: "owner_manual", SkipReason: "the owner set this narrator by hand",
			},
		},
	}
	must(repairs.SavePlan(w.db, id, plan), "save plan rows")
	w.step(id, state.EvAsk, database.TransitionRequest{})
	w.record(label, w.runKeysFull(id)...)
	w.record(label, "opv2:row:"+id+":"+repairs.PlanRowPrefix+"row-hush", "opv2:rowix:"+id+":"+repairs.PlanRowPrefix+"row-hush",
		"opv2:row:"+id+":"+repairs.PlanRowPrefix+"row-year-one", "opv2:rowix:"+id+":"+repairs.PlanRowPrefix+"row-year-one")
}

// writeRunInterruptedApplyRowMarks is an interrupted Fixer apply with one row
// applied: its per-row apply marker (opv3:rowmark:) says so, and a resume must
// not apply that row again (pebble_store_ops_v3_rowmarks.go).
func writeRunInterruptedApplyRowMarks(w *writer, label string) {
	live := false
	id := w.running(repairs.ApplyOpID, repairs.ApplyParams{
		FixerID: "fix-read-by-narrator", PlanOpID: newID(), RowIDs: []string{"row-hush", "row-knightfall"}, DryRun: &live,
	})
	must(w.db.PutRowMarkV3(id, database.RowMarkV3{RowID: "row-hush", MarkedAt: time.Now().UTC()}), "row mark")
	w.step(id, state.EvBootSweep, database.TransitionRequest{InterruptedWhy: state.WhyCrash})
	w.record(label, w.runKeysFull(id)...)
	w.record(label, "opv3:rowmark:"+id+":row-hush")
}

// ── markers, buckets and stamps ────────────────────────────────────────────

// scanStandDownMarker is internal/operations/registry's persisted form
// (scan_standdown.go scanStandDownMarker), which that package does not
// export; the JSON tags are copied from it at the tag.
type scanStandDownMarker struct {
	HolderOpID     string `json:"holder_op_id"`
	ScanOpID       string `json:"scan_op_id"`
	LeaseExpiryUTC int64  `json:"lease_expiry_unix_nano"`
}

// writeStandDownMarker leaves a stand-down marker as a crash of the holder
// leaves it: a maintenance op holds the scan gate over a quiesced scan. The
// lease expired when it was written, so R0's boot on the overlay reads it as
// stale and does not keep the scan stood down.
func writeStandDownMarker(w *writer, label string) {
	scan := w.running("library.scan", scanParams())
	w.step(scan, state.EvStopRequested, database.TransitionRequest{StopReason: state.StopQuiesce})
	w.step(scan, state.EvGoroutineReturned, database.TransitionRequest{StopReason: state.StopQuiesce, InterruptedWhy: state.WhyQuiesced})
	holder := w.running("maintenance.metadata-refresh", emptyParams)
	w.step(holder, state.EvBootSweep, database.TransitionRequest{InterruptedWhy: state.WhyCrash})
	blob, err := json.Marshal(scanStandDownMarker{HolderOpID: holder, ScanOpID: scan, LeaseExpiryUTC: time.Now().UnixNano()})
	must(err, "encode stand-down marker")
	must(w.db.SetSetting("registry.scan_standdown", string(blob), "json", false), "write stand-down marker")
	w.record(label, "setting:registry.scan_standdown")
	w.record(label, w.runKeysFull(scan)...)
	w.record(label, w.runKeysFull(holder)...)
}

// writeDebounceBucket leaves one pending subject in a Batchable op's
// debounce bucket (op:batch:), as a crash inside the batch window does.
func writeDebounceBucket(w *writer, label string) {
	book := firstBookID(w)
	must(w.db.AddToBatchBucket("dedup.check-book", database.OpSubject{Type: "book", ID: book}), "batch bucket")
	w.record(label, "op:batch:dedup.check-book:book:"+book)
}

// writeRequiresStamps records a dependency revision and a completion stamp for
// one book, as the registry's dependency scheduler does when a run with a
// subject completes (deps_scheduler.go).
func writeRequiresStamps(w *writer, label string) {
	book := firstBookID(w)
	sub := database.OpSubject{Type: "book", ID: book}
	rev, err := w.db.BumpDepRev(sub)
	must(err, "bump dep rev")
	must(w.db.RecordOpCompletion(sub, "dedup.check-book", "", rev), "record completion")
	w.record(label, "op:deprev:book:"+book, "op:completion:book:"+book+":dedup.check-book")
}

// writeCorruptRun leaves a finished run whose record no longer decodes while
// its progress half and its opv2 mirror row stay intact. With the mirror row
// current (its hash matches the progress row), HEAD's boot reconcile
// (ReconcileOpsV3WithV2) neither rebuilds nor deletes it, so the record is
// still undecodable when the tasks step reads it.
func writeCorruptRun(w *writer, label string) {
	id := w.running("maintenance.transcode-temp-cleanup", emptyParams)
	w.step(id, state.EvReturnNil, database.TransitionRequest{Result: json.RawMessage(`{"removed":0}`)})
	w.setTorn("opv3:run:"+id, `{"v":3,"id":"`+id+`","def_id":"maintenance.transcode-temp-cleanup","state":"succ`)
	w.record(label, "opv3:run:"+id)
}

var firstBook string

// firstBookID is a stage-1 book (the subject of the stamp, bucket and
// dependency-wait cases).
func firstBookID(w *writer) string {
	if firstBook != "" {
		return firstBook
	}
	books, err := w.db.GetAllBooksCore(1, 0)
	must(err, "list books")
	if len(books) == 0 {
		must(fmt.Errorf("stage 1 has no book"), "list books")
	}
	firstBook = books[0].ID
	return firstBook
}
