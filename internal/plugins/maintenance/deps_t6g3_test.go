// file: internal/plugins/maintenance/deps_t6g3_test.go
// version: 0.1.0
// guid: 8e2c5b47-3a91-4d06-9f58-b7140ce6a3d2
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	pathutil "github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// g3HostSpy is the server side of the group: configuration, the operation queue
// and the purge, flush and compact passes. It counts every pass it runs.
type g3HostSpy struct {
	queue OpQueueReader
	calls atomic.Int32
}

func (h *g3HostSpy) RootDir() string                           { return "" }
func (h *g3HostSpy) BackupRetentionDays() int                  { return 0 }
func (h *g3HostSpy) ActivityLogNightlyCompactionEnabled() bool { return false }
func (h *g3HostSpy) ActivityLogCompactionDays() int            { return 0 }
func (h *g3HostSpy) ActivityLogRetentionChangeDays() int       { return 0 }
func (h *g3HostSpy) ActivityLogRetentionDebugDays() int        { return 0 }
func (h *g3HostSpy) OperationQueueStore() OpQueueReader        { return h.queue }
func (h *g3HostSpy) RunAutoPurgeSoftDeleted(context.Context, string) error {
	h.calls.Add(1)
	return nil
}
func (h *g3HostSpy) CleanupOrphanedTempFiles(string, string) int { h.calls.Add(1); return 0 }
func (h *g3HostSpy) CleanupTrashedVersions() (versions.CleanupResult, error) {
	h.calls.Add(1)
	return versions.CleanupResult{}, nil
}
func (h *g3HostSpy) ActivityFlushOp(string) { h.calls.Add(1) }
func (h *g3HostSpy) MaintainActivityLog(context.Context, int, int) (int, int, int64, error) {
	h.calls.Add(1)
	return 0, 0, 0, nil
}

func (h *g3HostSpy) CompactActivityEntries(context.Context, time.Time, database.CompactProgress) (database.CompactResult, error) {
	h.calls.Add(1)
	return database.CompactResult{}, nil
}
func (h *g3HostSpy) InvalidateAuthorsCache() { h.calls.Add(1) }

// g3SpySrc is a real store that counts the marker write, so a run that reaches
// ReplaceOpRowsPrefix is seen even though the method's row is empty.
type g3SpySrc struct {
	*database.PebbleStore
	// replaced counts the judged-marker writes (prefix cbSwept); classLists counts
	// every other ReplaceOpRowsPrefix, which is the class-list result rows that
	// SaveClassLists keeps through the same row store.
	replaced, classLists atomic.Int32
}

func (s *g3SpySrc) ReplaceOpRowsPrefix(opID, prefix string, n int, at func(int) (database.OpRow, error)) error {
	if prefix == cbSwept {
		s.replaced.Add(1)
	} else {
		s.classLists.Add(1)
	}
	return s.PebbleStore.ReplaceOpRowsPrefix(opID, prefix, n, at)
}

func g3Fixture(t *testing.T) (*g3SpySrc, *g3HostSpy, string) {
	t.Helper()
	s, id := g2Fixture(t)
	src := &g3SpySrc{PebbleStore: s}
	return src, &g3HostSpy{queue: src}, id
}

