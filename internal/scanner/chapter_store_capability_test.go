// file: internal/scanner/chapter_store_capability_test.go
// version: 1.0.0
// guid: 6d41a893-2c57-4f1b-90e8-3a7b5c2d8e14
// last-edited: 2026-08-19

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type chapterCapableStore struct {
	database.Store
}

func (chapterCapableStore) GetChaptersForBook(string) ([]database.Chapter, error) {
	return []database.Chapter{{Title: "one"}}, nil
}
func (chapterCapableStore) SaveChaptersForBook(string, []database.Chapter) error { return nil }

type chapterDecorator struct {
	database.Store
	inner database.Store
}

func (d chapterDecorator) Unwrap() database.Store { return d.inner }

// TestResolveChapterStoreThroughDecorator pins chapter persistence to the
// production store shape. Neither method is on database.Store (compile-probed),
// so a bare assertion fails through a decorator and PersistChaptersForBook
// skips with a sampled warning that reads like an unsupported backend.
func TestResolveChapterStoreThroughDecorator(t *testing.T) {
	got := resolveChapterStore(chapterDecorator{inner: chapterCapableStore{}})
	if got == nil {
		t.Fatal("resolveChapterStore returned nil through the decorator; chapter extraction would be skipped in production")
	}
	ch, err := got.GetChaptersForBook("b1")
	if err != nil || len(ch) != 1 {
		t.Fatalf("GetChaptersForBook = (%v, %v), want 1 chapter and nil", ch, err)
	}
}

func TestResolveChapterStoreOnUncapableBackend(t *testing.T) {
	type plain struct{ database.Store }
	if got := resolveChapterStore(plain{}); got != nil {
		t.Fatalf("resolveChapterStore = %v without the chapter methods, want nil", got)
	}
}
