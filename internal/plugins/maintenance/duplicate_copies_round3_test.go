// file: internal/plugins/maintenance/duplicate_copies_round3_test.go
// version: 1.1.0
// guid: e4ed5d00-b92e-4387-91d0-1b1b72542bb2
// last-edited: 2026-10-01

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// errDCInjected stands in for a wrapped sentinel a write step returns (the
// engine's repairs.ErrStandDownLost is the one that matters in production).
var errDCInjected = errors.New("injected write failure")

// dcFailMove is a book_file writer whose move fails with errDCInjected.
type dcFailMove struct{ repairs.BookFileWriter }

func (dcFailMove) MoveBookFilesToBook([]string, string, string) error {
	return errDCInjected
}

// TestDuplicateCopies_PartialKeepsTheCause: a step failing after another
// step succeeded is ErrPartiallyApplied AND still the step's own error, so
// the engine can tell a lapsed stand-down (retry) from a real failure.
func TestDuplicateCopies_PartialKeepsTheCause(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	row := findRow(t, res, dupRowID(s, l))
	require.True(t, row.Applicable(), row.SkipReason)
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").
		WithJournal(dcFailMove{d.s}, d.s, "op-partial")
	err := newDuplicateCopiesFixer(d.p).Apply(context.Background(), w, row)
	require.ErrorIs(t, err, repairs.ErrPartiallyApplied)
	require.ErrorIs(t, err, errDCInjected, "the cause survives the partial wrap: %v", err)
}

// TestDuplicateCopies_IndexIncompleteSaysSo: when the hash lookup refuses
// because memdb's index is incomplete, the row's reason tells the owner to
// wait for warmup or restart, not a generic "unreadable".
func TestDuplicateCopies_IndexIncompleteSaysSo(t *testing.T) {
	d, a, _ := ubik(t)
	r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
	require.True(t, r.Applicable(), r.SkipReason)
	d.p.deps = scanDeps{fakeDeps: fakeDeps{store: dcNoHashLookup{d.s}, labels: d.labels}, scan: &scriptedScan{renewsLeft: -1}, ops: d.ops}
	re, err := newDuplicateCopiesFixer(d.p).Replan(context.Background(), nil, r, nil)
	require.NoError(t, err)
	require.False(t, re.Applicable())
	require.Equal(t, dcSkipIndexIncomplete, re.Skipped)
	require.Contains(t, re.SkipReason, "file index incomplete, restart or wait for warmup")
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{r.RowID})
	require.Zero(t, out.Applied)
	data, err := json.Marshal(out.Rows)
	require.NoError(t, err)
	require.Contains(t, string(data), "file index incomplete")
}

// dcRealVerdicts points the fixture's verdict reader at a real embedding
// store over the fixture's own Pebble DB.
func (d *dcFixture) dcRealVerdicts(t *testing.T) *database.EmbeddingStore {
	t.Helper()
	es := database.NewEmbeddingStore(d.s.DB())
	d.p.deps = scanDeps{fakeDeps: fakeDeps{store: d.s, labels: es}, scan: &scriptedScan{renewsLeft: -1}, ops: d.ops}
	return es
}

// TestDuplicateCopies_LegacyCandidateVerdictReachesReplan (S-1): a candidate
// stored before the entity index existed, dismissed after the plan, vetoes
// the apply. Its Replan reads verdicts only through the entity index, so the
// status write must index it.
func TestDuplicateCopies_LegacyCandidateVerdictReachesReplan(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	es := d.dcRealVerdicts(t)
	id, _, err := es.UpsertCandidateNew(database.DedupCandidate{EntityType: "book", EntityAID: s, EntityBID: l, Layer: "embedding", Status: "pending"})
	require.NoError(t, err)
	for _, side := range []string{s, l} {
		require.NoError(t, es.PebbleDB().Delete([]byte(fmt.Sprintf("dedup:e:book:%s:%016x", side, id)), pebble.Sync))
	}
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	row := findRow(t, res, dupRowID(s, l))
	require.True(t, row.Applicable(), row.SkipReason)

	require.NoError(t, es.UpdateCandidateStatus(id, "dismissed"))
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{row.RowID})
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.True(t, d.live(t, "L"), "the owner's dismissal held")
}

