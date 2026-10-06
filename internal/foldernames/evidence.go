// file: internal/foldernames/evidence.go
// version: 1.1.0
// guid: b940fbc0-ee6b-4be2-a4a8-26683848e423
// last-edited: 2026-10-06

// Package foldernames builds the person and series evidence the folder parse
// (metadata.ExtractMetadataFromFolderWith) decides an author-or-series folder
// segment with. The scanner, the importer and the reparse fixer
// (maintenance.reparse-folder-names) all read it from here, so the scan that
// creates a row and the fixer that reviews it decide a segment the same way.
package foldernames

import (
	"fmt"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// Store is the read surface the evidence needs.
type Store interface {
	authority.Reader
	GetAuthorByName(name string) (*database.Author, error)
	GetAllAuthors() ([]database.Author, error)
	GetAllSeries() ([]database.Series, error)
	SeriesBooks
}

// Snapshot is the library's author and series names at one moment, plus the
// authority index, read once (Load) and shared by every book a scan or a
// fixer plan parses. It is safe for concurrent use.
type Snapshot struct {
	store Store
	idx   *authority.Index
	// authorIDs maps a LettersKey to the ids of the author rows carrying it.
	authorIDs map[string]map[int]bool
	// series maps a LettersKey to the series rows carrying it.
	series map[string][]database.Series

	mu   sync.Mutex
	memo map[string]bool // LettersKey -> IsKnownSeries verdict
}

// Load reads the author and series lists. An error from either list fails
// the load: a snapshot missing its authors would take every junk series row
// for a real one, and one missing its series would read every series folder
// by shape alone.
func Load(store Store) (*Snapshot, error) {
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("foldernames: list authors: %w", err)
	}
	series, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("foldernames: list series: %w", err)
	}
	s := &Snapshot{
		store:     store,
		idx:       authority.NewIndex(store),
		authorIDs: make(map[string]map[int]bool, len(authors)),
		series:    make(map[string][]database.Series, len(series)),
		memo:      map[string]bool{},
	}
	for _, a := range authors {
		k := key(a.Name)
		if k == "" {
			continue
		}
		if s.authorIDs[k] == nil {
			s.authorIDs[k] = map[int]bool{}
		}
		s.authorIDs[k][a.ID] = true
	}
	for _, r := range series {
		if k := key(r.Name); k != "" {
			s.series[k] = append(s.series[k], r)
		}
	}
	return s, nil
}

func key(name string) string { return personname.LettersKey(strings.TrimSpace(name)) }

// Evidence is the snapshot as the folder parse's NameEvidence.
func (s *Snapshot) Evidence() metadata.NameEvidence {
	if s == nil {
		return metadata.NameEvidence{}
	}
	return metadata.NameEvidence{
		IsKnownAuthor: s.IsKnownPerson,
		IsAuthorRow:   s.IsAuthorRow,
		IsKnownSeries: s.IsKnownSeries,
	}
}

// IsKnownPerson reports whether the authority lists know name as an author
// or a narrator.
func (s *Snapshot) IsKnownPerson(name string) bool {
	if ok, err := s.idx.IsKnownPerson(name, authority.RoleAuthor); err == nil && ok {
		return true
	}
	ok, err := s.idx.IsKnownPerson(name, authority.RoleNarrator)
	return err == nil && ok
}

// IsAuthorRow reports whether the library has an author row of that name.
// It reads the store, not the snapshot, so an author a scan created a moment
// ago counts.
func (s *Snapshot) IsAuthorRow(name string) bool {
	a, err := s.store.GetAuthorByName(name)
	return err == nil && a != nil
}

