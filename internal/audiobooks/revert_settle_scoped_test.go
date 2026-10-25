// file: internal/audiobooks/revert_settle_scoped_test.go
// version: 1.2.0
// guid: cc0f61e4-02a1-4f7d-b22f-ec410f2ddc4a
// last-edited: 2026-10-25

package audiobooks

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// settleOwedIn reports whether the record holds a group owed to a failed settle
// (a pending intent alone does not count).
func settleOwedIn(rs *RevertService, record string) bool {
	kv, _ := rs.db.(rawKV)
	rec, err := readSettleOwed(kv, record)
	if err != nil {
		return false
	}
	for _, og := range rec.Groups {
		if !og.Pending {
			return true
		}
	}
	return false
}

// A scoped run that dies after restoring a book's primary flag and before it
// settles leaves its intent under its own record. A later whole-operation
// revert must find it: it counts the row it finds already restored as
// evidence and settles the group, instead of leaving it with two primaries.
func TestRevertOperation_SettlesAGroupADeadScopedRunLeftPending(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, yes := "vg-scoped-pending", true
	// The state the dead scoped run left: its demote row was reverted (o is
	// explicit true again) beside x, which the operation's hand-off crowned.
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	x, err := s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	require.NoError(t, s.CreateOperationChange(&database.OperationChange{
		OperationID: "op-scoped-pending", BookID: o.ID, ChangeType: undo.ChangeTypeBookPrimaryDemote,
		FieldName: "is_primary_version", OldValue: "true", NewValue: "false",
	}))
	scoped := newRevertScope([]string{o.ID}).recordID("op-scoped-pending")
	rs.markSettlePending(scoped, gid)
	require.True(t, rs.settlePendingGroups(scoped)[gid], "the dead scoped run's intent is recorded under its own key")

	res, err := rs.RevertOperation("op-scoped-pending")
	require.NoError(t, err, "%+v", res)
	assert.Empty(t, res.HandOffFailed)

	primaries := 0
	for _, id := range []string{o.ID, x.ID} {
		b, err := s.GetBookByID(id)
		require.NoError(t, err)
		if database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
			primaries++
		}
	}
	assert.Equal(t, 1, primaries, "the group is settled to one primary, not left with two")
	assert.Empty(t, rs.settlePendingGroups(scoped), "the scoped record is folded in and cleared")
	assert.Empty(t, rs.settlePendingGroups("op-scoped-pending"), "and the operation's record is cleared once settled")
}

// The same for a group a scoped run owed because its settle FAILED: the whole
// operation's revert retries it, with the later-pick check.
func TestRevertOperation_RetriesAGroupADeadScopedRunOwed(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, no := "vg-scoped-owed", false
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &no})
	require.NoError(t, err)
	_, err = s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, IsPrimaryVersion: &no})
	require.NoError(t, err)
	// No member is primary (o was restored to explicit false by a run that
	// failed to crown it).
	scoped := newRevertScope([]string{o.ID}).recordID("op-scoped-owed")
	require.NoError(t, rs.storeSettleOwed(s, scoped, settleOwedRecord{Groups: map[string]settleOwedGroup{
		gid: {Originals: []string{o.ID}},
	}}))
	// The operation's rows are gone (purged): only the owed record is left.
	res, err := rs.RevertOperation("op-scoped-owed")
	require.NoError(t, err, "%+v", res)
	b, err := s.GetBookByID(o.ID)
	require.NoError(t, err)
	assert.True(t, database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion), "the owed original is crowned")
	assert.False(t, settleOwedIn(rs, scoped), "the scoped record is cleared")
	assert.False(t, settleOwedIn(rs, "op-scoped-owed"))
}

func TestMergeSettleOwedGroup(t *testing.T) {
	pend := settleOwedGroup{Pending: true}
	owedA := settleOwedGroup{Originals: []string{"a", "b"}, ExplicitTrue: []string{"x", "y"}}
	owedB := settleOwedGroup{Originals: []string{"b", "c"}, ExplicitTrue: []string{"y", "z"}}

	assert.Equal(t, pend, mergeSettleOwedGroup(pend, pend))
	assert.Equal(t, owedA, mergeSettleOwedGroup(pend, owedA), "an owed settle beats a pending intent")
	assert.Equal(t, owedA, mergeSettleOwedGroup(owedA, pend))
	merged := mergeSettleOwedGroup(owedA, owedB)
	assert.Equal(t, []string{"a", "b", "c"}, merged.Originals)
	assert.Equal(t, []string{"y"}, merged.ExplicitTrue, "only a member explicit true in both snapshots is not a later pick")
	assert.False(t, merged.Pending)
}

// settleFaultStore is a PebbleStore whose reads can fail on demand and that
// notes every write to an owed-settle key.
type settleFaultStore struct {
	*database.PebbleStore
	mu               sync.Mutex
	dieOnGetBook     bool
	failGetRaw       map[string]bool
	owedWrites       []string // "set:<key>" / "delete:<key>"
	failedReadsOfKey int
}

