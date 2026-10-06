// file: internal/itunesguard/itunesguard_test.go
// version: 1.0.0
// guid: ccf32e01-0cfc-4d6c-bc5b-fb300898a243
// last-edited: 2026-10-06

package itunesguard

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeStore struct {
	rows map[string][]database.BookFile
	exts map[string][]database.ExternalIDMapping
}

func (s fakeStore) GetBookFiles(id string) ([]database.BookFile, error) { return s.rows[id], nil }
func (s fakeStore) GetExternalIDsForBook(id string) ([]database.ExternalIDMapping, error) {
	return s.exts[id], nil
}

func TestMayWrite(t *testing.T) {
	pid := "0123456789ABCDEF"
	st := fakeStore{
		rows: map[string][]database.BookFile{"row": {{ID: "f1", BookID: "row", FilePath: "/x/a.m4b", ITunesPersistentID: "AA"}}},
		exts: map[string][]database.ExternalIDMapping{
			"ext":  {{Source: "itunes", ExternalID: "BB"}},
			"tomb": {{Source: "itunes", ExternalID: "CC", Tombstoned: true}},
		},
	}
	may := MayWrite(st, "g")
	for name, b := range map[string]*database.Book{
		"book pid":   {ID: "pid", ITunesPersistentID: &pid, FilePath: "/x/p.m4b"},
		"row pid":    {ID: "row", FilePath: "/x/a.m4b"},
		"ext id":     {ID: "ext", FilePath: "/x/e.m4b"},
		"media path": {ID: "media", FilePath: "/x/iTunes Media/Audiobooks/m.m4b"},
	} {
		err := may(b)
		require.True(t, errors.Is(err, ErrITunesMember), "%s: %v", name, err)
	}
	require.NoError(t, may(&database.Book{ID: "tomb", FilePath: "/x/t.m4b"}), "a tombstoned itunes id is no iTunes copy")
	require.NoError(t, may(&database.Book{ID: "plain", FilePath: "/x/plain.m4b"}))
}