// IsKnownSeries reports whether the library has a real series of that name.
//
// A series row is not evidence when it is author junk. On prod (2026-10-06)
// 11,713 series rows letters-equal an author row's name, 3,188 of them
// filed under that same author ("Brandon Sanderson" as a series of Brandon
// Sanderson's), and treating those as series made "Brandon
// Sanderson/Brandon Sanderson - Elantris" a series folder. Neither name
// equality nor the row's own AuthorID decides which side is the junk one:
// of the 671 own-author rows holding books, many are real series whose
// AUTHOR row is the junk ("Rogue Merchant", "Ell Donsaii": a series name
// filed as its own author). So for a name an author row shares, each series
// row is judged on its books, the evidence that tells them apart:
//
//   - no books -> no evidence either way, skipped;
//   - any book credited to someone other than the same-named author rows
//     (or to no one) -> a real series ("Honor Harrington", books by David
//     Weber; "Star Wars", books by many);
//   - every book credited to a same-named author that also has books
//     OUTSIDE this series -> junk: a real author whose name was split off a
//     title (Brandon Sanderson, 1,063 books, three of them in a "Brandon
//     Sanderson" series);
//   - every book credited to a same-named author with no books elsewhere ->
//     the author row is the junk side, and the series stands.
//
// Person evidence from the authority lists is checked before this
// (metadata's folderLeadKind), so a known author is an author whatever
// series rows carry the name. A name no author row shares is a series
// whenever a row exists. The verdict is memoised per name for the
// snapshot's life. A read error answers true without memoising: the row
// exists and nothing proved it junk.
func (s *Snapshot) IsKnownSeries(name string) bool {
	k := key(name)
	if s == nil || k == "" {
		return false
	}
	rows := s.series[k]
	if len(rows) == 0 {
		return false
	}
	authorIDs := s.authorIDs[k]
	if len(authorIDs) == 0 {
		return true
	}
	s.mu.Lock()
	v, ok := s.memo[k]
	s.mu.Unlock()
	if ok {
		return v
	}
	verdict := false
	for _, r := range rows {
		real, err := seriesRowIsEvidence(s.store, r, authorIDs)
		if err != nil {
			return true
		}
		if real {
			verdict = true
			break
		}
	}
	s.mu.Lock()
	s.memo[k] = verdict
	s.mu.Unlock()
	return verdict
}

// SeriesBooks is the read seriesRowIsEvidence needs: a series' books and an
// author's books.
type SeriesBooks interface {
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
}

// PointStore is what IsRealSeries reads: one name's author and authorless
// series rows, and their books.
type PointStore interface {
	SeriesBooks
	GetAuthorByName(name string) (*database.Author, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
}

// IsRealSeries is Snapshot.IsKnownSeries for a caller with no series list to
// hand (a metadata search parses one book): the library's authorless series
// row of that name, judged against the author row of that name by the same
// rules. A lookup error is no evidence.
func IsRealSeries(store PointStore, name string) bool {
	name = strings.TrimSpace(name)
	if store == nil || name == "" {
		return false
	}
	r, err := store.GetSeriesByName(name, nil)
	if err != nil || r == nil {
		return false
	}
	a, err := store.GetAuthorByName(name)
	if err != nil {
		return false
	}
	if a == nil || key(a.Name) != key(name) {
		return true
	}
	real, err := seriesRowIsEvidence(store, *r, map[int]bool{a.ID: true})
	return err == nil && real
}

// seriesRowIsEvidence reports whether series row r is real-series evidence
// when authorIDs are the author rows sharing its name (IsKnownSeries lists
// the rules).
func seriesRowIsEvidence(store SeriesBooks, r database.Series, authorIDs map[int]bool) (bool, error) {
	books, err := store.GetBooksBySeriesIDCore(r.ID)
	if err != nil {
		return false, fmt.Errorf("foldernames: books of series %d: %w", r.ID, err)
	}
	if len(books) == 0 {
		return false, nil
	}
	if creditsSomeoneElse(books, authorIDs) {
		return true, nil
	}
	for id := range authorIDs {
		authored, err := store.GetBooksByAuthorIDCore(id)
		if err != nil {
			return false, fmt.Errorf("foldernames: books of author %d: %w", id, err)
		}
		for _, b := range authored {
			if b.SeriesID == nil || *b.SeriesID != r.ID {
				return false, nil
			}
		}
	}
	return true, nil
}

// creditsSomeoneElse reports whether any of books is credited to an author
// not in authorIDs (or to no author).
func creditsSomeoneElse(books []database.BookCore, authorIDs map[int]bool) bool {
	for _, b := range books {
		if b.AuthorID == nil || !authorIDs[*b.AuthorID] {
			return true
		}
	}
	return false
}