// TestDuplicateCopies_ReplanReadsOnlyItsBooksLabels (C-1a): a not_dup label
// written after the plan is honoured at apply through the label entity
// index, and a corrupt label of an unrelated book does not fail the row.
func TestDuplicateCopies_ReplanReadsOnlyItsBooksLabels(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	es := d.dcRealVerdicts(t)
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	row := findRow(t, res, dupRowID(s, l))
	require.True(t, row.Applicable(), row.SkipReason)

	require.NoError(t, es.PebbleDB().Set([]byte("dedup:label:00000000000000ff"), []byte("{corrupt"), pebble.Sync))
	re, err := newDuplicateCopiesFixer(d.p).Replan(context.Background(), nil, row, nil)
	require.NoError(t, err, "an unrelated corrupt label is not read")
	require.Equal(t, row.Fingerprint, re.Fingerprint)

	require.NoError(t, es.UpsertLabeledExample(database.LabeledExample{CandidateID: 4242, EntityAID: l, EntityBID: s, Label: "not_dup", LabelSource: "human"}))
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{row.RowID})
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.True(t, d.live(t, "L"), "the owner's not_dup held")
}

// dcCountingStore counts full-library book reads. It unwraps to the real
// store, so the library generation and its change log still resolve.
type dcCountingStore struct {
	database.Store
	full *atomic.Int32
}

func (c dcCountingStore) GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error) {
	c.full.Add(1)
	return c.Store.GetAllBooksCoreComplete(limit, offset)
}

func (c dcCountingStore) Unwrap() database.Store { return c.Store }

// TestTitleIndex_CatchesUpWithoutARebuild (C-1b): after the first build the
// title index follows creates, retitles into and out of a key, and hides by
// re-reading only the books written, never the whole library again; a
// generation bump the store did not log forces the (correct, slower) rebuild.
func TestTitleIndex_CatchesUpWithoutARebuild(t *testing.T) {
	f := newFragFixture(t)
	for i := 0; i < 200; i++ {
		_, err := f.s.CreateBook(&database.Book{Title: fmt.Sprintf("Filler %d", i), FilePath: f.path(fmt.Sprintf("Fill/%d", i))})
		require.NoError(t, err)
	}
	var full atomic.Int32
	st := dcCountingStore{Store: f.s, full: &full}
	fx := &folderBooksFixer{}
	ids, err := fx.titleIndex(st, "sword")
	require.NoError(t, err)
	require.Empty(t, ids)
	require.EqualValues(t, 1, full.Load())

	made, err := f.s.CreateBook(&database.Book{Title: "Sword!", FilePath: f.path("New/Sword")})
	require.NoError(t, err)
	other, err := f.s.CreateBook(&database.Book{Title: "Something Else", FilePath: f.path("New/Else")})
	require.NoError(t, err)
	ids, err = fx.titleIndex(st, "sword")
	require.NoError(t, err)
	require.Equal(t, []string{made.ID}, ids, "a create is caught up")

	_, err = f.s.ModifyBook(other.ID, func(b *database.Book) error { b.Title = "Sword"; return nil })
	require.NoError(t, err)
	_, err = f.s.ModifyBook(made.ID, func(b *database.Book) error { b.Title = "Shield"; return nil })
	require.NoError(t, err)
	ids, err = fx.titleIndex(st, "sword")
	require.NoError(t, err)
	require.Equal(t, []string{other.ID}, ids, "retitled into the key in, out of it out")
	ids, err = fx.titleIndex(st, "shield")
	require.NoError(t, err)
	require.Equal(t, []string{made.ID}, ids)

	_, err = f.s.ModifyBook(other.ID, func(b *database.Book) error {
		yes := true
		b.MarkedForDeletion = &yes
		return nil
	})
	require.NoError(t, err)
	ids, err = fx.titleIndex(st, "sword")
	require.NoError(t, err)
	require.Empty(t, ids, "a hidden book leaves the index")
	require.EqualValues(t, 1, full.Load(), "every catch-up re-read only the books written")

	gen, tracked := database.LibraryGenerationOf(st)
	require.True(t, tracked)
	gen.Bump() // a bump with no log record: the changed book cannot be named
	ids, err = fx.titleIndex(st, "shield")
	require.NoError(t, err)
	require.Equal(t, []string{made.ID}, ids)
	require.EqualValues(t, 2, full.Load(), "an unlisted change forces a rebuild")
}

// dcFailReadStore fails GetBookByID for one book (a corrupt book_sig
// sidecar, say) and counts full-library reads like dcCountingStore.
type dcFailReadStore struct {
	dcCountingStore
	bad string
}

func (c dcFailReadStore) GetBookByID(id string) (*database.Book, error) {
	if id == c.bad {
		return nil, errDCInjected
	}
	return c.Store.GetBookByID(id)
}