// g3WriterCalls is one call of every method of t6g3Writer, by method name.
func g3WriterCalls(bookID string) map[string]func(w t6g3Writer) {
	ctx := context.Background()
	return map[string]func(w t6g3Writer){
		"RunAutoPurgeSoftDeleted": func(w t6g3Writer) { _ = w.RunAutoPurgeSoftDeleted(ctx, "op") },
		"ActivityFlushOp":         func(w t6g3Writer) { w.ActivityFlushOp("op") },
		"ResolveTombstoneChains":  func(w t6g3Writer) { _, _ = w.ResolveTombstoneChains() },
		"CleanupTrashedVersions":  func(w t6g3Writer) { _, _ = w.CleanupTrashedVersions() },
		"CleanupOrphanedTempFiles": func(w t6g3Writer) {
			_ = w.CleanupOrphanedTempFiles("", "op")
		},
		"CleanupStaleTempFiles": func(w t6g3Writer) {
			_ = w.CleanupStaleTempFiles(t6g3Tmp(), pathutil.AppDirs{}, time.Hour)
		},
		"RemoveOldBackupFiles": func(w t6g3Writer) {
			_, _ = w.RemoveOldBackupFiles(ctx, t6g3Tmp(), pathutil.AppDirs{}, func(string) (time.Time, bool) { return time.Time{}, false }, time.Hour, func(slog.Level, string, ...any) {})
		},
		"CompactActivityEntries": func(w t6g3Writer) {
			_, _ = w.CompactActivityEntries(ctx, time.Now(), nil)
		},
		"MaintainActivityLog": func(w t6g3Writer) { _, _, _, _ = w.MaintainActivityLog(ctx, 1, 1) },
		"SetUserPreferenceForUser": func(w t6g3Writer) {
			_ = w.SetUserPreferenceForUser("u", "k", "v")
		},
		"ModifyBook": func(w t6g3Writer) {
			_, _ = w.ModifyBook(bookID, func(*database.Book) error { return nil })
		},
		"SetRaw": func(w t6g3Writer) { _ = w.SetRaw("cover_text:x", []byte("{}")) },
		"ModifyBookCredits": func(w t6g3Writer) {
			_, _, _ = w.ModifyBookCredits(bookID, func(*database.Book, *database.BookCreditsEdit) error { return nil })
		},
		"CreateNarrator": func(w t6g3Writer) { _, _ = w.CreateNarrator("Kevin Conroy") },
		"DeleteNarrator": func(w t6g3Writer) { _ = w.DeleteNarrator(1) },
		"CreateOperationChange": func(w t6g3Writer) {
			_ = w.CreateOperationChange(&database.OperationChange{OperationID: "op", BookID: bookID, ChangeType: "metadata_update"})
		},
		"InvalidateAuthorsCache": func(w t6g3Writer) { w.InvalidateAuthorsCache() },
		"ReplaceOpRowsPrefix": func(w t6g3Writer) {
			_ = w.ReplaceOpRowsPrefix("op", cbSwept, 0, func(int) (database.OpRow, error) { return database.OpRow{}, nil })
		},
		"SaveClassLists":  func(w t6g3Writer) { _, _ = w.SaveClassLists("op", map[string][]string{"a": {"1"}}) },
		"ClearClassLists": func(w t6g3Writer) { _ = w.ClearClassLists("op") },
	}
}

func t6g3Tmp() string { return "/nonexistent-f518c-root" }

// The call table names exactly the methods of the interface, so a method added to
// t6g3Writer without a gated body fails here.
func TestBooksT6G3Writer_CoversEveryMethodOfTheInterface(t *testing.T) {
	want := t6WriterMethods(reflect.TypeFor[t6g3Writer]())
	var got []string
	for m := range g3WriterCalls("x") {
		got = append(got, m)
	}
	slices.Sort(got)
	require.Equal(t, want, got)
}

// TestBooksT6G3Deps_NilInputsStayUnset: a missing store or host leaves the group's
// own refusals in place (no adapter around nil).
func TestBooksT6G3Deps_NilInputsStayUnset(t *testing.T) {
	src, host, _ := g3Fixture(t)
	for name, d := range map[string]t6g3Deps{
		"nil source": newT6G3Deps(nil, host, nil, "", nil),
		"nil host":   newT6G3Deps(src, nil, nil, "", nil),
	} {
		t.Run(name, func(t *testing.T) {
			require.Nil(t, d.Store)
			require.Nil(t, d.NewWriter)
			def := t6g1ByKind3(t, d, "books.maintenance.tombstone-cleanup")
			_, rc := newT6G3Host(def.Kind(), tasks.ModeLive, &booksGateRecorder{})
			_, err := t6g3Run(t, def, rc, "")
			require.ErrorContains(t, err, "is not set")
		})
	}
}

func t6g1ByKind3(t *testing.T, deps t6g3Deps, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range t6g3KindList(&Plugin{}, deps, nil) {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no kind %s", kind)
	return nil
}

// TestBooksT6G3Writer_EveryKindsCallsAreAdmittedByItsOwnEffects: every writer
// method a kind's core can call runs through the builder's writer over the
// Effects-enforcing writer built from that kind's own definition, in Live and
// Preview. A whole-row stamp wider than the kind's Effects is refused and
// RequireClean reports it.
func TestBooksT6G3Writer_EveryKindsCallsAreAdmittedByItsOwnEffects(t *testing.T) {
	src, host, id := g3Fixture(t)
	deps := newT6G3Deps(src, host, nil, "", nil)
	calls := g3WriterCalls(id)
	for headID, kw := range t6g3KindCalls {
		if len(kw.calls) == 0 {
			continue
		}
		def := t6g1ByKind3(t, deps, tasks.Kind("books."+headID))
		for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
			t.Run(headID+"/"+mode.String(), func(t *testing.T) {
				enforcing := writertest.New(def, mode)
				w := deps.NewWriter(enforcing)
				stamped := 0
				for _, m := range kw.calls {
					calls[m](w)
					if len(t6g3MethodResources[m]) > 0 {
						stamped++
					}
				}
				require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
				require.Len(t, enforcing.Writes(), stamped, "every method with a row is one stamped write")
			})
		}
	}
}

