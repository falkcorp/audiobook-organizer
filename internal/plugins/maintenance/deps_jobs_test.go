// file: internal/plugins/maintenance/deps_jobs_test.go
// version: 0.1.0
// guid: 5a2f9c18-d3e7-4b46-a081-c9e47f1b6d25
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// bareJobsStore is a jobs.Store with no capability the jobs reach: embedding
// the interface promotes only its methods.
type bareJobsStore struct{ jobs.Store }

func jobsDeclared(label string) []entity.Ref {
	var refs []entity.Ref
	for _, r := range t6WriterResources[label] {
		refs = append(refs, tasks.Res(writer.Module, string(r)))
	}
	return refs
}

// TestBooksJobsStore_ReadsHaveNoRowAndWritesAreGated: jobsReads is exactly the
// methods of jobs.Store with no row, so the embedded value cannot write, and
// every other method of jobs.Store is a gated write with a row (the two raw
// writes are routed by key family).
func TestBooksJobsStore_ReadsHaveNoRowAndWritesAreGated(t *testing.T) {
	reads := reflect.TypeFor[jobsReads]()
	for method := range reads.Methods() {
		n := method.Name
		// Key presence, not length: a method with an empty row still has a row (it
		// runs unfenced), which is a write through the embedded store.
		require.NotContains(t, t6WriterResources, n, "%s is in jobsReads but has a row: a write through the embedded store", n)
	}
	st := reflect.TypeFor[jobs.Store]()
	for method := range st.Methods() {
		n := method.Name
		if _, isRead := reads.MethodByName(n); isRead {
			continue
		}
		if n == "SetRaw" || n == "DeleteRaw" {
			continue
		}
		require.NotEmpty(t, t6WriterResources[n], "%s is a jobs.Store write with no row in the shared table", n)
		require.Contains(t, jobsWriteLabels, n, "%s has a row but is not a gated label of the wrapper", n)
	}
}

func TestBooksJobsStore_NoUnwrapAndNoPromotedStore(t *testing.T) {
	s, _ := g2Fixture(t)
	deps, err := newJobsDeps(s)
	require.NoError(t, err)
	w := deps.NewStore(writertest.ForSet("books.maintenance.x", nil, tasks.ModeLive))
	_, ok := w.(database.StoreUnwrapper)
	require.False(t, ok, "an Unwrap hands every write to AsCapability unstamped")
	require.Nil(t, database.AsPebbleStore(w), "no promoted store: the Pebble store is not reachable through the wrapper")
	require.Nil(t, database.NewPebbleMetricsStoreFromStore(w), "so the metrics store cannot be built from it either")
	// sweep-pebble-metrics-ttl resolves its sweeper capability first
	// (internal/maintenance/jobs.resolveMetricsSweeper), which the wrapper
	// carries as a gated method of its own.
	_, ok = w.(interface{ SweepExpiredMetrics() (int64, error) })
	require.True(t, ok, "the sweep job's narrow capability is on the wrapper")
}

func TestBooksJobsDeps_BuildRefusesASourceWithoutTheCapabilities(t *testing.T) {
	d, err := newJobsDeps(nil)
	require.Error(t, err)
	require.Nil(t, d.NewStore)

	s, _ := g2Fixture(t)
	d, err = newJobsDeps(bareJobsStore{s})
	require.ErrorContains(t, err, "SyncIdentityStore")
	require.ErrorContains(t, err, "SnapshotPrunePlanner")
	require.Nil(t, d.NewStore, "no NewStore: the run fails before the job starts")

	d, err = newJobsDeps(s)
	require.NoError(t, err)
	require.NotNil(t, d.NewStore)
}

