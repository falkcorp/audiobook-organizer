// file: internal/plugins/maintenance/deps_t6g2_test.go
// version: 0.1.0
// guid: 1d9a4c72-6e03-4b58-a7f1-3c8e50b2d946
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// booksGateRecorder is a tasks.Writer that records every Write it is given and
// either runs fn or refuses with a fixed error.
type booksGateRecorder struct {
	mu     sync.Mutex
	refuse error
	writes []tasks.Write
}

func (r *booksGateRecorder) Do(ctx context.Context, w tasks.Write, fn func(context.Context) ([]tasks.Change, error)) error {
	r.mu.Lock()
	r.writes = append(r.writes, w)
	r.mu.Unlock()
	if r.refuse != nil {
		return r.refuse
	}
	_, err := fn(ctx)
	return err
}
func (r *booksGateRecorder) MarkRowApplied(context.Context, string) {}
func (r *booksGateRecorder) RowApplied(context.Context, string) (bool, error) {
	return false, nil
}

func (r *booksGateRecorder) stamps() []tasks.Write {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.writes)
}

// booksHostSpy is the host of the boot passes: it counts every call.
type booksHostSpy struct {
	mu    sync.Mutex
	calls []string
}

func (h *booksHostSpy) note(name string) {
	h.mu.Lock()
	h.calls = append(h.calls, name)
	h.mu.Unlock()
}

func (h *booksHostSpy) BackfillExternalIDs(context.Context, bool, func(int, int, string), walkresume.Run) (bool, error) {
	h.note("BackfillExternalIDs")
	return false, nil
}

func (h *booksHostSpy) StripMovementAtoms(context.Context, walkresume.Run) (bool, error) {
	h.note("StripMovementAtoms")
	return false, nil
}

func (h *booksHostSpy) RemuxMalformedM4BFiles(context.Context, func(int, int, string), walkresume.Run) (bool, error) {
	h.note("RemuxMalformedM4BFiles")
	return false, nil
}

func (h *booksHostSpy) TranscodeMalformedM4BFiles(context.Context, func(int, int, string), walkresume.Run) (bool, error) {
	h.note("TranscodeMalformedM4BFiles")
	return false, nil
}

func (h *booksHostSpy) QuarantineKnownBadFiles(context.Context) (int, bool, error) {
	h.note("QuarantineKnownBadFiles")
	return 0, false, nil
}

func (h *booksHostSpy) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// g2WriterCalls is one call of every method of t6g2Writer, by method name.
func g2WriterCalls(bookID string) map[string]func(w t6g2Writer) error {
	ctx := context.Background()
	return map[string]func(w t6g2Writer) error{
		"BackfillExternalIDs": func(w t6g2Writer) error {
			_, err := w.BackfillExternalIDs(ctx, false, nil, walkresume.Run{})
			return err
		},
		"StripMovementAtoms": func(w t6g2Writer) error {
			_, err := w.StripMovementAtoms(ctx, walkresume.Run{})
			return err
		},
		"RemuxMalformedM4BFiles": func(w t6g2Writer) error {
			_, err := w.RemuxMalformedM4BFiles(ctx, nil, walkresume.Run{})
			return err
		},
		"TranscodeMalformedM4BFiles": func(w t6g2Writer) error {
			_, err := w.TranscodeMalformedM4BFiles(ctx, nil, walkresume.Run{})
			return err
		},
		"QuarantineKnownBadFiles": func(w t6g2Writer) error {
			_, _, err := w.QuarantineKnownBadFiles(ctx)
			return err
		},
		"RepairBookHashIndex": func(w t6g2Writer) error {
			_, err := w.RepairBookHashIndex(ctx, nil)
			return err
		},
		"RebuildBookAtPathIndex": func(w t6g2Writer) error {
			_, err := w.RebuildBookAtPathIndex(ctx, nil)
			return err
		},
		"BackfillBookAtPathIndex": func(w t6g2Writer) error {
			_, err := w.BackfillBookAtPathIndex(ctx, nil)
			return err
		},
		"DeleteOrphanBookFileAcoustIDEntries": func(w t6g2Writer) error {
			_, err := w.DeleteOrphanBookFileAcoustIDEntries(ctx, nil)
			return err
		},
		"RetireAuthorBooksLegacy": func(w t6g2Writer) error {
			_, err := w.RetireAuthorBooksLegacy(ctx, nil)
			return err
		},
		"ModifyBook": func(w t6g2Writer) error {
			_, err := w.ModifyBook(bookID, func(b *database.Book) error { b.Title = "Changed"; return nil })
			return err
		},
		"SaveChaptersForBook": func(w t6g2Writer) error {
			return w.SaveChaptersForBook(bookID, []database.Chapter{{Title: "Chapter One"}})
		},
		"BatchCreateBookFiles": func(w t6g2Writer) error {
			return w.BatchCreateBookFiles([]*database.BookFile{{BookID: bookID, FilePath: "/lib/The Long Halloween/part1.m4b"}})
		},
		"MigrateBookSigToSidecar": func(w t6g2Writer) error {
			_, err := w.MigrateBookSigToSidecar(bookID)
			return err
		},
	}
}