// TestTitleIndex_ReReadErrorRebuildsInsteadOfWedging: a catch-up re-read
// that fails for one changed book must not return the error with the cache
// left at its old generation (every later call would re-read the same book
// and fail the same way). It drops the cache and rebuilds from the full
// listing, which answers correctly.
func TestTitleIndex_ReReadErrorRebuildsInsteadOfWedging(t *testing.T) {
	f := newFragFixture(t)
	_, err := f.s.CreateBook(&database.Book{Title: "Filler", FilePath: f.path("Fill/0")})
	require.NoError(t, err)
	var full atomic.Int32
	st := dcFailReadStore{dcCountingStore: dcCountingStore{Store: f.s, full: &full}}
	fx := &folderBooksFixer{}
	ids, err := fx.titleIndex(st, "sword")
	require.NoError(t, err)
	require.Empty(t, ids)
	require.EqualValues(t, 1, full.Load())

	made, err := f.s.CreateBook(&database.Book{Title: "Sword", FilePath: f.path("New/Sword")})
	require.NoError(t, err)
	st.bad = made.ID
	ids, err = fx.titleIndex(st, "sword")
	require.NoError(t, err, "one unreadable book must not fail the duplicate check")
	require.Equal(t, []string{made.ID}, ids, "the full rebuild lists the book")
	require.EqualValues(t, 2, full.Load(), "the failed catch-up fell through to the full rebuild")

	// The rebuilt cache is current: a later write is caught up again.
	other, err := f.s.CreateBook(&database.Book{Title: "Sword", FilePath: f.path("New/Sword2")})
	require.NoError(t, err)
	ids, err = fx.titleIndex(st, "sword")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{made.ID, other.ID}, ids)
	require.EqualValues(t, 2, full.Load(), "the rebuilt cache caught up without another rebuild")
}

// TestDuplicateCopies_BulkApplyReadsTheLibraryOnce (C-1b): two rows of one
// apply, each re-planned under the lock after the other's writes bumped the
// generation, read every book at most once between them.
func TestDuplicateCopies_BulkApplyReadsTheLibraryOnce(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	s2 := d.copyBook(t, "S2", "Ubik", "lib/Philip K Dick/Ubik",
		dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"}, dcRow{track: 3, dur: 600, hash: "u3"})
	l2 := d.copyBook(t, "L2", "Ubik", "lib/Ubik copy",
		dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"}, dcRow{track: 3, dur: 600, hash: "u3"})
	var full atomic.Int32
	d.p.deps = scanDeps{fakeDeps: fakeDeps{store: dcCountingStore{Store: d.s, full: &full}, labels: d.labels}, scan: &scriptedScan{renewsLeft: -1}, ops: d.ops}
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	a, b := findRow(t, res, dupRowID(s, l)), findRow(t, res, dupRowID(s2, l2))
	require.True(t, a.Applicable(), a.SkipReason)
	require.True(t, b.Applicable(), b.SkipReason)
	before := full.Load()
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{a.RowID, b.RowID})
	require.Equal(t, 2, out.Applied, "%+v", out.Rows)
	require.LessOrEqual(t, full.Load()-before, int32(1),
		"Replan per row catches the title index up; it does not rebuild it from every book")
}

// dcFailModify is a book writer whose every ModifyBook fails with
// errDCInjected.
type dcFailModify struct{ *database.PebbleStore }

func (dcFailModify) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	return nil, errDCInjected
}

// TestFragmentFixer_PartialKeepsTheCause: the fragment fixer's partial()
// makes the same promise as the duplicate-copies one: after its repoint, a
// failing book write is ErrPartiallyApplied and still the write's own error.
func TestFragmentFixer_PartialKeepsTheCause(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	row := findRow(t, res, "moved:"+f.ids["parent"])
	require.True(t, row.Applicable(), row.SkipReason)
	w := repairs.NewWriter(dcFailModify{f.s}, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-partial")
	err := newFragmentFixer(f.p).Apply(context.Background(), w, row)
	require.ErrorIs(t, err, repairs.ErrPartiallyApplied)
	require.ErrorIs(t, err, errDCInjected, "the cause survives the partial wrap: %v", err)
}

// dcFailRetire is a book writer whose write that would hide a book fails
// with errDCInjected; every other write goes through.
type dcFailRetire struct{ *database.PebbleStore }

func (s dcFailRetire) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
		if err := fn(b); err != nil {
			return err
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			return errDCInjected
		}
		return nil
	})
}

// TestFolderBooksFixer_PartialKeepsTheCause: the folder-books fixer's
// partial() keeps the failing step's error too (its retire, after the
// groups were written).
func TestFolderBooksFixer_PartialKeepsTheCause(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "skipped: %s %s", row.Skipped, row.SkipReason)
	w := repairs.NewWriter(dcFailRetire{f.s}, f.s, fbFixerID, "bulk_update", "repairs-").
		WithJournal(f.s, f.s, "op-partial").WithCredits(f.s)
	err := newFolderBooksFixer(f.p).Apply(context.Background(), w, row)
	require.ErrorIs(t, err, repairs.ErrPartiallyApplied)
	require.ErrorIs(t, err, errDCInjected, "the cause survives the partial wrap: %v", err)
}
