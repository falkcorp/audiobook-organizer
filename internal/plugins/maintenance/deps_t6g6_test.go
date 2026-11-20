// file: internal/plugins/maintenance/deps_t6g6_test.go
// version: 0.1.0
// guid: c3a97e52-1d06-48bf-b5e9-6a2f0d718c43
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

type g6HostSpy struct{ calls atomic.Int32 }

func (h *g6HostSpy) CompactActivityEntries(context.Context, time.Time, database.CompactProgress) (database.CompactResult, error) {
	h.calls.Add(1)
	return database.CompactResult{}, nil
}

func (h *g6HostSpy) RecompactActivityDigests(context.Context) (database.RecompactResult, error) {
	h.calls.Add(1)
	return database.RecompactResult{}, nil
}

// g6WriterCalls is one call of every write method of t6g6Writer, by method name,
// including the writes behind the capability getters.
func g6WriterCalls(bookID string) map[string]func(w t6g6Writer) {
	ctx := context.Background()
	return map[string]func(w t6g6Writer){
		"BatchCreateBookFiles": func(w t6g6Writer) { _ = w.BatchCreateBookFiles(nil) },
		"CreateBookFile":       func(w t6g6Writer) { _ = w.CreateBookFile(&database.BookFile{BookID: bookID, FilePath: "/lib/x.m4b"}) },
		"UpdateBookFiles":      func(w t6g6Writer) { _, _ = w.UpdateBookFiles(ctx, nil, nil) },
		"ModifyBook": func(w t6g6Writer) {
			_, _ = w.ModifyBook(bookID, func(*database.Book) error { return nil })
		},
		"ModifyBookFile": func(w t6g6Writer) {
			_, _ = w.ModifyBookFile(bookID, "f", func(*database.BookFile) error { return nil })
		},
		"RecomputeBookAggregates": func(w t6g6Writer) { _ = w.RecomputeBookAggregates(bookID) },
		"CreateOperationChange": func(w t6g6Writer) {
			_ = w.CreateOperationChange(&database.OperationChange{OperationID: "op", BookID: bookID, ChangeType: "metadata_update"})
		},
		"SetRaw":                    func(w t6g6Writer) { _ = w.SetRaw("merge_user_state_pending:x", []byte("v")) },
		"DeleteRaw":                 func(w t6g6Writer) { _ = w.DeleteRaw("merge_user_state_pending:x") },
		"UpsertReviewItem":          func(w t6g6Writer) { _, _ = w.UpsertReviewItem(database.ReviewItem{}) },
		"DeleteReviewItemIfPending": func(w t6g6Writer) { _, _ = w.DeleteReviewItemIfPending("none") },
		"SetUserBookState":          func(w t6g6Writer) { _ = w.SetUserBookState(&database.UserBookState{UserID: "u", BookID: bookID}) },
		"SetUserPosition":           func(w t6g6Writer) { _ = w.SetUserPosition("u", bookID, "s", 1) },
		"ClearUserPositions":        func(w t6g6Writer) { _ = w.ClearUserPositions("u", bookID) },
		"CompactActivityEntries":    func(w t6g6Writer) { _, _ = w.CompactActivityEntries(ctx, time.Now(), nil) },
		"RecompactActivityDigests":  func(w t6g6Writer) { _, _ = w.RecompactActivityDigests(ctx) },
		"RebuildOpChangeByBookIndex": func(w t6g6Writer) {
			_, _ = w.RebuildOpChangeByBookIndex(ctx, nil)
		},
		"EnsureOpChangeByBookIndexWith": func(w t6g6Writer) {
			_, _ = w.EnsureOpChangeByBookIndexWith(ctx, database.OpChangeEnsureOptions{})
		},
		"CollapseHealDuplicates": func(w t6g6Writer) { _ = w.CollapseHealDuplicates("op", bookID, nil) },
		"HealTrack":              func(w t6g6Writer) { _ = w.HealTrack("/nonexistent-f518c/dst", "/nonexistent-f518c/src") },
		"HealWhisperJournal":     func(w t6g6Writer) { _ = w.HealWhisperJournal() },
		"MintOrGetSyncID": func(w t6g6Writer) {
			if s, ok := w.UserStateSyncWrites(); ok {
				_, _ = s.MintOrGetSyncID(bookID)
			}
		},
		"RecordSyncMerge": func(w t6g6Writer) {
			if s, ok := w.UserStateSyncWrites(); ok {
				_ = s.RecordSyncMerge("a", bookID)
			}
		},
		"CopyBookmarkIfAbsent": func(w t6g6Writer) {
			if s, ok := w.UserStateBookmarkWrites(); ok {
				_, _ = s.CopyBookmarkIfAbsent(progress.Bookmark{})
			}
		},
		"SetUserPositionAt": func(w t6g6Writer) {
			if s, ok := w.UserStateTimestampWriter(); ok {
				_ = s.SetUserPositionAt("u", bookID, "s", 1, time.Now())
			}
		},
	}
}

