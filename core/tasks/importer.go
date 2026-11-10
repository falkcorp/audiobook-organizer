// file: core/tasks/importer.go
// version: 1.1.0
// guid: f56c7a6a-86cb-4b03-9df0-0366ab0a8b04
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/storage"
)

// Importer writes an app's pre-core task state into the native stores, once,
// from inside that app's own cutover step (ABO: books' M4, 5T.8.3). The kernel
// builds it over c/tasks/ and hands it to the step in module.Importers (06
// §6.4.2; 03 §3.3 "Store interfaces"); the app decodes its own rows, so core
// holds no app key name or row format (03 R1(c)). Every method stages into the
// caller's batch b, which also carries the step's deletes of the source rows,
// so each row moves exactly once and a crash leaves the chunk either wholly
// moved or wholly unmoved (5T.4 "Boot writers"). Nothing is checked against
// executor state: no executor runs before P4 ends.
type Importer interface {
	// Run stages one run with its own ID (HEAD's op ID), its renamed Kind,
	// any RunState (terminal included) and its Seq counters, under
	// Expect(absent) on its guard key, so a resumed step never imports a run
	// twice (ErrRunConflict). A run that was running or stopping when HEAD
	// stopped is imported as StateInterrupted, so its ResumePolicy decides at
	// the first boot's resume sweep (5T.4 step 2).
	Run(ctx context.Context, b *storage.Batch, r RunRecord) error
	// RunData stages records of a run imported in this batch or an earlier
	// one: pages in their seq order, checkpoints, chunks, rows and marks. A
	// run's data may be split over several calls and batches.
	RunData(ctx context.Context, b *storage.Batch, id RunID, d RunData) error
	Clock(ctx context.Context, b *storage.Batch, k Kind, c Clock) error // ScheduleHash empty (5T.3.8)
	Pause(ctx context.Context, b *storage.Batch, p Pause) error
	StandDown(ctx context.Context, b *storage.Batch, s StandDown) error
	Debounce(ctx context.Context, b *storage.Batch, d DebounceBucket) error // pending debounced starts
	Stamp(ctx context.Context, b *storage.Batch, subject entity.Ref, k Kind, at time.Time) error
	// RenameKind stages into b the rewrite of up to limit stored references
	// to from (runs and their kind index, clock, definition snapshot, stamps)
	// as to, continuing after cursor; next is "" when none remain. It is how
	// an owner renames a kind after R2: a BootBlocking data migration of the
	// owner's calls it chunk by chunk (05-storage §5S.3.6); there is no alias
	// (D10).
	RenameKind(ctx context.Context, b *storage.Batch, from, to Kind, cursor string, limit int) (next string, err error)
}

// RunData is one portion of an imported run's records (Importer.RunData).
type RunData struct {
	Pages       []PageEntry       // events, intents, item outcomes and log lines
	Checkpoints map[string][]byte // name -> HEAD's checkpoint bytes (5T.3.6)
	Chunks      []Chunk
	Rows        []Row
	Marks       []string // applied row IDs
}

// NativeImporter is the Importer over the c/tasks/ keyspace ks, the one the
// kernel hands to an app's cutover step in module.Importers (06 §6.4.2) at the
// tasks flip. It writes the same keys NativeStores reads.
//
// Every batch it stages into must be indexed (ks.NewIndexedBatch, or a
// participant's indexed batch): RunData finds a run imported earlier in the
// same batch, and Run refuses a run the batch already holds, only by reading
// the batch's own writes. A plain batch is refused with ErrBatchNotIndexed.
//
// A run is imported as RunStore stages one (validRecord): a terminal run
// must carry its finish time. HEAD's CompletedAt is optional, and the spec
// gives no value for a terminal row without one, so the cutover step decides
// (05-tasks 5T.8.3: supply one, or quarantine the row); Run refuses it.
func NativeImporter(ks storage.Keyspace) Importer {
	return &nativeImporter{st: taskstore.New(ks)}
}

// nativeImporter is NativeImporter's implementation. It holds only the
// staging layer, so it is safe for concurrent use.
type nativeImporter struct {
	st *taskstore.Store
}

// Run implements Importer.Run.
func (im *nativeImporter) Run(ctx context.Context, b *storage.Batch, r RunRecord) error {
	if r.State == StateRunning || r.State == StateStopping {
		r.State = StateInterrupted
	}
	return insertRun(ctx, im.st, b, r)
}

