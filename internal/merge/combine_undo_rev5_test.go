// file: internal/merge/combine_undo_rev5_test.go
// version: 1.0.6
// guid: 5c1e8f47-2a9d-4b63-9e07-d84f3a6b21c9
// last-edited: 2026-10-18

package merge

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
)

// Round-5 review of #4064: regression tests for the survivor put-back's
// locking and markers, and for re-running an undo that could not record
// itself.

// rev5Store injects failures and client writes into an undo.
type rev5Store struct {
	*database.PebbleStore
	survivor string
	// onSurvivorPositions runs once, on the first read of the survivor's
	// positions, after the put-back read the survivor's state: where a
	// client write races the put-back's decision.
	onSurvivorPositions func()
	// failSurvState fails that many survivor state writes.
	failSurvState int
	// failMarkerDel fails that many reconcile-marker deletes.
	failMarkerDel int
	// failNotLanded fails the overwrite that withdraws a marker.
	failNotLanded bool
}

func (s *rev5Store) SetUserBookState(st *database.UserBookState) error {
	if st.BookID == s.survivor && s.failSurvState > 0 {
		s.failSurvState--
		return errors.New("injected survivor state write failure")
	}
	return s.PebbleStore.SetUserBookState(st)
}

func (s *rev5Store) DeleteRaw(key string) error {
	if strings.HasPrefix(key, survivorReconcilePrefix) && s.failMarkerDel > 0 {
		s.failMarkerDel--
		return errors.New("injected marker delete failure")
	}
	return s.PebbleStore.DeleteRaw(key)
}

func (s *rev5Store) SetRaw(key string, value []byte) error {
	if s.failNotLanded && strings.HasPrefix(key, survivorReconcilePrefix) && strings.Contains(string(value), `"not_landed":true`) {
		return errors.New("injected marker withdraw failure")
	}
	return s.PebbleStore.SetRaw(key, value)
}

func (s *rev5Store) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	if bookID == s.survivor && s.onSurvivorPositions != nil {
		f := s.onSurvivorPositions
		s.onSurvivorPositions = nil
		f()
	}
	return s.PebbleStore.ListUserPositionsForBook(userID, bookID)
}

// rev5Seed combines the undo fixture with listened time on both sides:
// 600 seconds on the absorbed book (carried), 100 on the survivor.
func rev5Seed(t *testing.T) (*database.PebbleStore, undoFixture, *CombineResult) {
	t.Helper()
	store := setupTestStore(t).(*database.PebbleStore)
	f := seedUndoFixture(t, store)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.absA, Status: "in_progress", ProgressPct: 40, LastActivityAt: at, LastSegmentID: "seg-a", TotalListenedSeconds: 600}))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.survivor, Status: "in_progress", ProgressPct: 10, LastActivityAt: at.Add(-time.Hour), LastSegmentID: "seg-s", TotalListenedSeconds: 100}))
	res, err := NewService(store).CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.survivor), "the follow carried the absorbed book's listened time")
	return store, f, res
}

func rev5Listened(t *testing.T, store *database.PebbleStore, user, book string) float64 {
	t.Helper()
	st, err := store.GetUserBookState(user, book)
	require.NoError(t, err)
	require.NotNil(t, st)
	return float64(st.TotalListenedSeconds)
}

// B1: a device sync that lands while the plain survivor put-back is deciding
// is kept. The client takes the user-state stripe, as the ABS handlers do,
// and starts from inside the put-back's read of the survivor's positions.
// The put-back holds the stripe from before that read until after its
// write (round 6), so the client waits and its write lands on top. Before
// round 5 the put-back took no stripe and did not re-read, so it wrote the
// before-snapshot (100 seconds) over the sync.
func TestRev5_PlainPutBackKeepsAClientWriteThatLandsBeforeTheStripe(t *testing.T) {
	store, f, res := rev5Seed(t)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor}
	fired := false
	var wait func() error
	w.onSurvivorPositions = func() {
		fired = true
		wait = stripedClientWrite(f.user.ID, f.survivor, func() error {
			cur, err := store.GetUserBookState(f.user.ID, f.survivor)
			if err != nil {
				return err
			}
			c := *cur
			c.TotalListenedSeconds += 900
			c.LastActivityAt = time.Now().UTC()
			c.ProgressPct = 88
			return store.SetUserBookState(&c)
		})
	}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.True(t, fired, "the client write started inside the survivor put-back")
	require.NoError(t, wait())
	// The carried 600 seconds go back to the absorbed book; the 900 the
	// client added stay on the survivor.
	require.Equal(t, float64(1000), rev5Listened(t, store, f.user.ID, f.survivor), "the client's 900 seconds are kept")
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.absA))
}