func g2Fixture(t *testing.T) (*database.PebbleStore, string) {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	hash := "fhash-halloween"
	b, err := s.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: "/lib/The Long Halloween/part1.m4b", FileHash: &hash})
	require.NoError(t, err)
	return s, b.ID
}

// TestBooksT6G2Writer_CoversEveryMethodOfTheInterface fails when a method is
// added to t6g2Writer without a row or without a gated body: the call table and
// the group's row view must name exactly the interface's methods.
func TestBooksT6G2Writer_CoversEveryMethodOfTheInterface(t *testing.T) {
	want := t6WriterMethods(reflect.TypeFor[t6g2Writer]())
	calls := g2WriterCalls("x")
	got := make([]string, 0, len(calls))
	for m := range calls {
		got = append(got, m)
	}
	slices.Sort(got)
	require.Equal(t, want, got, "g2WriterCalls must call exactly the methods of t6g2Writer")

	view := groupRows(nil, reflect.TypeFor[t6g2Writer]())
	for _, m := range want {
		_, ok := view[m]
		require.True(t, ok, "%s has no row in the shared method table: the gate would refuse it", m)
	}
}

// TestBooksT6G2Writer_EveryCallIsOneStampedWrite proves each method makes one
// Writer.Do named for it, stamped with the shared table's whole row.
func TestBooksT6G2Writer_EveryCallIsOneStampedWrite(t *testing.T) {
	for method := range g2WriterCalls("placeholder") {
		t.Run(method, func(t *testing.T) {
			s, id := g2Fixture(t)
			rec := &booksGateRecorder{}
			deps := newT6G2Deps(s, &booksHostSpy{}, func() string { return t.TempDir() })
			_ = g2WriterCalls(id)[method](deps.NewWriter(rec))
			stamps := rec.stamps()
			require.Len(t, stamps, 1, "one call is one Writer.Do")
			require.Equal(t, "books."+method, stamps[0].Name)
			var want []entity.Ref
			for _, r := range t6WriterResources[method] {
				want = append(want, tasks.Res("books", string(r)))
			}
			require.ElementsMatch(t, want, stamps[0].Resources, "the stamp is the method's whole row, not a retyped list")
		})
	}
}

// TestBooksT6G2Writer_RefusalDoesNothing: a refused write (preview, a fenced
// run, outside the effects) leaves the store untouched and never reaches the
// host's passes.
func TestBooksT6G2Writer_RefusalDoesNothing(t *testing.T) {
	for _, refusal := range []error{tasks.ErrPreview, tasks.ErrFenced, tasks.ErrOutsideEffects} {
		for method := range g2WriterCalls("x") {
			t.Run(method+"/"+refusal.Error(), func(t *testing.T) {
				s, id := g2Fixture(t)
				before := t6g5Snapshot(t, s)
				host := &booksHostSpy{}
				w := newT6G2Deps(s, host, func() string { return t.TempDir() }).NewWriter(&booksGateRecorder{refuse: refusal})
				err := g2WriterCalls(id)[method](w)
				require.ErrorIs(t, err, refusal)
				require.Zero(t, host.count(), "the host pass ran after a refusal")
				require.Equal(t, before, t6g5Snapshot(t, s), "the store changed after a refusal")
			})
		}
	}
}

