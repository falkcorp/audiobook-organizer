// file: internal/books/writer/metafetchgate/store_forward_test.go
// version: 1.1.0
// guid: 8802cdeb-4160-44e4-a64f-f7fa9983a421
// last-edited: 2026-11-19

package metafetchgate_test

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/metafetchgate"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// call is one write the recording inner store saw: the method it was reached
// by and the arguments it was given.
type call struct {
	method string
	args   []any
}

// sentinels are what the recording store returns, so a decorator that drops or
// swaps a result is seen.
type sentinels struct {
	book   *database.Book
	author *database.Author
	series *database.Series
	file   *database.BookFile // PatchBookFileFields: before
	after  *database.BookFile // PatchBookFileFields: after, a different row
	cred   []database.BookAuthor
}

// forwardRecorder is a metafetch.Store whose every write method records its own
// name and arguments and returns a sentinel. It is written out method by
// method, not generated, so the compiler checks each signature against
// metafetch.Store; TestGatedMethodsForwardToTheSameNamedInnerMethod checks each
// method records the name it is called by, so a typo here fails loudly.
type forwardRecorder struct {
	metafetch.Store // reads: never called by the test
	mu              sync.Mutex
	calls           []call
	ret             sentinels
	// fail is what every method returns as its error: nil on the forward pass,
	// a sentinel on the error pass (round 3, m2).
	fail error
}

func (r *forwardRecorder) rec(method string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call{method, args})
}

func (r *forwardRecorder) CreateBook(b *database.Book) (*database.Book, error) {
	r.rec("CreateBook", b)
	return r.ret.book, r.fail
}
func (r *forwardRecorder) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	r.rec("UpdateBook", id, b)
	return r.ret.book, r.fail
}
func (r *forwardRecorder) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	r.rec("ModifyBook", id, fn)
	return r.ret.book, r.fail
}
func (r *forwardRecorder) DeleteBook(id string) error { r.rec("DeleteBook", id); return r.fail }
func (r *forwardRecorder) FlagMetadataHashDuplicate(primaryID, duplicateID string) error {
	r.rec("FlagMetadataHashDuplicate", primaryID, duplicateID)
	return r.fail
}
func (r *forwardRecorder) SetLastWrittenAt(id string, t time.Time) error {
	r.rec("SetLastWrittenAt", id, t)
	return r.fail
}
func (r *forwardRecorder) MarkNeedsRescan(id string) error {
	r.rec("MarkNeedsRescan", id)
	return r.fail
}
func (r *forwardRecorder) AddBookTagWithSource(bookID, tag, source string) error {
	r.rec("AddBookTagWithSource", bookID, tag, source)
	return r.fail
}
func (r *forwardRecorder) RemoveBookTagsByPrefix(bookID, prefix, source string) error {
	r.rec("RemoveBookTagsByPrefix", bookID, prefix, source)
	return r.fail
}
func (r *forwardRecorder) RecordPathChange(c *database.BookPathChange) error {
	r.rec("RecordPathChange", c)
	return r.fail
}
func (r *forwardRecorder) SetBookNarrators(bookID string, n []database.BookNarrator) error {
	r.rec("SetBookNarrators", bookID, n)
	return r.fail
}
func (r *forwardRecorder) CreateAuthor(name string) (*database.Author, error) {
	r.rec("CreateAuthor", name)
	return r.ret.author, r.fail
}
func (r *forwardRecorder) SetBookAuthors(bookID string, a []database.BookAuthor) error {
	r.rec("SetBookAuthors", bookID, a)
	return r.fail
}
func (r *forwardRecorder) ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
	r.rec("ModifyBookAuthors", bookID, fn)
	return r.ret.cred, r.fail
}
func (r *forwardRecorder) CreateSeries(name string, authorID *int) (*database.Series, error) {
	r.rec("CreateSeries", name, authorID)
	return r.ret.series, r.fail
}
func (r *forwardRecorder) CreateBookFile(f *database.BookFile) error {
	r.rec("CreateBookFile", f)
	return r.fail
}
func (r *forwardRecorder) BatchCreateBookFiles(fs []*database.BookFile) error {
	r.rec("BatchCreateBookFiles", fs)
	return r.fail
}
func (r *forwardRecorder) UpdateBookFile(id string, f *database.BookFile) error {
	r.rec("UpdateBookFile", id, f)
	return r.fail
}
func (r *forwardRecorder) UpdateBookFileHashes(id, originalHash, postMetadataHash, fileHash string) error {
	r.rec("UpdateBookFileHashes", id, originalHash, postMetadataHash, fileHash)
	return r.fail
}
func (r *forwardRecorder) PatchBookFileFields(bookID, fileID string, p database.BookFileFieldPatch) (*database.BookFile, *database.BookFile, error) {
	r.rec("PatchBookFileFields", bookID, fileID, p)
	return r.ret.file, r.ret.after, r.fail
}
func (r *forwardRecorder) PutMetadataCache(e *database.MetadataCandidateCache) error {
	r.rec("PutMetadataCache", e)
	return r.fail
}
func (r *forwardRecorder) DeleteMetadataCache(bookID string) error {
	r.rec("DeleteMetadataCache", bookID)
	return r.fail
}
func (r *forwardRecorder) UpsertMetadataFieldState(st *database.MetadataFieldState) error {
	r.rec("UpsertMetadataFieldState", st)
	return r.fail
}
func (r *forwardRecorder) DeleteMetadataFieldState(bookID, field string) error {
	r.rec("DeleteMetadataFieldState", bookID, field)
	return r.fail
}
func (r *forwardRecorder) RecordMetadataChange(rec *database.MetadataChangeRecord) error {
	r.rec("RecordMetadataChange", rec)
	return r.fail
}
func (r *forwardRecorder) CreateOperationChange(c *database.OperationChange) error {
	r.rec("CreateOperationChange", c)
	return r.fail
}
func (r *forwardRecorder) DeleteOperationState(id string) error {
	r.rec("DeleteOperationState", id)
	return r.fail
}
func (r *forwardRecorder) SetUserPreference(key, value string) error {
	r.rec("SetUserPreference", key, value)
	return r.fail
}
func (r *forwardRecorder) DeleteUserPreference(key string) error {
	r.rec("DeleteUserPreference", key)
	return r.fail
}
func (r *forwardRecorder) SetUserPreferenceForUser(userID, key, value string) error {
	r.rec("SetUserPreferenceForUser", userID, key, value)
	return r.fail
}
func (r *forwardRecorder) SetRaw(key string, value []byte) error {
	r.rec("SetRaw", key, value)
	return r.fail
}
func (r *forwardRecorder) DeleteRaw(key string) error { r.rec("DeleteRaw", key); return r.fail }
func (r *forwardRecorder) DeleteRawBatch(keys []string) error {
	r.rec("DeleteRawBatch", keys)
	return r.fail
}