func TestBooksJobsStore_RawWritesAreRoutedByKeyFamily(t *testing.T) {
	cases := []struct {
		verb, key string
		row       []ops.Resource
	}{
		{"SetRaw", "merge:combine-journal:j1", []ops.Resource{ops.ResOperations}},
		{"SetRaw", "merge:combine-undo-applied:j1", []ops.Resource{ops.ResOperations}},
		{"SetRaw", "merge:group-redirect:g1", []ops.Resource{ops.ResBooks}},
		{"SetRaw", "merge_user_state_pending:a:b", []ops.Resource{ops.ResUserState}},
		{"SetRaw", "merge:survivor-reconcile:a:b", []ops.Resource{ops.ResUserState}},
		{"DeleteRaw", "merge:combine-undo-applied:j1", []ops.Resource{ops.ResOperations}},
		{"DeleteRaw", "merge:group-redirect:g1", []ops.Resource{ops.ResBooks}},
		{"DeleteRaw", "merge_user_state_pending:a:b", []ops.Resource{ops.ResUserState}},
		{"DeleteRaw", "book:series:dead", []ops.Resource{ops.ResBooks}},
		{"DeleteRaw", "book:author:dead", []ops.Resource{ops.ResBooks}},
	}
	for _, c := range cases {
		require.Equal(t, c.row, t6WriterResources[jobsRawLabel(c.verb, c.key)], "%s %s", c.verb, c.key)
	}
	// No family, or a family that is delete-only: no row, so the gate refuses.
	for _, c := range [][2]string{{"SetRaw", "settings:x"}, {"DeleteRaw", "settings:x"}, {"SetRaw", "book:series:dead"}} {
		require.NotContains(t, jobsRows(), jobsRawLabel(c[0], c[1]), "%s %s", c[0], c[1])
	}

	s, _ := g2Fixture(t)
	deps, err := newJobsDeps(s)
	require.NoError(t, err)
	enforcing := writertest.ForSet("books.maintenance.x", jobsDeclared("SetRaw/merge_journal"), tasks.ModeLive)
	w := deps.NewStore(enforcing)
	require.NoError(t, w.SetRaw("merge:combine-journal:j1", []byte("x")))
	require.Empty(t, enforcing.Refusals())
	// The store's claim guard would panic on a write to a key no family claims,
	// so a key that reaches it is a failure of this test: it is refused first.
	err = w.SetRaw("settings:x", []byte("x"))
	require.ErrorIs(t, err, tasks.ErrOutsideEffects, "an unclassified key is refused, not written")
	require.Len(t, enforcing.Writes(), 1, "only the classified write was stamped")
}

