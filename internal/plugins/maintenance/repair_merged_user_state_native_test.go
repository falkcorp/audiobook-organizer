// file: internal/plugins/maintenance/repair_merged_user_state_native_test.go
// version: 1.1.0
// guid: 3c9d2e71-6b04-4a58-9f1e-7d2b8a5c4e30
// last-edited: 2026-11-15

package maintenance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// t6g6UserStateSpy is the repair-merged kind's two halves over a real in-memory
// store: reads pass through, and every write is recorded into rec (the run's
// recorder) before it reaches the store. It is both repairMergedUserStateReads
// and repairMergedUserStateWrites; the recorder and the test store forward to it
// by name so neither set of methods is ambiguous.
type t6g6UserStateSpy struct {
	s   *database.PebbleStore
	rec func(string)
}

func (y *t6g6UserStateSpy) ListUsers() ([]database.User, error) { return y.s.ListUsers() }
func (y *t6g6UserStateSpy) ScanPrefix(p string) ([]database.KVPair, error) {
	return y.s.ScanPrefix(p)
}
func (y *t6g6UserStateSpy) ListUserPositionsSince(u string, t time.Time) ([]database.UserPosition, error) {
	return y.s.ListUserPositionsSince(u, t)
}
func (y *t6g6UserStateSpy) GetBookByID(id string) (*database.Book, error) { return y.s.GetBookByID(id) }
func (y *t6g6UserStateSpy) GetUserBookState(u, b string) (*database.UserBookState, error) {
	return y.s.GetUserBookState(u, b)
}
func (y *t6g6UserStateSpy) ListUserPositionsForBook(u, b string) ([]database.UserPosition, error) {
	return y.s.ListUserPositionsForBook(u, b)
}
func (y *t6g6UserStateSpy) UserStateSyncReads() (t6g6SyncReads, bool) {
	s := database.AsSyncIdentityStore(y.s)
	return s, s != nil
}
func (y *t6g6UserStateSpy) UserStateBookmarkReads() (t6g6BookmarkReads, bool) {
	s := database.AsBookmarkStore(y.s)
	return s, s != nil
}

func (y *t6g6UserStateSpy) SetRaw(k string, v []byte) error {
	y.rec("SetRaw " + k)
	return y.s.SetRaw(k, v)
}
func (y *t6g6UserStateSpy) DeleteRaw(k string) error {
	y.rec("DeleteRaw " + k)
	return y.s.DeleteRaw(k)
}
func (y *t6g6UserStateSpy) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	y.rec("ModifyBook " + id)
	return y.s.ModifyBook(id, fn)
}
func (y *t6g6UserStateSpy) SetUserBookState(st *database.UserBookState) error {
	y.rec("SetUserBookState " + st.BookID)
	return y.s.SetUserBookState(st)
}
func (y *t6g6UserStateSpy) SetUserPosition(u, b, seg string, pos float64) error {
	y.rec("SetUserPosition " + b)
	return y.s.SetUserPosition(u, b, seg, pos)
}
func (y *t6g6UserStateSpy) ClearUserPositions(u, b string) error {
	y.rec("ClearUserPositions " + b)
	return y.s.ClearUserPositions(u, b)
}
func (y *t6g6UserStateSpy) UserStateSyncWrites() (t6g6SyncWrites, bool) {
	return t6g6SpySync{y}, true
}
func (y *t6g6UserStateSpy) UserStateBookmarkWrites() (t6g6BookmarkWrites, bool) {
	return t6g6SpyBookmarks{y}, true
}
func (y *t6g6UserStateSpy) UserStateTimestampWriter() (database.UserPositionTimestampWriter, bool) {
	return t6g6SpyStamp{y}, true
}

type t6g6SpySync struct{ y *t6g6UserStateSpy }

