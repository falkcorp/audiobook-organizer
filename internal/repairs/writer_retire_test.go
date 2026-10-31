// file: internal/repairs/writer_retire_test.go
// version: 1.2.1
// guid: d4146810-528b-4b28-ab80-cf4d4aa0bdf1
// last-edited: 2026-10-30

package repairs

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// retireFx is a holder, a trashed shell with two rows, and a Writer over a real
// store. All data is synthetic.
type retireFx struct {
	st            *database.PebbleStore
	holder, shell *database.Book
	rows          []*database.BookFile
}

func newRetireFx(t *testing.T) *retireFx {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.WaitForWarmup()
	holder, err := st.CreateBook(&database.Book{Title: "Synthetic Holder", FilePath: "/lib/syn/holder"})
	require.NoError(t, err)
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: "/lib/syn/shell"})
	require.NoError(t, err)
	fx := &retireFx{st: st, holder: holder, shell: shell}
	for i, p := range []string{"/lib/syn/shell/01.mp3", "/lib/syn/shell/02.mp3"} {
		row := &database.BookFile{BookID: shell.ID, FilePath: p, FileHash: "h" + string(rune('a'+i)), TrackNumber: i + 1, Duration: 10, FileSize: 100}
		require.NoError(t, st.CreateBookFile(row))
		fx.rows = append(fx.rows, row)
	}
	// A retire acts on a trashed shell (the combine soft-deleted it); the
	// writer refuses a live one.
	trashed := true
	shell.MarkedForDeletion = &trashed
	_, err = st.UpdateBook(shell.ID, shell)
	require.NoError(t, err)
	return fx
}

func (fx *retireFx) ids() []string {
	out := make([]string, len(fx.rows))
	for i, r := range fx.rows {
		out[i] = r.ID
	}
	return out
}

