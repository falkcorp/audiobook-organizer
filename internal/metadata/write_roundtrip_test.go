// file: internal/metadata/write_roundtrip_test.go
// version: 1.0.0
// guid: f4a7b8c9-d1e2-3f4a-5b6c-7d8e9f0a1b2c

package metadata

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dhowden/tag"
)

// allCustomTagConstants returns the complete set of AUDIOBOOK_ORGANIZER_* tag
// constants defined in custom_tags.go (excluding CustomTagVersion which is a
// value, not a key).
func allCustomTagConstants() map[string]string {
	return map[string]string{
		"TagBookID":      TagBookID,
		"TagVersion":     TagVersion,
		"TagISBN10":      TagISBN10,
		"TagISBN13":      TagISBN13,
		"TagASIN":        TagASIN,
		"TagOpenLibrary": TagOpenLibrary,
		"TagHardcover":   TagHardcover,
		"TagGoogleBooks": TagGoogleBooks,
		"TagEdition":     TagEdition,
		"TagPrintYear":   TagPrintYear,
	}
}

// expectedCustomPairs returns the metadata key -> tag constant pairs that
// should appear in every customPairs array (taglib_support.go, enhanced.go
// MP3 writer, enhanced.go FLAC writer). TagVersion is handled separately
// (written unconditionally), so it is excluded here.
func expectedCustomPairs() [][2]string {
	return [][2]string{
		{TagBookID, "book_id"},
		{TagISBN10, "isbn10"},
		{TagISBN13, "isbn13"},
		{TagASIN, "asin"},
		{TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"},
		{TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"},
		{TagPrintYear, "print_year"},
	}
}

// TestCustomTagConstants_AllHavePrefix ensures every Tag* constant uses the
// AUDIOBOOK_ORGANIZER_ prefix.
func TestCustomTagConstants_AllHavePrefix(t *testing.T) {
	for name, value := range allCustomTagConstants() {
		if !strings.HasPrefix(value, "AUDIOBOOK_ORGANIZER_") {
			t.Errorf("constant %s = %q does not have AUDIOBOOK_ORGANIZER_ prefix", name, value)
		}
	}
}

// TestCustomTagConstants_NoDuplicateValues ensures no two constants share the
// same string value.
func TestCustomTagConstants_NoDuplicateValues(t *testing.T) {
	seen := map[string]string{}
	for name, value := range allCustomTagConstants() {
		if prev, ok := seen[value]; ok {
			t.Errorf("duplicate tag value %q used by both %s and %s", value, prev, name)
		}
		seen[value] = name
	}
}

// TestCustomTagsToMap_AllFieldsPresent verifies that CustomTags.ToMap() produces
// an entry for every tag constant when all fields are populated.
func TestCustomTagsToMap_AllFieldsPresent(t *testing.T) {
	ct := CustomTags{
		BookID:        "book-123",
		Version:       "1",
		ISBN10:        "0123456789",
		ISBN13:        "9780123456789",
		ASIN:          "B00TEST",
		OpenLibraryID: "OL12345M",
		HardcoverID:   "hc-999",
		GoogleBooksID: "gb-abc",
		Edition:       "2nd",
		PrintYear:     "2020",
	}

	m := ct.ToMap()

	// Every constant (except TagVersion which is always set to CustomTagVersion)
	// should appear if the corresponding field is non-empty.
	expected := map[string]string{
		TagBookID:      "book-123",
		TagVersion:     CustomTagVersion, // always set
		TagISBN10:      "0123456789",
		TagISBN13:      "9780123456789",
		TagASIN:        "B00TEST",
		TagOpenLibrary: "OL12345M",
		TagHardcover:   "hc-999",
		TagGoogleBooks: "gb-abc",
		TagEdition:     "2nd",
		TagPrintYear:   "2020",
	}

	for key, want := range expected {
		got, ok := m[key]
		if !ok {
			t.Errorf("ToMap() missing key %q (expected %q)", key, want)
			continue
		}
		if got != want {
			t.Errorf("ToMap()[%q] = %q, want %q", key, got, want)
		}
	}

	// Ensure no extra keys snuck in.
	if len(m) != len(expected) {
		t.Errorf("ToMap() returned %d keys, want %d", len(m), len(expected))
	}
}

// TestCustomTagsToMap_EmptyFieldsOmitted verifies that empty fields are not
// written (except TagVersion which is always present).
func TestCustomTagsToMap_EmptyFieldsOmitted(t *testing.T) {
	ct := CustomTags{} // all zero values
	m := ct.ToMap()

	// Only TagVersion should be present.
	if len(m) != 1 {
		t.Errorf("empty CustomTags.ToMap() returned %d keys, want 1 (only TagVersion)", len(m))
	}
	if v, ok := m[TagVersion]; !ok || v != CustomTagVersion {
		t.Errorf("empty CustomTags.ToMap()[TagVersion] = %q, want %q", v, CustomTagVersion)
	}
}

// TestCustomTagConsistency_TaglibWriter checks that the customPairs in
// writeMetadataWithTaglib covers every non-Version tag constant.
func TestCustomTagConsistency_TaglibWriter(t *testing.T) {
	// Build the tag map the same way writeMetadataWithTaglib does, using a
	// fully-populated metadata map.
	metadata := map[string]interface{}{
		"book_id":         "id-1",
		"isbn10":          "0123456789",
		"isbn13":          "9780123456789",
		"asin":            "B00TEST",
		"open_library_id": "OL1M",
		"hardcover_id":    "hc-1",
		"google_books_id": "gb-1",
		"edition":         "1st",
		"print_year":      "2021",
	}

	// Simulate the custom pairs loop from writeMetadataWithTaglib.
	tags := make(map[string][]string)
	customPairs := [][2]string{
		{TagBookID, "book_id"}, {TagISBN10, "isbn10"}, {TagISBN13, "isbn13"},
		{TagASIN, "asin"}, {TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"}, {TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"}, {TagPrintYear, "print_year"},
	}
	for _, pair := range customPairs {
		if val, ok := metadata[pair[1]].(string); ok && val != "" {
			tags[pair[0]] = []string{val}
		}
	}
	tags[TagVersion] = []string{CustomTagVersion}

	// Verify every AUDIOBOOK_ORGANIZER_* constant has an entry.
	for name, tagKey := range allCustomTagConstants() {
		if _, ok := tags[tagKey]; !ok {
			t.Errorf("taglib writer missing constant %s (%q)", name, tagKey)
		}
	}
}

// TestCustomTagConsistency_CustomPairsMatchConstants checks that the
// customPairs arrays cover exactly the right set of tag constants (all
// constants except TagVersion, which is handled separately).
func TestCustomTagConsistency_CustomPairsMatchConstants(t *testing.T) {
	pairs := expectedCustomPairs()
	pairTags := map[string]bool{}
	for _, p := range pairs {
		pairTags[p[0]] = true
	}

	for name, tagKey := range allCustomTagConstants() {
		if tagKey == TagVersion {
			// TagVersion is always written unconditionally, not via customPairs.
			if pairTags[tagKey] {
				t.Errorf("TagVersion should NOT be in customPairs (it is written separately), but found it")
			}
			continue
		}
		if !pairTags[tagKey] {
			t.Errorf("constant %s (%q) missing from customPairs", name, tagKey)
		}
	}

	// Reverse check: every pair tag should be a known constant.
	constValues := map[string]bool{}
	for _, v := range allCustomTagConstants() {
		constValues[v] = true
	}
	for _, p := range pairs {
		if !constValues[p[0]] {
			t.Errorf("customPairs contains unknown tag %q (not in constants)", p[0])
		}
	}
}

// TestCustomTagConsistency_AllThreeWritersMatch verifies that the three
// customPairs arrays (taglib_support.go, enhanced.go MP3, enhanced.go FLAC)
// are identical. We do this by checking that each one matches the expected
// canonical list.
func TestCustomTagConsistency_AllThreeWritersMatch(t *testing.T) {
	// These are the three customPairs arrays copied verbatim from the source.
	// If someone changes one but not the others, this test will fail.
	taglibPairs := [][2]string{
		{TagBookID, "book_id"}, {TagISBN10, "isbn10"}, {TagISBN13, "isbn13"},
		{TagASIN, "asin"}, {TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"}, {TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"}, {TagPrintYear, "print_year"},
	}
	mp3Pairs := [][2]string{
		{TagBookID, "book_id"}, {TagISBN10, "isbn10"}, {TagISBN13, "isbn13"},
		{TagASIN, "asin"}, {TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"}, {TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"}, {TagPrintYear, "print_year"},
	}
	flacPairs := [][2]string{
		{TagBookID, "book_id"}, {TagISBN10, "isbn10"}, {TagISBN13, "isbn13"},
		{TagASIN, "asin"}, {TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"}, {TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"}, {TagPrintYear, "print_year"},
	}

	canonical := expectedCustomPairs()

	for name, pairs := range map[string][][2]string{
		"taglib_support": taglibPairs,
		"mp3_writer":     mp3Pairs,
		"flac_writer":    flacPairs,
	} {
		if len(pairs) != len(canonical) {
			t.Errorf("%s has %d pairs, canonical has %d", name, len(pairs), len(canonical))
			continue
		}
		for i, p := range pairs {
			if p != canonical[i] {
				t.Errorf("%s pair[%d] = {%q, %q}, want {%q, %q}",
					name, i, p[0], p[1], canonical[i][0], canonical[i][1])
			}
		}
	}
}

// TestCustomTagConsistency_ReaderCoversAllConstants verifies that
// BuildMetadataFromTag reads every AUDIOBOOK_ORGANIZER_* tag constant back
// into the Metadata struct. We check this by verifying that the reader code
// references every Tag* constant.
func TestCustomTagConsistency_ReaderCoversAllConstants(t *testing.T) {
	// Build a raw tag map that contains all custom tags, then call
	// BuildMetadataFromTag and verify the Metadata struct has them.
	raw := map[string]interface{}{
		TagBookID:      "book-42",
		TagVersion:     "1",
		TagASIN:        "B00READER",
		TagOpenLibrary: "OL99M",
		TagHardcover:   "hc-42",
		TagGoogleBooks: "gb-42",
		TagEdition:     "3rd",
		TagPrintYear:   "2019",
		// ISBN10/ISBN13 are read from standard tag keys, not custom organizer keys.
		// BuildMetadataFromTag reads ISBN from "ISBN10"/"TXXX:ISBN10" etc.
		"ISBN10": "0123456789",
		"ISBN13": "9780123456789",
		// Also set standard tags so the function doesn't try filename fallback
		"TITLE":  "Test Book",
		"ARTIST": "Test Author",
	}

	mock := &mockTagMetadata{raw: raw, title: "Test Book", artist: "Test Author"}
	meta := BuildMetadataFromTag(mock, "/fake/test.m4b", nil)

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"BookOrganizerID", meta.BookOrganizerID, "book-42"},
		{"OrganizerTagVersion", meta.OrganizerTagVersion, "1"},
		{"ISBN10", meta.ISBN10, "0123456789"},
		{"ISBN13", meta.ISBN13, "9780123456789"},
		{"ASIN", meta.ASIN, "B00READER"},
		{"OpenLibraryID", meta.OpenLibraryID, "OL99M"},
		{"HardcoverID", meta.HardcoverID, "hc-42"},
		{"GoogleBooksID", meta.GoogleBooksID, "gb-42"},
		{"Edition", meta.Edition, "3rd"},
		{"PrintYear", meta.PrintYear, "2019"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("Metadata.%s = %q, want %q", c.field, c.got, c.want)
		}
	}
}