// jobsCalls is one call of every gated label, with the rows the call must be
// admitted by. Raw writes use a key of the family the label names.
func jobsCalls(bookID string) map[string]func(w jobs.Store) {
	noop := func(*database.BookFile) error { return nil }
	asCaps := func(w jobs.Store) *booksJobsStore { return w.(*booksJobsStore) }
	return map[string]func(w jobs.Store){
		"AddBookUserTag":       func(w jobs.Store) { _ = w.AddBookUserTag(bookID, "tag") },
		"BatchCreateBookFiles": func(w jobs.Store) { _ = w.BatchCreateBookFiles(nil) },
		"CreateAuthor":         func(w jobs.Store) { _, _ = w.CreateAuthor("Jim Gordon") },
		"CreateBookFile": func(w jobs.Store) {
			_ = w.CreateBookFile(&database.BookFile{BookID: bookID, FilePath: "/lib/y.m4b"})
		},
		"CreateOperationChange": func(w jobs.Store) {
			_ = w.CreateOperationChange(&database.OperationChange{OperationID: "op", BookID: bookID, ChangeType: "metadata_update"})
		},
		"CreateOperationResult":   func(w jobs.Store) { _ = w.CreateOperationResult(&database.OperationResult{OperationID: "op"}) },
		"DeleteBook":              func(w jobs.Store) { _ = w.DeleteBook("absent") },
		"DeleteOperationState":    func(w jobs.Store) { _ = w.DeleteOperationState("op") },
		"DeleteOperationWithLogs": func(w jobs.Store) { _ = w.DeleteOperationWithLogs("op") },
		"DeleteRawBatch":          func(w jobs.Store) { _ = w.DeleteRawBatch(nil) },
		"ModifyBook": func(w jobs.Store) {
			_, _ = w.ModifyBook(bookID, func(*database.Book) error { return nil })
		},
		"ModifyBookAuthors": func(w jobs.Store) {
			_, _ = w.ModifyBookAuthors(bookID, func(a []database.BookAuthor) ([]database.BookAuthor, error) { return a, nil })
		},
		"ModifyBookFile":          func(w jobs.Store) { _, _ = w.ModifyBookFile(bookID, "f", noop) },
		"MoveBookFilesToBook":     func(w jobs.Store) { _ = w.MoveBookFilesToBook(nil, bookID, bookID) },
		"PruneBookSnapshots":      func(w jobs.Store) { _, _ = w.PruneBookSnapshots(bookID, 5) },
		"ReassignExternalIDs":     func(w jobs.Store) { _ = w.ReassignExternalIDs(bookID, bookID) },
		"RecomputeBookAggregates": func(w jobs.Store) { _ = w.RecomputeBookAggregates(bookID) },
		"SaveOperationState":      func(w jobs.Store) { _ = w.SaveOperationState("op", []byte("s")) },
		"SaveOperationSummaryLog": func(w jobs.Store) { _ = w.SaveOperationSummaryLog(&database.OperationSummaryLog{ID: "op"}) },
		"SetBookFileHash":         func(w jobs.Store) { _ = w.SetBookFileHash("f", "h") },
		"SetSetting":              func(w jobs.Store) { _ = w.SetSetting("k", "v", "string", false) },
		"SetUserBookState": func(w jobs.Store) {
			_ = w.SetUserBookState(&database.UserBookState{UserID: "u", BookID: bookID})
		},
		"SetUserPosition": func(w jobs.Store) { _ = w.SetUserPosition("u", bookID, "seg", 1) },
		"UpdateBook":      func(w jobs.Store) { _, _ = w.UpdateBook(bookID, &database.Book{Title: "Year One"}) },
		"UpdateBookFile":  func(w jobs.Store) { _ = w.UpdateBookFile("f", &database.BookFile{}) },

		"SetRaw/merge_journal":      func(w jobs.Store) { _ = w.SetRaw("merge:combine-journal:j", []byte("x")) },
		"SetRaw/group_redirect":     func(w jobs.Store) { _ = w.SetRaw("merge:group-redirect:g", []byte("x")) },
		"SetRaw/user_state":         func(w jobs.Store) { _ = w.SetRaw("merge_user_state_pending:a:b", []byte("x")) },
		"DeleteRaw/merge_journal":   func(w jobs.Store) { _ = w.DeleteRaw("merge:combine-undo-applied:j") },
		"DeleteRaw/group_redirect":  func(w jobs.Store) { _ = w.DeleteRaw("merge:group-redirect:g") },
		"DeleteRaw/user_state":      func(w jobs.Store) { _ = w.DeleteRaw("merge_user_state_pending:a:b") },
		"DeleteRaw/dead_book_index": func(w jobs.Store) { _ = w.DeleteRaw("book:series:dead") },

		"MintOrGetSyncID":       func(w jobs.Store) { _, _ = asCaps(w).MintOrGetSyncID(bookID) },
		"RecordSyncMerge":       func(w jobs.Store) { _ = asCaps(w).RecordSyncMerge(bookID, bookID) },
		"MintOrGetSyncFileID":   func(w jobs.Store) { _, _ = asCaps(w).MintOrGetSyncFileID(bookID, "f") },
		"RepointSyncFileToBook": func(w jobs.Store) { _ = asCaps(w).RepointSyncFileToBook(bookID, bookID, "f") },
		"CreateBookmark": func(w jobs.Store) {
			_ = asCaps(w).CreateBookmark(progress.Bookmark{UserID: "u", ItemID: bookID, TimeSec: 1})
		},
		"ModifyBookFileDeferAggregates":  func(w jobs.Store) { _, _ = asCaps(w).ModifyBookFileDeferAggregates(bookID, "f", noop) },
		"MarkBookAggregatesBackfillDone": func(w jobs.Store) { _ = asCaps(w).MarkBookAggregatesBackfillDone() },
		"DeleteSeriesIfUnreferenced":     func(w jobs.Store) { _, _ = asCaps(w).DeleteSeriesIfUnreferenced(1, "x", nil) },
		"UpdateBookFileHashes":           func(w jobs.Store) { _ = asCaps(w).UpdateBookFileHashes("f", "a", "b", "c") },

		"AppendFileEvent":    func(w jobs.Store) { _ = asCaps(w).AppendFileEvent(database.FileEvent{Path: "/lib/y.m4b"}) },
		"ClearUserPositions": func(w jobs.Store) { _ = asCaps(w).ClearUserPositions("u", bookID) },
		"CopyBookmarkIfAbsent": func(w jobs.Store) {
			_, _ = asCaps(w).CopyBookmarkIfAbsent(progress.Bookmark{UserID: "u", ItemID: bookID})
		},
		"DeleteSeries":   func(w jobs.Store) { _ = asCaps(w).DeleteSeries(1) },
		"SetBookAuthors": func(w jobs.Store) { _ = asCaps(w).SetBookAuthors(bookID, nil) },
		"SetUserPositionAt": func(w jobs.Store) {
			_ = asCaps(w).SetUserPositionAt("u", bookID, "seg", 1, time.Unix(1, 0))
		},
		"SweepExpiredMetrics": func(w jobs.Store) { _, _ = asCaps(w).SweepExpiredMetrics() },
		"ModifyBookCredits": func(w jobs.Store) {
			_, _, _ = asCaps(w).ModifyBookCredits(bookID, func(*database.Book, *database.BookCreditsEdit) error { return nil })
		},
	}
}

