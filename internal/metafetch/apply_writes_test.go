// file: internal/metafetch/apply_writes_test.go
// version: 1.4.0
// guid: 5d095e77-781b-4acb-8d3f-c564f5f88f77
// last-edited: 2026-09-12
//
// Pins that a metadata apply writes every selected field, never a deselected
// one, records provenance for every field it writes, downloads the new cover,
// and writes the audio tags exactly once -- on the manual, batch and auto-fetch
// paths alike.

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// fullMeta sets every BookMetadata member an apply can write.
func fullMeta() metadata.BookMetadata {
	abridged := true
	return metadata.BookMetadata{
		Title: "New Title", Author: "New Author", Narrator: "New Narrator",
		Description: "A description", Publisher: "New Pub", PublishYear: 2020,
		ISBN: "9780000000002", ISBN10: "0000000002", ISBN13: "9780000000002",
		ASIN: "B00TEST123", CoverURL: "https://covers.example.test/c.jpg",
		Language: "en", Genre: "Fantasy", Series: "The Series", SeriesPosition: "3",
		DurationSec: 36000, Abridged: &abridged, Subtitle: "A Subtitle", PageCount: 320,
		SeriesSecondary: "Other Series", SeriesSecondaryPosition: "2",
		PublishYearIsAudiobookRelease: true,
	}
}

// notApplyFields are BookMetadata members that are deliberately NOT
// user-selectable apply fields, each with the reason. A new BookMetadata member
// fails TestFilterApplyFields_EveryMemberIsControlled until it is classified
// here or given an ApplyFields entry -- which is the drift this list exists to
// stop.
var notApplyFields = map[string]string{
	"PublishYearIsAudiobookRelease": "routing flag for the year, not a value",
	"AudibleRatingOverall":          "rating: never written to the book by an apply",
	"AudibleRatingPerformance":      "rating: never written to the book by an apply",
	"AudibleRatingStory":            "rating: never written to the book by an apply",
	"AudibleRatingCount":            "rating: never written to the book by an apply",
	"AudibleNumReviews":             "rating: never written to the book by an apply",
	"GoogleRatingAverage":           "rating: never written to the book by an apply",
	"GoogleRatingCount":             "rating: never written to the book by an apply",
	"CategoryTags":                  "additive book_tags, applied whatever the selection",
}

func TestFilterApplyFields_EveryMemberIsControlled(t *testing.T) {
	full := fullMeta()
	cleared := FilterApplyFields(full, []string{"__select_nothing__"})
	fv, cv := reflect.ValueOf(full), reflect.ValueOf(cleared)
	for i := 0; i < fv.NumField(); i++ {
		name := fv.Type().Field(i).Name
		if _, exempt := notApplyFields[name]; exempt {
			continue
		}
		if fv.Field(i).IsZero() {
			t.Errorf("fullMeta leaves %s zero, so this test cannot see it", name)
		}
		if !cv.Field(i).IsZero() {
			t.Errorf("BookMetadata.%s survives deselecting every apply field: give it an ApplyFields entry (or add it to notApplyFields with the reason)", name)
		}
	}
}

func TestFilterApplyFields_EmptyListAppliesEverything(t *testing.T) {
	assert.Equal(t, fullMeta(), FilterApplyFields(fullMeta(), nil))
}

// Deselecting one field clears exactly that field's values and nothing else.
func TestFilterApplyFields_OnlyTheDeselectedFieldIsCleared(t *testing.T) {
	all := FetchedProvenance(fullMeta())
	for _, f := range ApplyFields {
		t.Run(f.Key, func(t *testing.T) {
			var keep []string
			for _, k := range ApplyFieldKeys() {
				if k != f.Key {
					keep = append(keep, k)
				}
			}
			own := map[string]any{}
			f.provenance(fullMeta(), own)
			got := FetchedProvenance(FilterApplyFields(fullMeta(), keep))
			for k, v := range all {
				if _, mine := own[k]; mine {
					assert.NotContains(t, got, k, "deselected %q still carries %s", f.Key, k)
				} else {
					assert.Equal(t, v, got[k], "deselecting %q disturbed %s", f.Key, k)
				}
			}
		})
	}
}

// Every apply field records a fetched_value.
func TestFetchedProvenance_EveryApplyFieldRecords(t *testing.T) {
	prov := FetchedProvenance(fullMeta())
	for _, f := range ApplyFields {
		t.Run(f.Key, func(t *testing.T) {
			own := map[string]any{}
			f.provenance(fullMeta(), own)
			require.NotEmpty(t, own, "apply field %q records no fetched_value", f.Key)
			for k, v := range own {
				assert.Equal(t, v, prov[k], "%s", k)
			}
		})
	}
}

// applyHarness runs ApplyMetadataCandidate against a MockStore and captures
// the updated book and the fetched_value rows.
type applyHarness struct {
	mu            sync.Mutex
	book          *database.Book
	updated       *database.Book
	fetched       map[string]string
	seriesCreated int
}