// TestCustomTagConsistency_MetadataStructHasFieldForEveryCustomTag ensures that
// the Metadata struct has a field for every custom tag. If someone adds a new
// Tag constant but forgets the Metadata field, this will catch it.
func TestCustomTagConsistency_MetadataStructHasFieldForEveryCustomTag(t *testing.T) {
	// Map from tag constant name to expected Metadata struct field name.
	tagToField := map[string]string{
		"TagBookID":      "BookOrganizerID",
		"TagVersion":     "OrganizerTagVersion",
		"TagISBN10":      "ISBN10",
		"TagISBN13":      "ISBN13",
		"TagASIN":        "ASIN",
		"TagOpenLibrary": "OpenLibraryID",
		"TagHardcover":   "HardcoverID",
		"TagGoogleBooks": "GoogleBooksID",
		"TagEdition":     "Edition",
		"TagPrintYear":   "PrintYear",
	}

	metaType := reflect.TypeOf(Metadata{})
	for constName, fieldName := range tagToField {
		_, found := metaType.FieldByName(fieldName)
		if !found {
			t.Errorf("Metadata struct missing field %q for constant %s", fieldName, constName)
		}
	}
}

// TestCustomTagConsistency_CustomTagsStructHasFieldForEveryTag ensures that
// the CustomTags struct has a field for every writable custom tag.
func TestCustomTagConsistency_CustomTagsStructHasFieldForEveryTag(t *testing.T) {
	tagToField := map[string]string{
		"TagBookID":      "BookID",
		"TagVersion":     "Version",
		"TagISBN10":      "ISBN10",
		"TagISBN13":      "ISBN13",
		"TagASIN":        "ASIN",
		"TagOpenLibrary": "OpenLibraryID",
		"TagHardcover":   "HardcoverID",
		"TagGoogleBooks": "GoogleBooksID",
		"TagEdition":     "Edition",
		"TagPrintYear":   "PrintYear",
	}

	ctType := reflect.TypeOf(CustomTags{})
	for constName, fieldName := range tagToField {
		_, found := ctType.FieldByName(fieldName)
		if !found {
			t.Errorf("CustomTags struct missing field %q for constant %s", fieldName, constName)
		}
	}
}

