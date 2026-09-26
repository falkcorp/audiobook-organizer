// file: internal/foldercover/foldercover.go
// version: 1.0.0
// guid: 5b0d2e61-7c3a-4f8e-9a14-2d6c8b3e1f70
// last-edited: 2026-09-26

// Package foldercover decides whether a book should take its cover from an
// image in its own folder, and applies that decision.
//
// A folder cover is a FILL, never a replacement. It is used only when ALL of
// these hold, checked in this order (cheap checks first):
//
//  1. the book has no cover_url (a provider cover, an uploaded cover, a cover
//     the user picked, or a previously extracted one all set it);
//  2. there is no id-named cover under {root}/covers (a downloaded or uploaded
//     cover that was stored without a cover_url);
//  3. the book's field locks can be read and cover_url is not locked
//     (database.FieldKeyCoverURL). An unreadable lock set is "locked": the
//     book is skipped, never guessed at;
//  4. a folder candidate exists (metadata.FindFolderCover), subject to the
//     shared-folder rule below;
//  5. the book's first present audio file has no embedded picture. Embedded
//     art is the book's own cover and wins over a loose image.
//
// Shared folders: a folder that also holds audio NOT belonging to this book
// (several single-file books side by side) cannot attribute "cover.jpg" to any
// one of them. There, only an image whose stem matches one of this book's
// audio file stems ("Book.jpg" beside "Book.m4b") is accepted.
//
// The write goes through ModifyBook and re-checks rule 1 inside the callback,
// so a cover another writer set between the read and the write is never
// overwritten (the write is skipped with database.ErrSkipBookWrite).
package foldercover

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/audioext"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Outcome is what Evaluate or Apply decided for one book.
type Outcome string

const (
	OutcomeApplied          Outcome = "applied"
	OutcomeWouldApply       Outcome = "would_apply"
	OutcomeHasCover         Outcome = "has_cover"
	OutcomeHasEmbedded      Outcome = "has_embedded"
	OutcomeLocked           Outcome = "locked"
	OutcomeLocksUnavailable Outcome = "locks_unavailable"
	OutcomeNoFolder         Outcome = "no_folder"
	OutcomeNoCandidate      Outcome = "no_candidate"
	OutcomeRaced            Outcome = "raced"
	OutcomeError            Outcome = "error"
)

// Store is the store surface the fill needs.
type Store interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	database.MetadataFieldStateReader
}

// Plan is the decision for one book.
type Plan struct {
	BookID    string                `json:"book_id"`
	Outcome   Outcome               `json:"outcome"`
	Candidate *metadata.FolderCover `json:"candidate,omitempty"`
	// CoverURL is the cover_url written (Apply) -- empty otherwise.
	CoverURL string `json:"cover_url,omitempty"`
	Err      error  `json:"-"`
}

// embeddedPicture is metadata.ExtractCoverArtBytes; a seam for tests.
var embeddedPicture = func(path string) ([]byte, error) {
	data, _, err := metadata.ExtractCoverArtBytes(path)
	return data, err
}

func hasCover(book *database.Book) bool {
	return book.CoverURL != nil && strings.TrimSpace(*book.CoverURL) != ""
}

// bookAudio returns the book's present audio paths, sorted.
func bookAudio(store Store, book *database.Book) ([]string, error) {
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		return nil, fmt.Errorf("book files for %s: %w", book.ID, err)
	}
	var paths []string
	for i := range files {
		f := &files[i]
		if f.FilePath == "" || f.Missing {
			continue
		}
		paths = append(paths, f.FilePath)
	}
	if len(paths) == 0 && book.FilePath != "" {
		// A book whose rows are not written yet (the scanner's create path) or
		// a virtual single-file book: its own path is the audio.
		if fi, err := os.Stat(book.FilePath); err == nil && !fi.IsDir() {
			paths = append(paths, book.FilePath)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// bookFolders returns the folders to search: each distinct folder the book's
// audio lives in, plus -- for a book split across disc subfolders -- their
// common parent. A book with no audio falls back to its own path (a folder
// book) or its path's folder.
func bookFolders(book *database.Book, audio []string) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		if d != "" && d != "." && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	for _, p := range audio {
		add(filepath.Dir(p))
	}
	if len(dirs) > 1 {
		add(commonParent(dirs))
	}
	if len(dirs) == 0 && book.FilePath != "" {
		if fi, err := os.Stat(book.FilePath); err == nil && fi.IsDir() {
			add(book.FilePath)
		} else {
			add(filepath.Dir(book.FilePath))
		}
	}
	return dirs
}

func commonParent(dirs []string) string {
	parent := dirs[0]
	for _, d := range dirs[1:] {
		for parent != "" && parent != string(filepath.Separator) && parent != "." &&
			d != parent && !strings.HasPrefix(d, parent+string(filepath.Separator)) {
			parent = filepath.Dir(parent)
		}
	}
	return parent
}

// sharedStems returns nil when dir holds only this book's audio, and the set
// of this book's audio stems when other audio shares the folder.
func sharedStems(dir string, own map[string]bool, audio []string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	exts := audioext.DefaultSet()
	shared := false
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") || !exts.MatchPath(e.Name()) {
			continue
		}
		if !own[filepath.Join(dir, e.Name())] {
			shared = true
			break
		}
	}
	if !shared {
		return nil, nil
	}
	stems := map[string]bool{}
	for _, p := range audio {
		b := filepath.Base(p)
		stems[strings.ToLower(strings.TrimSuffix(b, filepath.Ext(b)))] = true
	}
	return stems, nil
}

