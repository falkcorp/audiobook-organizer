// file: internal/plugins/metafetch/books_tasklists_test.go
// version: 0.1.0
// guid: 3e8b1c57-92d4-4a60-b7f3-d05a6e19c482
// last-edited: 2026-11-22

package metafetch

import (
	"reflect"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// booksSpy is the store under the builder: every read is the embedded nil
// interface (never called), every write is counted.
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

func (s *booksSpy) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.note("ModifyBook")
	return &database.Book{ID: id}, nil
}

func (s *booksSpy) RecordMetadataChange(*database.MetadataChangeRecord) error {
	s.note("RecordMetadataChange")
	return nil
}
func (s *booksSpy) SetRaw(string, []byte) error   { s.note("SetRaw"); return nil }
func (s *booksSpy) DeleteRawBatch([]string) error { s.note("DeleteRawBatch"); return nil }

type fakeCandidates struct{}

func (fakeCandidates) GetCachedCandidates(string) (*database.MetadataCandidateCache, bool, error) {
	return nil, false, nil
}

func booksInputs(src KindSource) BooksTaskInputs {
	return BooksTaskInputs{
		Source:     src,
		Candidates: fakeCandidates{},
		Scoring:    func() config.MetadataScoringConfig { return config.MetadataScoringConfig{} },
		NewAudible: func() AudibleSearcher { return nil },
		Schedules:  func(tasks.Kind) *tasks.Schedule { return nil },
	}
}

func booksDeps(t *testing.T, src KindSource) KindDeps {
	t.Helper()
	d, err := newBooksKindDeps(booksInputs(src))
	require.NoError(t, err)
	return d
}

func kindByName(t *testing.T, deps KindDeps, k tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range (&Plugin{}).Kinds(deps, nil) {
		if d.Kind() == k {
			return d
		}
	}
	t.Fatalf("no kind %s", k)
	return nil
}

// Every method of kindWriter has a row, and each kind's own Effects admit the
// calls its core makes, in Live and in Preview (a Preview call is recorded and
// the store is never touched).
func TestBooksKindWriter_CallsAreAdmittedByTheKindsOwnEffects(t *testing.T) {
	cases := []struct {
		kind  tasks.Kind
		calls func(w kindWriter) []error
		want  []string
	}{
		{asinBackfillKind, func(w kindWriter) []error {
			_, e1 := w.ModifyBook("b", func(*database.Book) error { return nil })
			return []error{
				e1,
				w.RecordMetadataChange(&database.MetadataChangeRecord{BookID: "b"}),
				w.SetRawASINMiss("asinbackfill:miss:b", nil),
				w.SetRawASINMiss("asinbackfill:isbnmiss:b", nil),
				w.SetRawPCache("pcache:q", nil),
			}
		}, []string{"ModifyBook", "RecordMetadataChange", "SetRaw", "SetRaw", "SetRaw"}},
		{pcacheSweepKind, func(w kindWriter) []error {
			return []error{w.DeleteRawBatch([]string{"pcache:a", "pcache:b"})}
		}, []string{"DeleteRawBatch"}},
	}
	for _, tc := range cases {
		for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
			t.Run(string(tc.kind)+"/"+mode.String(), func(t *testing.T) {
				spy := &booksSpy{}
				deps := booksDeps(t, spy)
				enforcing := writertest.New(kindByName(t, deps, tc.kind), mode)
				errs := tc.calls(deps.NewWriter(enforcing))
				require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
				require.Len(t, enforcing.Writes(), len(tc.want), "every method is one stamped write")
				for _, err := range errs {
					if mode == tasks.ModeLive {
						require.NoError(t, err)
					} else {
						require.ErrorIs(t, err, tasks.ErrPreview)
					}
				}
				if mode == tasks.ModeLive {
					require.Equal(t, tc.want, spy.calls)
				} else {
					require.Empty(t, spy.calls, "a Preview run reached the store")
				}
			})
		}
	}
}

// Raw writes outside the method's family are refused and never reach the store.
func TestBooksKindWriter_RawKeyOutsideItsFamilyIsRefused(t *testing.T) {
	spy := &booksSpy{}
	deps := booksDeps(t, spy)
	w := deps.NewWriter(writertest.ForSet(asinBackfillKind, nil, tasks.ModeLive))
	for name, err := range map[string]error{
		"asin miss on a pcache key":     w.SetRawASINMiss("pcache:x", nil),
		"asin miss on an unrelated key": w.SetRawASINMiss("user:x", nil),
		"pcache on a miss key":          w.SetRawPCache("asinbackfill:miss:x", nil),
		"batch with a foreign key":      w.DeleteRawBatch([]string{"pcache:a", "book:b"}),
	} {
		require.ErrorIs(t, err, tasks.ErrOutsideEffects, name)
	}
	require.Empty(t, spy.calls)
}

// A kind that declares no write (calibrate-scoring) is refused every write, and
// a run with no writer at all (ReadOnly) refuses instead of dereferencing nil.
func TestBooksKindWriter_ReadOnlyKindAndNoWriterRefuse(t *testing.T) {
	spy := &booksSpy{}
	deps := booksDeps(t, spy)
	enforcing := writertest.New(kindByName(t, deps, calibrateScoringKind), tasks.ModeLive)
	require.ErrorIs(t, deps.NewWriter(enforcing).DeleteRawBatch([]string{"pcache:a"}), tasks.ErrOutsideEffects)
	_, err := deps.NewWriter(enforcing).ModifyBook("b", func(*database.Book) error { return nil })
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	_, err = deps.NewWriter(nil).ModifyBook("b", func(*database.Book) error { return nil })
	require.ErrorIs(t, err, tasks.ErrOutsideEffects, "a nil run writer refuses")
	require.ErrorIs(t, deps.NewWriter(nil).DeleteRawBatch([]string{"pcache:a"}), tasks.ErrOutsideEffects)
	require.Empty(t, spy.calls)
}

// Every field of KindDeps the builder owns is set; a field left unset would
// register a kind that fails at its first run.
func TestBooksKindDeps_EveryFieldIsSet(t *testing.T) {
	d := booksDeps(t, &booksSpy{})
	v := reflect.ValueOf(d)
	optional := map[string]bool{"BuildSourceChain": true}
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if optional[name] {
			continue
		}
		require.False(t, v.Field(i).IsZero(), "KindDeps.%s is unset", name)
	}
}

func TestBooksTaskLists_MissingInputsRefuse(t *testing.T) {
	full := booksInputs(&booksSpy{})
	defs, err := BooksTaskLists(full)
	require.NoError(t, err)
	require.Len(t, defs, 3)
	for name, mutate := range map[string]func(*BooksTaskInputs){
		"Source":     func(in *BooksTaskInputs) { in.Source = nil },
		"Candidates": func(in *BooksTaskInputs) { in.Candidates = nil },
		"Scoring":    func(in *BooksTaskInputs) { in.Scoring = nil },
		"NewAudible": func(in *BooksTaskInputs) { in.NewAudible = nil },
		"Schedules":  func(in *BooksTaskInputs) { in.Schedules = nil },
	} {
		in := full
		mutate(&in)
		_, err := BooksTaskLists(in)
		require.ErrorContains(t, err, name)
	}
}

// Every definition the list returns is registrable.
func TestBooksTaskLists_DefinitionsHaveNoProblems(t *testing.T) {
	defs, err := BooksTaskLists(booksInputs(&booksSpy{}))
	require.NoError(t, err)
	for _, d := range defs {
		require.Empty(t, tasks.Problems(d), d.Kind())
	}
}