func (h *applyHarness) run(t *testing.T, cand MetadataCandidate, fields []string) *FetchMetadataResponse {
	t.Helper()
	h.fetched = map[string]string{}
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b := *h.book
			return &b, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			c := *b
			h.updated = &c
			return b, nil
		},
		CreateAuthorFunc: func(name string) (*database.Author, error) {
			return &database.Author{ID: 5, Name: name}, nil
		},
		CreateSeriesFunc: func(name string, _ *int) (*database.Series, error) {
			h.seriesCreated++
			return &database.Series{ID: 9, Name: name}, nil
		},
		UpsertMetadataFieldStateFunc: func(s *database.MetadataFieldState) error {
			if s.FetchedValue != nil {
				h.fetched[s.Field] = *s.FetchedValue
			}
			return nil
		},
	}
	resp, err := NewService(mock).ApplyMetadataCandidate(h.book.ID, cand, fields)
	require.NoError(t, err)
	require.NotNil(t, h.updated, "UpdateBook was not called")
	return resp
}

func candidateFrom(m metadata.BookMetadata) MetadataCandidate {
	return MetadataCandidate{
		Title: m.Title, Author: m.Author, Narrator: m.Narrator, Series: m.Series,
		SeriesPosition: m.SeriesPosition, Year: m.PublishYear, Publisher: m.Publisher,
		ISBN: m.ISBN, ISBN10: m.ISBN10, ISBN13: m.ISBN13, ASIN: m.ASIN, Genre: m.Genre,
		CoverURL: m.CoverURL, Description: m.Description, Language: m.Language,
		Abridged: m.Abridged, Subtitle: m.Subtitle, PageCount: m.PageCount,
		SeriesSecondary: m.SeriesSecondary, SeriesSecondaryPosition: m.SeriesSecondaryPosition,
		DurationSec: m.DurationSec, Source: "Audible",
	}
}

// The candidate carried ASIN but the apply never mapped it, and it carried no
// Genre at all; a selected field lands, a deselected one is untouched.
func TestApplyMetadataCandidate_WritesASINAndGenre_DeselectedUntouched(t *testing.T) {
	oldPub := "Old Pub"
	h := &applyHarness{book: &database.Book{ID: "b1", Title: "Old", Publisher: &oldPub}}
	cand := MetadataCandidate{Title: "New Title", ASIN: "B00TEST123", Genre: "Fantasy", Publisher: "New Pub", Source: "Audible"}

	h.run(t, cand, []string{"title", "asin", "genre"})

	require.NotNil(t, h.updated.ASIN)
	assert.Equal(t, "B00TEST123", *h.updated.ASIN)
	require.NotNil(t, h.updated.Genre)
	assert.Equal(t, "Fantasy", *h.updated.Genre)
	require.NotNil(t, h.updated.Publisher)
	assert.Equal(t, "Old Pub", *h.updated.Publisher, "deselected publisher was written")

	// A batch apply (nil fields) writes them too.
	h.updated = nil
	h.run(t, cand, nil)
	assert.Equal(t, "B00TEST123", *h.updated.ASIN)
	assert.Equal(t, "Fantasy", *h.updated.Genre)
}

// Deselected ISBN writes neither ISBN column; deselected subtitle/abridged/
// page count/runtime/secondary series are untouched.
func TestApplyMetadataCandidate_DeselectedSignalFieldsUntouched(t *testing.T) {
	h := &applyHarness{book: &database.Book{ID: "b1", Title: "Old"}}
	h.run(t, candidateFrom(fullMeta()), []string{"title"})
	u := h.updated
	assert.Nil(t, u.ISBN10, "isbn deselected but ISBN10 written")
	assert.Nil(t, u.ISBN13, "isbn deselected but ISBN13 written")
	assert.Nil(t, u.Subtitle)
	assert.Nil(t, u.Abridged)
	assert.Nil(t, u.PageCount)
	assert.Nil(t, u.AudibleRuntimeMin)
	assert.Nil(t, u.SeriesSecondary)
	assert.Nil(t, u.ASIN)
	assert.Nil(t, u.Genre)
	assert.Equal(t, "New Title", u.Title)
}