// jobsRefusedCalls is one call of every refused label: a write the wrapper
// carries that has no row.
func jobsRefusedCalls(bookID string) map[string]func(w jobs.Store) error {
	asCaps := func(w jobs.Store) *booksJobsStore { return w.(*booksJobsStore) }
	return map[string]func(w jobs.Store) error{
		"AddBookTagWithSource": func(w jobs.Store) error { return asCaps(w).AddBookTagWithSource(bookID, "t", "s") },
		"ClearSyncMerge":       func(w jobs.Store) error { return asCaps(w).ClearSyncMerge(bookID, bookID) },
		"ClearSyncRedirect": func(w jobs.Store) error {
			_, _, err := asCaps(w).ClearSyncRedirect(bookID)
			return err
		},
		"DeleteBookmark":      func(w jobs.Store) error { return asCaps(w).DeleteBookmark("u", bookID, 1) },
		"DeleteUserBookState": func(w jobs.Store) error { return asCaps(w).DeleteUserBookState("u", bookID) },
		"MarkIdentDirty": func(w jobs.Store) error {
			_, err := asCaps(w).MarkIdentDirty(bookID)
			return err
		},
		"MintOrGetSyncFileIDs": func(w jobs.Store) error {
			_, err := asCaps(w).MintOrGetSyncFileIDs(bookID, []string{"f"})
			return err
		},
		"RemoveBookTagsByPrefix": func(w jobs.Store) error { return asCaps(w).RemoveBookTagsByPrefix(bookID, "p", "s") },
		"RepointSyncFile":        func(w jobs.Store) error { return asCaps(w).RepointSyncFile(bookID, "a", "b") },
		"RepointSyncItem":        func(w jobs.Store) error { return asCaps(w).RepointSyncItem(bookID, bookID) },
		"ReplaceUserPositions":   func(w jobs.Store) error { return asCaps(w).ReplaceUserPositions("u", bookID, nil) },
		"RevertBookToVersion": func(w jobs.Store) error {
			_, err := asCaps(w).RevertBookToVersion(bookID, time.Unix(1, 0))
			return err
		},
		"ModifyBookNarrators": func(w jobs.Store) error {
			_, err := asCaps(w).ModifyBookNarrators(bookID, func(n []database.BookNarrator) ([]database.BookNarrator, error) { return n, nil })
			return err
		},
		"UpdateBookmarkTitle": func(w jobs.Store) error { return asCaps(w).UpdateBookmarkTitle("u", bookID, 1, "t") },
		"UpdateSeriesName":    func(w jobs.Store) error { return asCaps(w).UpdateSeriesName(1, "x") },
	}
}

func TestBooksJobsStore_RefusedCallTableCoversEveryRefusedLabel(t *testing.T) {
	got := make([]string, 0)
	for l := range jobsRefusedCalls("x") {
		got = append(got, l)
	}
	want := slices.Clone(jobsRefusedLabels)
	slices.Sort(got)
	slices.Sort(want)
	require.Equal(t, want, got)
	for _, l := range jobsRefusedLabels {
		require.NotContains(t, jobsWriteLabels, l, "%s is both gated and refused", l)
	}
}

