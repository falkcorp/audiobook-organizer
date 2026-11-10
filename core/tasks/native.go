// file: core/tasks/native.go
// version: 1.0.0
// guid: b82512e6-4c65-4b2a-b347-85d82bd5b3c7
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// valueVersion is the codec.JSON version of every c/tasks/ value (5T.6.1).
const valueVersion = 1

// maintenanceAttempts bounds RetryOnConflict for the batches Prune and
// ReplaceRows commit themselves.
const maintenanceAttempts = 16

// pruneChunkRuns and replaceChunkRows bound one maintenance batch.
const (
	pruneChunkRuns   = 64
	replaceChunkRows = 256
)

// NativeStores is core's implementation over the c/tasks/ keyspace (5T.6.1).
// Every store is safe for concurrent use: none holds mutable state, every
// write is staged into the caller's batch (Prune and ReplaceRows excepted)
// and every precondition is the store's commit-time check on a guard key.
//
// Beyond the interfaces, Stores.Runs also offers NewBatch (an indexed batch
// over ks), Importer (NativeImporter over the same ks), and Marks and
// Strikes (readers of the applied-row marks and watchdog strikes, which no
// RunStore method reads); taskstest.StoresSuite finds them by type assertion.
func NativeStores(ks storage.Keyspace) Stores {
	st := taskstore.New(ks)
	return Stores{
		Runs:      &nativeRuns{st: st},
		Clocks:    nativeClocks{st: st},
		Pause:     nativePause{st: st},
		StandDown: nativeStandDown{st: st},
	}
}

// encodeValue is codec.JSON[T](valueVersion).Encode.
func encodeValue[T any](v T) ([]byte, error) { return codec.JSON[T](valueVersion).Encode(v) }

// decodeValue is codec.JSON[T](valueVersion).Decode.
func decodeValue[T any](b []byte) (T, error) { return codec.JSON[T](valueVersion).Decode(b) }

// putValue encodes v and stages it at key.
func putValue[T any](st *taskstore.Store, b *storage.Batch, key []byte, v T) error {
	raw, err := encodeValue(v)
	if err != nil {
		return err
	}
	return st.Set(b, key, raw)
}

// decodeRun decodes a stored run row.
func decodeRun(v []byte) (RunRecord, error) {
	r, err := decodeValue[RunRecord](v)
	if err != nil {
		return RunRecord{}, fmt.Errorf("tasks: undecodable run record: %w", err)
	}
	return r, nil
}

// knownState reports the RunState vocabulary NativeStores stores.
func knownState(s RunState) bool {
	switch s {
	case StateQueued, StateWaitingDeps, StateRunning, StateStopping, StateInterrupted,
		StateInterruptedQuiesced, StateAwaitingDecision, StateSucceeded, StateFailed,
		StateTimedOut, StateCanceled, StateInterruptedDropped:
		return true
	default:
		return false
	}
}

// unfinishedStates are the non-terminal states, in the order Unfinished
// yields them.
func unfinishedStates() []RunState {
	return []RunState{StateQueued, StateWaitingDeps, StateRunning, StateStopping, StateInterrupted,
		StateInterruptedQuiesced, StateAwaitingDecision}
}

// validRecord is what every stored run row must satisfy.
func validRecord(r RunRecord) error {
	switch {
	case r.ID == "":
		return errors.New("tasks: run record without an ID")
	case r.Kind == "":
		return fmt.Errorf("tasks: run %s without a kind", r.ID)
	case !knownState(r.State):
		return fmt.Errorf("tasks: run %s in unknown state %q", r.ID, r.State)
	}
	return nil
}

// runIndex is the derived-key view of a run: a queue entry while queued
// (enqueued at Created, so a re-queued run keeps its place), a done-index
// entry while terminal (at Finished).
func runIndex(r RunRecord) taskstore.RunIndex {
	return taskstore.RunIndex{
		ID:       string(r.ID),
		Kind:     string(r.Kind),
		State:    string(r.State),
		Queued:   r.State == StateQueued,
		Priority: uint8(r.Priority),
		Enqueued: r.Created,
		Terminal: r.State.Terminal(),
		Finished: r.Finished,
	}
}

// putRun stages r as the run row and moves its derived keys from prev (nil
// for a new run). Held is computed at read time and never stored (5T.3.11).
func putRun(st *taskstore.Store, b *storage.Batch, r RunRecord, prev *taskstore.RunIndex) error {
	if err := validRecord(r); err != nil {
		return err
	}
	r.Held = false
	r.Params, r.Result = jsonOrNil(r.Params), jsonOrNil(r.Result)
	raw, err := encodeValue(r)
	if err != nil {
		return err
	}
	return st.PutRun(b, raw, prev, runIndex(r))
}