// S3: the user re-picks the combine's own title (column and lock) after an
// undo that completed but could not record itself. The re-run finds the
// undo's applied record and only records the journal; it used to replay,
// read both as the combine's, and take them away again.
func TestRev5_RerunKeepsAUserRepickOfTheOverrideValue(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	ms, keep, res := combineWithTitleOverride(t, store)
	undoWithoutRecording(t, store, ms, res.JournalID)
	_, err := store.ModifyBook(keep, func(b *database.Book) error { b.Title = "Override T"; return nil })
	require.NoError(t, err)
	require.NoError(t, database.RecordUserOverrides(store, keep, map[string]any{database.FieldKeyTitle: "Override T"}))

	re, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Empty(t, re.Warnings)
	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.Equal(t, "Override T", b.Title, "the title the user picked after the undo stays")
	require.Equal(t, "Override T", lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)), "and so does their lock")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, CombineJournalUndone, j.Status)
}

// S3: the user credits the override's author again after an unrecorded undo;
// the re-run leaves the link.
func TestRev5_RerunKeepsAReaddedOverrideAuthorLink(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	prior, err := store.CreateAuthor("Prior Writer")
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(keep, []database.BookAuthor{{BookID: keep, AuthorID: prior.ID, Role: "author"}}))
	ms := NewService(store)
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: "Override Writer"}, CombineOptions{})
	require.NoError(t, err)
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.NotNil(t, j.Override.AuthorIDAfter)
	a := *j.Override.AuthorIDAfter
	undoWithoutRecording(t, store, ms, res.JournalID)
	require.NoError(t, store.SetBookAuthors(keep, []database.BookAuthor{
		{BookID: keep, AuthorID: prior.ID, Role: "author", Position: 0},
		{BookID: keep, AuthorID: a, Role: "author", Position: 1},
	}))

	re, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Empty(t, re.Warnings)
	cur, err := store.GetBookAuthors(keep)
	require.NoError(t, err)
	found := false
	for _, l := range cur {
		if l.AuthorID == a && l.Role == "author" {
			found = true
		}
	}
	require.True(t, found, "the link the user added after the undo survives the re-run: %+v", cur)
}

// rev5AddListening is the user listening to the survivor: more listened time
// and a newer last-played, written as a client does.
func rev5AddListening(t *testing.T, store *database.PebbleStore, user, book string, seconds int) {
	t.Helper()
	cur, err := store.GetUserBookState(user, book)
	require.NoError(t, err)
	c := *cur
	c.TotalListenedSeconds += float64(seconds)
	c.LastActivityAt = time.Now().UTC()
	require.NoError(t, store.SetUserBookState(&c))
}

// S2: the plain put-back's state write fails and so does the delete of its
// marker. The marker is withdrawn as not landed, so the re-run after the
// user listened reconciles instead of skipping: the carried 600 seconds are
// not left on the survivor as well (it used to read 600 + 300 = 900).
func TestRev5_FailedPutBackWithAStuckMarkerIsNotReadAsLanded(t *testing.T) {
	store, f, res := rev5Seed(t)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor, failSurvState: 1, failMarkerDel: 1}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "injected survivor state write failure")
	require.NotContains(t, err.Error(), "neither dropped", "the marker was withdrawn")
	rev5AddListening(t, store, f.user.ID, f.survivor, 300)

	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, float64(400), rev5Listened(t, store, f.user.ID, f.survivor), "before 100 + the 300 listened since")
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.absA))
}

// S2, reconcile path: the user listened to the survivor before the undo, so
// it reconciles; that state write fails and the marker delete fails. The
// re-run after more listening reconciles again from the after-snapshot.
func TestRev5_FailedReconcileWithAStuckMarkerIsNotReadAsLanded(t *testing.T) {
	store, f, res := rev5Seed(t)
	rev5AddListening(t, store, f.user.ID, f.survivor, 200)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor, failSurvState: 1, failMarkerDel: 1}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "injected survivor state write failure")
	rev5AddListening(t, store, f.user.ID, f.survivor, 300)

	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.survivor), "before 100 + 200 + 300 listened since the follow")
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.absA))
}

