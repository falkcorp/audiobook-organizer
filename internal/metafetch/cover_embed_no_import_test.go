// file: internal/metafetch/cover_embed_no_import_test.go
// version: 1.0.0
// guid: 2c7f4a91-6e3b-4d58-b1a0-8f5d3e2c9b16
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

type noImportChecker struct{}

func (noImportChecker) IsProtected(string) bool { return true }

type panicImporter struct{}

func (panicImporter) ImportPath(context.Context, string, string) (string, error) {
	panic("a protected file was handed to the importer")
}

// Cover embeds pass mfs.safeWriteDeps straight to tagger.EmbedCoverArtSafe.
// With an Importer there, a protected file was copied to RootDir/<basename>
// and its row repointed. SetSafeWriteDeps must drop it so the embed refuses.
func TestSetSafeWriteDeps_DropsImporter(t *testing.T) {
	mfs := NewService(nil)
	mfs.SetSafeWriteDeps(tagger.SafeWriteDeps{ProtectedCache: noImportChecker{}, Importer: panicImporter{}})
	if mfs.safeWriteDeps.Importer != nil {
		t.Fatal("safeWriteDeps kept the Importer; a cover embed would import a protected file into the library root")
	}
	if mfs.safeWriteDeps.ProtectedCache == nil {
		t.Fatal("the protected-path guard must stay wired")
	}
	// The embed's own call: a refusal, not an import.
	err := tagger.EmbedCoverArtSafe(context.Background(), "/seeding/book.m4b", "/covers/c.jpg", mfs.safeWriteDeps)
	if err == nil {
		t.Fatal("EmbedCoverArtSafe accepted a protected path")
	}
}