func (fx *retireFx) spec() RetireSpec {
	return RetireSpec{Shell: fx.shell.ID, Holder: fx.holder.ID, VersionID: "ver-syn-1", FileIDs: fx.ids(),
		Format: "mp3", Source: "retired_book", Now: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}

func TestRetireVersion_JournalsVersionThenRowsThenWrites(t *testing.T) {
	fx := newRetireFx(t)
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-retire")
	require.NoError(t, w.RetireVersion(fx.spec()))

	rows, err := fx.st.GetOperationChanges("op-retire")
	require.NoError(t, err)
	require.Len(t, rows, 3, "one version row and one row per file")
	require.Equal(t, undo.ChangeTypeBookVersionCreate, rows[0].ChangeType, "the version is journaled and created before any row moves")
	v, err := undo.DecodeBookVersionCreate(rows[0])
	require.NoError(t, err)
	require.Equal(t, fx.shell.ID, v.RetiredFromBookID)
	for _, c := range rows[1:] {
		require.Equal(t, undo.ChangeTypeBookFileRetire, c.ChangeType)
		require.False(t, c.Voided)
		r, err := undo.DecodeBookFileRetire(c)
		require.NoError(t, err)
		require.Equal(t, fx.shell.ID, r.Shell)
		require.Equal(t, fx.holder.ID, r.Holder)
		require.Equal(t, "ver-syn-1", r.VersionID)
	}

	for _, id := range fx.ids() {
		got, err := fx.st.GetBookFileByID(fx.holder.ID, id)
		require.NoError(t, err)
		require.NotNil(t, got, "row %s is keyed under the holder", id)
		require.True(t, got.Retired)
		require.Equal(t, "ver-syn-1", got.VersionID)
		gone, err := fx.st.GetBookFileByID(fx.shell.ID, id)
		require.NoError(t, err)
		require.Nil(t, gone)
	}
	ver, err := fx.st.GetBookVersion("ver-syn-1")
	require.NoError(t, err)
	require.Equal(t, database.BookVersionStatusRetired, ver.Status)
}

// The provenance a caller hands in rides into the version, in the store and in
// the journaled create row; a zero IngestDate is the retire time.
func TestRetireVersion_CarriesProvenanceIntoTheVersion(t *testing.T) {
	fx := newRetireFx(t)
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-prov")
	spec := fx.spec()
	spec.SourceOriginalPath = "/lib/syn/shell"
	spec.MetadataJSON = `{"shell_title":"Synthetic Shell"}`
	spec.IngestDate = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	require.NoError(t, w.RetireVersion(spec))

	ver, err := fx.st.GetBookVersion(spec.VersionID)
	require.NoError(t, err)
	require.Equal(t, "/lib/syn/shell", ver.SourceOriginalPath)
	require.JSONEq(t, `{"shell_title":"Synthetic Shell"}`, ver.MetadataJSON)
	require.True(t, spec.IngestDate.Equal(ver.IngestDate), "the shell's own ingest time, not the retire time: %s", ver.IngestDate)
	rows, err := fx.st.GetOperationChanges("op-prov")
	require.NoError(t, err)
	made, err := undo.DecodeBookVersionCreate(rows[0])
	require.NoError(t, err)
	require.Equal(t, ver.MetadataJSON, made.MetadataJSON, "the journal carries what the store holds")
	require.Equal(t, "/lib/syn/shell", made.SourceOriginalPath)

	// A zero IngestDate is the retire stamp.
	fx2 := newRetireFx(t)
	w2 := NewWriter(fx2.st, fx2.st, "fx", "bulk_update", "repairs-").WithJournal(fx2.st, fx2.st, "op-prov2")
	require.NoError(t, w2.RetireVersion(fx2.spec()))
	v2, err := fx2.st.GetBookVersion("ver-syn-1")
	require.NoError(t, err)
	require.True(t, fx2.spec().Now.Equal(v2.IngestDate))
}

// CheckRetireSupport answers before anything is journaled: a Writer over a
// store that cannot retire is refused, one over the real store is not.
func TestCheckRetireSupport(t *testing.T) {
	fx := newRetireFx(t)
	require.NoError(t, CheckRetireSupport(NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-cs")))
	require.Error(t, CheckRetireSupport(NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-")), "no book_file store wired")
	rows, err := fx.st.GetOperationChanges("op-cs")
	require.NoError(t, err)
	require.Empty(t, rows, "the check journals nothing")
}

func TestRetireVersion_ResumeJournalsNothingTwice(t *testing.T) {
	fx := newRetireFx(t)
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-resume")
	require.NoError(t, w.RetireVersion(fx.spec()))
	before, err := fx.st.GetOperationChanges("op-resume")
	require.NoError(t, err)

	again := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-resume")
	require.NoError(t, again.RetireVersion(fx.spec()), "a finished retire resumed is a no-op")
	after, err := fx.st.GetOperationChanges("op-resume")
	require.NoError(t, err)
	require.Len(t, after, len(before))
}

func TestRetireVersion_LinksCombineAndStampsTheRows(t *testing.T) {
	fx := newRetireFx(t)
	spec := fx.spec()
	spec.CombineJournalID = "cj-syn"
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-linked")
	require.NoError(t, w.RetireVersion(spec))

	links, err := merge.ListCombineRetireLinks(fx.st, "cj-syn")
	require.NoError(t, err)
	require.Equal(t, []merge.CombineRetireLink{{ShellID: fx.shell.ID, OpID: "op-linked"}}, links)
	rows, err := fx.st.GetOperationChanges("op-linked")
	require.NoError(t, err)
	for _, c := range rows {
		if c.ChangeType != undo.ChangeTypeBookFileRetire {
			continue
		}
		r, err := undo.DecodeBookFileRetire(c)
		require.NoError(t, err)
		require.Equal(t, "cj-syn", r.CombineJournalID, "the combine rides inside the row so the link can be rebuilt from the journal")
	}
}

// orderStore records, in call order, the combine-retire link write ("link")
// and every change-row write ("change"), so a test can assert the link goes
// first.
type orderStore struct {
	*database.PebbleStore
	events *[]string
}

func (s orderStore) SetRaw(key string, value []byte) error {
	*s.events = append(*s.events, "link")
	return s.PebbleStore.SetRaw(key, value)
}

func (s orderStore) CreateOperationChange(c *database.OperationChange) error {
	*s.events = append(*s.events, "change")
	return s.PebbleStore.CreateOperationChange(c)
}

func (s orderStore) CreateOperationChanges(cs []*database.OperationChange) error {
	*s.events = append(*s.events, "change")
	return s.PebbleStore.CreateOperationChanges(cs)
}

// The link must exist before the first change row: rows without a link are the
// one case the combine's undo cannot find. Asserted on the order of the writes,
// not only on both being there.
func TestRetireVersion_LinkIsWrittenBeforeTheFirstChangeRow(t *testing.T) {
	fx := newRetireFx(t)
	var events []string
	st := orderStore{PebbleStore: fx.st, events: &events}
	spec := fx.spec()
	spec.CombineJournalID = "cj-order"
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-order")
	require.NoError(t, w.RetireVersion(spec))

	require.NotEmpty(t, events)
	require.Equal(t, "link", events[0], "the link is the first write: %v", events)
	linkAt := -1
	for i, e := range events {
		if e == "link" {
			linkAt = i
		}
	}
	require.Equal(t, 0, linkAt, "and the only link write, before every change row: %v", events)
	require.Contains(t, events, "change")
}

// A shell the combine moved every file off has no rows to retire, but still has
// the external-id and merged-into rows the combine's undo reverses, so the link
// is written before the empty-plan return.
func TestRetireVersion_FilelessShellStillLinksToTheCombine(t *testing.T) {
	fx := newRetireFx(t)
	spec := fx.spec()
	spec.FileIDs = nil
	spec.CombineJournalID = "cj-empty"
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-empty")
	require.NoError(t, w.RetireVersion(spec))

	links, err := merge.ListCombineRetireLinks(fx.st, "cj-empty")
	require.NoError(t, err)
	require.Equal(t, []merge.CombineRetireLink{{ShellID: fx.shell.ID, OpID: "op-empty"}}, links)
	v, err := fx.st.GetBookVersion("ver-syn-1")
	require.NoError(t, err)
	require.Nil(t, v, "no rows to retire means no version is created")
}

// A second retire op of the same shell adds a link; it does not replace the
// first op's.
func TestRetireVersion_SecondOpOfTheSameShellAddsALink(t *testing.T) {
	fx := newRetireFx(t)
	for _, op := range []string{"op-first", "op-second"} {
		spec := fx.spec()
		spec.FileIDs = nil
		spec.CombineJournalID = "cj-two"
		w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, op)
		require.NoError(t, w.RetireVersion(spec))
	}
	links, err := merge.ListCombineRetireLinks(fx.st, "cj-two")
	require.NoError(t, err)
	require.Equal(t, []merge.CombineRetireLink{{ShellID: fx.shell.ID, OpID: "op-first"}, {ShellID: fx.shell.ID, OpID: "op-second"}}, links)
}

// A shell restored since the plan (the combine was undone) is not retired and
// is not linked: its rows would be taken from a live book.
func TestRetireVersion_RefusesAShellThatIsNoLongerTrashed(t *testing.T) {
	fx := newRetireFx(t)
	live := false
	fx.shell.MarkedForDeletion = &live
	fx.shell.MarkedForDeletionAt = nil
	_, err := fx.st.UpdateBook(fx.shell.ID, fx.shell)
	require.NoError(t, err)
	spec := fx.spec()
	spec.CombineJournalID = "cj-live"
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-live")
	require.ErrorIs(t, w.RetireVersion(spec), ErrChangedSincePlan)

	links, err := merge.ListCombineRetireLinks(fx.st, "cj-live")
	require.NoError(t, err)
	require.Empty(t, links)
	rows, err := fx.st.GetOperationChanges("op-live")
	require.NoError(t, err)
	require.Empty(t, rows)
	for _, id := range fx.ids() {
		got, err := fx.st.GetBookFileByID(fx.shell.ID, id)
		require.NoError(t, err)
		require.NotNil(t, got, "the row never moved")
	}
}

// refusingStore fails RetireBookFiles after the rows were journaled.
type refusingStore struct {
	*database.PebbleStore
	err error
}

func (s refusingStore) RetireBookFiles([]database.BookFileMove, string, string) error { return s.err }

func TestRetireVersion_RefusalVoidsRowsAndMapsErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		wantIs  error
		wantNot error
	}{
		{"index incomplete is retry later", database.ErrRetireIndexIncomplete, ErrRetryLater, ErrChangedSincePlan},
		{"already retired is changed since plan", database.ErrBookFileRetired, ErrChangedSincePlan, ErrRetryLater},
		{"version mismatch is changed since plan", database.ErrRetiredVersionMismatch, ErrChangedSincePlan, ErrRetryLater},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newRetireFx(t)
			st := refusingStore{PebbleStore: fx.st, err: tc.err}
			w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(st, fx.st, "op-refused")
			err := w.RetireVersion(fx.spec())
			require.ErrorIs(t, err, tc.wantIs)
			require.False(t, errors.Is(err, tc.wantNot))

			rows, gerr := fx.st.GetOperationChanges("op-refused")
			require.NoError(t, gerr)
			voided := 0
			for _, c := range rows {
				if c.ChangeType == undo.ChangeTypeBookFileRetire {
					require.True(t, c.Voided, "a refused write's row is voided: the revert must not undo what never happened")
					voided++
				}
			}
			require.Equal(t, 2, voided)
			for _, id := range fx.ids() {
				got, err := fx.st.GetBookFileByID(fx.shell.ID, id)
				require.NoError(t, err)
				require.NotNil(t, got, "the row never moved")
			}
		})
	}
}