// insertRun stages a new run under the guard precondition that its row is
// absent: ErrRunConflict at staging when b already sees it, storage's
// ErrConflict at commit when another batch wrote it first.
func insertRun(ctx context.Context, st *taskstore.Store, b *storage.Batch, r RunRecord) error {
	if err := validRecord(r); err != nil {
		return err
	}
	if _, found, err := st.ReadForUpdate(ctx, b, taskstore.RunKey(string(r.ID))); err != nil {
		return err
	} else if found {
		return fmt.Errorf("%w: run %s already exists", ErrRunConflict, r.ID)
	}
	return putRun(st, b, r, nil)
}

// jsonOrNil maps an empty json.RawMessage, which encoding/json refuses to
// marshal, to nil (stored as null).
func jsonOrNil(m json.RawMessage) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	return m
}

// intentEntry is the stored form of one Preview intent.
type intentEntry struct {
	Write   Write
	Changes []Change
}

// checkpointValue wraps checkpoint bytes, so the value is a JSON object.
type checkpointValue struct{ Data []byte }

// markValue is an applied-row mark; the key carries everything.
type markValue struct{}

// stampValue is a Requires stamp.
type stampValue struct{ At time.Time }

func stagePage[T any](st *taskstore.Store, b *storage.Batch, id RunID, p taskstore.Page, cursor keys.Seg, v T) error {
	return putValue(st, b, taskstore.PageKey(string(id), p, cursor), v)
}

func stageItem(st *taskstore.Store, b *storage.Batch, id RunID, key string, o ItemOutcome) error {
	if key == "" {
		return fmt.Errorf("tasks: item outcome of run %s without a key", id)
	}
	return stagePage(st, b, id, taskstore.PageItems, keys.Str(key), o)
}

func stageCheckpoint(st *taskstore.Store, b *storage.Batch, id RunID, name string, v []byte) error {
	if name == "" {
		return fmt.Errorf("tasks: checkpoint of run %s without a name", id)
	}
	return putValue(st, b, taskstore.CheckpointKey(string(id), name), checkpointValue{Data: v})
}

func stageChunk(st *taskstore.Store, b *storage.Batch, id RunID, c Chunk) error {
	if c.N < 0 {
		return fmt.Errorf("tasks: chunk %d of run %s: a chunk number is never negative", c.N, id)
	}
	return putValue(st, b, taskstore.ChunkKey(string(id), uint64(c.N)), c)
}

func stageRow(st *taskstore.Store, b *storage.Batch, id RunID, row Row) error {
	if row.ID == "" {
		return fmt.Errorf("tasks: row of run %s without an ID", id)
	}
	return putValue(st, b, taskstore.RowKey(string(id), row.ID), row)
}

func stageMark(st *taskstore.Store, b *storage.Batch, id RunID, rowID string) error {
	if rowID == "" {
		return fmt.Errorf("tasks: mark of run %s without a row ID", id)
	}
	return putValue(st, b, taskstore.MarkKey(string(id), rowID), markValue{})
}

func stageStandDown(st *taskstore.Store, b *storage.Batch, key Key, s *StandDown) error {
	if key == "" {
		return errors.New("tasks: stand-down marker without a key")
	}
	k := taskstore.StandDownKey(string(key))
	if s == nil {
		return st.Delete(b, k)
	}
	v := *s
	v.Key = key
	return putValue(st, b, k, v)
}

func stageStamp(st *taskstore.Store, b *storage.Batch, subject entity.Ref, k Kind, at time.Time) error {
	if k == "" || subject.Module == "" || subject.Kind == "" {
		return fmt.Errorf("tasks: stamp of %q for %v: the kind and the subject's module and kind must be set", k, subject)
	}
	s := taskstore.Subject{Module: subject.Module, Kind: subject.Kind, ID: subject.ID}
	if err := putValue(st, b, taskstore.ReqKey(s, string(k)), stampValue{At: at}); err != nil {
		return err
	}
	return putValue(st, b, taskstore.ReqRevKey(string(k), s), stampValue{At: at})
}

