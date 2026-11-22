// file: internal/plugins/acoustid/books_tasklists_test.go
// version: 0.1.0
// guid: 91e7c4a2-3b58-4d06-a1f9-60d2e8b53c47
// last-edited: 2026-11-22

package acoustid

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
)

// booksSpy is the store under the builder: every read is the embedded nil
// interface (never called), every write is counted. It also carries the batched
// wipe, so FingerprintResetter resolves.
type booksSpy struct {
	kindStore
	mu    sync.Mutex
	calls []string
}

func (s *booksSpy) note(m string) {
	s.mu.Lock()
	s.calls = append(s.calls, m)
	s.mu.Unlock()
}

func (s *booksSpy) UpdateBookFile(string, *database.BookFile) error {
	s.note("UpdateBookFile")
	return nil
}
func (s *booksSpy) ModifyBook(id string, _ func(*database.Book) error) (*database.Book, error) {
	s.note("ModifyBook")
	return &database.Book{ID: id}, nil
}
func (s *booksSpy) ClearBookSignature(string) error { s.note("ClearBookSignature"); return nil }
func (s *booksSpy) ReplaceFingerprintWindows(database.FingerprintWindowRef, []database.FingerprintWindow) error {
	s.note("ReplaceFingerprintWindows")
	return nil
}
func (s *booksSpy) RecordFingerprintWindowFailure(*database.FingerprintWindowFailure) error {
	s.note("RecordFingerprintWindowFailure")
	return nil
}
func (s *booksSpy) ClearAllAcoustIDFingerprints(context.Context, int, func(int, int, int)) (int, int, error) {
	s.note("ClearAllAcoustIDFingerprints")
	return 1, 2, nil
}

type booksCands struct{ spy *booksSpy }

func (booksCands) ListCandidates(database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	return nil, 0, nil
}
func (c booksCands) DeleteCandidates([]int64) (int, error) {
	c.spy.note("DeleteCandidates")
	return 1, nil
}

func booksInputs(spy *booksSpy) BooksTaskInputs {
	return BooksTaskInputs{
		Plugin:     &Plugin{},
		Source:     spy,
		Candidates: booksCands{spy},
		Schedules:  func(tasks.Kind) *tasks.Schedule { return nil },
	}
}

func booksDeps(t *testing.T, in BooksTaskInputs) KindDeps {
	t.Helper()
	d, err := newBooksKindDeps(in)
	require.NoError(t, err)
	return d
}

func kindByName(t *testing.T, in BooksTaskInputs, k tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range booksReceiver(in).Kinds(booksDeps(t, in), nil) {
		if d.Kind() == k {
			return d
		}
	}
	t.Fatalf("no kind %s", k)
	return nil
}

// writerCall is one method of the writer, by the name the gate stamps it with.
type writerCall struct {
	method string
	call   func(w kindWriter) error
}

var (
	callUpdateBookFile = writerCall{"UpdateBookFile", func(w kindWriter) error { return w.UpdateBookFile("f", &database.BookFile{ID: "f"}) }}
	callModifyBook     = writerCall{"ModifyBook", func(w kindWriter) error {
		_, err := w.ModifyBook("b", func(*database.Book) error { return nil })
		return err
	}}
	callClearSignature = writerCall{"ClearBookSignature", func(w kindWriter) error { return w.ClearBookSignature("b") }}
	callReplaceWindows = writerCall{"ReplaceFingerprintWindows", func(w kindWriter) error {
		return w.ReplaceFingerprintWindows(database.FileWindowRef("f"), nil)
	}}
	callRecordFailure = writerCall{"RecordFingerprintWindowFailure", func(w kindWriter) error {
		return w.RecordFingerprintWindowFailure(&database.FingerprintWindowFailure{Ref: database.FileWindowRef("f")})
	}}
	callDeleteCandidates = writerCall{"DeleteCandidates", func(w kindWriter) error {
		_, err := w.DeleteCandidates([]int64{1})
		return err
	}}
	callWipe = writerCall{"ClearAllAcoustIDFingerprints", func(w kindWriter) error {
		r := w.FingerprintResetter()
		if r == nil {
			return nil
		}
		_, _, err := r.ClearAllAcoustIDFingerprints(context.Background(), 10, nil)
		return err
	}}
)

// Every call a kind's core can make is admitted by that kind's own Effects, in
// Live and Preview. The calls per kind are the writes its core reaches (kinds_*.go comments).
func TestBooksKindWriter_CallsAreAdmittedByTheKindsOwnEffects(t *testing.T) {
	synth := []writerCall{callUpdateBookFile, callModifyBook, callClearSignature}
	cases := []struct {
		kind  tasks.Kind
		calls []writerCall
	}{
		{backfillKind, synth},
		{fingerprintRescanKind, synth},
		{durationRepairKind, synth},
		{lshBackfillKind, []writerCall{callUpdateBookFile}},
		{onlineLookupKind, []writerCall{callUpdateBookFile}},
		{resetAllKind, []writerCall{callUpdateBookFile, callDeleteCandidates, callWipe}},
		{windowBackfillKind, []writerCall{callReplaceWindows, callRecordFailure}},
	}
	for _, tc := range cases {
		for _, c := range tc.calls {
			for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
				t.Run(string(tc.kind)+"/"+c.method+"/"+mode.String(), func(t *testing.T) {
					spy := &booksSpy{}
					in := booksInputs(spy)
					enforcing := writertest.New(kindByName(t, in, tc.kind), mode)
					err := c.call(booksDeps(t, in).NewWriter(enforcing))
					require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
					if mode == tasks.ModePreview {
						require.ErrorIs(t, err, tasks.ErrPreview)
						require.Empty(t, spy.calls, "a Preview run reached the store")
						return
					}
					require.NoError(t, err)
					require.Equal(t, []string{c.method}, spy.calls)
				})
			}
		}
	}
}