// TestBooksT6G2Writer_ReadOnlyRunHasNoWriter: core hands a ReadOnly kind a nil
// tasks.Writer. The writer the builder returns refuses every write instead of
// dereferencing it.
func TestBooksT6G2Writer_ReadOnlyRunHasNoWriter(t *testing.T) {
	s, id := g2Fixture(t)
	host := &booksHostSpy{}
	w := newT6G2Deps(s, host, func() string { return t.TempDir() }).NewWriter(nil)
	for method, call := range g2WriterCalls(id) {
		require.ErrorIs(t, call(w), tasks.ErrOutsideEffects, method)
	}
	require.Zero(t, host.count())
}

// TestBooksT6G2Store_PreviewReadsWriteNothing is the preview guarantee: the
// two reads a preview run uses to say what a live run would do touch no key.
// The control shows the same fixture does change when the gated write runs, so
// an unchanged snapshot cannot be an unreadable one.
func TestBooksT6G2Store_PreviewReadsWriteNothing(t *testing.T) {
	s, id := g2Fixture(t)

	// A pre-signature-sidecar row, the shape the migration classifies.
	raw, err := s.GetRaw("book:" + id)
	require.NoError(t, err)
	var row map[string]any
	require.NoError(t, json.Unmarshal(raw, &row))
	row["book_sig_v1"] = "sig-halloween"
	row["book_sig_v1_mask"] = "mask-halloween"
	row["book_sig_segments"] = 4096
	row["book_sig_coverage_pct"] = 87
	row["book_sig_built_at"] = "2026-08-13T12:00:00Z"
	legacy, err := json.Marshal(row)
	require.NoError(t, err)
	require.NoError(t, s.SetRaw("book:"+id, legacy))

	// Missing hash-index keys, so the check has something to find and the repair
	// something to write.
	keys, err := s.ScanPrefix("book_hash:")
	require.NoError(t, err)
	require.NotEmpty(t, keys)
	for _, kv := range keys {
		require.NoError(t, s.DeleteRaw(kv.Key))
	}

	deps := newT6G2Deps(s, &booksHostSpy{}, func() string { return t.TempDir() })
	before := t6g5Snapshot(t, s)

	outcome, err := deps.Store.ClassifyBookSigMigration(id)
	require.NoError(t, err)
	require.Equal(t, database.BookSigMigrateMigrated, outcome, "the fixture must be a row the migration would act on")
	run, err := deps.Store.CheckBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	require.Positive(t, run.Missing, "the fixture must have a missing index key for the check to report")
	_, err = deps.Store.VerifyBookAtPathIndex(context.Background())
	require.NoError(t, err)

	require.Equal(t, before, t6g5Snapshot(t, s), "a preview read wrote")

	// Control: the gated writes do change this fixture.
	w := deps.NewWriter(&booksGateRecorder{})
	_, err = w.MigrateBookSigToSidecar(id)
	require.NoError(t, err)
	_, err = w.RepairBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	require.NotEqual(t, before, t6g5Snapshot(t, s), "the control write left the fixture unchanged: the snapshot cannot see writes")
}

// TestBooksT6G2Deps_ReportPathConfines: the builder's ReportPath is the
// confining resolver (Plugin.resolveReportPath's), not a pass-through, so an
// explicit path outside the library's report directory is refused.
func TestBooksT6G2Deps_ReportPathConfines(t *testing.T) {
	s, _ := g2Fixture(t)
	root := t.TempDir()
	deps := newT6G2Deps(s, &booksHostSpy{}, func() string { return root })

	got, err := deps.ReportPath("", "book-shape.tsv")
	require.NoError(t, err)
	require.Contains(t, got, root)

	_, err = deps.ReportPath("/etc/passwd", "book-shape.tsv")
	require.Error(t, err, "an explicit path outside the report directory must be refused")
}

func TestBooksT6G2Store_SupportsReflectsTheStore(t *testing.T) {
	s, _ := g2Fixture(t)
	deps := newT6G2Deps(s, &booksHostSpy{}, func() string { return "" })
	for _, c := range []t6g2Capability{t6g2CapAtPathVerify, t6g2CapAtPathRebuild, t6g2CapAtPathBackfill, t6g2CapHashIndex, t6g2CapAcoustIDRepair, t6g2CapSigMigrate, t6g2CapChapters, t6g2CapFolderBuild} {
		require.True(t, deps.Store.Supports(c), string(c))
	}
	require.False(t, deps.Store.Supports(t6g2Capability("nonsense")))
	require.Contains(t, deps.Store.Describe(), "PebbleStore")
}

