// file: internal/plugins/deluge/books_tasklists_test.go
// version: 0.1.0
// guid: c84e3a91-5d27-4b06-8e1f-a70b6d92c315
// last-edited: 2026-11-22

package deluge

import (
	"reflect"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
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

func (s *booksSpy) ModifyBookFile(_, id string, _ func(*database.BookFile) error) (*database.BookFile, error) {
	s.note("ModifyBookFile")
	return &database.BookFile{ID: id}, nil
}

func (s *booksSpy) UpdateBookFile(string, *database.BookFile) error {
	s.note("UpdateBookFile")
	return nil
}

type booksFakeCache struct{}

func (booksFakeCache) Refresh() error          { return nil }
func (booksFakeCache) IsProtected(string) bool { return false }

func booksInputs(src KindSource) BooksTaskInputs {
	return BooksTaskInputs{
		Source:    src,
		Cache:     booksFakeCache{},
		Config:    &config.Config{},
		Schedules: func(tasks.Kind) *tasks.Schedule { return nil },
	}
}

func booksDeps(t *testing.T, in BooksTaskInputs) KindDeps {
	t.Helper()
	d, err := newBooksKindDeps(in)
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

// Each kind's own Effects admit the calls its core makes, in Live and Preview
// (a Preview call is recorded and the store is never touched).
func TestBooksKindWriter_CallsAreAdmittedByTheKindsOwnEffects(t *testing.T) {
	cases := []struct {
		kind  tasks.Kind
		calls func(w kindWriter) []error
		want  []string
	}{
		{centralizeKind, func(w kindWriter) []error {
			_, e1 := w.ModifyBookFile("b", "f", func(*database.BookFile) error { return nil })
			return []error{e1, w.UpdateBookFile("f", &database.BookFile{ID: "f"})}
		}, []string{"ModifyBookFile", "UpdateBookFile"}},
		{linkBackfillKind, func(w kindWriter) []error {
			_, e1 := w.ModifyBookFile("b", "f", func(*database.BookFile) error { return nil })
			return []error{e1}
		}, []string{"ModifyBookFile"}},
	}
	for _, tc := range cases {
		for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
			t.Run(string(tc.kind)+"/"+mode.String(), func(t *testing.T) {
				spy := &booksSpy{}
				deps := booksDeps(t, booksInputs(spy))
				enforcing := writertest.New(kindByName(t, deps, tc.kind), mode)
				errs := tc.calls(deps.NewWriter(enforcing))
				require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
				require.Len(t, enforcing.Writes(), len(tc.want))
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

// A read-only kind is refused every write; a run with no writer (nil) refuses
// instead of dereferencing nil. Neither reaches the store.
func TestBooksKindWriter_ReadOnlyKindAndNoWriterRefuse(t *testing.T) {
	spy := &booksSpy{}
	deps := booksDeps(t, booksInputs(spy))
	ro := deps.NewWriter(writertest.New(kindByName(t, deps, pathUpdateKind), tasks.ModeLive))
	_, err := ro.ModifyBookFile("b", "f", func(*database.BookFile) error { return nil })
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.ErrorIs(t, ro.UpdateBookFile("f", nil), tasks.ErrOutsideEffects)

	none := deps.NewWriter(nil)
	require.ErrorIs(t, none.UpdateBookFile("f", nil), tasks.ErrOutsideEffects)
	require.Empty(t, spy.calls)
}

// A nil *Client leaves Client an untyped-nil interface (Deluge not configured);
// a real one is set.
func TestBooksKindDeps_ClientNilnessIsPreserved(t *testing.T) {
	require.Nil(t, booksDeps(t, booksInputs(&booksSpy{})).Client)
	in := booksInputs(&booksSpy{})
	in.Client = &delugeclient.Client{}
	require.NotNil(t, booksDeps(t, in).Client)
}

// Every field of KindDeps the builder owns is set (Client is optional by
// design; the link seams default in production).
func TestBooksKindDeps_EveryFieldIsSet(t *testing.T) {
	d := booksDeps(t, booksInputs(&booksSpy{}))
	v := reflect.ValueOf(d)
	optional := map[string]bool{"Client": true, "LinkHash": true, "LinkNow": true, "LinkRoundFiles": true}
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if !optional[name] {
			require.False(t, v.Field(i).IsZero(), "KindDeps.%s is unset", name)
		}
	}
}

func TestBooksTaskLists_MissingInputsRefuseAndDefinitionsAreClean(t *testing.T) {
	full := booksInputs(&booksSpy{})
	defs, err := BooksTaskLists(full)
	require.NoError(t, err)
	require.Len(t, defs, 4)
	for _, d := range defs {
		require.Empty(t, tasks.Problems(d), d.Kind())
	}
	for name, mutate := range map[string]func(*BooksTaskInputs){
		"Source":    func(in *BooksTaskInputs) { in.Source = nil },
		"Cache":     func(in *BooksTaskInputs) { in.Cache = nil },
		"Config":    func(in *BooksTaskInputs) { in.Config = nil },
		"Schedules": func(in *BooksTaskInputs) { in.Schedules = nil },
	} {
		in := full
		mutate(&in)
		_, err := BooksTaskLists(in)
		require.ErrorContains(t, err, name)
	}
}

// link-backfill's declared Effects are exactly the row of the one store write its
// core makes (ModifyBookFile): a narrower declaration is refused by the fence
// after the flip, a wider one is access the kind does not use.
func TestBooksKindWriter_LinkBackfillDeclaresExactlyItsWriteRow(t *testing.T) {
	rows, err := booksKindRows()
	require.NoError(t, err)
	def := kindByName(t, booksDeps(t, booksInputs(&booksSpy{})), linkBackfillKind)
	require.Equal(t, refStrings(rows["ModifyBookFile"]), refStrings2(tasks.CommonOf(def).Effects.WriteSet()))
}

// Several inputs missing are named in sorted order, the same way every call.
func TestBooksTaskLists_MissingInputsAreNamedInSortedOrder(t *testing.T) {
	for range 20 {
		_, err := BooksTaskLists(BooksTaskInputs{})
		require.ErrorContains(t, err, "missing input(s): [Cache Config Schedules Source]")
	}
}