// errSettleTestDied stands for the process dying: the store panics with it on
// the first book read once armed, and the test recovers it.
var errSettleTestDied = errors.New("synthetic process death")

func (s *settleFaultStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	die := s.dieOnGetBook
	s.mu.Unlock()
	if die {
		panic(errSettleTestDied)
	}
	return s.PebbleStore.GetBookByID(id)
}

func (s *settleFaultStore) GetRaw(key string) ([]byte, error) {
	s.mu.Lock()
	fail := s.failGetRaw[key]
	if fail {
		s.failedReadsOfKey++
	}
	s.mu.Unlock()
	if fail {
		return nil, errors.New("synthetic raw read error")
	}
	return s.PebbleStore.GetRaw(key)
}

func (s *settleFaultStore) SetRaw(key string, value []byte) error {
	s.mu.Lock()
	s.owedWrites = append(s.owedWrites, "set:"+key)
	s.mu.Unlock()
	return s.PebbleStore.SetRaw(key, value)
}

func (s *settleFaultStore) DeleteRaw(key string) error {
	s.mu.Lock()
	s.owedWrites = append(s.owedWrites, "delete:"+key)
	s.mu.Unlock()
	return s.PebbleStore.DeleteRaw(key)
}

// settleScopeFx is a retire op whose shell's primary demote was already
// reverted (the state a scoped run that died after restoring rows and before its
// settle leaves): the shell is explicit primary again beside the holder, which
// the operation's hand-off crowned. Until the group is settled it has two
// primaries; the settle crowns the shell, the operation's original, back.
type settleScopeFx struct {
	*retireRevertFx
	op, group string
}

func newSettleScopeFx(t *testing.T, op string) *settleScopeFx {
	t.Helper()
	fx := newRetireRevertFx(t)
	w := fx.writer(op)
	fx.retire(t, w, "ver-"+op)
	require.NoError(t, w.Journal(fx.shell.ID, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))
	untrashBook(t, fx.st, fx.shell)
	group, yes := "vg-"+op, true
	for _, id := range []string{fx.holder.ID, fx.shell.ID} {
		_, err := fx.st.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID, b.IsPrimaryVersion = &group, &yes
			return nil
		})
		require.NoError(t, err)
	}
	return &settleScopeFx{retireRevertFx: fx, op: op, group: group}
}

func (fx *settleScopeFx) primaries(t *testing.T) int {
	t.Helper()
	n := 0
	for _, id := range []string{fx.holder.ID, fx.shell.ID} {
		b, err := fx.st.GetBookByID(id)
		require.NoError(t, err)
		if database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
			n++
		}
	}
	return n
}

func (fx *settleScopeFx) scopedRecord() string {
	return newRevertScope([]string{fx.shell.ID}).recordID(fx.op)
}

// A scoped run dies after restoring rows and before its settle. A whole-op run
// then returns early, before ITS settle. That run must have moved nothing: the
// scoped retry still finds its own intent and settles the group, ending with
// exactly one primary (the eager fold of the scoped record into the op's moved
// the intent to a record only a whole-op run would read).
func TestRevertOperationForBooks_ScopedRetrySettlesAfterAWholeOpRunReturnedEarly(t *testing.T) {
	fx := newSettleScopeFx(t, "op-early-whole")
	rs := NewRevertService(fx.st)
	rs.markSettlePending(fx.scopedRecord(), fx.group)
	require.Equal(t, 2, fx.primaries(t), "the dead run left two primaries")

	// The whole-op run reads its rows and dies before its settle.
	faulty := &settleFaultStore{PebbleStore: fx.st, dieOnGetBook: true}
	require.PanicsWithValue(t, errSettleTestDied, func() { _, _ = NewRevertService(faulty).RevertOperation(fx.op) })
	assert.True(t, rs.settlePendingGroups(fx.scopedRecord())[fx.group],
		"the early return left the scoped run's intent under its own key")
	assert.Empty(t, rs.settlePendingGroups(fx.op), "and put nothing in the operation's record")
	assert.Empty(t, faulty.owedWrites, "it wrote and deleted no owed record")

	res, err := rs.RevertOperationForBooks(fx.op, []string{fx.shell.ID})
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, 1, fx.primaries(t), "the scoped retry settled the group: exactly one primary")
	assert.Empty(t, rs.settlePendingGroups(fx.scopedRecord()), "and cleared its own record")
}

// The mirror: a whole-op run that died left its intent in the operation's
// record; a scoped run of the same books counts the rows it finds already
// restored as evidence and settles the group.
func TestRevertOperationForBooks_ScopedRunSeesADeadWholeOpRunsPendingGroup(t *testing.T) {
	fx := newSettleScopeFx(t, "op-dead-whole")
	rs := NewRevertService(fx.st)
	rs.markSettlePending(fx.op, fx.group)
	require.Equal(t, 2, fx.primaries(t))

	res, err := rs.RevertOperationForBooks(fx.op, []string{fx.shell.ID})
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, 1, fx.primaries(t), "the whole-op run's pending group was settled by the scoped run")
	assert.True(t, rs.settlePendingGroups(fx.op)[fx.group],
		"a scoped run reads the operation's record and never writes it")
}