// stageDebounce merges d into the bucket stored under its kind and name,
// read through b under the bucket's guard precondition, so a concurrent
// merge fails the commit and the caller's retry merges again from a fresh
// read (5T.7): subjects are unioned in arrival order, First keeps the
// earliest arrival, Last the latest, and Params the latest non-empty value.
// A d whose First is zero is a flush: it deletes the bucket.
func stageDebounce(ctx context.Context, st *taskstore.Store, b *storage.Batch, d DebounceBucket) error {
	if d.Kind == "" {
		return errors.New("tasks: debounce bucket without a kind")
	}
	key := taskstore.DebounceKey(string(d.Kind), d.Bucket)
	v, found, err := st.ReadForUpdate(ctx, b, key)
	if err != nil {
		return err
	}
	if d.First.IsZero() {
		if !found {
			return nil
		}
		return st.Delete(b, key)
	}
	merged := DebounceBucket{Kind: d.Kind, Bucket: d.Bucket, First: d.First, Last: d.Last, Params: jsonOrNil(d.Params)}
	if found {
		cur, err := decodeValue[DebounceBucket](v)
		if err != nil {
			return fmt.Errorf("tasks: debounce bucket %s/%s: %w", d.Kind, d.Bucket, err)
		}
		merged.Subjects = cur.Subjects
		if !cur.First.IsZero() && cur.First.Before(merged.First) {
			merged.First = cur.First
		}
		if cur.Last.After(merged.Last) {
			merged.Last = cur.Last
		}
		if len(merged.Params) == 0 {
			merged.Params = cur.Params
		}
	}
	for _, s := range d.Subjects {
		if !slices.Contains(merged.Subjects, s) {
			merged.Subjects = append(merged.Subjects, s)
		}
	}
	return putValue(st, b, key, merged)
}

// nativeRuns is NativeStores' RunStore.
type nativeRuns struct {
	st *taskstore.Store
}

// NewBatch is an indexed batch over the c/tasks/ keyspace, for a build that
// holds no other handle (WriteRuns' open, a test). It is indexed so that a
// second write of one run in the build reads the first.
func (r *nativeRuns) NewBatch() *storage.Batch { return r.st.NewBatch() }

// Importer is NativeImporter over the same keyspace.
func (r *nativeRuns) Importer() Importer { return &nativeImporter{st: r.st} }

// NewID implements RunStore.NewID: a ULID.
func (r *nativeRuns) NewID(context.Context) (RunID, error) {
	id, err := taskstore.NewULID(time.Now(), rand.Reader)
	return RunID(id), err
}

// Insert implements RunStore.Insert.
func (r *nativeRuns) Insert(ctx context.Context, b *storage.Batch, rec RunRecord) error {
	return insertRun(ctx, r.st, b, rec)
}