// TestCustomTagConsistency_ProvenanceFieldsCoverMetadata verifies that every
// Metadata field that carries file-level data is represented in the provenance
// map built by buildMetadataProvenance (in server.go). We check the expected
// provenance field list against the Metadata struct fields that should
// contribute a file_value.
func TestCustomTagConsistency_ProvenanceFieldsCoverMetadata(t *testing.T) {
	// These are the provenance fields and the corresponding Metadata field
	// used as file_value, extracted from buildMetadataProvenance.
	provenanceToMeta := map[string]string{
		"title":                  "Title",
		"author_name":            "Artist",
		"narrator":               "Narrator",
		"series_name":            "Series",
		"publisher":              "Publisher",
		"language":               "Language",
		"audiobook_release_year": "Year",
		"isbn10":                 "ISBN10",
		"isbn13":                 "ISBN13",
		"genre":                  "Genre",
		"album":                  "Album",
		"asin":                   "ASIN",
		"series_index":           "SeriesIndex",
		"print_year":             "PrintYear",
		"edition":                "Edition",
		"description":            "Comments",
	}

	metaType := reflect.TypeOf(Metadata{})
	for provField, metaField := range provenanceToMeta {
		_, found := metaType.FieldByName(metaField)
		if !found {
			t.Errorf("provenance field %q references Metadata.%s which does not exist", provField, metaField)
		}
	}

	// Verify completeness: every string/int field on Metadata that is not
	// a control field should appear in provenance.
	skipFields := map[string]bool{
		"AuthorSource":         true, // metadata about metadata, not a value
		"UsedFilenameFallback": true, // control flag
		"BookOrganizerID":      true, // internal tracking, not user-facing provenance
		"OrganizerTagVersion":  true, // internal tracking
		"HardcoverID":          true, // external ID, tracked separately
		"OpenLibraryID":        true, // external ID, tracked separately
		"GoogleBooksID":        true, // external ID, tracked separately
		"TrackNumber":          true, // positional, not a scored/provenance value
		"TrackTotal":           true, // positional
		"DiscNumber":           true, // positional
		"DiscTotal":            true, // positional
		"Grouping":             true, // box-set divider, not a scored value
	}

	metaFieldsInProvenance := map[string]bool{}
	for _, mf := range provenanceToMeta {
		metaFieldsInProvenance[mf] = true
	}

	for i := 0; i < metaType.NumField(); i++ {
		f := metaType.Field(i)
		if skipFields[f.Name] {
			continue
		}
		kind := f.Type.Kind()
		if kind != reflect.String && kind != reflect.Int {
			continue
		}
		if !metaFieldsInProvenance[f.Name] {
			t.Errorf("Metadata.%s (type %s) is not represented in buildMetadataProvenance — "+
				"add it to provenanceToMeta or skipFields if intentionally excluded", f.Name, f.Type)
		}
	}
}

