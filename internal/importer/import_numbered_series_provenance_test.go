// file: internal/importer/import_numbered_series_provenance_test.go
// version: 1.0.1
// guid: 8d4a17c3-6e92-4b05-a1f8-3c70e5b29d64
// last-edited: 2026-11-17

package importer

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// fixedExtractor answers every ExtractMetadata with one Metadata, so the import
// runs on tags this test chose without an audio file that carries them.
type fixedExtractor struct{ m metadata.Metadata }

func (f fixedExtractor) ExtractMetadata(string) (metadata.Metadata, error) { return f.m, nil }

// PR #4698 round 2, B2(c), the importer writer. The tagger's own reading of a
// file is TWO fields that look independent and are one: the series tag
// "Bane 02" and, because the file has no SERIES_INDEX tag, the position read from
// the TITLE's "Part 2" (metadata.DetectVolumeNumber). ImportFile links the book
// into a numbered series row VERBATIM (no strip) when one already exists, and
// stores that position. The result is a book inside "Bane 02" whose own position
// equals the 2 in the series name, written by one reading of one file.
//
// This pins what the writer produces. The series-denumber decision that must not
// take that position as evidence is
// TestSeriesDenumber_Round3_B2c_AnOwnPositionCopiedFromTheNameIsNotEvidence.
func TestImportFile_PartTitleWritesItsNumberIntoTheNumberedSeriesRow(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := pebbleImportStore(t, dir)

	author, err := store.CreateAuthor("Bruce Wayne")
	if err != nil {
		t.Fatalf("CreateAuthor: %v", err)
	}
	numbered, err := store.CreateSeries("Bane 02", &author.ID)
	if err != nil {
		t.Fatalf("CreateSeries: %v", err)
	}

	// The metadata the real tag pipeline builds for these tags, not a hand-set
	// SeriesIndex: the position must come out of the title.
	meta := metadata.BuildMetadataFromTaglibMap(map[string][]string{
		"TITLE":  {"Bane Part 2"},
		"ARTIST": {"Bruce Wayne"},
		"SERIES": {"Bane 02"},
	}, path, nil)
	if meta.Series != "Bane 02" || meta.SeriesIndex != 2 {
		t.Fatalf("tag pipeline gave series %q at %d, want %q at 2 (read from the title)", meta.Series, meta.SeriesIndex, "Bane 02")
	}
	metadata.SetMetadataExtractor(fixedExtractor{m: meta})
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	resp, err := NewImportService(store).ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	book, err := store.GetBookByID(resp.ID)
	if err != nil || book == nil {
		t.Fatalf("GetBookByID: %v (book %v)", err, book)
	}
	if book.SeriesID == nil || *book.SeriesID != numbered.ID {
		t.Fatalf("book series = %v, want the numbered row %d", book.SeriesID, numbered.ID)
	}
	if book.SeriesSequence == nil || *book.SeriesSequence != 2 {
		t.Fatalf("book position = %v, want 2 (the Part number of the title)", book.SeriesSequence)
	}
}

// The narrowing of the same trace: the importer does not CREATE a numbered row.
// A lookup miss is (nil, nil), and only a lookup ERROR creates, so a numbered
// series tag with no numbered row links nothing.
func TestImportFile_DoesNotCreateANumberedSeriesRow(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := pebbleImportStore(t, dir)
	if _, err := store.CreateAuthor("Bruce Wayne"); err != nil {
		t.Fatalf("CreateAuthor: %v", err)
	}
	meta := metadata.BuildMetadataFromTaglibMap(map[string][]string{
		"TITLE": {"Bane Part 2"}, "ARTIST": {"Bruce Wayne"}, "SERIES": {"Bane 02"},
	}, path, nil)
	metadata.SetMetadataExtractor(fixedExtractor{m: meta})
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	resp, err := NewImportService(store).ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	book, err := store.GetBookByID(resp.ID)
	if err != nil || book == nil {
		t.Fatalf("GetBookByID: %v (book %v)", err, book)
	}
	if book.SeriesID != nil {
		t.Errorf("book linked to series %d, want none: the importer links an existing row and does not create one", *book.SeriesID)
	}
	all, err := store.GetAllSeries()
	if err != nil {
		t.Fatalf("GetAllSeries: %v", err)
	}
	for _, s := range all {
		if s.Name == "Bane 02" {
			t.Errorf("the importer created the numbered series %q", s.Name)
		}
	}
}