func (w t6g6SpySync) MintOrGetSyncID(b string) (string, error) {
	w.y.rec("MintOrGetSyncID " + b)
	return database.AsSyncIdentityStore(w.y.s).MintOrGetSyncID(b)
}
func (w t6g6SpySync) RecordSyncMerge(l, win string) error {
	w.y.rec("RecordSyncMerge " + l)
	return database.AsSyncIdentityStore(w.y.s).RecordSyncMerge(l, win)
}

type t6g6SpyBookmarks struct{ y *t6g6UserStateSpy }

func (w t6g6SpyBookmarks) CopyBookmarkIfAbsent(b progress.Bookmark) (bool, error) {
	w.y.rec("CopyBookmarkIfAbsent " + b.ItemID)
	return database.AsBookmarkCopier(w.y.s).CopyBookmarkIfAbsent(b)
}

type t6g6SpyStamp struct{ y *t6g6UserStateSpy }

func (w t6g6SpyStamp) SetUserPositionAt(u, b, seg string, pos float64, at time.Time) error {
	w.y.rec("SetUserPositionAt " + b)
	return w.y.s.SetUserPositionAt(u, b, seg, pos, at)
}

// Recorder forwards: the repair-merged writes go to the spy.
func (r *t6g6Recorder) SetRaw(k string, v []byte) error { return r.us.SetRaw(k, v) }
func (r *t6g6Recorder) DeleteRaw(k string) error        { return r.us.DeleteRaw(k) }
func (r *t6g6Recorder) SetUserBookState(s *database.UserBookState) error {
	return r.us.SetUserBookState(s)
}
func (r *t6g6Recorder) SetUserPosition(u, b, seg string, p float64) error {
	return r.us.SetUserPosition(u, b, seg, p)
}
func (r *t6g6Recorder) ClearUserPositions(u, b string) error { return r.us.ClearUserPositions(u, b) }
func (r *t6g6Recorder) UserStateSyncWrites() (t6g6SyncWrites, bool) {
	return r.us.UserStateSyncWrites()
}
func (r *t6g6Recorder) UserStateBookmarkWrites() (t6g6BookmarkWrites, bool) {
	return r.us.UserStateBookmarkWrites()
}
func (r *t6g6Recorder) UserStateTimestampWriter() (database.UserPositionTimestampWriter, bool) {
	return r.us.UserStateTimestampWriter()
}

// t6g6UserStateStore is the read side over the same real store.
type t6g6UserStateStore struct {
	t6g6Store
	y *t6g6UserStateSpy
}

func (s t6g6UserStateStore) ListUsers() ([]database.User, error) { return s.y.ListUsers() }
func (s t6g6UserStateStore) ScanPrefix(p string) ([]database.KVPair, error) {
	return s.y.ScanPrefix(p)
}
func (s t6g6UserStateStore) ListUserPositionsSince(u string, t time.Time) ([]database.UserPosition, error) {
	return s.y.ListUserPositionsSince(u, t)
}
func (s t6g6UserStateStore) GetBookByID(id string) (*database.Book, error) {
	return s.y.GetBookByID(id)
}
func (s t6g6UserStateStore) GetUserBookState(u, b string) (*database.UserBookState, error) {
	return s.y.GetUserBookState(u, b)
}
func (s t6g6UserStateStore) ListUserPositionsForBook(u, b string) ([]database.UserPosition, error) {
	return s.y.ListUserPositionsForBook(u, b)
}
func (s t6g6UserStateStore) UserStateSyncReads() (t6g6SyncReads, bool) {
	return s.y.UserStateSyncReads()
}
func (s t6g6UserStateStore) UserStateBookmarkReads() (t6g6BookmarkReads, bool) {
	return s.y.UserStateBookmarkReads()
}