// TestBooksJobsStore_RefusedLabelsHaveNoRowAndNeverReachTheStore: a refused
// label has no row in the shared table, and calling it, under a declared set
// that holds every resource there is, returns ErrNoRow wrapped in
// ErrOutsideEffects without the store being called. A store error cannot be
// mistaken for it: the wrapper's own refusal is the only source of ErrNoRow.
func TestBooksJobsStore_RefusedLabelsHaveNoRowAndNeverReachTheStore(t *testing.T) {
	s, id := g2Fixture(t)
	deps, err := newJobsDeps(s)
	require.NoError(t, err)
	var everything []entity.Ref
	for _, rs := range t6WriterResources {
		for _, r := range rs {
			everything = append(everything, tasks.Res(writer.Module, string(r)))
		}
	}
	for label, call := range jobsRefusedCalls(id) {
		t.Run(label, func(t *testing.T) {
			require.NotContains(t, t6WriterResources, label, "%s gained a row: move it from jobsRefusedLabels to jobsWriteLabels", label)
			enforcing := writertest.ForSet("books.maintenance.x", everything, tasks.ModeLive)
			err := call(deps.NewStore(enforcing))
			require.ErrorIs(t, err, tasks.ErrOutsideEffects)
			require.ErrorIs(t, err, writer.ErrNoRow)
			require.Empty(t, enforcing.Writes(), "a refused call stamps nothing")
		})
	}
}

func TestBooksJobsStore_CallTableCoversEveryGatedLabel(t *testing.T) {
	calls := jobsCalls("x")
	got := make([]string, 0, len(calls))
	for l := range calls {
		got = append(got, l)
	}
	want := slices.Clone(jobsWriteLabels)
	slices.Sort(got)
	slices.Sort(want)
	require.Equal(t, want, got)
}

// TestBooksJobsStore_EveryGatedWriteIsOneStampOfItsRowAndNothingMore: under a
// declared set equal to the method's own row the call is admitted and is exactly
// one stamped write, Live and Preview; under an empty declared set it is refused
// and the store is untouched. (The per-job proof, that each job's declared Effects
// contain the row of every method it calls, is the jobs package's
// TestJobWrites_TraceMatchesCode: jobTraces is unexported there.)
func TestBooksJobsStore_EveryGatedWriteIsOneStampOfItsRowAndNothingMore(t *testing.T) {
	s, id := g2Fixture(t)
	deps, err := newJobsDeps(s)
	require.NoError(t, err)
	for label, call := range jobsCalls(id) {
		row := t6WriterResources[label]
		for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
			t.Run(label+"/"+mode.String(), func(t *testing.T) {
				enforcing := writertest.ForSet("books.maintenance.x", jobsDeclared(label), mode)
				call(deps.NewStore(enforcing))
				require.NoError(t, enforcing.RequireClean(), "the stamp is wider than the method's row")
				if len(row) > 0 {
					require.Len(t, enforcing.Writes(), 1, "one method call is one stamped write")
				}
			})
		}
		if len(row) == 0 {
			continue
		}
		t.Run(label+"/refused", func(t *testing.T) {
			enforcing := writertest.ForSet("books.maintenance.x", nil, tasks.ModeLive)
			call(deps.NewStore(enforcing))
			require.Error(t, enforcing.RequireClean(), "a row outside the declared set must be refused")
			require.NotEmpty(t, enforcing.Refusals())
		})
	}
}

func TestBooksJobsStore_NoWriterRefusesWritesAndStillReads(t *testing.T) {
	s, id := g2Fixture(t)
	deps, err := newJobsDeps(s)
	require.NoError(t, err)
	w := deps.NewStore(nil)
	require.ErrorIs(t, w.RecomputeBookAggregates(id), tasks.ErrOutsideEffects)
	require.ErrorIs(t, w.SetRaw("merge:group-redirect:g", nil), tasks.ErrOutsideEffects)
	b, err := w.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, id, b.ID)
	_ = context.Background()
}