// S2: when the marker can be neither deleted nor withdrawn, the error says
// what a re-run will do, and that re-run reports its skip.
func TestRev5_StuckMarkerIsNamedInTheErrorAndTheSkipIsReported(t *testing.T) {
	store, f, res := rev5Seed(t)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor, failSurvState: 1, failMarkerDel: 1, failNotLanded: true}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "could be neither dropped")
	j, err := NewService(store).GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, j.LastError, "could be neither dropped", "the journal names it")
	rev5AddListening(t, store, f.user.ID, f.survivor, 300)

	re, err := NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(re.Warnings, "\n"), "was put back by an earlier run of this undo and listened to since; left as is")
}

// A put-back whose state write fails (the marker delete works) is finished
// by the re-run with nothing listened in between.
func TestRev5_PartialPutBackThenRerun(t *testing.T) {
	store, f, res := rev5Seed(t)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor, failSurvState: 1}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.Error(t, err)
	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, float64(100), rev5Listened(t, store, f.user.ID, f.survivor))
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.absA))
}

// S1: a re-combine of the same pair after an undo whose marker drop failed.
// The absorbed book has a position only and the survivor no state row, so
// the follow's newest timestamp is the carried position's kept UpdatedAt:
// both follows had the same one, and the marker keyed by it made the second
// undo skip, leaving the carried position on the survivor. Markers are now
// keyed by a per-follow id.
func TestRev5_LeftoverMarkerDoesNotApplyToAReCombineOfTheSamePair(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	u := seedSyncUser(t, store)
	require.NoError(t, store.SetUserPosition(u.ID, gone, "seg-g", 777))
	ms := NewService(store)

	res1, err := ms.CombineBooks([]string{keep, gone}, keep, nil)
	require.NoError(t, err)
	j1, err := ms.GetCombineJournal(res1.JournalID)
	require.NoError(t, err)
	require.Len(t, j1.Absorbed, 1)
	require.Len(t, j1.Absorbed[0].Progress, 1, "the user is journaled")
	p1 := j1.Absorbed[0].Progress[0]
	require.NotEmpty(t, p1.FollowID)

	// Undo #1 completes; dropping its marker fails (best effort), so the
	// marker stays.
	_, err = NewService(&rev5Store{PebbleStore: store, survivor: keep, failMarkerDel: 1}).UndoCombine(res1.JournalID)
	require.NoError(t, err)
	rows, err := store.ScanPrefix(survivorReconcilePrefix)
	require.NoError(t, err)
	require.Len(t, rows, 1, "undo #1 left its marker")

	res2, err := ms.CombineBooks([]string{keep, gone}, keep, nil)
	require.NoError(t, err)
	j2, err := ms.GetCombineJournal(res2.JournalID)
	require.NoError(t, err)
	p2 := j2.Absorbed[0].Progress[0]
	require.Equal(t, followStamp(p1), followStamp(p2), "the two follows share a timestamp: the old key collided")
	require.NotEqual(t, survivorReconcileKey(keep, gone, p1), survivorReconcileKey(keep, gone, p2))

	// The user listens on the survivor.
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, store.SetUserPosition(u.ID, keep, "seg-new", 4321))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: keep, Status: "in_progress", ProgressPct: 30, LastActivityAt: time.Now().UTC(), LastSegmentID: "seg-new", TotalListenedSeconds: 50}))

	_, err = ms.UndoCombine(res2.JournalID)
	require.NoError(t, err)
	pos, err := store.ListUserPositionsForBook(u.ID, keep)
	require.NoError(t, err)
	var segs []string
	for _, p := range pos {
		segs = append(segs, p.SegmentID)
	}
	require.Equal(t, []string{"seg-new"}, segs, "the carried position was taken back off the survivor and the user's own stays")
}

// A journal from before FollowID keeps the timestamp marker key, so a re-run
// of its undo still finds the markers an earlier run of the same journal
// wrote (survivorReconcileKey).
func TestRev5_JournalWithoutFollowIDKeepsTheTimestampMarkerKey(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	p := CombineUserProgress{UserID: "u", SurvivorPosAfter: []database.UserPosition{{SegmentID: "s", UpdatedAt: at}}}
	require.Equal(t, fmt.Sprintf("%ss:a:u:%d", survivorReconcilePrefix, at.UnixNano()), survivorReconcileKey("s", "a", p))
	p.FollowID = "F1"
	require.Equal(t, survivorReconcilePrefix+"s:a:u:f:F1", survivorReconcileKey("s", "a", p))
}

// lockRaceStore starts another lock-row writer right after the combine's
// lock write, as the combine reads its lock rows back.
type lockRaceStore struct {
	*database.PebbleStore
	book    string
	armed   bool
	started bool
	done    chan struct{}
}