// TestBooksT6G2Deps_NilInputsStayUnset: the builder does not wrap a missing
// input in an adapter, so the group's own refusals (Store, NewWriter) still
// fire instead of the run failing later on a nil dereference.
func TestBooksT6G2Deps_NilInputsStayUnset(t *testing.T) {
	s, _ := g2Fixture(t)
	kind := t6g2KindExternalIDBackfill
	par := tasks.Fixed(1, "test")

	d := newT6G2Deps(nil, &booksHostSpy{}, func() string { return "" })
	require.Nil(t, d.Store)
	require.Nil(t, d.NewWriter)
	require.Nil(t, d.ReportPath)
	_, err := t6g2NativeEnv(t6g2NewRC(kind, tasks.ModeLive, &booksGateRecorder{}), d, par)
	require.ErrorContains(t, err, "t6g2Deps.Store is not set")

	d = newT6G2Deps(s, nil, func() string { return "" })
	require.NotNil(t, d.Store)
	require.Nil(t, d.NewWriter)
	_, err = t6g2NativeEnv(t6g2NewRC(kind, tasks.ModeLive, &booksGateRecorder{}), d, par)
	require.ErrorContains(t, err, "t6g2Deps.NewWriter is not set")

	d = newT6G2Deps(s, &booksHostSpy{}, nil)
	require.Nil(t, d.ReportPath)
	_, err = t6g2NativeEnv(t6g2NewRC(kind, tasks.ModeLive, &booksGateRecorder{}), d, par)
	require.NoError(t, err)
}

// TestBooksT6G2Writer_EveryKindsCallsAreAdmittedByItsOwnEffects is the proof
// that a whole-row stamp is not wider than what a kind declares: each native
// kind of the group is built over the builder's deps, and every writer method
// its core can call (the table's calls) runs through the builder's writer over
// the Effects-enforcing writer built from the kind's own definition, in Live and
// in Preview. A stamp outside the kind's Effects is a refusal the writer
// records, which RequireClean reports.
func TestBooksT6G2Writer_EveryKindsCallsAreAdmittedByItsOwnEffects(t *testing.T) {
	s, id := g2Fixture(t)
	deps := newT6G2Deps(s, &booksHostSpy{}, func() string { return t.TempDir() })
	defs := t6g2KindList(deps, nil)
	for _, row := range t6g2Rows() {
		if len(row.calls) == 0 {
			continue
		}
		def := t6g2KindByName(t, defs, row.kind)
		for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
			t.Run(string(row.kind)+"/"+mode.String(), func(t *testing.T) {
				enforcing := writertest.New(def, mode)
				w := deps.NewWriter(enforcing)
				for _, method := range row.calls {
					_ = g2WriterCalls(id)[method](w)
				}
				require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
				require.Len(t, enforcing.Writes(), len(row.calls), "every call is one stamped write")
			})
		}
	}
}

// The author_books_legacy retirement (F5-18j) is served through the capability
// of the store: the check reads and writes nothing, the retirement is one gated
// call stamped with the shared table's row (authors) and refused when the writer
// refuses.
func TestBooksT6G2_AuthorBooksLegacyRetireIsACapabilityCheckThenOneGatedWrite(t *testing.T) {
	s, _ := g2Fixture(t)
	deps := newT6G2Deps(s, &booksHostSpy{}, func() string { return t.TempDir() })
	require.True(t, deps.Store.Supports(t6g2CapAuthorBooksLegacyRetire), "the Pebble store carries the retirement; a false here makes the kind refuse")

	before := t6g5Snapshot(t, s)
	rep, err := deps.Store.CheckAuthorBooksLegacyRetire(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, rep.Apply, "the check is the preview")
	require.Equal(t, before, t6g5Snapshot(t, s), "the check wrote")

	rec := &booksGateRecorder{}
	rep, err = deps.NewWriter(rec).RetireAuthorBooksLegacy(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, rep.Apply)
	stamps := rec.stamps()
	require.Len(t, stamps, 1)
	require.Len(t, stamps[0].Resources, 1)
	require.Equal(t, string(ops.ResAuthors), stamps[0].Resources[0].Kind)

	refused := &booksGateRecorder{refuse: tasks.ErrOutsideEffects}
	_, err = deps.NewWriter(refused).RetireAuthorBooksLegacy(context.Background(), nil)
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
}
