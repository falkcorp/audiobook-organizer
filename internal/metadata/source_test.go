// file: internal/metadata/source_test.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2f3a-4b5c-d6e7f8a9b0c1
// last-edited: 2026-10-12

package metadata

import "testing"

// TestInterfaceCompliance verifies all clients implement MetadataSource.
func TestInterfaceCompliance(t *testing.T) {
	var _ MetadataSource = (*OpenLibraryClient)(nil)
	var _ MetadataSource = (*GoogleBooksClient)(nil)
	var _ MetadataSource = (*AudnexusClient)(nil)
	var _ MetadataSource = (*WikipediaClient)(nil)
}

// The local catalog's PublishYear is Audible's release year, so applying one
// of its candidates writes it as the audiobook release year, not the print
// year.
func TestSourceProducesAudiobookReleaseYear_LocalCatalog(t *testing.T) {
	for name, want := range map[string]bool{
		(&AudibleClient{}).Name():     true,
		(&AudnexusClient{}).Name():    true,
		CatalogSourceName:             true,
		(&OpenLibraryClient{}).Name(): false,
		(&GoogleBooksClient{}).Name(): false,
	} {
		if got := SourceProducesAudiobookReleaseYear(name); got != want {
			t.Errorf("SourceProducesAudiobookReleaseYear(%q) = %v, want %v", name, got, want)
		}
	}
	if got := CategoryTagSource(CatalogSourceName); got != "audible_category" {
		t.Errorf("CategoryTagSource(catalog) = %q, want audible_category", got)
	}
}
