// file: internal/audiobooks/revert_repoint_index_test.go
// version: 1.1.0
// guid: 5c2e8a41-7d93-4b6f-9e0a-3f1b6d8c2a74
// last-edited: 2026-10-01

package audiobooks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// repointStub serves repointPathIndexAwayFrom: hidden book "c" holds one row
// at p; books "a" and "b" are live with rows there. modify decides what
// ModifyBookFile finds for each candidate.
type repointStub struct {
	*ledgerStub
	modify   map[string]string // row id -> "gone", "moved", or "" (still at p)
	modified []string
}

const repointTestPath = "/lib/Sword/01.mp3"

func (s *repointStub) GetBookByID(id string) (*database.Book, error) {
	b := &database.Book{ID: id}
	if id == "c" {
		yes, now := true, time.Now()
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
	}
	return b, nil
}

func (s *repointStub) GetBookFiles(id string) ([]database.BookFile, error) {
	if id == "c" {
		return []database.BookFile{{ID: "rc", BookID: "c", FilePath: repointTestPath}}, nil
	}
	return nil, nil
}

func (s *repointStub) BookFilesAtPath(string) ([]database.BookFile, error) {
	return []database.BookFile{
		{ID: "rc", BookID: "c", FilePath: repointTestPath},
		{ID: "ra", BookID: "a", FilePath: repointTestPath},
		{ID: "rb", BookID: "b", FilePath: repointTestPath},
	}, nil
}

func (s *repointStub) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	row := &database.BookFile{ID: fileID, BookID: bookID, FilePath: repointTestPath}
	switch s.modify[fileID] {
	case "gone":
		return nil, nil
	case "moved":
		row.FilePath = "/elsewhere.mp3"
	}
	if err := fn(row); err != nil {
		return nil, err
	}
	s.modified = append(s.modified, fileID)
	return row, nil
}

// TestRepointPathIndexAwayFrom_SkipsGoneAndMovedCandidates: a candidate row
// that vanished or moved off the path is passed over for the next live one;
// when every candidate vanished or moved, no live book holds the path and
// the hidden row keeps the key, which is not an error.
func TestRepointPathIndexAwayFrom_SkipsGoneAndMovedCandidates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		modify   map[string]string
		want     []string
		wantFail bool
	}{
		{"first ok", nil, []string{"ra"}, false},
		{"first gone", map[string]string{"ra": "gone"}, []string{"rb"}, false},
		{"first moved", map[string]string{"ra": "moved"}, []string{"rb"}, false},
		{"all gone or moved", map[string]string{"ra": "gone", "rb": "moved"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &repointStub{ledgerStub: &ledgerStub{}, modify: tc.modify}
			rs := NewRevertService(s)
			err := rs.repointPathIndexAwayFrom(s, "c")
			if tc.wantFail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.want, s.modified)
		})
	}
}
