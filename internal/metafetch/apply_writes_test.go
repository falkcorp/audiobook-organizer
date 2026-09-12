// file: internal/metafetch/apply_writes_test.go
// version: 1.0.0
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
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

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
		require.NoError(t, svc.FinishApplyFileWork("b1", "", tt.fileIO, tt.writeTags))
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
	err := svc.FinishApplyFileWork("b1", "", true, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list exploded", "the rename-side fault must win")
	assert.Equal(t, 1, countPrefix(*calls, "tags:"))
}

// The cover is downloaded, before the file work, and the book is repointed.
func TestFinishApplyFileWork_DownloadsCoverFirst(t *testing.T) {
	const cover = "https://covers.example.test/new.jpg"
	svc, calls, book := fileWorkHarness(t, t.TempDir(), false, false, nil)
	require.NoError(t, svc.FinishApplyFileWork("b1", cover, true, true))
	assert.Equal(t, []string{"cover:" + cover, "tags:b1"}, *calls)
	require.NotNil(t, book.CoverURL)
	assert.Equal(t, "/api/v1/covers/local/b1.jpg", *book.CoverURL)
}

// Auto-fetch writes tags through the shared core. It used to write them only
// under write_back_metadata (off in production), so the DB took the fetched
// metadata while the files kept the old tags.
func TestFetchMetadataForBook_WritesTagsThroughSharedPath(t *testing.T) {
	tests := []struct {
		writeBackMetadata, autoTags bool
		want                        int
	}{
		{writeBackMetadata: false, autoTags: true, want: 1},
		{writeBackMetadata: true, autoTags: true, want: 1},
		{writeBackMetadata: true, autoTags: false, want: 1},
		{writeBackMetadata: false, autoTags: false, want: 0},
	}
	for _, tt := range tests {
		svc, calls, _ := fileWorkHarness(t, "", false, tt.autoTags, nil)
		config.AppConfig.WriteBackMetadata = tt.writeBackMetadata
		svc.overrideSources = []metadata.MetadataSource{fakeSource{
			name:    "Audible",
			results: []metadata.BookMetadata{{Title: "A Book", Author: "Some Author", Genre: "Fantasy"}},
		}}
		_, err := svc.FetchMetadataForBook(context.Background(), "b1")
		require.NoError(t, err)
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