func TestApplyMetadataCandidate_SeriesPositionHonored(t *testing.T) {
	seriesID := 7
	cand := MetadataCandidate{Title: "Old", Series: "The Series", SeriesPosition: "4", Source: "Audible"}

	t.Run("series_position alone updates the existing series' position", func(t *testing.T) {
		h := &applyHarness{book: &database.Book{ID: "b1", Title: "Old", SeriesID: &seriesID}}
		h.run(t, cand, []string{"series_position"})
		require.NotNil(t, h.updated.SeriesSequence, "series_position selected but not written")
		assert.Equal(t, 4, *h.updated.SeriesSequence)
		require.NotNil(t, h.updated.SeriesID)
		assert.Equal(t, 7, *h.updated.SeriesID, "series was deselected but repointed")
		assert.Zero(t, h.seriesCreated)
		assert.Contains(t, h.fetched, "series_position")
	})

	t.Run("series without series_position leaves the position alone", func(t *testing.T) {
		h := &applyHarness{book: &database.Book{ID: "b1", Title: "Old"}}
		h.run(t, cand, []string{"series"})
		require.NotNil(t, h.updated.SeriesID)
		assert.Nil(t, h.updated.SeriesSequence, "series_position deselected but written")
	})

	t.Run("both", func(t *testing.T) {
		h := &applyHarness{book: &database.Book{ID: "b1", Title: "Old"}}
		h.run(t, cand, []string{"series", "series_position"})
		require.NotNil(t, h.updated.SeriesSequence)
		assert.Equal(t, 4, *h.updated.SeriesSequence)
	})
}

// columnProvenance maps each Book column an apply writes to the fetched_value
// row that must accompany it.
var columnProvenance = map[string]string{
	"Title": "title", "AuthorID": "author_name", "Narrator": "narrator",
	"Publisher": "publisher", "Language": "language",
	"AudiobookReleaseYear": "audiobook_release_year", "PrintYear": "print_year",
	"ISBN10": "isbn10", "ISBN13": "isbn13", "ASIN": "asin",
	"Description": "description", "Genre": "genre", "Abridged": "abridged",
	"Subtitle": "subtitle", "PageCount": "page_count",
	"SeriesSecondary": "series_secondary", "SeriesSecondaryPosition": "series_secondary_position",
	"AudibleRuntimeMin": "audible_runtime_min", "SeriesID": "series_name",
	"SeriesSequence": "series_position", "SeriesPositionRaw": "series_position",
	"CoverURL": "cover_url",
}

// bookkeepingColumns change on every apply but are not provider values.
var bookkeepingColumns = map[string]bool{
	"MetadataReviewStatus": true, "MetadataSource": true, "MetadataSourceHash": true,
	"VersionNotes": true,
}

// End to end: every Book column the apply changed has a fetched_value row. The
// old hand list recorded 8 fields; this walks the real Book struct, so a
// column that starts being written without provenance fails here.
func TestApplyMetadataCandidate_ProvenanceForEveryWrittenField(t *testing.T) {
	before := database.Book{ID: "b1", Title: "Old"}
	h := &applyHarness{book: &before}
	h.run(t, candidateFrom(fullMeta()), nil)

	bv, av := reflect.ValueOf(before), reflect.ValueOf(*h.updated)
	changed := 0
	for i := 0; i < bv.NumField(); i++ {
		name := bv.Type().Field(i).Name
		if reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) || bookkeepingColumns[name] {
			continue
		}
		changed++
		key, ok := columnProvenance[name]
		if !ok {
			t.Errorf("apply wrote Book.%s but no provenance key is mapped for it", name)
			continue
		}
		assert.Contains(t, h.fetched, key, "apply wrote Book.%s with no fetched_value row %q", name, key)
	}
	assert.Greater(t, changed, 15, "the full candidate should change most columns")

	// And every ApplyField's rows reached the store, JSON-encoded.
	for k, v := range FetchedProvenance(fullMeta()) {
		want, err := json.Marshal(v)
		require.NoError(t, err)
		assert.Equal(t, string(want), h.fetched[k], "fetched_value row %s", k)
	}
}

// fileWorkHarness is a store with one book and one file for the file-side core.
func fileWorkHarness(t *testing.T, rootDir string, autoRename, autoTags bool, filesErr error) (*Service, *[]string, *database.Book) {
	t.Helper()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.RootDir = rootDir
	config.AppConfig.AutoRenameOnApply = autoRename
	config.AppConfig.AutoWriteTagsOnApply = autoTags

	book := &database.Book{ID: "b1", Title: "A Book", FilePath: "/lib/a/a.m4b"}
	var mu sync.Mutex
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			mu.Lock()
			defer mu.Unlock()
			b := *book
			return &b, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			mu.Lock()
			defer mu.Unlock()
			*book = *b
			return b, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			if filesErr != nil {
				return nil, filesErr
			}
			return []database.BookFile{{ID: "f1", BookID: bookID, FilePath: "/lib/a/a.m4b"}}, nil
		},
	}
	svc := NewService(mock)
	var calls []string
	svc.tagWriter = func(id string) (int, error) {
		calls = append(calls, "tags:"+id)
		return 1, nil
	}
	svc.coverDownload = func(coverURL, destDir, bookID string) (string, error) {
		calls = append(calls, "cover:"+coverURL)
		return filepath.Join(destDir, "covers", bookID+".jpg"), nil
	}
	return svc, &calls, book
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// The write-once guard. Every apply caller used to follow ApplyMetadataFileIO
// (which tags under auto_write_tags_on_apply) with its own write-back.
func TestFinishApplyFileWork_WritesTagsOnce(t *testing.T) {
	tests := []struct {
		autoTags, fileIO, writeTags bool
		want                        int
	}{
		{autoTags: true, fileIO: true, writeTags: true, want: 1},
		{autoTags: true, fileIO: true, writeTags: false, want: 1},
		{autoTags: false, fileIO: true, writeTags: true, want: 1},
		{autoTags: false, fileIO: true, writeTags: false, want: 0},
		{autoTags: true, fileIO: false, writeTags: true, want: 1},
		{autoTags: true, fileIO: false, writeTags: false, want: 0},
	}
	for _, tt := range tests {
		svc, calls, _ := fileWorkHarness(t, "", false, tt.autoTags, nil)
		require.NoError(t, svc.FinishApplyFileWork("b1", "", tt.fileIO, tt.writeTags, nil))
		assert.Equal(t, tt.want, countPrefix(*calls, "tags:"),
			"auto_write_tags=%v fileIO=%v writeTags=%v: tag writes", tt.autoTags, tt.fileIO, tt.writeTags)
	}
}