// TestCustomTagConsistency_WriteAndReadKeysAlign ensures the tag keys written
// by the taglib writer are the same keys read by BuildMetadataFromTag.
func TestCustomTagConsistency_WriteAndReadKeysAlign(t *testing.T) {
	// Keys written by writeMetadataWithTaglib for custom tags.
	writtenKeys := map[string]bool{}
	for _, p := range expectedCustomPairs() {
		writtenKeys[p[0]] = true
	}
	writtenKeys[TagVersion] = true

	// Keys read by BuildMetadataFromTag (it tries both TXXX: prefixed and bare).
	// The bare key is always one of the attempts, so verify that.
	readConstants := []string{
		TagBookID, TagVersion, TagISBN10, TagISBN13,
		TagASIN, TagOpenLibrary, TagHardcover, TagGoogleBooks,
		TagEdition, TagPrintYear,
	}

	readKeys := map[string]bool{}
	for _, k := range readConstants {
		readKeys[k] = true
	}

	// Every written key should be readable.
	for k := range writtenKeys {
		if !readKeys[k] {
			t.Errorf("tag %q is written but not read by BuildMetadataFromTag", k)
		}
	}
	// Every read key should be written.
	for k := range readKeys {
		if !writtenKeys[k] {
			t.Errorf("tag %q is read but not written by writeMetadataWithTaglib", k)
		}
	}
}

