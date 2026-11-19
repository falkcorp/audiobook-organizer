// file: internal/books/writer/metafetchgate/store_test.go
// version: 1.0.0
// guid: b60f3d92-14e7-4a85-9c2d-7e1a58f4c036
// last-edited: 2026-11-19

package metafetchgate_test

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/metafetchgate"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// gatedMethods are the methods of metafetch.Store that write, every one of
// which Store overrides. readMethods are the ones that only read and are
// promoted from the inner store. A method of metafetch.Store in neither list
// fails TestEveryStoreMethodIsClassified until someone decides which it is:
// this is what stops a write added to the store later from being promoted
// ungated.
var gatedMethods = []string{
	"AddBookTagWithSource", "BatchCreateBookFiles", "CreateAuthor", "CreateBook", "CreateBookFile", "CreateOperationChange",
	"CreateSeries", "DeleteBook", "DeleteMetadataCache", "DeleteMetadataFieldState", "DeleteOperationState", "DeleteRaw",
	"DeleteRawBatch", "DeleteUserPreference", "FlagMetadataHashDuplicate", "MarkNeedsRescan", "ModifyBook", "ModifyBookAuthors",
	"PatchBookFileFields", "PutMetadataCache", "RecordMetadataChange", "RecordPathChange", "RemoveBookTagsByPrefix",
	"SetBookAuthors", "SetBookNarrators", "SetLastWrittenAt", "SetRaw", "SetUserPreference", "SetUserPreferenceForUser",
	"UpdateBook", "UpdateBookFile", "UpdateBookFileHashes", "UpsertMetadataFieldState",
}

var readMethods = []string{
	"BookFilesAtPath", "CountPrefix", "GetAllBooksCore", "GetAllImportPaths", "GetAllPreferencesForUser", "GetAllUserPreferences",
	"GetAuthorByID", "GetAuthorByName", "GetBookAuthors", "GetBookByFilePath", "GetBookByID", "GetBookChangeHistory",
	"GetBookFileByID", "GetBookFileByPath", "GetBookFiles", "GetBookFilesForIDsCore", "GetBookNarrators", "GetBookTags",
	"GetBookTagsDetailed", "GetBooksByAuthorIDCore", "GetBooksByMetadataSourceHash", "GetBooksBySeriesIDCore",
	"GetBooksByVersionGroup", "GetChaptersForBook", "GetMetadataCache", "GetMetadataFieldStates", "GetNarratorByID", "GetRaw",
	"GetSeriesByID", "GetSeriesByName", "GetUserPreference", "GetUserPreferenceForUser", "ListMetadataCacheKeys", "ScanPrefix",
	"ScanPrefixPage",
}

func storeMethods() []string {
	ty := reflect.TypeFor[metafetch.Store]()
	out := make([]string, ty.NumMethod())
	for i := range out {
		out[i] = ty.Method(i).Name
	}
	sort.Strings(out)
	return out
}

// Every method of metafetch.Store is classified exactly once, as gated or as a
// read; no classified name is a method the store no longer has.
func TestEveryStoreMethodIsClassified(t *testing.T) {
	all := storeMethods()
	classified := append(slices.Clone(gatedMethods), readMethods...)
	sort.Strings(classified)
	require.Equal(t, slices.Compact(slices.Clone(classified)), classified, "a method is classified twice")
	require.Equal(t, all, classified, "metafetch.Store changed: classify the new or removed method as gated (and give it a row) or as a read")
}

// A method classified as a read has no row (a row names a write); every gated
// method has a row, or is a raw write qualified by key family.
func TestGatedMethodsHaveRowsAndReadsDoNot(t *testing.T) {
	rows, err := metafetchgate.Rows()
	require.NoError(t, err)
	raw := map[string]bool{"SetRaw": true, "DeleteRaw": true, "DeleteRawBatch": true}
	for _, m := range gatedMethods {
		if raw[m] {
			continue
		}
		require.Contains(t, rows, m, "gated method %s has no row", m)
		require.NotEmpty(t, rows[m], "gated method %s has an empty row: it would pass the fence unchecked", m)
	}
	for _, m := range readMethods {
		require.NotContains(t, rows, m, "read method %s has a row", m)
	}
}