func g6ByKind(t *testing.T, deps t6g6Deps, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range t6g6KindList(&Plugin{}, deps, nil) {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no kind %s", kind)
	return nil
}

// The call table names exactly the write methods of the interface and the writes
// behind its capability getters, so a method added without a gated body fails.
func TestBooksT6G6Writer_CoversEveryMethodOfTheInterface(t *testing.T) {
	want := t6WriterMethods(reflect.TypeFor[t6g6Writer]())
	var got []string
	for m := range g6WriterCalls("x") {
		got = append(got, m)
	}
	slices.Sort(got)
	require.Equal(t, want, got)
}

func TestBooksT6G6Deps_NilInputsStayUnset(t *testing.T) {
	s, _ := g2Fixture(t)
	d := newT6G6Deps(nil, &g6HostSpy{}, nil, "", nil, nil)
	require.Nil(t, d.Store)
	require.Nil(t, d.NewWriter)
	require.Nil(t, d.ResolveReportPath)

	d = newT6G6Deps(s, nil, nil, "", func() string { return "" }, nil)
	require.NotNil(t, d.Store)
	require.Nil(t, d.NewWriter)
	require.NotNil(t, d.ResolveReportPath)

	d = newT6G6Deps(s, &g6HostSpy{}, nil, "", nil, nil)
	require.NotNil(t, d.NewWriter)
	require.Nil(t, d.ResolveReportPath, "no root, no confinement to give: unset, not a pass-through")
}

// TestBooksT6G6Writer_EveryKindsCallsAreAdmittedByItsOwnEffects: every writer
// method a kind's interfaces list runs through the builder's writer over the
// Effects-enforcing writer built from that kind's own definition, Live and
// Preview.
func TestBooksT6G6Writer_EveryKindsCallsAreAdmittedByItsOwnEffects(t *testing.T) {
	s, id := g2Fixture(t)
	deps := newT6G6Deps(s, &g6HostSpy{}, nil, "", func() string { return t.TempDir() }, nil)
	calls := g6WriterCalls(id)
	for kind, kw := range t6g6KindWriters() {
		def := g6ByKind(t, deps, kind)
		methods := t6WriterMethods(kw.writers...)
		for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
			t.Run(string(kind)+"/"+mode.String(), func(t *testing.T) {
				enforcing := writertest.New(def, mode)
				w := deps.NewWriter(enforcing)
				for _, m := range methods {
					calls[m](w)
				}
				require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
				// A capability getter hands a writer out only when the store has the
				// capability, which the fixture store does for all of them.
				require.Len(t, enforcing.Writes(), len(methods), "every method is one stamped write")
			})
		}
	}
}

// A read-only run has no writer: every gated write refuses and the host is
// untouched.
func TestBooksT6G6Writer_NoWriterRefusesEveryWrite(t *testing.T) {
	s, id := g2Fixture(t)
	host := &g6HostSpy{}
	deps := newT6G6Deps(s, host, nil, "", nil, nil)
	w := deps.NewWriter(nil)
	require.ErrorIs(t, w.SetRaw("k", nil), tasks.ErrOutsideEffects)
	_, err := w.CompactActivityEntries(context.Background(), time.Now(), nil)
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.Nil(t, w.HealWhisperJournal(), "a refused open is no journal")
	_ = id
	require.Zero(t, host.calls.Load())
}
