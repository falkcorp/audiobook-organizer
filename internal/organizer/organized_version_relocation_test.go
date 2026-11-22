// file: internal/organizer/organized_version_relocation_test.go
// version: 0.1.0
// guid: 7d3e91a4-5c28-4f6b-b0e7-2a8c14d96f35
// last-edited: 2026-11-22

package organizer

import (
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// createSpy records which create call the organize service made for the copy.
type createSpy struct {
	*database.PebbleStore
	plain      int
	relocating []string // originalID of each CreateBookWithRelocation
}

func (s *createSpy) CreateBook(b *database.Book) (*database.Book, error) {
	s.plain++
	return s.PebbleStore.CreateBook(b)
}

func (s *createSpy) CreateBookWithRelocation(b *database.Book, originalID string) (*database.Book, error) {
	s.relocating = append(s.relocating, originalID)
	return s.PebbleStore.CreateBookWithRelocation(b, originalID)
}

// Only the single-book organize request's copy stages the follow-up that moves
// the original's torrents (F6-03, replacing NotifyDelugeAfterOrganize, which ran
// only there). The bulk organize, library clone and metadata-apply copies call
// CreateOrganizedVersion and must stay silent, as at base.
func TestCreateOrganizedVersion_OnlyTheFollowTorrentsVariantStagesTheRelocation(t *testing.T) {
	cases := []struct {
		name         string
		follow       bool
		wantPlain    int
		wantRelocate []string
	}{
		{"CreateOrganizedVersion (bulk organize, clone, metadata apply) stays silent", false, 1, nil},
		{"CreateOrganizedVersionFollowTorrents (single-book organize) stages for the original", true, 0, []string{"batcave-original"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps, err := database.NewPebbleStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewPebbleStore: %v", err)
			}
			ps.WaitForWarmup()
			t.Cleanup(func() { _ = ps.Close() })
			spy := &createSpy{PebbleStore: ps}
			svc := NewService(spy)

			gid := "vg-batcave"
			imported := "imported"
			src, err := ps.CreateBook(&database.Book{
				ID: "batcave-original", Title: "Year One", FilePath: filepath.Join(t.TempDir(), "incoming", "year-one.m4b"),
				VersionGroupID: &gid, LibraryState: &imported,
			})
			if err != nil {
				t.Fatal(err)
			}
			spy.plain = 0

			landing := &Landing{Path: filepath.Join(t.TempDir(), "organized-year-one.m4b")}
			var created *database.Book
			if tc.follow {
				created, err = svc.CreateOrganizedVersionFollowTorrents(src, landing, "", &noopLogger{})
			} else {
				created, err = svc.CreateOrganizedVersion(src, landing, "", &noopLogger{})
			}
			if err != nil || created == nil {
				t.Fatalf("create = %v, %v", created, err)
			}
			if spy.plain != tc.wantPlain {
				t.Errorf("CreateBook called %d times, want %d", spy.plain, tc.wantPlain)
			}
			if len(spy.relocating) != len(tc.wantRelocate) || (len(tc.wantRelocate) == 1 && spy.relocating[0] != tc.wantRelocate[0]) {
				t.Errorf("CreateBookWithRelocation originals = %v, want %v", spy.relocating, tc.wantRelocate)
			}
		})
	}
}