func (s *lockRaceStore) UpsertMetadataFieldState(st *database.MetadataFieldState) error {
	err := s.PebbleStore.UpsertMetadataFieldState(st)
	if st.BookID == s.book && !s.started {
		s.armed = true
	}
	return err
}

func (s *lockRaceStore) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	if bookID == s.book && s.armed {
		s.armed, s.started = false, true
		go func() {
			defer close(s.done)
			_ = database.RecordUserOverrides(s.PebbleStore, bookID, map[string]any{database.FieldKeyTitle: "Someone Else"})
		}()
		// The other writer gets the lock at once unless the combine still
		// holds it.
		select {
		case <-s.done:
		case <-time.After(300 * time.Millisecond):
		}
	}
	return s.PebbleStore.GetMetadataFieldStates(bookID)
}

// NIT 1: the combine journals the lock row its own write left, not one a
// writer set between that write and the read-back. The read-back used to run
// after the metadata-state lock was released.
func TestRev5_OverrideLockReadBackIsTheCombinesOwn(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	w := &lockRaceStore{PebbleStore: store, book: keep, done: make(chan struct{})}
	ms := NewService(w)
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Title: "Override T"}, CombineOptions{})
	require.NoError(t, err)
	require.True(t, w.started, "the other writer ran")
	<-w.done
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, "Override T", lockValue(t, j.Override.FieldStatesAfter[database.FieldKeyTitle]), "the journal holds the combine's lock")
	require.Equal(t, "Someone Else", lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)), "the other writer's lock landed after")
}

// NIT 2: the fill-empty undo takes the author link it filled back off the
// survivor and sets the book row's AuthorID from the links left. It used to
// take the first link whatever its role; a narrator credit added since
// became the book's primary author.
func TestRev5_FillEmptyUndoPicksAnAuthorLinkForAuthorID(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	filled, err := store.CreateAuthor("Filled Writer")
	require.NoError(t, err)
	narrator, err := store.CreateAuthor("Some Narrator")
	require.NoError(t, err)
	later, err := store.CreateAuthor("Later Writer")
	require.NoError(t, err)
	keep := ulid.Make().String()
	_, err = store.CreateBook(&database.Book{ID: keep, Title: "Keep", Format: "mp3", FilePath: "/tmp/rev5/keep.mp3", AuthorID: &filled.ID})
	require.NoError(t, err)
	// The merge filled the empty author; since then a narrator credit (first)
	// and another author were added.
	require.NoError(t, store.SetBookAuthors(keep, []database.BookAuthor{
		{BookID: keep, AuthorID: narrator.ID, Role: "narrator", Position: 0},
		{BookID: keep, AuthorID: filled.ID, Role: "author", Position: 1},
		{BookID: keep, AuthorID: later.ID, Role: "author", Position: 2},
	}))
	j := NewCombineJournal(keep)
	j.Status = CombineJournalApplied
	j.StepJournaled = true
	j.FilledEmpty = &CombineFillEmpty{AuthorID: &filled.ID, Authors: []database.BookAuthor{{BookID: keep, AuthorID: filled.ID, Role: "author"}}}
	require.NoError(t, WriteCombineJournal(store, j))

	_, err = NewService(store).UndoCombine(j.ID)
	require.NoError(t, err)
	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.NotNil(t, b.AuthorID)
	require.Equal(t, later.ID, *b.AuthorID, "the author link left, not the narrator credit")
	links, err := store.GetBookAuthors(keep)
	require.NoError(t, err)
	require.Len(t, links, 2)
}

func TestRev5_PrimaryAuthorOfSkipsNonAuthorRoles(t *testing.T) {
	require.Nil(t, primaryAuthorOf([]database.BookAuthor{{AuthorID: 1, Role: "narrator"}, {AuthorID: 2, Role: "editor", Position: 1}}))
	got := primaryAuthorOf([]database.BookAuthor{{AuthorID: 1, Role: "narrator"}, {AuthorID: 4, Role: "co-author"}, {AuthorID: 3, Role: "Author ", Position: 2}, {AuthorID: 2, Role: "", Position: 1}})
	require.NotNil(t, got)
	require.Equal(t, 2, *got, "the lowest position among author-role links, over an earlier co-author")
	got = primaryAuthorOf([]database.BookAuthor{{AuthorID: 1, Role: "narrator"}, {AuthorID: 5, Role: "co-author", Position: 1}})
	require.NotNil(t, got)
	require.Equal(t, 5, *got, "a co-author when no author link is left")
}
