// file: internal/plugins/maintenance/deps_t6g7_test.go
// version: 0.1.0
// guid: d27b4e08-6a93-4c51-8e1f-3b905a7c4d26
// last-edited: 2026-11-22

package maintenance

import (
	"context"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// g7FakeMerge is the user-merge service: it answers and counts.
type g7FakeMerge struct{ calls atomic.Int32 }

func (m *g7FakeMerge) ActorIsAdmin(string) (bool, error) { m.calls.Add(1); return true, nil }
func (m *g7FakeMerge) BuildPlan(context.Context, usermerge.PlanRequest) (*usermerge.Plan, error) {
	m.calls.Add(1)
	return &usermerge.Plan{}, nil
}

func (m *g7FakeMerge) LoadJournal(string) (*usermerge.Journal, error) {
	m.calls.Add(1)
	return &usermerge.Journal{}, nil
}

func (m *g7FakeMerge) Apply(context.Context, usermerge.ApplyRequest, usermerge.Progress) (*usermerge.Journal, error) {
	m.calls.Add(1)
	return &usermerge.Journal{}, nil
}

func (m *g7FakeMerge) Resume(context.Context, string, usermerge.Progress) (*usermerge.Journal, error) {
	m.calls.Add(1)
	return &usermerge.Journal{}, nil
}

func (m *g7FakeMerge) Undo(context.Context, usermerge.UndoRequest, usermerge.Progress) (*usermerge.Journal, error) {
	m.calls.Add(1)
	return &usermerge.Journal{}, nil
}

// g7Inputs is a full set of inputs over a real in-memory store.
func g7Inputs(t *testing.T) (G7Inputs, *g7FakeMerge, *atomic.Int32, string) {
	t.Helper()
	s, id := g2Fixture(t)
	merge := &g7FakeMerge{}
	wb := &atomic.Int32{}
	return G7Inputs{
		Reads: s, Writes: s, UserMerge: merge,
		BulkWriteBack: func(context.Context, string, []string, bool, int, operations.ProgressReporter) error {
			wb.Add(1)
			return nil
		},
		RootDir:               func() string { return t.TempDir() },
		StandDownKey:          "books.gotham-watch",
		InvalidateSeriesCache: func() {},
		SupportedExtensions:   func() []string { return []string{".m4b"} },
	}, merge, wb, id
}

// g7WriterCalls is one call of every method of t6g7Writer, by method name.
func g7WriterCalls(bookID string) map[string]func(w t6g7Writer) {
	ctx := context.Background()
	return map[string]func(w t6g7Writer){
		"BatchUpsertBookFiles": func(w t6g7Writer) { _ = w.BatchUpsertBookFiles(nil) },
		"CreateOperationChange": func(w t6g7Writer) {
			_ = w.CreateOperationChange(&database.OperationChange{OperationID: "op", BookID: bookID, ChangeType: "metadata_update"})
		},
		"CreateSeries":               func(w t6g7Writer) { _, _ = w.CreateSeries("Year One", nil) },
		"DeleteSeriesIfUnreferenced": func(w t6g7Writer) { _, _ = w.DeleteSeriesIfUnreferenced(99, "Year One", nil) },
		"ModifyBook": func(w t6g7Writer) {
			_, _ = w.ModifyBook(bookID, func(*database.Book) error { return nil })
		},
		"ModifyBookFile": func(w t6g7Writer) {
			_, _ = w.ModifyBookFile(bookID, "none", func(*database.BookFile) error { return nil })
		},
		"RebuildReviewStatusIndex": func(w t6g7Writer) { _, _ = w.RebuildReviewStatusIndex(false) },
		"RecordMetadataChange": func(w t6g7Writer) {
			_ = w.RecordMetadataChange(&database.MetadataChangeRecord{BookID: bookID, Field: "title"})
		},
		"RecordPathChange": func(w t6g7Writer) {
			_ = w.RecordPathChange(&database.BookPathChange{BookID: bookID})
		},
		"SetUserPreferenceForUser": func(w t6g7Writer) { _ = w.SetUserPreferenceForUser("u", "k", "v") },
		"UpdateBookFiles":          func(w t6g7Writer) { _, _ = w.UpdateBookFiles(ctx, nil, nil) },
		"RunBulkWriteBack":         func(w t6g7Writer) { _ = w.RunBulkWriteBack(ctx, "op", nil, false, 0, nil) },
		"ApplyUserMerge": func(w t6g7Writer) {
			_, _ = w.ApplyUserMerge(ctx, usermerge.ApplyRequest{}, nil)
		},
		"ResumeUserMerge": func(w t6g7Writer) { _, _ = w.ResumeUserMerge(ctx, "j", nil) },
		"UndoUserMerge": func(w t6g7Writer) {
			_, _ = w.UndoUserMerge(ctx, usermerge.UndoRequest{}, nil)
		},
	}
}

func g7DefByKind(t *testing.T, deps t6g7Deps, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range t6g7KindList(&Plugin{}, deps, nil) {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no kind %s", kind)
	return nil
}

// The call table names exactly the methods of the interface, so a method added
// to t6g7Writer without a gated body fails here.
func TestBooksT6G7Writer_CoversEveryMethodOfTheInterface(t *testing.T) {
	want := t6WriterMethods(reflect.TypeFor[t6g7Writer]())
	var got []string
	for m := range g7WriterCalls("x") {
		got = append(got, m)
	}
	slices.Sort(got)
	require.Equal(t, want, got)
}

// A full set of inputs leaves no field of t6g7Deps unset: a field added to the
// deps and not wired by the builder fails here.
func TestBooksT6G7Deps_EveryFieldIsSet(t *testing.T) {
	in, _, _, _ := g7Inputs(t)
	d := newT6G7Deps(in)
	v := reflect.ValueOf(d)
	for i := 0; i < v.NumField(); i++ {
		require.False(t, v.Field(i).IsZero(), "t6g7Deps.%s is unset", v.Type().Field(i).Name)
	}
}

// A missing store leaves the group's own refusals in place, and the optional
// reads default to HEAD's.
func TestBooksT6G7Deps_NilInputsStayUnsetAndDefaultsAreHEADs(t *testing.T) {
	in, _, _, _ := g7Inputs(t)
	for name, mutate := range map[string]func(*G7Inputs){
		"nil reads":  func(i *G7Inputs) { i.Reads = nil },
		"nil writes": func(i *G7Inputs) { i.Writes = nil },
	} {
		t.Run(name, func(t *testing.T) {
			i := in
			mutate(&i)
			d := newT6G7Deps(i)
			require.Nil(t, d.Store)
			require.Nil(t, d.NewWriter)
		})
	}
	noRoot := in
	noRoot.RootDir = nil
	d := newT6G7Deps(noRoot)
	require.Nil(t, d.RootDir)
	require.Nil(t, d.ResolveReportPath, "no root, no report path to confine")

	bare := in
	bare.AgreedChapterTitle, bare.ChapterProber, bare.SupportedExtensions = nil, nil, nil
	d = newT6G7Deps(bare)
	require.Nil(t, d.AgreedChapterTitle, "no injected extension accessor, no global-config fallback")
	require.NotNil(t, d.ChapterProber)
}

// Keep the full store compatibility proof in tests: production depends only
// on the narrow roles, so the database.Store reference ratchet does not grow.
var (
	_ G7Reads  = database.Store(nil)
	_ G7Writes = database.Store(nil)
)

// The report path is confined under the library root, as HEAD's resolver does.
func TestBooksT6G7Deps_ReportPathIsConfinedUnderTheRoot(t *testing.T) {
	in, _, _, _ := g7Inputs(t)
	root := t.TempDir()
	in.RootDir = func() string { return root }
	d := newT6G7Deps(in)
	_, err := d.ResolveReportPath("/etc/passwd", "report.tsv")
	require.Error(t, err, "an explicit path outside the report directory is refused")
	got, err := d.ResolveReportPath("", "report.tsv")
	require.NoError(t, err)
	require.Contains(t, got, root)
}

// Every writer method each kind's core can call runs through the builder's
// writer over the Effects-enforcing writer built from that kind's own
// definition, in Live and Preview. A whole-row stamp wider than the kind's
// Effects is refused and RequireClean reports it.
func TestBooksT6G7Writer_EveryKindsCallsAreAdmittedByItsOwnEffects(t *testing.T) {
	in, _, _, id := g7Inputs(t)
	deps := newT6G7Deps(in)
	calls := g7WriterCalls(id)
	checked := 0
	for kind, kw := range t6g7KindWriters() {
		methods := t6WriterMethods(kw.writers...)
		if len(methods) == 0 {
			continue
		}
		def := g7DefByKind(t, deps, kind)
		for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
			t.Run(string(kind)+"/"+mode.String(), func(t *testing.T) {
				enforcing := writertest.New(def, mode)
				w := deps.NewWriter(enforcing)
				stamped := 0
				for _, m := range methods {
					call, ok := calls[m]
					require.True(t, ok, "no call for %s", m)
					call(w)
					if len(t6g7WriterResource[m]) > 0 {
						stamped++
					}
				}
				require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
				require.Len(t, enforcing.Writes(), stamped, "every method with a row is one stamped write")
			})
		}
		checked++
	}
	require.Equal(t, 12, checked, "the kinds that write through a writer interface")
}

// A Preview run reaches no store write and no service: the gate refuses before
// the body.
func TestBooksT6G7Writer_PreviewRunsNothing(t *testing.T) {
	in, merge, wb, id := g7Inputs(t)
	deps := newT6G7Deps(in)
	def := g7DefByKind(t, deps, userMergeKind)
	enforcing := writertest.New(def, tasks.ModePreview)
	w := deps.NewWriter(enforcing)
	for name, call := range g7WriterCalls(id) {
		if name == "ApplyUserMerge" || name == "ResumeUserMerge" || name == "UndoUserMerge" {
			call(w)
		}
	}
	require.Zero(t, merge.calls.Load(), "a Preview run reached the merge service")
	require.Zero(t, wb.Load())
}

// A read-only run has no writer: every gated write refuses instead of
// dereferencing nil, and nothing is called.
func TestBooksT6G7Writer_NoRunWriterRefuses(t *testing.T) {
	in, merge, wb, id := g7Inputs(t)
	deps := newT6G7Deps(in)
	w := deps.NewWriter(nil)
	for name, call := range g7WriterCalls(id) {
		_ = name
		call(w)
	}
	_, err := w.ModifyBook(id, func(*database.Book) error { return nil })
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	_, err = w.ApplyUserMerge(context.Background(), usermerge.ApplyRequest{}, nil)
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.ErrorIs(t, w.RunBulkWriteBack(context.Background(), "op", nil, false, 0, nil), tasks.ErrOutsideEffects)
	require.Zero(t, merge.calls.Load())
	require.Zero(t, wb.Load())
}

// A Live write reaches its service once the gate admits it.
func TestBooksT6G7Writer_LiveReachesTheServices(t *testing.T) {
	in, merge, wb, _ := g7Inputs(t)
	deps := newT6G7Deps(in)
	rec := &booksGateRecorder{}
	w := deps.NewWriter(rec)
	_, err := w.ApplyUserMerge(context.Background(), usermerge.ApplyRequest{}, nil)
	require.NoError(t, err)
	_, err = w.ResumeUserMerge(context.Background(), "j", nil)
	require.NoError(t, err)
	_, err = w.UndoUserMerge(context.Background(), usermerge.UndoRequest{}, nil)
	require.NoError(t, err)
	require.NoError(t, w.RunBulkWriteBack(context.Background(), "op", nil, false, 0, nil))
	require.EqualValues(t, 3, merge.calls.Load())
	require.EqualValues(t, 1, wb.Load())
	require.Len(t, rec.stamps(), 4, "each is one stamped write")
}

// Without a merge service or a write-back runner the call refuses with a named
// error after the gate, as HEAD refused.
func TestBooksT6G7_MissingServicesRefuseByName(t *testing.T) {
	in, _, _, _ := g7Inputs(t)
	in.UserMerge, in.BulkWriteBack = nil, nil
	deps := newT6G7Deps(in)
	w := deps.NewWriter(&booksGateRecorder{})
	_, err := w.ApplyUserMerge(context.Background(), usermerge.ApplyRequest{}, nil)
	require.ErrorIs(t, err, errT6g7NoUserMerge)
	require.ErrorIs(t, w.RunBulkWriteBack(context.Background(), "op", nil, false, 0, nil), errT6g7NoWriteBack)
	_, err = deps.Store.ActorIsAdmin("u")
	require.ErrorIs(t, err, errT6g7NoUserMerge)
	_, err = deps.Store.BuildPlan(context.Background(), usermerge.PlanRequest{})
	require.ErrorIs(t, err, errT6g7NoUserMerge)
	_, err = deps.Store.LoadJournal("j")
	require.ErrorIs(t, err, errT6g7NoUserMerge)
}

// A nil *g7FakeMerge in the interface is not == nil. It must refuse by name like
// the untyped nil, not panic at the first call.
func TestBooksT6G7_TypedNilUserMergeRefusesByName(t *testing.T) {
	in, _, _, _ := g7Inputs(t)
	var typedNil *g7FakeMerge
	in.UserMerge = typedNil
	deps := newT6G7Deps(in)
	w := deps.NewWriter(&booksGateRecorder{})
	_, err := w.ApplyUserMerge(context.Background(), usermerge.ApplyRequest{}, nil)
	require.ErrorIs(t, err, errT6g7NoUserMerge)
	_, err = deps.Store.ActorIsAdmin("u")
	require.ErrorIs(t, err, errT6g7NoUserMerge)
}

// A typed nil passes != nil but is not a value: BooksTaskLists and newT6G7Deps use isSet.
func TestIsSet_TypedNilIsNotSet(t *testing.T) {
	require.False(t, isSet((*g7FakeMerge)(nil)))
	require.False(t, isSet(nil))
	require.True(t, isSet(&g7FakeMerge{}))
	require.True(t, isSet(bareG7Reads{}))
}

// bareG7Reads and bareG7Writes expose only the interface's own methods, so no
// optional capability resolves through them.
type bareG7Reads struct{ G7Reads }
type bareG7Writes struct{ G7Writes }

// A store without an optional capability refuses at the call, by name.
func TestBooksT6G7_MissingCapabilitiesRefuse(t *testing.T) {
	in, _, _, _ := g7Inputs(t)
	in.Reads, in.Writes = bareG7Reads{in.Reads}, bareG7Writes{in.Writes}
	deps := newT6G7Deps(in)
	_, err := deps.Store.GetSeriesBookRefCount(1)
	require.ErrorContains(t, err, "series reference counter")
	_, err = deps.Store.GetAllSeriesBookRefCounts()
	require.ErrorContains(t, err, "series reference-count reader")
	_, err = deps.Store.GetBooksBySeriesIDsAllVersions([]int{1})
	require.ErrorContains(t, err, "series membership reader")
	_, err = deps.Store.GetAllBookFilesCoreIncludingRetired()
	require.ErrorContains(t, err, "retired book_file reader")
	_, err = deps.Store.ReviewStatusIndexReport()
	require.ErrorContains(t, err, "review-status index repairer")
	require.Error(t, deps.Store.SeriesApplyReady())
	w := deps.NewWriter(&booksGateRecorder{})
	_, err = w.DeleteSeriesIfUnreferenced(1, "x", nil)
	require.ErrorContains(t, err, "check-and-delete series writer")
	_, err = w.RebuildReviewStatusIndex(true)
	require.ErrorContains(t, err, "review-status index repairer")
}

// The review-status report is the repairer with apply false: a read.
func TestBooksT6G7Store_ReviewStatusReportNeverApplies(t *testing.T) {
	in, _, _, _ := g7Inputs(t)
	spy := &g7RepairSpy{G7Writes: in.Writes}
	in.Writes = spy
	deps := newT6G7Deps(in)
	_, err := deps.Store.ReviewStatusIndexReport()
	require.NoError(t, err)
	require.Equal(t, []bool{false}, spy.applies)
}

type g7RepairSpy struct {
	G7Writes
	applies []bool
}

func (s *g7RepairSpy) RebuildReviewStatusIndex(apply bool) (database.ReviewStatusIndexRepair, error) {
	s.applies = append(s.applies, apply)
	return database.ReviewStatusIndexRepair{}, nil
}