// RunData implements Importer.RunData: it records the guard precondition of
// the run (which must exist as b sees it) and stages every record of d.
func (im *nativeImporter) RunData(ctx context.Context, b *storage.Batch, id RunID, d RunData) error {
	if _, found, err := readForUpdate(ctx, im.st, b, taskstore.RunKey(string(id))); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("%w: RunData of %s, which is neither stored nor imported in this batch (an import batch that stages Run and RunData together must be indexed)", ErrRunNotFound, id)
	}
	for _, p := range d.Pages {
		if err := im.stagePage(b, id, p); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(d.Checkpoints))
	for name := range d.Checkpoints {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := stageCheckpoint(im.st, b, id, name, d.Checkpoints[name]); err != nil {
			return err
		}
	}
	for _, c := range d.Chunks {
		if err := stageChunk(im.st, b, id, c); err != nil {
			return err
		}
	}
	for _, row := range d.Rows {
		if err := stageRow(im.st, b, id, row); err != nil {
			return err
		}
	}
	for _, rowID := range d.Marks {
		if err := stageMark(im.st, b, id, rowID); err != nil {
			return err
		}
	}
	return nil
}

func (im *nativeImporter) stagePage(b *storage.Batch, id RunID, p PageEntry) error {
	switch p.Page {
	case PageEvents:
		return stagePage(im.st, b, id, taskstore.PageEvents, taskstore.SeqSeg(p.Seq), p.Event)
	case PageIntents:
		return stagePage(im.st, b, id, taskstore.PageIntents, taskstore.SeqSeg(p.Seq), intentEntry{Write: p.Write, Changes: p.Changes})
	case PageItems:
		key := p.Key
		if key == "" {
			key = p.Item.Key
		}
		return stageItem(im.st, b, id, key, p.Item)
	case PageLog:
		return stagePage(im.st, b, id, taskstore.PageLog, taskstore.SeqSeg(p.Seq), p.Log)
	default:
		return fmt.Errorf("tasks: import of run %s: page entry of unknown page %d", id, p.Page)
	}
}

// Clock implements Importer.Clock: the clock is stored with an empty
// ScheduleHash, so the first boot's scheduler re-derives it (5T.3.8).
func (im *nativeImporter) Clock(ctx context.Context, b *storage.Batch, k Kind, c Clock) error {
	if k == "" {
		return errors.New("tasks: import of a clock without a kind")
	}
	key := taskstore.SchedKey(string(k))
	if _, _, err := readForUpdate(ctx, im.st, b, key); err != nil {
		return err
	}
	c.ScheduleHash = ""
	return putValue(im.st, b, key, c)
}

// Pause implements Importer.Pause.
func (im *nativeImporter) Pause(_ context.Context, b *storage.Batch, p Pause) error {
	return putValue(im.st, b, taskstore.PauseKey(), p)
}

// StandDown implements Importer.StandDown.
func (im *nativeImporter) StandDown(_ context.Context, b *storage.Batch, s StandDown) error {
	return stageStandDown(im.st, b, s.Key, &s)
}

// Debounce implements Importer.Debounce: the bucket is merged into any
// stored bucket of the same kind and name, as RunStore.Debounce merges.
func (im *nativeImporter) Debounce(ctx context.Context, b *storage.Batch, d DebounceBucket) error {
	if d.First.IsZero() {
		return fmt.Errorf("tasks: import of debounce bucket %s/%s without a first arrival", d.Kind, d.Bucket)
	}
	return stageDebounce(ctx, im.st, b, d)
}

// Stamp implements Importer.Stamp.
func (im *nativeImporter) Stamp(_ context.Context, b *storage.Batch, subject entity.Ref, k Kind, at time.Time) error {
	return stageStamp(im.st, b, subject, k, at)
}

// The phases of RenameKind, in the order it walks them.
const (
	renameRuns = iota
	renameClock
	renameDef
	renameDebounce
	renameStamps
	renameIdem
	renamePhases
)