// Tags are still written after a rename failure, and the rename error is the
// one reported (moved here from the batch path's tests with the logic).
func TestFinishApplyFileWork_RenameFailureStillWritesTags(t *testing.T) {
	svc, calls, _ := fileWorkHarness(t, "", true, false, errors.New("list exploded"))
	svc.tagWriter = func(id string) (int, error) {
		*calls = append(*calls, "tags:"+id)
		return 0, errors.New("downstream symptom")
	}
	err := svc.FinishApplyFileWork("b1", "", true, true, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list exploded", "the rename-side fault must win")
	assert.Equal(t, 1, countPrefix(*calls, "tags:"))
}

// The cover is downloaded, before the file work, and the book is repointed.
func TestFinishApplyFileWork_DownloadsCoverFirst(t *testing.T) {
	const cover = "https://covers.example.test/new.jpg"
	svc, calls, book := fileWorkHarness(t, t.TempDir(), false, false, nil)
	require.NoError(t, svc.FinishApplyFileWork("b1", cover, true, true, nil))
	assert.Equal(t, []string{"cover:" + cover, "tags:b1"}, *calls)
	require.NotNil(t, book.CoverURL)
	assert.Equal(t, "/api/v1/covers/local/b1.jpg", *book.CoverURL)
}

// The caller's scan stand-down checkpoint is re-run before each file-writing
// step. Losing the hold stops the sequel at that step: nothing after it runs,
// and the error names the step. The file I/O is observed through the tag write
// the pipeline performs under auto_write_tags_on_apply.
func TestFinishApplyFileWork_StopsWhereStandDownIsLost(t *testing.T) {
	const cover = "https://covers.example.test/new.jpg"
	lost := errors.New("scan stand-down lost")
	tests := []struct {
		name              string
		fileIO, writeTags bool
		failOnCall        int
		wantCalls         []string
		wantStep          string
	}{
		{"cover download", true, true, 1, nil, "the cover download"},
		{"file I/O", true, false, 2, []string{"cover:" + cover}, "the file I/O"},
		{"tag write", false, true, 2, []string{"cover:" + cover}, "the tag write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, calls, _ := fileWorkHarness(t, t.TempDir(), false, true, nil)
			n := 0
			checkpoint := func() error {
				n++
				if n >= tt.failOnCall {
					return lost
				}
				return nil
			}
			err := svc.FinishApplyFileWork("b1", cover, tt.fileIO, tt.writeTags, checkpoint)
			require.ErrorIs(t, err, lost)
			assert.Contains(t, err.Error(), "stopped before "+tt.wantStep)
			assert.Equal(t, tt.wantCalls, *calls, "a step ran after the stand-down was lost")
		})
	}
}

// Auto-fetch writes tags only under write_back_metadata (off by default), and
// auto_write_tags_on_apply -- a setting for explicit applies, on by default --
// has no say. Routing auto-fetch through the apply pipeline once made the
// Fetch button retag (and rename) a library book under default config.
func TestFetchMetadataForBook_WritesTagsThroughSharedPath(t *testing.T) {
	tests := []struct {
		writeBackMetadata, autoTags bool
		want                        int
	}{
		{writeBackMetadata: false, autoTags: true, want: 0},
		{writeBackMetadata: true, autoTags: true, want: 1},
		{writeBackMetadata: true, autoTags: false, want: 1},
		{writeBackMetadata: false, autoTags: false, want: 0},
	}
	for _, tt := range tests {
		// Under root_dir: auto-fetch touches files only for a book that already
		// has a library copy there.
		svc, calls, _ := fileWorkHarness(t, "/lib", false, tt.autoTags, nil)
		config.AppConfig.WriteBackMetadata = tt.writeBackMetadata
		var scheduled []string
		svc.fileWorkScheduler = func(bookID string, work func()) {
			scheduled = append(scheduled, bookID)
			work()
		}
		svc.overrideSources = []metadata.MetadataSource{fakeSource{
			name:    "Audible",
			results: []metadata.BookMetadata{{Title: "A Book", Author: "Some Author", Genre: "Fantasy"}},
		}}
		_, err := svc.FetchMetadataForBook(context.Background(), "b1")
		require.NoError(t, err)
		assert.Equal(t, []string{"b1"}, scheduled, "the file work goes through the scheduler (the pool)")
		assert.Equal(t, tt.want, countPrefix(*calls, "tags:"),
			"write_back_metadata=%v auto_write_tags=%v: tag writes", tt.writeBackMetadata, tt.autoTags)
	}
}