func TestRetireVersion_RowGoneFromShellRefusesBeforeJournalingIt(t *testing.T) {
	fx := newRetireFx(t)
	spec := fx.spec()
	spec.FileIDs = append(spec.FileIDs, "no-such-row")
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-gone")
	require.ErrorIs(t, w.RetireVersion(spec), ErrChangedSincePlan)
	rows, err := fx.st.GetOperationChanges("op-gone")
	require.NoError(t, err)
	for _, c := range rows {
		require.NotEqual(t, undo.ChangeTypeBookFileRetire, c.ChangeType, "no retire row for a plan that no longer holds")
	}
}

func TestRehomeRetiredVersion_JournalsAndMoves(t *testing.T) {
	fx := newRetireFx(t)
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-rehome")
	require.NoError(t, w.RetireVersion(fx.spec()))
	newHolder, err := fx.st.CreateBook(&database.Book{Title: "Synthetic New Holder", FilePath: "/lib/syn/new"})
	require.NoError(t, err)

	require.NoError(t, w.RehomeRetiredVersion("ver-syn-1", newHolder.ID))
	v, err := fx.st.GetBookVersion("ver-syn-1")
	require.NoError(t, err)
	require.Equal(t, newHolder.ID, v.BookID)
	rows, err := fx.st.GetOperationChanges("op-rehome")
	require.NoError(t, err)
	last := rows[len(rows)-1]
	require.Equal(t, undo.ChangeTypeRetiredVersionRehome, last.ChangeType)
	require.Equal(t, fx.holder.ID, last.OldValue)
	require.Equal(t, newHolder.ID, last.NewValue)

	require.NoError(t, w.RehomeRetiredVersion("ver-syn-1", newHolder.ID), "already there is a no-op")
}

func TestRetireVersion_TagsOnlyWriterRefuses(t *testing.T) {
	fx := newRetireFx(t)
	w := NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, "op-tags")
	w.restrictToTags()
	require.ErrorIs(t, w.RetireVersion(fx.spec()), ErrTagsOnlyWriter)
	require.ErrorIs(t, w.RehomeRetiredVersion("v", "b"), ErrTagsOnlyWriter)
}
