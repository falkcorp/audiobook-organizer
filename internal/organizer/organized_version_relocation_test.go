// file: internal/organizer/organized_version_relocation_test.go
// version: 0.1.0
// guid: 7d3e91a4-5c28-4f6b-b0e7-2a8c14d96f35
// last-edited: 2026-11-22

package organizer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
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
			svc.SetRelocatingCopyCreator(spy)

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

// A Service nobody gave a RelocatingCopyCreator refuses the single-book organize
// copy instead of creating one that moves no torrent, and writes no row.
func TestCreateOrganizedVersionFollowTorrents_RefusesWithoutACreator(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	ps.WaitForWarmup()
	t.Cleanup(func() { _ = ps.Close() })
	svc := NewService(ps)
	imported := "imported"
	src, err := ps.CreateBook(&database.Book{ID: "robin-original", Title: "Dark Victory", FilePath: filepath.Join(t.TempDir(), "dark-victory.m4b"), LibraryState: &imported})
	if err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	config.AppConfig.RootDir = rootDir // the rollback removes only files under the root
	landing := &Landing{Path: filepath.Join(rootDir, "organized-dark-victory.m4b")}
	// The caller has already written the organized file when the refusal comes: the
	// refusal removes it (the same rollback as any failed CreateBook), so neither a
	// row nor a file is left behind.
	if err := os.WriteFile(landing.Path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	landing.Created = []string{landing.Path}
	if _, err := svc.CreateOrganizedVersionFollowTorrents(src, landing, "", &noopLogger{}); !errors.Is(err, ErrRelocatingCopyCreatorNotWired) {
		t.Fatalf("err = %v, want ErrRelocatingCopyCreatorNotWired", err)
	}
	if _, err := os.Stat(landing.Path); !os.IsNotExist(err) {
		t.Fatalf("the organized file survives the refusal: stat err = %v", err)
	}
	all, err := ps.GetAllBooksCore(100, 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("books after the refusal = %d, %v; want only the original", len(all), err)
	}
}