// TestBooksT6G3Writer_EmptyRowMethodsStayUnfencedAndTheCoreOwnsTheMode pins the
// decision for the methods whose row is empty. Control: the builder's own
// ReplaceOpRowsPrefix reaches the store in a Preview run (the gate does not stop
// it for its mode; it refuses only a stopped run). Proof: a Preview run of the one core that calls it does not reach it,
// because the core derives Apply from the run's mode.
func TestBooksT6G3Writer_EmptyRowMethodsStayUnfencedAndTheCoreOwnsTheMode(t *testing.T) {
	src, host, _ := g3Fixture(t)
	runs := runningCBRuns(t)
	deps := newT6G3Deps(src, host, nil, "", runs)
	calls := g3WriterCalls("x")

	def := t6g1ByKind3(t, deps, "books.maintenance.credits-backfill")
	preview := writertest.New(def, tasks.ModePreview)
	calls["ReplaceOpRowsPrefix"](deps.NewWriter(preview))
	require.EqualValues(t, 1, src.replaced.Load(), "control: the gate does not stop an empty-row method for its mode (it refuses only a stopped run: writer.TestGate_EmptyRowCancelTimeoutLease)")
	require.Empty(t, preview.Writes())

	src.replaced.Store(0)
	run := writertest.New(def, tasks.ModePreview)
	_, rc := newT6G3Host(def.Kind(), tasks.ModePreview, run)
	raw, err := t6g3Run(t, def, rc, "")
	require.NoError(t, err)
	require.NoError(t, run.RequireClean())
	require.EqualValues(t, 0, src.replaced.Load(), "a Preview run of the credits backfill wrote a judged marker")
	// The result's class lists ARE stored by a preview (the owner reads the
	// preview's classes from them), but as the run's own rows in the tasks run
	// store (F5-18d, O10), not through the writer's unfenced op-row methods.
	// This fixture has no book in any class, so the summary is the proof the
	// lists were kept (the census tests in kinds_t6_g3_paths_test.go count rows).
	require.Zero(t, src.classLists.Load(), "the preview run stored its class lists through the writer")
	var res creditsBackfillResult
	require.NoError(t, json.Unmarshal(raw, &res))
	require.NotNil(t, res.ClassLists, "the preview run did not keep its class lists")
	for row, err := range runs.Rows(t.Context(), cbFakeRunID) {
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(row.ID, database.ClassListRowPrefix), "row %q", row.ID)
	}
}

// TestBooksT6G3Writer_ErrorlessMethodsLogInsteadOfRunningWhenRefused: a method
// with no error result cannot return a refusal, so it does nothing and the host
// is untouched.
func TestBooksT6G3Writer_ErrorlessMethodsLogInsteadOfRunningWhenRefused(t *testing.T) {
	src, host, _ := g3Fixture(t)
	deps := newT6G3Deps(src, host, nil, "", nil)
	rec := &booksGateRecorder{refuse: tasks.ErrOutsideEffects}
	w := deps.NewWriter(rec)
	w.ActivityFlushOp("op")
	require.Zero(t, w.CleanupOrphanedTempFiles("", "op"))
	require.Zero(t, w.CleanupStaleTempFiles(t6g3Tmp(), pathutil.AppDirs{}, time.Hour))
	require.Zero(t, host.calls.Load(), "a refused method still ran its body")
	require.Len(t, rec.stamps(), 3)

	// A read-only run has no writer at all: every gated write refuses.
	ro := deps.NewWriter(nil)
	require.ErrorIs(t, ro.DeleteNarrator(1), tasks.ErrOutsideEffects)
	_, err := ro.CleanupTrashedVersions()
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.Zero(t, host.calls.Load())
}
