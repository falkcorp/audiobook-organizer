// file: internal/books/searchindex/cutover_test.go
// version: 0.1.0
// guid: 6c2b7ad6-e743-438c-809c-9337f8e43ff4
// last-edited: 2026-11-20

package searchindex_test

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/searchindex/searchindextest"
)

// TestBooksIndexRebuiltAtCutover is the native half of the spec test: books'
// index is rebuilt at <db parent>/index/books/books.bleve while search is
// degraded, and then answers equal the answers R0 recorded, over both recorded
// corpora (stage1 and its overlay).
func TestBooksIndexRebuiltAtCutover(t *testing.T) {
	for name, path := range map[string]string{
		"stage1":  "../testdata/r2corpus/answers/search.json",
		"overlay": "../testdata/r2corpus/answers/overlay/search.json",
	} {
		t.Run(name, func(t *testing.T) {
			o := searchindextest.LoadOracle(t, path)
			searchindextest.RunBooksIndexRebuiltAtCutover(t, o, func(text string) bool {
				return strings.HasPrefix(text, "^") // a regex: the filter layer's, not the index's
			})
		})
	}
}
