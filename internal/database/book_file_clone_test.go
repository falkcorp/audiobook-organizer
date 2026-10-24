// file: internal/database/book_file_clone_test.go
// version: 1.0.0
// guid: 5e9c2a71-0b3d-4f86-a1e4-c7d20b9f3a58
// last-edited: 2026-10-24

package database

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// requireNoSharedMemory fails on any pointer, slice or map reachable from a and
// b that is the same allocation.
func requireNoSharedMemory(t *testing.T, path string, a, b reflect.Value) {
	t.Helper()
	switch a.Kind() {
	case reflect.Struct:
		if a.Type() == reflect.TypeFor[time.Time]() {
			return
		}
		for i := range a.NumField() {
			if a.Type().Field(i).IsExported() {
				requireNoSharedMemory(t, path+"."+a.Type().Field(i).Name, a.Field(i), b.Field(i))
			}
		}
	case reflect.Pointer:
		if a.IsNil() {
			return
		}
		require.NotEqual(t, a.Pointer(), b.Pointer(), "%s: the clone shares a pointer with the source", path)
		requireNoSharedMemory(t, path+"*", a.Elem(), b.Elem())
	case reflect.Slice:
		if a.Len() == 0 {
			return
		}
		require.NotEqual(t, a.Pointer(), b.Pointer(), "%s: the clone shares a slice's backing array with the source", path)
		for i := range a.Len() {
			requireNoSharedMemory(t, path+"[]", a.Index(i), b.Index(i))
		}
	case reflect.Map:
		if a.Len() == 0 {
			return
		}
		require.NotEqual(t, a.Pointer(), b.Pointer(), "%s: the clone shares a map with the source", path)
	}
}

func TestCloneBookFile_SharesNothing(t *testing.T) {
	var src BookFile
	fillBookFileNonZero(t, &src) // bookfile_rescan_preserve_test.go: reflection-driven, covers new fields

	got := cloneBookFile(&src)

	require.Equal(t, src, got, "the clone carries every field")
	requireNoSharedMemory(t, "BookFile", reflect.ValueOf(src), reflect.ValueOf(got))
}

// The clone's whole purpose, stated as behaviour: a write through a field of the
// clone (what a staging step could do) never reaches the struct it was taken
// from, and a write to the source afterwards never reaches the clone.
func TestCloneBookFile_InPlaceWritesDoNotCross(t *testing.T) {
	mtime := int64(100)
	status := "ok"
	src := BookFile{
		ID:                  "f1",
		RawTags:             map[string]string{"k": "v"},
		AcoustIDFingerprint: []byte{1, 2, 3},
		LastScanMtime:       &mtime,
		TranscribeStatus:    &status,
	}

	c := cloneBookFile(&src)
	c.RawTags["k"] = "changed"
	c.AcoustIDFingerprint[0] = 99
	*c.LastScanMtime = 5
	*c.TranscribeStatus = "failed"

	require.Equal(t, "v", src.RawTags["k"])
	require.Equal(t, byte(1), src.AcoustIDFingerprint[0])
	require.Equal(t, int64(100), *src.LastScanMtime)
	require.Equal(t, "ok", *src.TranscribeStatus)

	src.AcoustIDFingerprint[1] = 77
	*src.TranscribeStatus = "later"
	require.Equal(t, byte(2), c.AcoustIDFingerprint[1])
	require.Equal(t, "failed", *c.TranscribeStatus)
}

func TestCloneBookFile_PreservesNilness(t *testing.T) {
	got := cloneBookFile(&BookFile{ID: "f1"})
	require.Nil(t, got.RawTags)
	require.Nil(t, got.AcoustIDFingerprint)
	require.Nil(t, got.LastScanMtime)
	require.Nil(t, got.IntroMeta)
}
