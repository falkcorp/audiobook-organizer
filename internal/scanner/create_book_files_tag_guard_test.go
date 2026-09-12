// file: internal/scanner/create_book_files_tag_guard_test.go
// version: 1.1.0
// guid: 91ef8708-fbbd-4341-9dcd-4b994c0456c9
// last-edited: 2026-09-12

package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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

// TestApplyTagPositionsIfTrusted_RepeatedPath pins how the guard treats a
// segment list that repeats a path (album groups are concatenated without
// dedup), so each row carries its own position.
func TestApplyTagPositionsIfTrusted_RepeatedPath(t *testing.T) {
	newRows := func() []*database.BookFile {
		return []*database.BookFile{
			{ID: "r1", FilePath: "/lib/book/a.mp3", TrackNumber: 1},
			{ID: "r2", FilePath: "/lib/book/b.mp3", TrackNumber: 2},
			{ID: "r3", FilePath: "/lib/book/a.mp3", TrackNumber: 3},
		}
	}

	t.Run("same tag position twice is refused", func(t *testing.T) {
		bfs := newRows()
		placements := []metadata.TagPlacement{
			{Track: 2, TrackTotal: 2}, {Track: 1, TrackTotal: 2}, {Track: 2, TrackTotal: 2},
		}
		applyTagPositionsIfTrusted(bfs, placements, "/lib/book", logger.New("test"))
		for i, bf := range bfs {
			if bf.TrackNumber != i+1 || bf.TrackCount != 0 || bf.DiscNumber != 0 {
				t.Errorf("row %s: track %d/%d disc %d, want positional track %d with no tag numbers",
					bf.ID, bf.TrackNumber, bf.TrackCount, bf.DiscNumber, i+1)
			}
		}
	})

	// Rows are keyed by ID, so an accepted book moves every row to its OWN
	// placement. Keyed by path, r1 and r3 would share one map entry and both
	// land on the placement stored last.
	t.Run("each row takes its own placement", func(t *testing.T) {
		bfs := newRows()
		placements := []metadata.TagPlacement{
			{Track: 2, TrackTotal: 3}, {Track: 1, TrackTotal: 3}, {Track: 3, TrackTotal: 3},
		}
		applyTagPositionsIfTrusted(bfs, placements, "/lib/book", logger.New("test"))
		for i, bf := range bfs {
			if bf.TrackNumber != placements[i].Track || bf.TrackCount != 3 {
				t.Errorf("row %s: track %d/%d, want %d/3", bf.ID, bf.TrackNumber, bf.TrackCount, placements[i].Track)
			}
		}
	})
}

// chapterFiles returns "Chapter 1.mp3" .. "Chapter n.mp3".
func chapterFiles(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "Chapter " + strconv.Itoa(i+1) + ".mp3"
	}
	return out
}

// TestCreateBookFilesForBook_RefusedBookFallsBackToNaturalOrder pins the
// positional fallback on the directory-read path (segmentFiles nil), where the
// list comes from os.ReadDir and is therefore alphabetical: Chapter 1, 10, 11,
// 12, 2, ... With one tag read failing the whole book is refused, and the
// positional numbers must still follow the chapter numbers, not the
// alphabet. With every tag readable the tag numbers are taken as before.
func TestCreateBookFilesForBook_RefusedBookFallsBackToNaturalOrder(t *testing.T) {
	oldExts := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	defer func() { config.AppConfig.SupportedExtensions = oldExts }()

	for _, tc := range []struct {
		name       string
		unreadable string
	}{
		{name: "one unreadable file refuses and keeps numeric order", unreadable: "Chapter 5.mp3"},
		{name: "all readable takes the tag tracks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupPebbleStore(t)
			defer cleanup()
			SetStore(store)
			defer SetStore(nil)

			names := chapterFiles(12)
			tags := make(map[string]metadata.Metadata, len(names))
			for i, n := range names {
				if n != tc.unreadable {
					tags[n] = tagAt(i+1, 12, 0, 0)
				}
			}
			metadata.SetMetadataExtractor(tagGuardExtractor(tags))
			defer metadata.SetMetadataExtractor(nil)

			dir := t.TempDir()
			for _, n := range names {
				if err := os.WriteFile(filepath.Join(dir, n), []byte("audio "+n), 0o644); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}
			book, err := store.CreateBook(&database.Book{FilePath: dir, Title: "Chapters"})
			if err != nil {
				t.Fatalf("CreateBook: %v", err)
			}

			createBookFilesForBook(dir, nil, logger.New("test"), normalizeToDirectory)

			rows, err := store.GetBookFiles(book.ID)
			if err != nil {
				t.Fatalf("GetBookFiles: %v", err)
			}
			if len(rows) != 12 {
				t.Fatalf("got %d rows, want 12", len(rows))
			}
			for _, r := range rows {
				name := filepath.Base(r.FilePath)
				n, _ := strconv.Atoi(name[len("Chapter ") : len(name)-len(".mp3")])
				if r.TrackNumber != n {
					t.Errorf("%s: TrackNumber %d, want %d", name, r.TrackNumber, n)
				}
			}
		})
	}
}

// id3AlbumFile is the bytes of a file carrying only an ID3v2.3 TALB frame,
// enough for tag.ReadFrom (quickReadAlbum) to see the album.
func id3AlbumFile(album string) []byte {
	body := append([]byte{0}, album...) // ISO-8859-1 text
	frame := append([]byte("TALB"), byte(len(body)>>24), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)), 0, 0)
	frame = append(frame, body...)
	n := len(frame)
	hdr := []byte{'I', 'D', '3', 3, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	return append(append(hdr, frame...), make([]byte, 64)...)
}

// TestGroupFilesIntoBooks_AlbumGroupSegmentsInNaturalOrder pins the album-tag
// grouping path: 8 of 12 chapter files (Chapter 5..12) share an album tag,
// which is below the multi-file detector's shared-album threshold, so the book
// comes from the album group, whose files arrive alphabetically: 10, 11, 12,
// 5, ... 9. Its SegmentFiles is the positional fallback order and must be
// numeric; FilePath stays the first file in arrival order (Chapter 10), as
// before, because it is the book's lookup key.
func TestGroupFilesIntoBooks_AlbumGroupSegmentsInNaturalOrder(t *testing.T) {
	dir := t.TempDir()
	names := chapterFiles(12)
	tagged := map[string]bool{}
	for i, n := range names {
		content := []byte("untagged " + n)
		if i >= 4 { // Chapter 5..12 carry the album tag
			content = id3AlbumFile("The Book")
			tagged[n] = true
		}
		if err := os.WriteFile(filepath.Join(dir, n), content, 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	// Alphabetical, as os.ReadDir hands them to the scan walk.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, filepath.Join(dir, e.Name()))
	}

	var album *Book
	books := groupFilesIntoBooks(context.Background(), files)
	for i := range books {
		if len(books[i].SegmentFiles) == 8 {
			album = &books[i]
		}
	}
	if album == nil {
		t.Fatalf("no 8-file album-group book; got %d books: %+v", len(books), books)
	}
	for i, p := range album.SegmentFiles {
		if want := "Chapter " + strconv.Itoa(i+5) + ".mp3"; filepath.Base(p) != want {
			t.Fatalf("SegmentFiles[%d] = %s, want %s (full order %v)", i, filepath.Base(p), want, album.SegmentFiles)
		}
	}
	if want := filepath.Join(dir, "Chapter 10.mp3"); album.FilePath != want {
		t.Errorf("FilePath = %s, want %s (first file in arrival order)", album.FilePath, want)
	}
}