// The web apply dialogs render the fields from ONE shared list; it must name
// exactly ApplyFields, in the same order.
func TestApplyFieldsMatchWebList(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "config", "metadataApplyFields.ts"))
	require.NoError(t, err)
	block := regexp.MustCompile(`METADATA_APPLY_FIELDS = \[([\s\S]*?)\] as const`).FindSubmatch(src)
	require.NotNil(t, block, "METADATA_APPLY_FIELDS not found in the web list")
	var web []string
	for _, m := range regexp.MustCompile(`'([a-z0-9_]+)'`).FindAllSubmatch(block[1], -1) {
		web = append(web, string(m[1]))
	}
	assert.Equal(t, ApplyFieldKeys(), web)
}

func TestPathUnderRoot(t *testing.T) {
	tests := []struct {
		p, root string
		want    bool
	}{
		{"/library/a/b.m4b", "/library", true},
		{"/library", "/library", true},
		{"/library/a", "/library/", true},
		{"/library-old/a.m4b", "/library", false},
		{"/librarything", "/library", false},
		{"", "/library", false},
		{"/library/a", "", false},
		{"/x/y", "/", true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, pathUnderRoot(tt.p, tt.root), "pathUnderRoot(%q, %q)", tt.p, tt.root)
	}
}

// protectedSetup makes a library root and an iTunes tree that isProtectedPath
// flags, both real directories under one temp dir.
func protectedSetup(t *testing.T) (root, itunes string) {
	t.Helper()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	base := t.TempDir()
	root, itunes = filepath.Join(base, "library"), filepath.Join(base, "itunes")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.MkdirAll(itunes, 0o755))
	config.AppConfig.RootDir = root
	config.AppConfig.ITunes.LibraryReadPath = filepath.Join(itunes, "iTunes Library.xml")
	config.AppConfig.ITunes.LibraryWritePath = ""
	return root, itunes
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// A version sibling under root_dir is not "the library copy" while one of its
// file rows still points into the iTunes tree.
func TestExistingLibraryCopy_RejectsSiblingWithProtectedFileRow(t *testing.T) {
	root, itunes := protectedSetup(t)
	vg := "vg1"
	book := &database.Book{ID: "orig", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg}
	sib := database.Book{ID: "lib1", FilePath: filepath.Join(root, "A", "a"), VersionGroupID: &vg}
	rows := map[string][]database.BookFile{
		"lib1": {{ID: "f1", BookID: "lib1", FilePath: filepath.Join(root, "A", "a", "01.m4b")}},
	}
	svc := NewService(&database.MockStore{
		GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{*book, sib}, nil },
		GetBookFilesFunc:           func(id string) ([]database.BookFile, error) { return rows[id], nil },
	})

	got, ok := svc.existingLibraryCopy(book)
	require.True(t, ok)
	require.NotNil(t, got)
	assert.Equal(t, "lib1", got.ID, "a clean sibling is the library copy")

	rows["lib1"] = append(rows["lib1"], database.BookFile{ID: "f2", BookID: "lib1", FilePath: filepath.Join(itunes, "Music", "02.m4b")})
	got, ok = svc.existingLibraryCopy(book)
	assert.False(t, ok)
	assert.Nil(t, got, "a sibling with an iTunes file row is not a library copy")
	assert.Nil(t, svc.libraryCopyFor(book, existingCopyOnly))
	assert.False(t, svc.autoFetchHasLibraryCopy(book))
}

// The rename leg drops every entry whose source is protected. The library
// file is renamed (so the pipeline did run); the iTunes file is never moved.
func TestRunApplyPipeline_NeverMovesAProtectedFileRow(t *testing.T) {
	root, itunes := protectedSetup(t)
	config.AppConfig.AutoRenameOnApply = true
	config.AppConfig.AutoWriteTagsOnApply = false
	bookDir := filepath.Join(root, "Old Place")
	libFile := filepath.Join(bookDir, "01.m4b")
	itFile := filepath.Join(itunes, "Music", "02.m4b")
	writeFile(t, libFile, "library audio")
	writeFile(t, itFile, "itunes audio")

	book := &database.Book{ID: "lib1", Title: "A Book", FilePath: bookDir}
	var mu sync.Mutex
	files := []database.BookFile{
		{ID: "f1", BookID: "lib1", FilePath: libFile},
		{ID: "f2", BookID: "lib1", FilePath: itFile},
	}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { b := *book; return &b, nil },
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]database.BookFile(nil), files...), nil
		},
	})
	_, err := svc.runApplyPipeline("lib1", book, createLibraryCopy)

	_, itErr := os.Stat(itFile)
	require.NoError(t, itErr, "the iTunes file must stay where it is (pipeline err: %v)", err)
	got, _ := os.ReadFile(itFile)
	assert.Equal(t, "itunes audio", string(got))
	_, libErr := os.Stat(libFile)
	assert.True(t, os.IsNotExist(libErr), "the library file should have been renamed, proving the rename ran (pipeline err: %v)", err)
}