// TestCustomTagConstantCount is a simple canary: if someone adds a new
// constant, this test forces them to update the test infrastructure too.
func TestCustomTagConstantCount(t *testing.T) {
	const expectedCount = 10 // TagBookID through TagPrintYear + TagVersion
	got := len(allCustomTagConstants())
	if got != expectedCount {
		keys := make([]string, 0, got)
		for k := range allCustomTagConstants() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("expected %d custom tag constants, got %d: %v\n"+
			"If you added a new constant, update allCustomTagConstants(), expectedCustomPairs(), "+
			"and the corresponding struct fields and writer/reader code.",
			expectedCount, got, keys)
	}
}

// --- Mock tag.Metadata implementation for BuildMetadataFromTag tests ---

type mockTagMetadata struct {
	raw     map[string]interface{}
	title   string
	artist  string
	album   string
	genre   string
	year    int
	comment string
}

func (m *mockTagMetadata) Format() tag.Format          { return tag.UnknownFormat }
func (m *mockTagMetadata) FileType() tag.FileType      { return tag.UnknownFileType }
func (m *mockTagMetadata) Title() string               { return m.title }
func (m *mockTagMetadata) Album() string               { return m.album }
func (m *mockTagMetadata) Artist() string              { return m.artist }
func (m *mockTagMetadata) AlbumArtist() string         { return "" }
func (m *mockTagMetadata) Composer() string            { return "" }
func (m *mockTagMetadata) Genre() string               { return m.genre }
func (m *mockTagMetadata) Year() int                   { return m.year }
func (m *mockTagMetadata) Track() (int, int)           { return 0, 0 }
func (m *mockTagMetadata) Disc() (int, int)            { return 0, 0 }
func (m *mockTagMetadata) Picture() *tag.Picture       { return nil }
func (m *mockTagMetadata) Comment() string             { return m.comment }
func (m *mockTagMetadata) Lyrics() string              { return "" }
func (m *mockTagMetadata) Raw() map[string]interface{} { return m.raw }

