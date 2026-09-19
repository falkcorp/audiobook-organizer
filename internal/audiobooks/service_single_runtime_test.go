// file: internal/audiobooks/service_single_runtime_test.go
// version: 1.0.0
// guid: f6c07ea6-9ca9-424a-aaeb-bcc61e1a445f
// last-edited: 2026-09-19

package audiobooks

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

func rowsOf(n int) []database.BookFile {
	out := make([]database.BookFile, n)
	for i := range out {
		out[i] = database.BookFile{ID: string(rune('a' + i)), BookID: "b1"}
	}
	return out
}

// TestFileProbeBackfill_OnlyForSingleFileBooks: opening a book probes
// Book.FilePath to fill an empty Duration. For a multi-file book that path is
// ONE chapter, and storing its length as the book's runtime is the "only the
// first file" bug; the other media fields are per-stream and still filled.
func TestFileProbeBackfill_OnlyForSingleFileBooks(t *testing.T) {
	for _, c := range []struct {
		name     string
		files    []database.BookFile
		filesErr error
		wantDur  bool
	}{
		{"single file row", rowsOf(1), nil, true},
		{"no file rows", nil, nil, true},
		{"three chapter rows", rowsOf(3), nil, false},
		{"file rows unreadable", nil, errors.New("boom"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubMediaInfo(t, func(string) (*mediainfo.MediaInfo, error) {
				return &mediainfo.MediaInfo{Duration: 1200, Codec: "mp3"}, nil
			})
			for _, call := range []string{"GetAudiobook", "GetAudiobookTags"} {
				rs := &rowStore{t: t, row: database.Book{ID: "b1", Title: "T", FilePath: "/lib/T/01.mp3"}}
				st := rs.store()
				st.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return c.files, c.filesErr }
				svc := NewAudiobookService(st)
				var err error
				if call == "GetAudiobook" {
					_, err = svc.GetAudiobook(context.Background(), "b1")
				} else {
					_, err = svc.GetAudiobookTags(context.Background(), "b1", "", "")
				}
				if err != nil {
					t.Fatal(err)
				}
				gotDur := rs.row.Duration != nil
				if gotDur != c.wantDur {
					t.Fatalf("%s: Duration filled = %v (row %+v), want %v", call, gotDur, rs.row.Duration, c.wantDur)
				}
			}
		})
	}
}