// Auto-fetch never creates a library copy. A protected book with none gets no
// file work; the same book with a clean library sibling gets it, once.
func TestFinishAutoFetchFileWork_NeverCreatesALibraryCopy(t *testing.T) {
	root, itunes := protectedSetup(t)
	config.AppConfig.AutoRenameOnApply = false
	config.AppConfig.AutoWriteTagsOnApply = true
	vg := "vg1"
	book := &database.Book{ID: "b1", Title: "A Book", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg}
	var siblings []database.Book
	svc := NewService(&database.MockStore{
		GetBookByIDFunc:            func(string) (*database.Book, error) { b := *book; return &b, nil },
		GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return siblings, nil },
		GetBookFilesFunc: func(id string) ([]database.BookFile, error) {
			return []database.BookFile{{ID: "f-" + id, BookID: id, FilePath: filepath.Join(root, "A Book", "a.m4b")}}, nil
		},
		CreateBookFunc: func(*database.Book) (*database.Book, error) {
			t.Error("auto-fetch created a book row (a library copy)")
			return nil, errors.New("refused")
		},
	})
	var tags []string
	svc.tagWriter = func(id string) (int, error) { tags = append(tags, id); return 1, nil }

	require.NoError(t, svc.FinishAutoFetchFileWork("b1", "", true))
	assert.Empty(t, tags, "no library copy: auto-fetch must not touch the files")

	siblings = []database.Book{*book, {ID: "lib1", Title: "A Book", FilePath: filepath.Join(root, "A Book"), VersionGroupID: &vg}}
	require.NoError(t, svc.FinishAutoFetchFileWork("b1", "", true))
	// The tag write is asked for b1; writeBackForBook resolves the copy.
	assert.Equal(t, []string{"b1"}, tags, "an existing library copy gets the file work, tagged once")
}

// With no pool wired (organize's per-call service) auto-fetch touches no file.
func TestFetchMetadataForBook_NoPoolTouchesNoFiles(t *testing.T) {
	svc, calls, _ := fileWorkHarness(t, "/lib", true, true, nil)
	config.AppConfig.WriteBackMetadata = true
	svc.overrideSources = []metadata.MetadataSource{fakeSource{
		name:    "Audible",
		results: []metadata.BookMetadata{{Title: "A Book", Author: "Some Author"}},
	}}
	_, err := svc.FetchMetadataForBook(context.Background(), "b1")
	require.NoError(t, err)
	assert.Zero(t, countPrefix(*calls, "tags:"))
}

// Auto-fetch writes a series position only together with a series name.
func TestFetchMetadataForBook_PositionNeedsASeriesName(t *testing.T) {
	for _, tt := range []struct {
		series  string
		wantPos bool
	}{{"", false}, {"The Saga", true}} {
		svc, _, book := fileWorkHarness(t, "", false, false, nil)
		existing := 7
		book.SeriesID = &existing
		ms := svc.db.(*database.MockStore)
		ms.GetSeriesByNameFunc = func(string, *int) (*database.Series, error) { return &database.Series{ID: 9, Name: tt.series}, nil }
		svc.overrideSources = []metadata.MetadataSource{fakeSource{
			name:    "Audible",
			results: []metadata.BookMetadata{{Title: "A Book", Author: "Some Author", Series: tt.series, SeriesPosition: "3"}},
		}}
		_, err := svc.FetchMetadataForBook(context.Background(), "b1")
		require.NoError(t, err)
		if tt.wantPos {
			require.NotNil(t, book.SeriesSequence, "series %q", tt.series)
			assert.Equal(t, 3, *book.SeriesSequence)
		} else {
			assert.Nil(t, book.SeriesSequence, "a position with no series name was pinned onto series %d", existing)
		}
	}
}

// A failed cover download leaves the previous (renderable) cover, never the
// provider's remote url, which the UI cannot display.
func TestFetchMetadataForBook_FailedCoverDownloadKeepsPreviousCover(t *testing.T) {
	svc, _, book := fileWorkHarness(t, t.TempDir(), false, false, nil)
	prev := "/api/v1/covers/local/b1.jpg"
	book.CoverURL = &prev
	svc.coverDownload = func(string, string, string) (string, error) { return "", errors.New("provider 503") }
	svc.overrideSources = []metadata.MetadataSource{fakeSource{
		name:    "Audible",
		results: []metadata.BookMetadata{{Title: "A Book", Author: "Some Author", CoverURL: "https://covers.example.test/new.jpg"}},
	}}
	_, err := svc.FetchMetadataForBook(context.Background(), "b1")
	require.NoError(t, err)
	require.NotNil(t, book.CoverURL)
	assert.Equal(t, prev, *book.CoverURL)
}