// Get implements RunStore.Get.
func (r *nativeRuns) Get(ctx context.Context, id RunID) (RunRecord, error) {
	v, found, err := r.st.Get(ctx, taskstore.RunKey(string(id)))
	if err != nil {
		return RunRecord{}, err
	}
	if !found {
		return RunRecord{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	return decodeRun(v)
}

// readRun reads a run through b under its guard precondition.
func (r *nativeRuns) readRun(ctx context.Context, b *storage.Batch, id RunID) (RunRecord, error) {
	v, found, err := r.st.ReadForUpdate(ctx, b, taskstore.RunKey(string(id)))
	if err != nil {
		return RunRecord{}, err
	}
	if !found {
		return RunRecord{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	return decodeRun(v)
}

// openRun reads a run under its guard precondition and, for the
// closed-ledger family (progress, checkpoints, chunks, rows, marks),
// refuses a run that is terminal: HEAD's closed-ledger rule
// (pebble_store_ops_v3.go:1655-1666; rowmarks.go:54-56), applied to the read
// the write derives from. A write staged over a non-terminal read that
// another actor then made terminal fails at commit (the guard precondition)
// and is refused here on the retry's fresh read.
func (r *nativeRuns) openRun(ctx context.Context, b *storage.Batch, id RunID, closedLedger bool) (RunRecord, error) {
	rec, err := r.readRun(ctx, b, id)
	if err != nil {
		return RunRecord{}, err
	}
	if closedLedger && rec.State.Terminal() {
		return RunRecord{}, fmt.Errorf("%w: run %s is %s; its ledger is closed", ErrRunConflict, id, rec.State)
	}
	return rec, nil
}

// Transition implements RunStore.Transition: next replaces the row when the
// row b reads still has want's state, attempt and fence epoch, else
// ErrRunConflict. next.ID and next.Kind default to the run's; a different
// ID or kind is refused (a kind changes only through Importer.RenameKind).
func (r *nativeRuns) Transition(ctx context.Context, b *storage.Batch, id RunID, want Expect, next RunRecord) error {
	cur, err := r.readRun(ctx, b, id)
	if err != nil {
		return err
	}
	if cur.State != want.State || cur.Attempt != want.Attempt || cur.FenceEpoch != want.FenceEpoch {
		return fmt.Errorf("%w: run %s is %s attempt %d fence %d, want %s attempt %d fence %d",
			ErrRunConflict, id, cur.State, cur.Attempt, cur.FenceEpoch, want.State, want.Attempt, want.FenceEpoch)
	}
	if next.ID == "" {
		next.ID = id
	}
	if next.Kind == "" {
		next.Kind = cur.Kind
	}
	if next.ID != id || next.Kind != cur.Kind {
		return fmt.Errorf("tasks: Transition of run %s (%s) to a record of run %s (%s)", id, cur.Kind, next.ID, next.Kind)
	}
	prev := runIndex(cur)
	return putRun(r.st, b, next, &prev)
}

// Progress implements RunStore.Progress.
func (r *nativeRuns) Progress(ctx context.Context, b *storage.Batch, id RunID, done, total int, current string) error {
	cur, err := r.openRun(ctx, b, id, true)
	if err != nil {
		return err
	}
	prev := runIndex(cur)
	cur.Done, cur.Total, cur.Current = done, total, current
	return putRun(r.st, b, cur, &prev)
}

// records yields the run rows the ids name, skipping IDs whose row is gone
// (an index entry read before a concurrent delete). An undecodable row is
// yielded as an error and the walk goes on if the consumer continues (5T.5:
// open, hidden from listings).
func (r *nativeRuns) records(ctx context.Context, ids iter.Seq2[string, error]) iter.Seq2[RunRecord, error] {
	return func(yield func(RunRecord, error) bool) {
		for id, err := range ids {
			if err != nil {
				yield(RunRecord{}, err)
				return
			}
			v, found, err := r.st.Get(ctx, taskstore.RunKey(id))
			if err != nil {
				yield(RunRecord{}, err)
				return
			}
			if !found {
				continue
			}
			rec, err := decodeRun(v)
			if err != nil {
				if !yield(RunRecord{}, fmt.Errorf("run %s: %w", id, err)) {
					return
				}
				continue
			}
			if !yield(rec, nil) {
				return
			}
		}
	}
}

// Queued implements RunStore.Queued: queue order, priority first
// (PrioritySystem first), then enqueue time.
func (r *nativeRuns) Queued(ctx context.Context) iter.Seq2[RunRecord, error] {
	return r.records(ctx, r.st.IDs(ctx, taskstore.QueuePrefix()))
}

// Unfinished implements RunStore.Unfinished: every non-terminal run, state
// by state.
func (r *nativeRuns) Unfinished(ctx context.Context) iter.Seq2[RunRecord, error] {
	return func(yield func(RunRecord, error) bool) {
		for _, s := range unfinishedStates() {
			for rec, err := range r.records(ctx, r.st.IDs(ctx, taskstore.StateIndexPrefix(string(s)))) {
				if !yield(rec, err) {
					return
				}
			}
		}
	}
}

// listLimit is RunFilter.Limit's clamp: 0 = 100, above 1000 = 1000.
func listLimit(n int) int {
	switch {
	case n <= 0:
		return 100
	case n > 1000:
		return 1000
	default:
		return n
	}
}

// listedBefore reports whether a lists before b: Created descending, then
// RunID descending.
func listedBefore(a, b RunRecord) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.After(b.Created)
	}
	return a.ID > b.ID
}

// List implements RunStore.List. The candidates come from the state index
// when States is set, else from the kind index when Kinds is set, else from
// every run row; every filter is then applied to the decoded row, the
// matches are sorted into RunFilter order and the page after After is
// yielded. Retention (Prune) bounds the history it reads.
func (r *nativeRuns) List(ctx context.Context, q RunQuery) iter.Seq2[RunRecord, error] {
	return func(yield func(RunRecord, error) bool) {
		var cursor *RunRecord
		if q.After != "" {
			c, err := r.Get(ctx, q.After)
			if err != nil {
				yield(RunRecord{}, fmt.Errorf("tasks: list cursor: %w", err))
				return
			}
			cursor = &c
		}
		var matches []RunRecord
		for rec, err := range r.candidates(ctx, q) {
			if err != nil {
				if !yield(RunRecord{}, err) {
					return
				}
				continue
			}
			if listMatches(q, rec) && (cursor == nil || listedBefore(*cursor, rec)) {
				matches = append(matches, rec)
			}
		}
		slices.SortFunc(matches, func(a, b RunRecord) int {
			switch {
			case listedBefore(a, b):
				return -1
			case listedBefore(b, a):
				return 1
			default:
				return 0
			}
		})
		for i, rec := range matches {
			if i == listLimit(q.Limit) || !yield(rec, nil) {
				return
			}
		}
	}
}

// candidates is List's source of rows (see List).
func (r *nativeRuns) candidates(ctx context.Context, q RunQuery) iter.Seq2[RunRecord, error] {
	var prefixes [][]byte
	switch {
	case len(q.States) > 0:
		for _, s := range q.States {
			prefixes = append(prefixes, taskstore.StateIndexPrefix(string(s)))
		}
	case len(q.Kinds) > 0:
		for _, k := range q.Kinds {
			prefixes = append(prefixes, taskstore.KindIndexPrefix(string(k)))
		}
	default:
		return func(yield func(RunRecord, error) bool) {
			for kv, err := range r.st.Scan(ctx, taskstore.RunPrefix(), nil, 0) {
				if err != nil {
					yield(RunRecord{}, err)
					return
				}
				rec, err := decodeRun(kv.Value)
				if !yield(rec, err) {
					return
				}
			}
		}
	}
	return func(yield func(RunRecord, error) bool) {
		seen := map[string]bool{}
		for _, p := range prefixes {
			ids := func(yield func(string, error) bool) {
				for id, err := range r.st.IDs(ctx, p) {
					if err == nil && seen[id] {
						continue
					}
					seen[id] = true
					if !yield(id, err) {
						return
					}
				}
			}
			for rec, err := range r.records(ctx, ids) {
				if !yield(rec, err) {
					return
				}
			}
		}
	}
}

// listMatches applies every RunQuery filter but the cursor.
func listMatches(q RunQuery, rec RunRecord) bool {
	if len(q.Kinds) > 0 && !slices.Contains(q.Kinds, rec.Kind) {
		return false
	}
	if len(q.States) > 0 && !slices.Contains(q.States, rec.State) {
		return false
	}
	if q.Parent != "" && rec.Parent != q.Parent {
		return false
	}
	return q.Since.IsZero() || !rec.Created.Before(q.Since)
}

// Counts implements RunStore.Counts from the state index.
func (r *nativeRuns) Counts(ctx context.Context) (map[RunState]int64, error) {
	raw, err := r.st.StateCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[RunState]int64, len(raw))
	for s, n := range raw {
		out[RunState(s)] = n
	}
	return out, nil
}

// AppendEvent implements RunStore.AppendEvent.
func (r *nativeRuns) AppendEvent(ctx context.Context, b *storage.Batch, id RunID, seq uint64, e RunEvent) error {
	if _, err := r.openRun(ctx, b, id, false); err != nil {
		return err
	}
	return stagePage(r.st, b, id, taskstore.PageEvents, taskstore.SeqSeg(seq), e)
}

// AppendIntent implements RunStore.AppendIntent.
func (r *nativeRuns) AppendIntent(ctx context.Context, b *storage.Batch, id RunID, seq uint64, w Write, c []Change) error {
	if _, err := r.openRun(ctx, b, id, false); err != nil {
		return err
	}
	return stagePage(r.st, b, id, taskstore.PageIntents, taskstore.SeqSeg(seq), intentEntry{Write: w, Changes: c})
}

// PutItem implements RunStore.PutItem.
func (r *nativeRuns) PutItem(ctx context.Context, b *storage.Batch, id RunID, key string, o ItemOutcome) error {
	if _, err := r.openRun(ctx, b, id, false); err != nil {
		return err
	}
	return stageItem(r.st, b, id, key, o)
}

// AppendLog implements RunStore.AppendLog.
func (r *nativeRuns) AppendLog(ctx context.Context, b *storage.Batch, id RunID, seq uint64, l LogLine) error {
	if _, err := r.openRun(ctx, b, id, false); err != nil {
		return err
	}
	return stagePage(r.st, b, id, taskstore.PageLog, taskstore.SeqSeg(seq), l)
}

// pageLimit is PageQuery.Limit's clamp: 0 = 100, above 1000 = 1000.
func pageLimit(n int) int { return listLimit(n) }

// pageOf maps a PageKind to its key segment.
func pageOf(k PageKind) (taskstore.Page, bool) {
	switch k {
	case PageEvents:
		return taskstore.PageEvents, true
	case PageIntents:
		return taskstore.PageIntents, true
	case PageItems:
		return taskstore.PageItems, true
	case PageLog:
		return taskstore.PageLog, true
	default:
		return "", false
	}
}

// Pages implements RunStore.Pages: one page of a run in seq order (item
// outcomes in key order), after the keyset cursor q.After, at most
// q.Limit entries (after the Outcome filter, for items).
func (r *nativeRuns) Pages(ctx context.Context, id RunID, q PageQuery) iter.Seq2[PageEntry, error] {
	return func(yield func(PageEntry, error) bool) {
		p, ok := pageOf(q.Page)
		if !ok {
			yield(PageEntry{}, fmt.Errorf("tasks: Pages of run %s: unknown page %d", id, q.Page))
			return
		}
		var after []byte
		if q.After != "" {
			if p == taskstore.PageItems {
				after = taskstore.PageKey(string(id), p, keys.Str(q.After))
			} else {
				seq, err := strconv.ParseUint(q.After, 10, 64)
				if err != nil {
					yield(PageEntry{}, fmt.Errorf("tasks: Pages of run %s: cursor %q: %w", id, q.After, err))
					return
				}
				after = taskstore.PageKey(string(id), p, taskstore.SeqSeg(seq))
			}
		}
		n, limit := 0, pageLimit(q.Limit)
		for kv, err := range r.st.Scan(ctx, taskstore.PagePrefix(string(id), p), after, 0) {
			if err != nil {
				yield(PageEntry{}, err)
				return
			}
			e, err := decodePage(q.Page, kv)
			if err != nil {
				yield(PageEntry{}, fmt.Errorf("tasks: page of run %s: %w", id, err))
				return
			}
			if q.Page == PageItems && q.Outcome != "" && !strings.HasPrefix(e.Item.Outcome, q.Outcome) {
				continue
			}
			if !yield(e, nil) {
				return
			}
			if n++; n == limit {
				return
			}
		}
	}
}

// decodePage decodes one stored page entry.
func decodePage(k PageKind, kv storage.KV) (PageEntry, error) {
	segs, err := taskstore.Segments(kv.Key)
	if err != nil || len(segs) != 4 {
		return PageEntry{}, errors.New("malformed page key")
	}
	cursor := segs[3]
	e := PageEntry{Page: k}
	if k == PageItems {
		e.Key = string(cursor)
		e.Item, err = decodeValue[ItemOutcome](kv.Value)
		return e, err
	}
	if len(cursor) != 8 {
		return PageEntry{}, errors.New("malformed page seq")
	}
	for _, c := range cursor {
		e.Seq = e.Seq<<8 | uint64(c)
	}
	e.Key = strconv.FormatUint(e.Seq, 10)
	switch k {
	case PageEvents:
		e.Event, err = decodeValue[RunEvent](kv.Value)
	case PageIntents:
		var in intentEntry
		in, err = decodeValue[intentEntry](kv.Value)
		e.Write, e.Changes = in.Write, in.Changes
	default:
		e.Log, err = decodeValue[LogLine](kv.Value)
	}
	return e, err
}

// SaveCheckpoint implements RunStore.SaveCheckpoint.
func (r *nativeRuns) SaveCheckpoint(ctx context.Context, b *storage.Batch, id RunID, name string, v []byte) error {
	if _, err := r.openRun(ctx, b, id, true); err != nil {
		return err
	}
	return stageCheckpoint(r.st, b, id, name, v)
}

// Checkpoint implements RunStore.Checkpoint.
func (r *nativeRuns) Checkpoint(ctx context.Context, id RunID, name string) ([]byte, bool, error) {
	v, found, err := r.st.Get(ctx, taskstore.CheckpointKey(string(id), name))
	if err != nil || !found {
		return nil, false, err
	}
	c, err := decodeValue[checkpointValue](v)
	if err != nil {
		return nil, false, fmt.Errorf("tasks: checkpoint %q of run %s: %w", name, id, err)
	}
	return c.Data, true, nil
}

// SaveChunk implements RunStore.SaveChunk.
func (r *nativeRuns) SaveChunk(ctx context.Context, b *storage.Batch, id RunID, c Chunk) error {
	if _, err := r.openRun(ctx, b, id, true); err != nil {
		return err
	}
	return stageChunk(r.st, b, id, c)
}

// scanValues decodes every value under prefix, in key order.
func scanValues[T any](ctx context.Context, st *taskstore.Store, prefix []byte) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		for kv, err := range st.Scan(ctx, prefix, nil, 0) {
			if err != nil {
				yield(zero, err)
				return
			}
			v, err := decodeValue[T](kv.Value)
			if err != nil {
				yield(zero, err)
				return
			}
			if !yield(v, nil) {
				return
			}
		}
	}
}