// RenameKind implements Importer.RenameKind. It walks six families in a
// fixed order: the kind index (each run row and its index entry), the clock,
// the definition snapshot, the debounce buckets of from (a stored reference
// to the kind too, 5T.8.2), the stamps (through their reverse index) and the
// start idempotency records, so a client's retry of a start under the new
// name within the record's window still finds its run.
// The cursor names the phase and the last key the phase staged, so a second
// call into the same uncommitted batch never stages a reference twice; after
// the batch commits, every rewritten reference is gone from from's keys, so
// a call over committed state that finds nothing is a no-op (idempotent).
// Where to already holds a clock, a definition snapshot, an idempotency
// record or a stamp, to's copy is kept (a stamp keeps the later time) and
// from's is deleted; a debounce bucket is merged. The definition snapshot and
// the idempotency records move as bytes, unread, so neither value may embed
// its kind (the kind lives in the key).
func (im *nativeImporter) RenameKind(ctx context.Context, b *storage.Batch, from, to Kind, cursor string, limit int) (string, error) {
	if from == "" || to == "" || from == to {
		return "", fmt.Errorf("tasks: RenameKind %q -> %q: both kinds must be set and differ", from, to)
	}
	if limit < 1 {
		return "", fmt.Errorf("tasks: RenameKind limit %d: must be at least 1", limit)
	}
	if err := im.st.Bind(b); err != nil {
		return "", err
	}
	if !b.Indexed() {
		return "", ErrBatchNotIndexed
	}
	phase, after, err := parseRenameCursor(cursor)
	if err != nil {
		return "", err
	}
	budget := limit
	for ; phase < renamePhases; phase++ {
		last, more, err := im.renamePhase(ctx, b, phase, string(from), string(to), after, &budget)
		if err != nil {
			return "", err
		}
		if more {
			return formatRenameCursor(phase, last), nil
		}
		after = nil
	}
	return "", nil
}

// renamePhase stages up to *budget rewrites of one phase after the key
// after, and reports the last key it staged and whether keys remain.
func (im *nativeImporter) renamePhase(ctx context.Context, b *storage.Batch, phase int, from, to string, after []byte, budget *int) (last []byte, more bool, err error) {
	switch phase {
	case renameClock:
		return im.renameSingle(ctx, b, taskstore.SchedKey(from), taskstore.SchedKey(to), true, budget)
	case renameDef:
		return im.renameSingle(ctx, b, taskstore.DefKey(from), taskstore.DefKey(to), false, budget)
	}
	var prefix []byte
	var each func(key, value []byte) error
	switch phase {
	case renameRuns:
		prefix = taskstore.KindIndexPrefix(from)
		each = func(key, _ []byte) error { return im.renameRun(ctx, b, key, from, to) }
	case renameDebounce:
		prefix = taskstore.DebouncePrefix(from)
		each = func(key, _ []byte) error { return im.renameBucket(ctx, b, key, to) }
	case renameIdem:
		prefix = taskstore.IdemPrefix(from)
		each = func(key, _ []byte) error { return im.renameIdem(ctx, b, key, to) }
	default:
		prefix = taskstore.ReqRevPrefix(from)
		each = func(key, value []byte) error { return im.renameStamp(ctx, b, key, value, from, to) }
	}
	n := 0
	for kv, err := range im.st.Scan(ctx, prefix, after, *budget+1) {
		if err != nil {
			return nil, false, err
		}
		if n == *budget {
			return last, true, nil
		}
		if err := each(kv.Key, kv.Value); err != nil {
			return nil, false, err
		}
		last = kv.Key
		n++
	}
	*budget -= n
	return last, false, nil
}

// renameSingle moves one key of a single-key family (the clock, which is a
// guard key, or the definition snapshot). With no budget left it only
// reports whether the key remains, so the cursor restarts this phase.
func (im *nativeImporter) renameSingle(ctx context.Context, b *storage.Batch, fromKey, toKey []byte, guarded bool, budget *int) ([]byte, bool, error) {
	if *budget == 0 {
		_, found, err := im.st.Get(ctx, fromKey)
		return nil, found, err
	}
	read := func(key []byte) ([]byte, bool, error) {
		if guarded {
			return readForUpdate(ctx, im.st, b, key)
		}
		return im.st.Get(ctx, key)
	}
	v, found, err := read(fromKey)
	if err != nil || !found {
		return nil, false, err
	}
	_, toFound, err := read(toKey)
	if err != nil {
		return nil, false, err
	}
	if !toFound {
		if err := im.st.Set(b, toKey, v); err != nil {
			return nil, false, err
		}
	}
	if err := im.st.Delete(b, fromKey); err != nil {
		return nil, false, err
	}
	*budget--
	return nil, false, nil
}