// admitAll is a writer that declares every resource any row names, so every
// gated call is admitted and its closure runs.
func admitAll(t *testing.T) (*metafetchgate.Store, *forwardRecorder, *writertest.Writer) {
	t.Helper()
	rows, err := metafetchgate.Rows()
	require.NoError(t, err)
	var set []entity.Ref
	seen := map[string]bool{}
	for _, row := range rows {
		for _, r := range row {
			if !seen[string(r)] {
				seen[string(r)] = true
				set = append(set, tasks.Res(writer.Module, string(r)))
			}
		}
	}
	w := writertest.ForSet("books.f518b", set, tasks.ModeLive)
	rec := &forwardRecorder{ret: sentinels{
		book: &database.Book{ID: "ret-book"}, author: &database.Author{Name: "ret-author"},
		series: &database.Series{Name: "ret-series"}, file: &database.BookFile{ID: "ret-file"}, after: &database.BookFile{ID: "ret-file-after"},
		cred: []database.BookAuthor{{BookID: "ret-cred"}},
	}}
	return metafetchgate.Wrap(t.Context(), rec, writer.New(w, rows)), rec, w
}

// sentinelArgs builds one distinct, recognisable value per parameter of fn. A
// raw write gets a key in a known family; DeleteRawBatch a two-key batch of one
// family. Pointers, slices and funcs are fresh and non-nil, so passing the
// wrong argument, or a copy, is visible.
func sentinelArgs(m string, fn reflect.Value) []reflect.Value {
	ty := fn.Type()
	in := make([]reflect.Value, ty.NumIn())
	for i := range in {
		pt := ty.In(i)
		switch {
		case pt.Kind() == reflect.String:
			in[i] = reflect.ValueOf(fmt.Sprintf("s%d", i))
		case pt.Kind() == reflect.Ptr:
			in[i] = reflect.New(pt.Elem())
		case pt.Kind() == reflect.Slice:
			s := reflect.MakeSlice(pt, 2, 2)
			in[i] = s
		case pt.Kind() == reflect.Func:
			in[i] = reflect.MakeFunc(pt, func([]reflect.Value) []reflect.Value {
				out := make([]reflect.Value, pt.NumOut())
				for j := range out {
					out[j] = reflect.Zero(pt.Out(j))
				}
				return out
			})
		case pt == reflect.TypeFor[time.Time]():
			in[i] = reflect.ValueOf(time.Unix(1_700_000_000+int64(i), 0))
		case pt.Kind() == reflect.Struct:
			// A by-value struct is compared by value, so a zero one would equal
			// the zero a decorator that dropped it passes instead (a dropped
			// PatchBookFileFields patch): set one field.
			v := reflect.New(pt).Elem()
			setOneField(v)
			in[i] = v
		default:
			in[i] = reflect.Zero(pt)
		}
	}
	switch m {
	case "SetRaw":
		in[0] = reflect.ValueOf("pcache:s0")
		in[1] = reflect.ValueOf([]byte{1, 2, 3})
	case "DeleteRaw":
		in[0] = reflect.ValueOf("pcache:s0")
	case "DeleteRawBatch":
		in[0] = reflect.ValueOf([]string{"pcache:a", "pcache:b"})
	}
	return in
}