// Chunks implements RunStore.Chunks, in chunk-number order.
func (r *nativeRuns) Chunks(ctx context.Context, id RunID) iter.Seq2[Chunk, error] {
	return scanValues[Chunk](ctx, r.st, taskstore.ChunkPrefix(string(id)))
}

// ReplaceRows implements RunStore.ReplaceRows (see RunStore): one batch
// clears the rows under prefix, then the n rows are written in batches of
// replaceChunkRows; each batch carries the run's guard precondition, is
// refused once the run is terminal and commits under RetryOnConflict.
func (r *nativeRuns) ReplaceRows(ctx context.Context, id RunID, prefix string, n int, at func(i int) (Row, error)) error {
	if prefix == "" {
		return fmt.Errorf("tasks: ReplaceRows of run %s with an empty prefix: refused (it would delete every writer's rows)", id)
	}
	if n < 0 || (n > 0 && at == nil) {
		return fmt.Errorf("tasks: ReplaceRows of run %s: %d rows and no source", id, n)
	}
	commit := func(stage func(b *storage.Batch) error) error {
		return storage.RetryOnConflict(ctx, maintenanceAttempts, func(ctx context.Context) (err error) {
			b := r.st.Keyspace().NewBatch()
			defer func() { err = errors.Join(err, b.Close()) }()
			if _, err := r.openRun(ctx, b, id, true); err != nil {
				return err
			}
			if err := stage(b); err != nil {
				return err
			}
			return b.Commit(ctx, storage.Sync)
		})
	}
	if err := commit(func(b *storage.Batch) error {
		return r.st.DeletePrefix(b, taskstore.RowIDPrefix(string(id), prefix))
	}); err != nil {
		return err
	}
	for start := 0; start < n; start += replaceChunkRows {
		end := min(start+replaceChunkRows, n)
		rows := make([]Row, 0, end-start)
		for i := start; i < end; i++ {
			row, err := at(i)
			if err != nil {
				return fmt.Errorf("tasks: ReplaceRows of run %s: row %d: %w", id, i, err)
			}
			if !strings.HasPrefix(row.ID, prefix) {
				return fmt.Errorf("tasks: ReplaceRows of run %s: row %q does not begin with %q", id, row.ID, prefix)
			}
			rows = append(rows, row)
		}
		if err := commit(func(b *storage.Batch) error {
			for _, row := range rows {
				if err := stageRow(r.st, b, id, row); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// Rows implements RunStore.Rows, in row-ID order.
func (r *nativeRuns) Rows(ctx context.Context, id RunID) iter.Seq2[Row, error] {
	return scanValues[Row](ctx, r.st, taskstore.RowPrefix(string(id)))
}

// MarkRow implements RunStore.MarkRow.
func (r *nativeRuns) MarkRow(ctx context.Context, b *storage.Batch, id RunID, rowID string) error {
	if _, err := r.openRun(ctx, b, id, true); err != nil {
		return err
	}
	return stageMark(r.st, b, id, rowID)
}

// Marks yields the row IDs of a run's applied-row marks, in row-ID order.
// No RunStore method reads them; the executor's resume reads them here.
func (r *nativeRuns) Marks(ctx context.Context, id RunID) iter.Seq2[string, error] {
	return r.st.IDs(ctx, taskstore.MarkPrefix(string(id)))
}

// Debounce implements RunStore.Debounce: d is merged into the stored bucket
// under the bucket's guard precondition (stageDebounce); a d whose First is
// zero deletes the bucket (the flush).
func (r *nativeRuns) Debounce(ctx context.Context, b *storage.Batch, d DebounceBucket) error {
	return stageDebounce(ctx, r.st, b, d)
}

// DebounceBuckets implements RunStore.DebounceBuckets, kind by kind.
func (r *nativeRuns) DebounceBuckets(ctx context.Context) iter.Seq2[DebounceBucket, error] {
	return scanValues[DebounceBucket](ctx, r.st, taskstore.DebouncePrefix(""))
}

// Strike implements RunStore.Strike: each strike takes a fresh number from
// a durable sequence, so two strikes never share a key.
func (r *nativeRuns) Strike(ctx context.Context, b *storage.Batch, id RunID, s Strike) error {
	if _, err := r.openRun(ctx, b, id, false); err != nil {
		return err
	}
	n, err := r.st.NextStrike(ctx, b)
	if err != nil {
		return err
	}
	return putValue(r.st, b, taskstore.StrikeKey(string(id), n), s)
}

// Strikes yields a run's watchdog strikes in the order they were staged. No
// RunStore method reads them; the watchdog reads them here.
func (r *nativeRuns) Strikes(ctx context.Context, id RunID) iter.Seq2[Strike, error] {
	return scanValues[Strike](ctx, r.st, taskstore.StrikePrefix(string(id)))
}

// Delete implements RunStore.Delete.
func (r *nativeRuns) Delete(ctx context.Context, b *storage.Batch, id RunID) error {
	rec, err := r.readRun(ctx, b, id)
	if err != nil {
		return err
	}
	if !rec.State.Terminal() {
		return fmt.Errorf("%w: run %s is %s", ErrRunActive, id, rec.State)
	}
	return r.st.DeleteRun(b, runIndex(rec))
}

// Stamp implements RunStore.Stamp.
func (r *nativeRuns) Stamp(_ context.Context, b *storage.Batch, subject entity.Ref, k Kind, at time.Time) error {
	return stageStamp(r.st, b, subject, k, at)
}

// Stamped implements RunStore.Stamped.
func (r *nativeRuns) Stamped(ctx context.Context, subject entity.Ref, k Kind) (time.Time, bool, error) {
	s := taskstore.Subject{Module: subject.Module, Kind: subject.Kind, ID: subject.ID}
	v, found, err := r.st.Get(ctx, taskstore.ReqKey(s, string(k)))
	if err != nil || !found {
		return time.Time{}, false, err
	}
	at, err := decodeValue[stampValue](v)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("tasks: stamp of %s for %v: %w", k, subject, err)
	}
	return at.At, true, nil
}

// Prune implements RunStore.Prune: up to limit runs of the done index
// finished before before are deleted, pruneChunkRuns per batch, each batch
// committed under RetryOnConflict and rebuilt from fresh reads on a
// conflict. A run whose fresh read is no longer terminal, or no longer
// finished before before, is skipped (DATA8-8).
func (r *nativeRuns) Prune(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit < 1 {
		return 0, fmt.Errorf("tasks: Prune limit %d: must be at least 1", limit)
	}
	ids, err := r.st.DoneBefore(ctx, before, limit)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for start := 0; start < len(ids); start += pruneChunkRuns {
		n, err := r.pruneChunk(ctx, ids[start:min(start+pruneChunkRuns, len(ids))], before)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// pruneChunk deletes, in one batch, the runs of ids that a fresh read finds
// terminal and finished before before; it returns how many it deleted.
func (r *nativeRuns) pruneChunk(ctx context.Context, ids []string, before time.Time) (int, error) {
	n := 0
	err := storage.RetryOnConflict(ctx, maintenanceAttempts, func(ctx context.Context) (err error) {
		n = 0
		b := r.st.Keyspace().NewBatch()
		defer func() { err = errors.Join(err, b.Close()) }()
		for _, id := range ids {
			v, found, err := r.st.ReadForUpdate(ctx, b, taskstore.RunKey(id))
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			rec, err := decodeRun(v)
			if err != nil || !rec.State.Terminal() || !rec.Finished.Before(before) {
				continue // undecodable rows are left for an operator, never guessed at
			}
			if err := r.st.DeleteRun(b, runIndex(rec)); err != nil {
				return err
			}
			n++
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// nativeClocks is NativeStores' ClockStore over sched/<kind>.
type nativeClocks struct{ st *taskstore.Store }

// Get implements ClockStore.Get.
func (c nativeClocks) Get(ctx context.Context, k Kind) (Clock, bool, error) {
	v, found, err := c.st.Get(ctx, taskstore.SchedKey(string(k)))
	if err != nil || !found {
		return Clock{}, false, err
	}
	clk, err := decodeValue[Clock](v)
	if err != nil {
		return Clock{}, false, fmt.Errorf("tasks: clock of %s: %w", k, err)
	}
	return clk, true, nil
}

// sameClock compares clocks by instant, not by location or monotonic
// reading.
func sameClock(a, b Clock) bool {
	return a.LastDue.Equal(b.LastDue) && a.LastFire.Equal(b.LastFire) && a.LastRun.Equal(b.LastRun) &&
		a.Missed == b.Missed && a.GateClosedAt.Equal(b.GateClosedAt) && a.ScheduleHash == b.ScheduleHash
}

// Advance implements ClockStore.Advance.
func (c nativeClocks) Advance(ctx context.Context, b *storage.Batch, k Kind, prev Clock, had bool, next Clock) error {
	if k == "" {
		return errors.New("tasks: clock advance without a kind")
	}
	key := taskstore.SchedKey(string(k))
	v, found, err := c.st.ReadForUpdate(ctx, b, key)
	if err != nil {
		return err
	}
	if found != had {
		return fmt.Errorf("%w: %s (stored %v, expected %v)", ErrClockConflict, k, found, had)
	}
	if found {
		cur, err := decodeValue[Clock](v)
		if err != nil {
			return fmt.Errorf("tasks: clock of %s: %w", k, err)
		}
		if !sameClock(cur, prev) {
			return fmt.Errorf("%w: %s", ErrClockConflict, k)
		}
	}
	return putValue(c.st, b, key, next)
}

// nativePause is NativeStores' PauseStore over the pause key.
type nativePause struct{ st *taskstore.Store }

// Get implements PauseStore.Get.
func (p nativePause) Get(ctx context.Context) (Pause, bool, error) {
	v, found, err := p.st.Get(ctx, taskstore.PauseKey())
	if err != nil || !found {
		return Pause{}, false, err
	}
	m, err := decodeValue[Pause](v)
	if err != nil {
		return Pause{}, false, fmt.Errorf("tasks: pause marker: %w", err)
	}
	return m, true, nil
}

// Set implements PauseStore.Set.
func (p nativePause) Set(_ context.Context, b *storage.Batch, m *Pause) error {
	if m == nil {
		return p.st.Delete(b, taskstore.PauseKey())
	}
	return putValue(p.st, b, taskstore.PauseKey(), *m)
}

// nativeStandDown is NativeStores' StandDownStore over standdown/<key>.
type nativeStandDown struct{ st *taskstore.Store }

// List implements StandDownStore.List, in key order.
func (s nativeStandDown) List(ctx context.Context) iter.Seq2[StandDown, error] {
	return scanValues[StandDown](ctx, s.st, taskstore.StandDownPrefix())
}

// Set implements StandDownStore.Set; the marker is stored under key, which
// wins over s.Key.
func (s nativeStandDown) Set(_ context.Context, b *storage.Batch, key Key, m *StandDown) error {
	return stageStandDown(s.st, b, key, m)
}