// refuseAll is a writer whose declared set is empty: every stamped write is
// refused. The inner store is nil, so a gated method that reaches it past the
// gate panics.
func refuseAll(t *testing.T) (*metafetchgate.Store, *writertest.Writer) {
	t.Helper()
	rows, err := metafetchgate.Rows()
	require.NoError(t, err)
	w := writertest.ForSet("books.f518b", nil, tasks.ModeLive)
	return metafetchgate.Wrap(t.Context(), nil, writer.New(w, rows)), w
}

// zeroArgs are the zero values for fn's parameters, except that SetRaw,
// DeleteRaw and DeleteRawBatch get a key in each known family in turn.
func zeroArgs(fn reflect.Value) []reflect.Value {
	ty := fn.Type()
	in := make([]reflect.Value, ty.NumIn())
	for i := range in {
		in[i] = reflect.Zero(ty.In(i))
	}
	return in
}

// No gated method reaches the inner store past a refusing writer, and every
// one returns the refusal: the whole write surface is fenced, none of it
// promoted ungated.
func TestNoGatedMethodReachesTheStoreWhenTheWriterRefuses(t *testing.T) {
	for _, m := range gatedMethods {
		t.Run(m, func(t *testing.T) {
			g, w := refuseAll(t)
			fn := reflect.ValueOf(g).MethodByName(m)
			require.True(t, fn.IsValid())
			args := zeroArgs(fn)
			// Raw writes are classified by key: give them a key in a known family.
			switch m {
			case "SetRaw":
				args[0] = reflect.ValueOf("pcache:x")
			case "DeleteRaw":
				args[0] = reflect.ValueOf("pcache:x")
			case "DeleteRawBatch":
				args[0] = reflect.ValueOf([]string{"pcache:x"})
			}
			var out []reflect.Value
			require.NotPanics(t, func() { out = fn.Call(args) }, "%s reached the nil inner store past the gate", m)
			err, _ := out[len(out)-1].Interface().(error)
			require.ErrorIs(t, err, tasks.ErrOutsideEffects, "%s was not refused", m)
			require.Empty(t, w.Writes())
			require.NotEmpty(t, w.Refusals(), "%s never asked the writer", m)
		})
	}
}

// A raw write on a key in no known family is refused without asking the
// writer, so a new raw write fails closed; a batch spanning families is
// refused, not split.
func TestRawWritesAreClassifiedByKeyFamilyAndFailClosed(t *testing.T) {
	g, w := refuseAll(t)
	for name, err := range map[string]error{
		"SetRaw unknown":        g.SetRaw("mystery:1", nil),
		"DeleteRaw unknown":     g.DeleteRaw("mystery:1"),
		"DeleteRawBatch":        g.DeleteRawBatch([]string{"pcache:a", "metadata_rejection:b"}),
		"DeleteRawBatch unknwn": g.DeleteRawBatch([]string{"mystery:1"}),
	} {
		require.ErrorIs(t, err, tasks.ErrOutsideEffects, name)
		require.ErrorIs(t, err, writer.ErrNoRow, name)
	}
	require.Empty(t, w.Refusals(), "an unclassifiable key is refused before the writer is asked")
}

// Each raw family stamps the resource it is, whichever raw write carries it.
func TestRawFamiliesStampTheirResource(t *testing.T) {
	for _, f := range writer.RawFamilies() {
		for _, m := range []string{"SetRaw", "DeleteRaw", "DeleteRawBatch"} {
			g, w := refuseAll(t)
			key := f.Prefix + "k"
			var err error
			switch m {
			case "SetRaw":
				err = g.SetRaw(key, nil)
			case "DeleteRaw":
				err = g.DeleteRaw(key)
			default:
				err = g.DeleteRawBatch([]string{key, key})
			}
			require.ErrorIs(t, err, tasks.ErrOutsideEffects)
			require.Contains(t, err.Error(), fmt.Sprintf("books/%s", f.Resource), "%s on %s", m, f.Prefix)
			require.Len(t, w.Refusals(), 1)
			require.True(t, strings.HasPrefix(w.Refusals()[0].Write.Name, "books."+m+"/"), w.Refusals()[0].Write.Name)
		}
	}
}