// renameRun rewrites the run that one kind-index entry of from names.
func (im *nativeImporter) renameRun(ctx context.Context, b *storage.Batch, ixKey []byte, from, to string) error {
	_, rawID, err := taskstore.ParseKindIndexKey(ixKey)
	if err != nil {
		return fmt.Errorf("tasks: RenameKind: %w", err)
	}
	id := RunID(rawID)
	v, found, err := readForUpdate(ctx, im.st, b, taskstore.RunKey(string(id)))
	if err != nil {
		return err
	}
	if !found {
		return im.st.Delete(b, ixKey) // an index entry without its run
	}
	rec, err := decodeRun(v)
	if err != nil {
		return fmt.Errorf("tasks: RenameKind of run %s: %w", id, err)
	}
	if string(rec.Kind) != from {
		return im.st.Delete(b, ixKey) // a stale entry: the row names another kind
	}
	prev := runIndex(rec)
	rec.Kind = Kind(to)
	return putRun(im.st, b, rec, &prev)
}

// renameBucket moves one debounce bucket of from to to, merging it into a
// bucket to already holds.
func (im *nativeImporter) renameBucket(ctx context.Context, b *storage.Batch, key []byte, to string) error {
	v, found, err := readForUpdate(ctx, im.st, b, key)
	if err != nil || !found {
		return err
	}
	d, err := decodeValue[DebounceBucket](v)
	if err != nil {
		return fmt.Errorf("tasks: RenameKind of a debounce bucket: %w", err)
	}
	if err := im.st.Delete(b, key); err != nil {
		return err
	}
	d.Kind = Kind(to)
	return stageDebounce(ctx, im.st, b, d)
}

// renameStamp moves one stamp of from (found through its reverse-index key)
// to to, keeping the later time where to already has one.
func (im *nativeImporter) renameStamp(ctx context.Context, b *storage.Batch, revKey, value []byte, from, to string) error {
	_, subject, err := taskstore.ParseReqRevKey(revKey)
	if err != nil {
		return fmt.Errorf("tasks: RenameKind: %w", err)
	}
	at, err := decodeValue[stampValue](value)
	if err != nil {
		return fmt.Errorf("tasks: RenameKind of a stamp: %w", err)
	}
	if cur, found, err := im.st.Get(ctx, taskstore.ReqKey(subject, to)); err != nil {
		return err
	} else if found {
		if have, err := decodeValue[stampValue](cur); err == nil && have.At.After(at.At) {
			at = have
		}
	}
	for _, k := range [][]byte{taskstore.ReqKey(subject, from), revKey} {
		if err := im.st.Delete(b, k); err != nil {
			return err
		}
	}
	ref := entity.Ref{Module: subject.Module, Kind: subject.Kind, ID: subject.ID}
	return stageStamp(im.st, b, ref, Kind(to), at.At)
}

// renameIdem moves one start idempotency record of from to to under both
// keys' guard preconditions (idem/ is Serialized), keeping a record to
// already holds under the same client key.
func (im *nativeImporter) renameIdem(ctx context.Context, b *storage.Batch, key []byte, to string) error {
	_, idemKey, err := taskstore.ParseIdemKey(key)
	if err != nil {
		return fmt.Errorf("tasks: RenameKind: %w", err)
	}
	v, found, err := readForUpdate(ctx, im.st, b, key)
	if err != nil || !found {
		return err
	}
	toKey := taskstore.IdemKey(to, idemKey)
	_, toFound, err := readForUpdate(ctx, im.st, b, toKey)
	if err != nil {
		return err
	}
	if !toFound {
		if err := im.st.Set(b, toKey, v); err != nil {
			return err
		}
	}
	return im.st.Delete(b, key)
}

// formatRenameCursor is "<phase>:<hex of the last staged key>".
func formatRenameCursor(phase int, last []byte) string {
	return strconv.Itoa(phase) + ":" + hex.EncodeToString(last)
}

// parseRenameCursor is formatRenameCursor's inverse; "" starts at the first
// phase. A cursor whose key is empty starts the phase from its first key.
func parseRenameCursor(c string) (phase int, after []byte, err error) {
	if c == "" {
		return renameRuns, nil, nil
	}
	p, k, ok := strings.Cut(c, ":")
	phase, perr := strconv.Atoi(p)
	after, kerr := hex.DecodeString(k)
	if !ok || perr != nil || kerr != nil || phase < renameRuns || phase >= renamePhases {
		return 0, nil, fmt.Errorf("tasks: RenameKind: malformed cursor %q", c)
	}
	if len(after) == 0 {
		after = nil
	}
	return phase, after, nil
}