// testPathLocks is a keyed mutex standing in for the server's pathLocks, which
// metafetch cannot import. It records every key taken.
type testPathLocks struct {
	mu   sync.Mutex
	m    map[string]*sync.Mutex
	keys []string
}

func (l *testPathLocks) lock(p string) func() {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*sync.Mutex{}
	}
	m, ok := l.m[p]
	if !ok {
		m = &sync.Mutex{}
		l.m[p] = m
	}
	l.keys = append(l.keys, p)
	l.mu.Unlock()
	m.Lock()
	var once sync.Once
	return func() { once.Do(m.Unlock) }
}

func (l *testPathLocks) taken() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.keys...)
}

// An auto-fetch of book A, whose files live in its library copy B, and a
// manual apply of B write the same files, so they must serialize on B's path.
// Auto-fetch used to lock A's protected path (which nothing writes) around the
// whole job while the pipeline wrote B's files, so the two ran at once. Both
// tag-write sites are covered: the pipeline's (auto_write_tags_on_apply) and
// the sequel's own.
func TestFileWork_AutoFetchAndManualApplySerializeOnLibraryCopyPath(t *testing.T) {
	for _, autoTags := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto_write_tags=%v", autoTags), func(t *testing.T) {
			root, itunes := protectedSetup(t)
			config.AppConfig.AutoRenameOnApply = false
			config.AppConfig.AutoWriteTagsOnApply = autoTags
			vg := "vg1"
			a := database.Book{ID: "a", Title: "A Book", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg}
			b := database.Book{ID: "b", Title: "A Book", FilePath: filepath.Join(root, "A Book"), VersionGroupID: &vg}
			books := map[string]database.Book{"a": a, "b": b}
			svc := NewService(&database.MockStore{
				GetBookByIDFunc: func(id string) (*database.Book, error) {
					bk, ok := books[id]
					if !ok {
						return nil, nil
					}
					return &bk, nil
				},
				GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{a, b}, nil },
				GetBookFilesFunc: func(id string) ([]database.BookFile, error) {
					return []database.BookFile{{ID: "f-" + id, BookID: id, FilePath: filepath.Join(books[id].FilePath, "01.m4b")}}, nil
				},
			})

			// A's tag write parks inside the writer, holding whatever lock it
			// took. B's job is started only then, and must not reach its own tag
			// write until A is released: both write B's files. Deterministic in
			// the failing direction -- no race between two sleeps decides it.
			entered := make(chan string, 2)
			proceed := make(chan struct{})
			svc.tagWriter = func(id string) (int, error) {
				entered <- id
				<-proceed
				return 1, nil
			}
			locks := &testPathLocks{}
			svc.SetPathLocker(locks.lock)

			var wg sync.WaitGroup
			wg.Go(func() { assert.NoError(t, svc.FinishAutoFetchFileWork("a", "", true)) })
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(proceed)
				t.Fatal("auto-fetch of A never reached its tag write")
			}
			wg.Go(func() { assert.NoError(t, svc.FinishApplyFileWork("b", "", true, true, nil)) })
			select {
			case id := <-entered:
				t.Errorf("the manual apply of B reached its tag write (id %q) while auto-fetch of A was writing B's files", id)
			case <-time.After(200 * time.Millisecond):
			}
			close(proceed)
			wg.Wait()

			keys := locks.taken()
			assert.Contains(t, keys, b.FilePath, "the file work must lock the library copy's path")
			assert.NotContains(t, keys, a.FilePath, "A's protected path is never written; a lock on it guards nothing")

			// A manual apply of A itself writes B's files too (through the rename
			// pipeline when auto_write_tags_on_apply is on), so its path locks
			// must name B's path and never A's.
			svc.tagWriter = func(string) (int, error) { return 1, nil }
			manual := &testPathLocks{}
			svc.SetPathLocker(manual.lock)
			require.NoError(t, svc.FinishApplyFileWork("a", "", true, true, nil))
			assert.Contains(t, manual.taken(), b.FilePath, "a manual apply of A must lock its library copy's path")
			assert.NotContains(t, manual.taken(), a.FilePath, "a manual apply of A must never key a lock on A's protected path")
		})
	}
}