// setOneField makes the struct v non-zero by setting its first settable
// pointer, string or integer field.
func setOneField(v reflect.Value) {
	for j := 0; j < v.NumField(); j++ {
		f := v.Field(j)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.Ptr:
			f.Set(reflect.New(f.Type().Elem()))
		case reflect.String:
			f.SetString("x")
		case reflect.Int, reflect.Int64:
			f.SetInt(7)
		default:
			continue
		}
		return
	}
	panic("setOneField: " + v.Type().String() + " has no settable pointer, string or integer field")
}

// same compares an argument the wrapper received with the one the inner store
// got: pointers and funcs by identity (a copy is a different write), the rest
// by value.
func same(a, b any) bool {
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if av.Kind() != bv.Kind() || av.Type() != bv.Type() {
		return false
	}
	switch av.Kind() {
	case reflect.Ptr, reflect.Func:
		return av.Pointer() == bv.Pointer()
	}
	return reflect.DeepEqual(a, b)
}

// TestGatedMethodsForwardToTheSameNamedInnerMethod (round 2, m6): every gated
// method, admitted by its writer, calls the SAME-NAMED method of the inner store
// exactly once, with the arguments it was given in the order it was given them,
// and hands back the inner store's results. The refusal tests above run over a
// writer that never runs the closure, so a decorator that forwarded to the wrong
// method of the same signature (MarkNeedsRescan calling DeleteBook, two string
// arguments swapped) kept a correct stamp and passed all of them.
func TestGatedMethodsForwardToTheSameNamedInnerMethod(t *testing.T) {
	for _, m := range gatedMethods {
		t.Run(m, func(t *testing.T) {
			g, rec, w := admitAll(t)
			fn := reflect.ValueOf(g).MethodByName(m)
			require.True(t, fn.IsValid())
			args := sentinelArgs(m, fn)
			var out []reflect.Value
			require.NotPanics(t, func() { out = fn.Call(args) })

			require.Empty(t, w.Refusals(), "%s was refused by a writer that declares every resource", m)
			require.Len(t, rec.calls, 1, "%s must reach the inner store exactly once: %v", m, rec.calls)
			got := rec.calls[0]
			require.Equal(t, m, got.method, "%s forwarded to the inner method %s", m, got.method)
			require.Len(t, got.args, len(args), m)
			for i := range args {
				require.True(t, same(args[i].Interface(), got.args[i]),
					"%s: argument %d reached the inner store as %v, not the %v it was given", m, i, got.args[i], args[i].Interface())
			}

			// The results are the inner store's, in order.
			var want []any
			switch m {
			case "CreateBook", "UpdateBook", "ModifyBook":
				want = []any{rec.ret.book}
			case "CreateAuthor":
				want = []any{rec.ret.author}
			case "CreateSeries":
				want = []any{rec.ret.series}
			case "ModifyBookAuthors":
				want = []any{rec.ret.cred}
			case "PatchBookFileFields":
				want = []any{rec.ret.file, rec.ret.after}
			}
			require.Len(t, out, len(want)+1, "%s: unexpected result count", m)
			for i, wnt := range want {
				require.True(t, same(wnt, out[i].Interface()), "%s: result %d is not the inner store's", m, i)
			}
			require.Nil(t, out[len(out)-1].Interface(), "%s: the inner store's nil error was not returned", m)
		})
	}
}

// TestGatedMethodsReturnTheInnerError (round 3, m2): every gated method, admitted
// by its writer, returns the error the inner store returned: a forward written
// as `_ = s.innerStore.X(...); return nil` swallowed it and passed the forward test,
// whose inner store never failed.
func TestGatedMethodsReturnTheInnerError(t *testing.T) {
	for _, m := range gatedMethods {
		t.Run(m, func(t *testing.T) {
			g, rec, w := admitAll(t)
			boom := fmt.Errorf("inner %s failed", m)
			rec.fail = boom
			fn := reflect.ValueOf(g).MethodByName(m)
			require.True(t, fn.IsValid())
			var out []reflect.Value
			require.NotPanics(t, func() { out = fn.Call(sentinelArgs(m, fn)) })
			require.Empty(t, w.Refusals(), "%s was refused by a writer that declares every resource", m)
			require.Len(t, rec.calls, 1, "%s must reach the inner store exactly once", m)
			err, _ := out[len(out)-1].Interface().(error)
			require.ErrorIs(t, err, boom, "%s did not return the inner store's error", m)
		})
	}
}
