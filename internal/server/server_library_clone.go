// file: internal/server/server_library_clone.go
// version: 1.1.0
// guid: 3f7c2a91-5d4e-4b8a-9c61-0e2d7f4a8b35
// last-edited: 2026-09-24

package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// This file implements maintenance.LibraryCloner on *Server: the
// itunes-clone-into-library op clones an iTunes-only book into the library
// root as a new organized version, through the same organize service every
// other version-making path uses (CreateOrganizedVersion: new row, book_file
// rows with the iTunes PID moved onto them, source demoted to
// organized_source, versionprimary hand-off).
//
// The one difference from a normal organize is the transfer: REFLINK ONLY.
// organizer's "reflink" strategy has no fallback, so a clone that the
// filesystem cannot make fails instead of becoming a full copy (which
// copy_file_range does silently across datasets) or a hardlink (which would
// share the iTunes file's inode, so a later tag write would mutate it).

var libraryCloneLog = logger.New("server.library-clone")

// reflinkOrganizer is an Organizer over a copy of the live config with the
// strategy forced to reflink.
func (s *Server) reflinkOrganizer() *organizer.Organizer {
	cfg := config.AppConfig
	cfg.OrganizationStrategy = "reflink"
	org := organizer.NewOrganizer(&cfg)
	org.SetStore(s.storeForWiring())
	return org
}

// singleFileBook returns book with FilePath set to its one active file, or
// nil when it has more than one. book.file_path is stale on renamed books,
// so the single-file organize is driven from the book_file row.
func singleFileBook(book *database.Book, files []database.BookFile) *database.Book {
	if len(files) != 1 {
		return nil
	}
	b := *book
	b.FilePath = files[0].FilePath
	return &b
}

// PlanLibraryClone implements maintenance.LibraryCloner: the library paths the
// clone of book's active files would land at. Nothing is written.
func (s *Server) PlanLibraryClone(book *database.Book, files []database.BookFile) ([]string, error) {
	org := s.reflinkOrganizer()
	if b := singleFileBook(book, files); b != nil {
		p, err := org.GenerateTargetPath(b)
		if err != nil {
			return nil, err
		}
		return []string{p}, nil
	}
	planned, err := org.PlanFilePaths(book, files)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(planned))
	for _, f := range files {
		if p, ok := planned[f.FilePath]; ok {
			out = append(out, p)
		}
	}
	if len(out) != len(files) {
		return nil, fmt.Errorf("planner placed %d of %d file(s)", len(out), len(files))
	}
	return out, nil
}

// CloneBookIntoLibrary implements maintenance.LibraryCloner: reflink book's
// active files into the library and create the organized version row. Returns
// the new book's id.
//
// Every file must be WRITTEN by this call, at exactly the planned path. The
// organizer is built for re-organizing a book, so on an occupied destination
// it adopts a byte-identical occupant as "already there" or picks a _copyN
// sibling. For a clone both are wrong: adoption gives the new version row
// another book's file (and a rollback of the clone would then delete that
// book's audio), and a _copyN name is a copy nobody planned. So an occupied
// destination refuses before anything is written, and a landing that adopted
// or renamed anything (a writer raced in between) is unwound: the files this
// call created are removed and no row is made.
func (s *Server) CloneBookIntoLibrary(book *database.Book, files []database.BookFile, opID string) (string, error) {
	if s.organizeService == nil {
		return "", fmt.Errorf("organize service is not wired")
	}
	planned, err := s.PlanLibraryClone(book, files)
	if err != nil {
		return "", fmt.Errorf("plan destinations: %w", err)
	}
	for _, p := range planned {
		if _, err := os.Lstat(p); err == nil {
			return "", fmt.Errorf("destination %s already exists: %w", p, fs.ErrExist)
		}
	}
	org := s.reflinkOrganizer()
	src := book
	var landing *organizer.Landing
	if b := singleFileBook(book, files); b != nil {
		src = b
		landing, err = org.OrganizeSingleFile(b)
	} else {
		landing, err = org.OrganizeBookDirectory(book, files)
	}
	if err != nil {
		return "", fmt.Errorf("reflink into library: %w", err)
	}
	if landing.InPlace {
		return "", fmt.Errorf("landing is in place; refusing to version %s", book.ID)
	}
	if err := freshLanding(landing, planned); err != nil {
		left := removeCreated(landing.Created, config.AppConfig.RootDir)
		if len(left) > 0 {
			return "", fmt.Errorf("%w; could not remove %v", err, left)
		}
		return "", err
	}
	created, err := s.organizeService.CreateOrganizedVersion(src, landing, opID, libraryCloneLog)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// errLandingNotFresh reports a landing that is not exactly the planned paths,
// each one written by this organize.
var errLandingNotFresh = errors.New("clone did not land fresh at the planned paths")

// freshLanding returns nil when landing's destinations are exactly planned
// and every one of them is in landing.Created.
func freshLanding(landing *organizer.Landing, planned []string) error {
	landed := []string{landing.Path}
	if landing.Files != nil {
		landed = landed[:0]
		for _, d := range landing.Files {
			landed = append(landed, d)
		}
	}
	want := make(map[string]bool, len(planned))
	for _, p := range planned {
		want[filepath.Clean(p)] = true
	}
	made := make(map[string]bool, len(landing.Created))
	for _, c := range landing.Created {
		made[filepath.Clean(c)] = true
	}
	if len(landed) != len(want) {
		return fmt.Errorf("%w: %d landed, %d planned", errLandingNotFresh, len(landed), len(want))
	}
	for _, d := range landed {
		d = filepath.Clean(d)
		if !want[d] {
			return fmt.Errorf("%w: %s was not planned", errLandingNotFresh, d)
		}
		if !made[d] {
			return fmt.Errorf("%w: %s was already there (adopted, not written)", errLandingNotFresh, d)
		}
	}
	return nil
}

// removeCreated deletes the files an organize wrote (never anything outside
// root) and then their directories if empty. Returns what it could not remove.
func removeCreated(created []string, root string) []string {
	var left []string
	dirs := map[string]bool{}
	for _, p := range created {
		if !pathutil.IsWithin(p, root) {
			left = append(left, p)
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, p)
			continue
		}
		dirs[filepath.Dir(p)] = true
	}
	for d := range dirs {
		if pathutil.IsWithin(d, root) && filepath.Clean(d) != filepath.Clean(root) {
			_ = os.Remove(d) // only succeeds when empty
		}
	}
	return left
}

// LibraryITunesPath implements maintenance.LibraryCloner: the iTunes path the
// organize service records for a library file.
func (s *Server) LibraryITunesPath(path string) string {
	if s.organizeService == nil || s.organizeService.ComputeITunesPath == nil {
		return ""
	}
	return s.organizeService.ComputeITunesPath(path)
}