// Auto-fetch never renames, and writes tags only under write_back_metadata --
// with the explicit-apply switches at their shipped defaults (both on). It used
// to run the apply rename pipeline, so the Fetch button moved a library book's
// files to the naming-pattern path and retagged them under default config.
func TestFinishAutoFetchFileWork_NeverRenamesAndTagsOnlyUnderWriteBack(t *testing.T) {
	for _, writeBack := range []bool{false, true} {
		t.Run(fmt.Sprintf("write_back_metadata=%v", writeBack), func(t *testing.T) {
			root, _ := protectedSetup(t)
			config.AppConfig.AutoRenameOnApply = true
			config.AppConfig.AutoWriteTagsOnApply = true
			bookDir := filepath.Join(root, "Old Place")
			libFile := filepath.Join(bookDir, "01.m4b")
			writeFile(t, libFile, "library audio")
			book := &database.Book{ID: "lib1", Title: "A Book", FilePath: bookDir}
			svc := NewService(&database.MockStore{
				GetBookByIDFunc: func(string) (*database.Book, error) { b := *book; return &b, nil },
				GetBookFilesFunc: func(string) ([]database.BookFile, error) {
					return []database.BookFile{{ID: "f1", BookID: "lib1", FilePath: libFile}}, nil
				},
			})
			var tags []string
			svc.tagWriter = func(id string) (int, error) { tags = append(tags, id); return 1, nil }

			require.NoError(t, svc.FinishAutoFetchFileWork("lib1", "", writeBack))

			got, err := os.ReadFile(libFile)
			require.NoError(t, err, "auto-fetch moved the book's file")
			assert.Equal(t, "library audio", string(got))
			if writeBack {
				assert.Equal(t, []string{"lib1"}, tags, "write_back_metadata on: the tags are written once")
			} else {
				assert.Empty(t, tags, "write_back_metadata off: auto-fetch must not write tags")
			}
		})
	}
}

// Two file-work jobs for the same book run one after the other. The second
// used to start while the first was still writing, keyed on a path it had read
// before the first job's rename moved the files.
func TestFinishApplyFileWork_SameBookJobsSerialize(t *testing.T) {
	svc, _, _ := fileWorkHarness(t, t.TempDir(), false, false, nil)
	entered := make(chan string, 4)
	proceed := make(chan struct{})
	svc.tagWriter = func(id string) (int, error) {
		entered <- "tags"
		<-proceed
		return 1, nil
	}
	svc.coverDownload = func(coverURL, destDir, bookID string) (string, error) {
		entered <- "cover:" + coverURL
		return filepath.Join(destDir, "covers", bookID+".jpg"), nil
	}
	locks := &testPathLocks{}
	svc.SetPathLocker(locks.lock)
	next := func() string {
		select {
		case got := <-entered:
			return got
		case <-time.After(5 * time.Second):
			close(proceed)
			t.Fatal("the first job stalled")
			return ""
		}
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		assert.NoError(t, svc.FinishApplyFileWork("b1", "https://covers.example.test/x.jpg", false, true, nil))
	})
	require.Equal(t, "cover:https://covers.example.test/x.jpg", next())
	require.Equal(t, "tags", next())
	wg.Go(func() {
		assert.NoError(t, svc.FinishApplyFileWork("b1", "https://covers.example.test/y.jpg", false, true, nil))
	})
	select {
	case got := <-entered:
		t.Errorf("a second job on the same book started (%s) while the first was writing its tags", got)
	case <-time.After(200 * time.Millisecond):
	}
	close(proceed)
	wg.Wait()
	assert.Contains(t, locks.taken(), bookLockKey("b1"))
}

// Auto-fetch keeps a cover the book already has; only an explicit apply
// replaces it. Before the apply-writes change auto-fetch used DownloadCoverArt,
// which skips an existing file, and that change had routed it through the
// replace path.
func TestAutoFetchKeepsExistingCover_ApplyReplacesIt(t *testing.T) {
	const cover = "https://covers.example.test/new.jpg"
	root := t.TempDir()
	svc, calls, book := fileWorkHarness(t, root, false, false, nil)
	writeFile(t, filepath.Join(root, "covers", "b1.jpg"), "hand-picked")

	require.NoError(t, svc.FinishAutoFetchFileWork("b1", cover, false))
	assert.Zero(t, countPrefix(*calls, "cover:"), "auto-fetch replaced an existing cover")
	require.NotNil(t, book.CoverURL)
	assert.Equal(t, "/api/v1/covers/local/b1.jpg", *book.CoverURL, "the kept cover is the one served")

	// The no-pool auto-fetch path (organize's per-call service) keeps it too.
	svc.overrideSources = []metadata.MetadataSource{fakeSource{
		name:    "Audible",
		results: []metadata.BookMetadata{{Title: "A Book", Author: "Some Author", CoverURL: cover}},
	}}
	_, err := svc.FetchMetadataForBook(context.Background(), "b1")
	require.NoError(t, err)
	assert.Zero(t, countPrefix(*calls, "cover:"), "the no-pool auto-fetch path replaced an existing cover")

	// An explicit apply replaces it.
	require.NoError(t, svc.FinishApplyFileWork("b1", cover, false, false, nil))
	assert.Equal(t, 1, countPrefix(*calls, "cover:"), "an explicit apply must replace the cover")
}