// A whole-op run that does settle folds the scoped record in and deletes it
// only once the operation's record is stored.
func TestRevertOperation_FoldedScopedRecordIsDeletedAfterTheStore(t *testing.T) {
	fx := newSettleScopeFx(t, "op-fold-order")
	rs := NewRevertService(fx.st)
	rs.markSettlePending(fx.scopedRecord(), fx.group)

	faulty := &settleFaultStore{PebbleStore: fx.st}
	res, err := NewRevertService(faulty).RevertOperation(fx.op)
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, 1, fx.primaries(t))
	scopedKey := settleOwedKeyPrefix + fx.scopedRecord()
	opKey := settleOwedKeyPrefix + fx.op
	require.Contains(t, faulty.owedWrites, "delete:"+scopedKey)
	assert.Empty(t, rs.settlePendingGroups(fx.scopedRecord()))
	// The settle succeeds here, so storeSettleOwed has nothing to keep and
	// deletes the operation's record ("delete:<op>"); a failed settle would
	// write "set:<op>". Either way the store is the operation record's last
	// write, and it must precede the scoped delete: deleting the folded
	// records first loses what they owed if the run dies before the store.
	lastOpWrite, scopedDelete := -1, -1
	for i, w := range faulty.owedWrites {
		switch w {
		case "set:" + opKey, "delete:" + opKey:
			lastOpWrite = i
		case "delete:" + scopedKey:
			scopedDelete = i
		}
	}
	require.GreaterOrEqual(t, lastOpWrite, 0, "the run stored the operation's record: %v", faulty.owedWrites)
	assert.Less(t, lastOpWrite, scopedDelete,
		"the operation's record is stored before the folded scoped record is deleted: %v", faulty.owedWrites)
}

// A failed or undecodable read of the operation's record must not let a write
// drop the groups it owes, and a scoped record that does not decode is never
// deleted.
func TestRevertOperation_UnreadableOwedRecordsAreNeverOverwrittenOrDeleted(t *testing.T) {
	t.Run("operation record read fails", func(t *testing.T) {
		fx := newSettleScopeFx(t, "op-n1-read")
		rs := NewRevertService(fx.st)
		require.NoError(t, rs.storeSettleOwed(fx.st, fx.op, settleOwedRecord{Groups: map[string]settleOwedGroup{
			"vg-other-owed": {Originals: []string{"book-other"}},
		}}))
		rs.markSettlePending(fx.scopedRecord(), fx.group)
		opKey, scopedKey := settleOwedKeyPrefix+fx.op, settleOwedKeyPrefix+fx.scopedRecord()

		faulty := &settleFaultStore{PebbleStore: fx.st, failGetRaw: map[string]bool{opKey: true}}
		_, err := NewRevertService(faulty).RevertOperation(fx.op)
		require.NoError(t, err)
		assert.Greater(t, faulty.failedReadsOfKey, 0)
		assert.NotContains(t, faulty.owedWrites, "set:"+opKey, "no write over a record that could not be read")
		assert.NotContains(t, faulty.owedWrites, "delete:"+opKey)
		assert.NotContains(t, faulty.owedWrites, "delete:"+scopedKey, "and nothing folded into it is deleted")
		got := rs.settlePendingGroups(fx.scopedRecord())
		assert.True(t, got[fx.group], "the scoped record is still there")
		rec, err := readSettleOwed(fx.st, fx.op)
		require.NoError(t, err)
		assert.Contains(t, rec.Groups, "vg-other-owed", "the operation's own owed group survives")
	})
	t.Run("operation record undecodable", func(t *testing.T) {
		fx := newSettleScopeFx(t, "op-n1-decode")
		rs := NewRevertService(fx.st)
		require.NoError(t, fx.st.SetRaw(settleOwedKeyPrefix+fx.op, []byte("{not json")))
		_, err := rs.RevertOperation(fx.op)
		require.NoError(t, err)
		raw, err := fx.st.GetRaw(settleOwedKeyPrefix + fx.op)
		require.NoError(t, err)
		assert.Equal(t, "{not json", string(raw), "left as it was")
	})
	t.Run("scoped record undecodable", func(t *testing.T) {
		fx := newSettleScopeFx(t, "op-n1-scoped")
		rs := NewRevertService(fx.st)
		bad := settleOwedKeyPrefix + fx.op + scopedRecordMarker + "zz-bad"
		require.NoError(t, fx.st.SetRaw(bad, []byte("{not json")))
		rs.markSettlePending(fx.scopedRecord(), fx.group)
		res, err := rs.RevertOperation(fx.op)
		require.NoError(t, err, "%+v", res)
		raw, err := fx.st.GetRaw(bad)
		require.NoError(t, err)
		assert.Equal(t, "{not json", string(raw), "an undecodable scoped record is not deleted")
		assert.Empty(t, rs.settlePendingGroups(fx.scopedRecord()), "the decodable one is folded and cleared")
		assert.Equal(t, 1, fx.primaries(t))
	})
}