var errNoFolder = errors.New("book has no folder")

// BestFolderImage returns the image in the book's folder(s) that the rule
// above selects, or nil when there is none. It applies the shared-folder rule
// but none of the cover/lock/embedded checks: it answers "which folder image
// is this book's", which the cover-text reader needs for books that already
// have a cover. It writes nothing.
func BestFolderImage(store Store, book *database.Book) (*metadata.FolderCover, error) {
	best, _, err := bestFolderImage(store, book)
	if errors.Is(err, errNoFolder) {
		return nil, nil
	}
	return best, err
}

func bestFolderImage(store Store, book *database.Book) (*metadata.FolderCover, []string, error) {
	audio, err := bookAudio(store, book)
	if err != nil {
		return nil, nil, err
	}
	dirs := bookFolders(book, audio)
	if len(dirs) == 0 {
		return nil, audio, errNoFolder
	}
	own := make(map[string]bool, len(audio))
	for _, p := range audio {
		own[p] = true
	}
	var best *metadata.FolderCover
	for _, dir := range dirs {
		stems, err := sharedStems(dir, own, audio)
		if err != nil {
			return nil, audio, fmt.Errorf("read folder %s: %w", dir, err)
		}
		c, err := metadata.FindFolderCover(dir, metadata.FolderCoverFilter{OnlyStems: stems})
		if err != nil {
			return nil, audio, err
		}
		if c != nil && (best == nil || c.NameRank < best.NameRank ||
			(c.NameRank == best.NameRank && c.Area() > best.Area())) {
			best = c
		}
	}
	return best, audio, nil
}

// FirstAudio returns the book's first present audio file ("" when none): the
// file whose embedded picture counts as the book's embedded cover.
func FirstAudio(store Store, book *database.Book) (string, error) {
	audio, err := bookAudio(store, book)
	if err != nil || len(audio) == 0 {
		return "", err
	}
	return audio[0], nil
}

// Evaluate decides, without writing anything, whether book should take a
// folder cover. It reads the folder and, when a candidate exists, the first
// audio file's tags. rootDir is the app root ({root}/covers, {root}/.covers).
func Evaluate(store Store, book *database.Book, rootDir string) Plan {
	plan := Plan{BookID: book.ID}
	if hasCover(book) || (rootDir != "" && metadata.CoverPathForBook(rootDir, book.ID) != "") {
		plan.Outcome = OutcomeHasCover
		return plan
	}
	locked, err := database.LockedUserFields(store, book.ID)
	if err != nil {
		plan.Outcome, plan.Err = OutcomeLocksUnavailable, err
		return plan
	}
	if locked[database.FieldKeyCoverURL] {
		plan.Outcome = OutcomeLocked
		return plan
	}
	best, audio, err := bestFolderImage(store, book)
	switch {
	case errors.Is(err, errNoFolder):
		plan.Outcome = OutcomeNoFolder
		return plan
	case err != nil:
		plan.Outcome, plan.Err = OutcomeError, err
		return plan
	case best == nil:
		plan.Outcome = OutcomeNoCandidate
		return plan
	}
	plan.Candidate = best
	if len(audio) > 0 {
		data, err := embeddedPicture(audio[0])
		if err == nil && len(data) > 0 {
			plan.Outcome = OutcomeHasEmbedded
			return plan
		}
	}
	plan.Outcome = OutcomeWouldApply
	return plan
}

// Apply evaluates book and, when a folder cover should be used, stores it in
// {rootDir}/.covers and sets cover_url. It never overwrites a cover: the
// callback re-checks cover_url at write time.
func Apply(store Store, book *database.Book, rootDir string) Plan {
	plan := Evaluate(store, book, rootDir)
	if plan.Outcome != OutcomeWouldApply {
		return plan
	}
	if rootDir == "" {
		plan.Outcome, plan.Err = OutcomeError, errors.New("root dir is not configured")
		return plan
	}
	img, err := metadata.LoadFolderCover(*plan.Candidate)
	if err != nil {
		plan.Outcome, plan.Err = OutcomeError, err
		return plan
	}
	stored, err := metadata.StoreCoverImage(rootDir, img)
	if err != nil {
		plan.Outcome, plan.Err = OutcomeError, err
		return plan
	}
	url := metadata.LocalCoverURL(stored)
	raced := false
	written, err := store.ModifyBook(book.ID, func(cur *database.Book) error {
		if hasCover(cur) {
			raced = true
			return database.ErrSkipBookWrite
		}
		cur.CoverURL = &url
		return nil
	})
	switch {
	case err != nil:
		plan.Outcome, plan.Err = OutcomeError, err
	case written == nil:
		plan.Outcome, plan.Err = OutcomeError, fmt.Errorf("book %s was deleted before the cover could be written", book.ID)
	case raced:
		plan.Outcome = OutcomeRaced
	default:
		plan.Outcome, plan.CoverURL = OutcomeApplied, url
	}
	return plan
}