// The native kind reads through env.Store and writes through env.Writer: a live
// run moves the stranded state onto the survivor, every write reaches the
// recorder (and is within the declared WriteSet), and a Preview run writes
// nothing at all.
func TestT6G6More_RepairMergedThroughShell(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}
	build := func(t *testing.T) (*t6g6Probe, database.Store, string, string, string, tasks.Definition) {
		store, userID, winnerID, loserID := strandedFixture(t)
		pebble, ok := store.(*database.PebbleStore)
		require.True(t, ok)
		p := newT6G6Probe(t)
		y := &t6g6UserStateSpy{s: pebble, rec: p.rec.rec}
		p.rec.us = y
		p.store = t6g6UserStateStore{y: y}
		defs := (&Plugin{}).t6g6Kinds(p.deps(), nil)
		return p, store, userID, winnerID, loserID, t6g6ByKind(t, defs, repairMergedUserStateKind)
	}

	p, store, userID, winnerID, loserID, def := build(t)
	_, err := runT6G6Kind(t, def, tasks.ModeLive, gate, `{}`)
	require.NoError(t, err)
	require.Same(t, gate, p.given, "NewWriter must be given the run's writer")
	require.Equal(t, 1, p.writerCalls())
	w, err := store.GetUserBookState(userID, winnerID)
	require.NoError(t, err)
	require.NotNil(t, w, "the survivor holds the state")
	require.Equal(t, 61, w.ProgressPct)
	lpos, err := store.ListUserPositionsForBook(userID, loserID)
	require.NoError(t, err)
	require.Empty(t, lpos, "the loser is drained")
	calls := p.rec.snapshot()
	require.NotEmpty(t, calls)
	require.Contains(t, calls, "SetUserBookState "+winnerID)
	t6g6AssertWritesDeclared(t, calls, def)

	// Preview reports without writing.
	p, store, userID, winnerID, loserID, def = build(t)
	_, err = runT6G6Kind(t, def, tasks.ModePreview, nil, `{}`)
	require.NoError(t, err)
	require.Empty(t, p.rec.snapshot(), "a preview wrote")
	w, err = store.GetUserBookState(userID, winnerID)
	require.NoError(t, err)
	require.Nil(t, w)
	l, err := store.GetUserBookState(userID, loserID)
	require.NoError(t, err)
	require.Equal(t, 61, l.ProgressPct)
}

// Every capability the halves carry is visible on the composed store at once:
// the merge package probes the whole object, so a layer that hid the ones below
// it would silently skip the survivor follow or the bookmark copy.
func TestT6G6More_ComposedUserStateStacksCapabilities(t *testing.T) {
	store, _, _, _ := strandedFixture(t)
	h := t6g6HeadUserState{UserStateRepairStore: store.(*database.PebbleStore)}
	c := t6g6ComposeUserState(h, h)
	require.NotNil(t, database.AsSyncIdentityStore(c), "sync identity")
	require.NotNil(t, database.AsBookmarkStore(c), "bookmarks")
	require.NotNil(t, database.AsBookmarkCopier(c), "bookmark copier")
	_, ok := database.AsCapability[database.UserPositionTimestampWriter](c)
	require.True(t, ok, "timestamp writer")
}

// pending_only reaches the repair: with it set, only the durable pending-repair
// records are completed, so the stranded state a bare run would infer a survivor
// for stays where it is.
func TestT6G6More_RepairMergedPendingOnlyReachesTheCore(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}
	run := func(t *testing.T, params string) (stateOnWinner bool) {
		store, userID, winnerID, _ := strandedFixture(t)
		pebble, ok := store.(*database.PebbleStore)
		require.True(t, ok)
		p := newT6G6Probe(t)
		y := &t6g6UserStateSpy{s: pebble, rec: p.rec.rec}
		p.rec.us = y
		p.store = t6g6UserStateStore{y: y}
		def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), repairMergedUserStateKind)
		_, err := runT6G6Kind(t, def, tasks.ModeLive, gate, params)
		require.NoError(t, err)
		w, err := store.GetUserBookState(userID, winnerID)
		require.NoError(t, err)
		return w != nil
	}
	require.True(t, run(t, `{}`), "a bare run moves the stranded state")
	require.False(t, run(t, `{"pending_only":true}`), "pending_only leaves it: no pending record names it")
}