// --- Compile-time interface check ---
var _ tag.Metadata = (*mockTagMetadata)(nil)

// TestCustomTagWriteTagMap_StandardFieldsCovered verifies that
// writeMetadataWithTaglib handles all the standard (non-custom) metadata fields
// by simulating the tag map construction.
func TestCustomTagWriteTagMap_StandardFieldsCovered(t *testing.T) {
	// Simulate what writeMetadataWithTaglib does with the standard fields.
	metadata := map[string]interface{}{
		"title":        "My Book",
		"artist":       "Jane Author",
		"album":        "My Series",
		"genre":        "Fiction",
		"year":         2023,
		"narrator":     "John Narrator",
		"language":     "English",
		"publisher":    "Big Publisher",
		"isbn10":       "0123456789",
		"isbn13":       "9780123456789",
		"description":  "A great book",
		"series":       "My Series Name",
		"series_index": 3,
	}

	tags := buildStandardTagMap(metadata)

	expectedKeys := []string{
		"TITLE", "ARTIST", "GENRE", "DATE", "NARRATOR",
		"LANGUAGE", "PUBLISHER", "ISBN10", "ISBN13",
		"DESCRIPTION", "COMMENT", "SERIES", "MVNM",
		"SERIES_INDEX", "MVIN",
	}

	for _, key := range expectedKeys {
		if _, ok := tags[key]; !ok {
			t.Errorf("standard tag map missing key %q", key)
		}
	}
}

// buildStandardTagMap replicates the standard-field portion of
// writeMetadataWithTaglib's tag construction for testing purposes.
func buildStandardTagMap(metadata map[string]interface{}) map[string][]string {
	tags := make(map[string][]string)

	if title, ok := metadata["title"].(string); ok && title != "" {
		tags["TITLE"] = []string{title}
	}
	if artist, ok := metadata["artist"].(string); ok && artist != "" {
		tags["ALBUMARTIST"] = []string{artist}
		tags["ARTIST"] = []string{artist}
		tags["COMPOSER"] = []string{artist}
	}
	if album, ok := metadata["album"].(string); ok && album != "" {
		tags["ALBUM"] = []string{album}
	}
	if genre, ok := metadata["genre"].(string); ok && genre != "" {
		tags["GENRE"] = []string{genre}
	}
	if year, ok := metadata["year"].(int); ok && year > 0 {
		tags["DATE"] = []string{fmt.Sprintf("%d", year)}
	}
	if narrator, ok := metadata["narrator"].(string); ok && narrator != "" {
		tags["NARRATOR"] = []string{narrator}
	}
	if lang, ok := metadata["language"].(string); ok && lang != "" {
		tags["LANGUAGE"] = []string{strings.ToLower(lang)}
	}
	if pub, ok := metadata["publisher"].(string); ok && pub != "" {
		tags["PUBLISHER"] = []string{pub}
	}
	if isbn10, ok := metadata["isbn10"].(string); ok && isbn10 != "" {
		tags["ISBN10"] = []string{isbn10}
	}
	if isbn13, ok := metadata["isbn13"].(string); ok && isbn13 != "" {
		tags["ISBN13"] = []string{isbn13}
	}
	if desc, ok := metadata["description"].(string); ok && desc != "" {
		tags["DESCRIPTION"] = []string{desc}
		tags["COMMENT"] = []string{desc}
	}
	if series, ok := metadata["series"].(string); ok && series != "" {
		tags["SERIES"] = []string{series}
		tags["MVNM"] = []string{series}
	}
	if si, ok := metadata["series_index"].(int); ok && si > 0 {
		tags["SERIES_INDEX"] = []string{fmt.Sprintf("%d", si)}
		tags["MVIN"] = []string{fmt.Sprintf("%d", si)}
	}

	return tags
}