// The scan declares only embeddings and runs through the engine: every store
// write is refused for it, and a run with no writer (nil) refuses instead of
// dereferencing nil. Neither reaches the store.
func TestBooksKindWriter_ScanAndNoWriterRefuse(t *testing.T) {
	spy := &booksSpy{}
	in := booksInputs(spy)
	scan := booksDeps(t, in).NewWriter(writertest.New(kindByName(t, in, scanKind), tasks.ModeLive))
	for _, c := range []writerCall{callUpdateBookFile, callModifyBook, callClearSignature, callReplaceWindows, callWipe} {
		require.ErrorIs(t, c.call(scan), tasks.ErrOutsideEffects, c.method)
	}
	none := booksDeps(t, in).NewWriter(nil)
	for _, c := range []writerCall{callUpdateBookFile, callDeleteCandidates, callWipe} {
		require.ErrorIs(t, c.call(none), tasks.ErrOutsideEffects, c.method)
	}
	require.Empty(t, spy.calls)
}

// No candidate store (embedding disabled) leaves Candidates an untyped nil and
// the delete refuses, rather than panicking on a nil store.
func TestBooksKindDeps_NoCandidateStore(t *testing.T) {
	in := booksInputs(&booksSpy{})
	in.Candidates = nil
	d := booksDeps(t, in)
	require.Nil(t, d.Candidates)
	_, err := d.NewWriter(writertest.New(kindByName(t, in, resetAllKind), tasks.ModeLive)).DeleteCandidates([]int64{1})
	require.ErrorIs(t, err, errNoCandidateStore)
}

// The kinds are declared on the registered plugin: a zero plugin would hand
// window-backfill a worker hub nothing serves.
func TestBooksTaskLists_ReceiverIsTheRegisteredPlugin(t *testing.T) {
	in := booksInputs(&booksSpy{})
	require.Same(t, in.Plugin, booksReceiver(in))
	hub := in.Plugin.WorkerHub()
	require.Same(t, hub, booksReceiver(in).windowHooks(booksDeps(t, in).Window).Hub)
	require.NotSame(t, hub, (&Plugin{}).windowHooks(WindowKindDeps{}).Hub, "a zero plugin has a hub of its own")
}

// Every field of KindDeps the builder owns is set; the allowlist names why each
// of the others may be zero.
func TestBooksKindDeps_EveryFieldIsSet(t *testing.T) {
	in := booksInputs(&booksSpy{})
	in.NewEngine = func(tasks.Writer) ScanEngine { return nil }
	v := reflect.ValueOf(booksDeps(t, in))
	optional := map[string]string{
		"Lookup":      "a nil hook is HEAD's own function",
		"Fingerprint": "a nil hook is HEAD's own function",
		"Window":      "a nil hook is filled from the registered plugin",
	}
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if _, ok := optional[name]; !ok {
			require.False(t, v.Field(i).IsZero(), "KindDeps.%s is unset", name)
		}
	}
}

func TestBooksTaskLists_MissingInputsRefuseAndDefinitionsAreClean(t *testing.T) {
	full := booksInputs(&booksSpy{})
	defs, err := BooksTaskLists(full)
	require.NoError(t, err)
	require.Len(t, defs, 8)
	for _, d := range defs {
		require.Empty(t, tasks.Problems(d), d.Kind())
	}
	for name, mutate := range map[string]func(*BooksTaskInputs){
		"Plugin":    func(in *BooksTaskInputs) { in.Plugin = nil },
		"Source":    func(in *BooksTaskInputs) { in.Source = nil },
		"Schedules": func(in *BooksTaskInputs) { in.Schedules = nil },
	} {
		in := full
		mutate(&in)
		_, err := BooksTaskLists(in)
		require.ErrorContains(t, err, name)
	}
}

// backfill's declared Effects are exactly the union of the rows of the store
// writes its core makes (UpdateBookFile, ModifyBook, ClearBookSignature): a
// narrower declaration is refused by the fence after the flip, a wider one is
// access the kind does not use.
func TestBooksKindWriter_BackfillDeclaresExactlyItsWriteRows(t *testing.T) {
	rows, err := booksKindRows()
	require.NoError(t, err)
	union := resources.Union(rows["UpdateBookFile"], rows["ModifyBook"], rows["ClearBookSignature"])
	def := kindByName(t, booksInputs(&booksSpy{}), backfillKind)
	require.Equal(t, refStrings(union), refStrings2(tasks.CommonOf(def).Effects.WriteSet()))
}
