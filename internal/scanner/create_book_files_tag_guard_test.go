// file: internal/scanner/create_book_files_tag_guard_test.go
// version: 1.0.0
// guid: 91ef8708-fbbd-4341-9dcd-4b994c0456c9
// last-edited: 2026-09-12

package scanner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// tagGuardExtractor returns a fixed tag reading per file basename; a basename
// with no entry fails the read, as an unreadable file would.
type tagGuardExtractor map[string]metadata.Metadata

func (e tagGuardExtractor) ExtractMetadata(filePath string) (metadata.Metadata, error) {
	m, ok := e[filepath.Base(filePath)]
	if !ok {
		return metadata.Metadata{}, errors.New("unreadable")
	}
	return m, nil
}

// tagAt is a tag reading at a position; disc 0 means no disc tag.
func tagAt(track, trackTotal, disc, discTotal int) metadata.Metadata {
	all := map[string]string{"TRCK": fmt.Sprintf("%d/%d", track, trackTotal)}
	if disc > 0 {
		all["TPOS"] = fmt.Sprintf("%d/%d", disc, discTotal)
	}
	return metadata.Metadata{
		TrackNumber: track, TrackTotal: trackTotal,
		DiscNumber: disc, DiscTotal: discTotal,
		AllTags: all,
	}
}

type wantPos struct{ track, trackCount, disc, discCount int }

// TestCreateBookFilesForBook_TagTrackGuard pins that the scanner judges tag
// track/disc numbers per BOOK at import, with the same guard as
// maintenance.tag-backfill, instead of copying each file's tag onto its row.
// Files are listed in segment order; on refusal the positional order (1..n in
// that order) must survive with no tag disc/count numbers.
func TestCreateBookFilesForBook_TagTrackGuard(t *testing.T) {
	cases := []struct {
		name  string
		files []string                     // segment order
		tags  map[string]metadata.Metadata // by basename; absent = unreadable
		want  map[string]wantPos           // by basename
	}{
		{
			name:  "every file tagged track 1 keeps positional order",
			files: []string{"01.mp3", "02.mp3", "03.mp3"},
			tags: map[string]metadata.Metadata{
				"01.mp3": tagAt(1, 1, 0, 0), "02.mp3": tagAt(1, 1, 0, 0), "03.mp3": tagAt(1, 1, 0, 0),
			},
			want: map[string]wantPos{"01.mp3": {1, 0, 0, 0}, "02.mp3": {2, 0, 0, 0}, "03.mp3": {3, 0, 0, 0}},
		},
		{
			name:  "distinct tag tracks are taken",
			files: []string{"a.mp3", "b.mp3", "c.mp3"},
			tags: map[string]metadata.Metadata{
				"a.mp3": tagAt(3, 3, 0, 0), "b.mp3": tagAt(1, 3, 0, 0), "c.mp3": tagAt(2, 3, 0, 0),
			},
			want: map[string]wantPos{"a.mp3": {3, 3, 0, 0}, "b.mp3": {1, 3, 0, 0}, "c.mp3": {2, 3, 0, 0}},
		},
		{
			name:  "multi-disc with track 1 on each disc is taken",
			files: []string{"a.mp3", "b.mp3", "c.mp3", "d.mp3"},
			tags: map[string]metadata.Metadata{
				"a.mp3": tagAt(1, 2, 1, 2), "b.mp3": tagAt(2, 2, 1, 2),
				"c.mp3": tagAt(1, 2, 2, 2), "d.mp3": tagAt(2, 2, 2, 2),
			},
			want: map[string]wantPos{
				"a.mp3": {1, 2, 1, 2}, "b.mp3": {2, 2, 1, 2}, "c.mp3": {1, 2, 2, 2}, "d.mp3": {2, 2, 2, 2},
			},
		},
		{
			name:  "mixed disc tags are refused",
			files: []string{"a.mp3", "b.mp3", "c.mp3", "d.mp3"},
			tags: map[string]metadata.Metadata{
				"a.mp3": tagAt(1, 2, 1, 2), "b.mp3": tagAt(2, 2, 1, 2),
				"c.mp3": tagAt(1, 2, 0, 0), "d.mp3": tagAt(2, 2, 0, 0),
			},
			want: map[string]wantPos{
				"a.mp3": {1, 0, 0, 0}, "b.mp3": {2, 0, 0, 0}, "c.mp3": {3, 0, 0, 0}, "d.mp3": {4, 0, 0, 0},
			},
		},
		{
			name:  "one unreadable file refuses the book",
			files: []string{"a.mp3", "b.mp3", "c.mp3"},
			tags: map[string]metadata.Metadata{
				"a.mp3": tagAt(3, 3, 0, 0), "c.mp3": tagAt(1, 3, 0, 0),
			},
			want: map[string]wantPos{"a.mp3": {1, 0, 0, 0}, "b.mp3": {2, 0, 0, 0}, "c.mp3": {3, 0, 0, 0}},
		},
		{
			name:  "single-file book takes its tag track",
			files: []string{"book.m4b"},
			tags:  map[string]metadata.Metadata{"book.m4b": tagAt(5, 9, 1, 1)},
			want:  map[string]wantPos{"book.m4b": {5, 9, 1, 1}},
		},
		{
			name:  "single-file book without a track tag stays track 1",
			files: []string{"book.m4b"},
			tags:  map[string]metadata.Metadata{"book.m4b": {Title: "Book"}},
			want:  map[string]wantPos{"book.m4b": {1, 0, 0, 0}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupPebbleStore(t)
			defer cleanup()
			SetStore(store)
			defer SetStore(nil)
			metadata.SetMetadataExtractor(tagGuardExtractor(tc.tags))
			defer metadata.SetMetadataExtractor(nil)

			dir := t.TempDir()
			segs := make([]string, len(tc.files))
			for i, name := range tc.files {
				segs[i] = filepath.Join(dir, name)
				if err := os.WriteFile(segs[i], []byte("audio "+name), 0o644); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}
			book, err := store.CreateBook(&database.Book{FilePath: dir, Title: "Guarded Book"})
			if err != nil {
				t.Fatalf("CreateBook: %v", err)
			}

			createBookFilesForBook(dir, segs, logger.New("test"), normalizeToDirectory)

			rows, err := store.GetBookFiles(book.ID)
			if err != nil {
				t.Fatalf("GetBookFiles: %v", err)
			}
			if len(rows) != len(tc.want) {
				t.Fatalf("got %d book_file rows, want %d", len(rows), len(tc.want))
			}
			for _, r := range rows {
				name := filepath.Base(r.FilePath)
				w, ok := tc.want[name]
				if !ok {
					t.Fatalf("unexpected row for %s", name)
				}
				got := wantPos{r.TrackNumber, r.TrackCount, r.DiscNumber, r.DiscCount}
				if got != w {
					t.Errorf("%s: track %d/%d disc %d/%d, want track %d/%d disc %d/%d", name,
						got.track, got.trackCount, got.disc, got.discCount,
						w.track, w.trackCount, w.disc, w.discCount)
				}
				if m, readable := tc.tags[name]; readable && len(m.AllTags) > 0 && len(r.RawTags) == 0 {
					t.Errorf("%s: RawTags not captured", name)
				}
			}
		})
	}
}
