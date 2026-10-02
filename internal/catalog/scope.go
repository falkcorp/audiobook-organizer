// file: internal/catalog/scope.go
// version: 1.0.0
// guid: 5b9d3f27-8c1a-4e6b-9f42-1d7c0a8e3b56
// last-edited: 2026-10-01

package catalog

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ScopeReader is the narrow store slice the harvest scope needs.
type ScopeReader interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetAuthorsByIDs(ids []int) (map[int]*database.Author, error)
}

// bookFileVisitor walks every book_file row without copying it
// (PebbleStore.VisitBookFiles, memdb-backed). allBookFilesCore is the
// copying fallback.
type bookFileVisitor interface {
	VisitBookFiles(fn func(*database.BookFile)) error
}

type allBookFilesCore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
}

// PresentFileBookIDs returns the ids of books with at least one book_file
// row not marked missing. store is unwrapped through the decorator chain.
func PresentFileBookIDs(store any) (map[string]bool, error) {
	out := map[string]bool{}
	if v, ok := database.AsCapability[bookFileVisitor](store); ok {
		err := v.VisitBookFiles(func(f *database.BookFile) {
			if !f.Missing && f.BookID != "" {
				out[f.BookID] = true
			}
		})
		return out, err
	}
	if g, ok := database.AsCapability[allBookFilesCore](store); ok {
		rows, err := g.GetAllBookFilesCore()
		if err != nil {
			return nil, err
		}
		for i := range rows {
			if !rows[i].Missing && rows[i].BookID != "" {
				out[rows[i].BookID] = true
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("catalog scope: store cannot list book files")
}

// ScopeAuthor is one author to harvest. Key is the harvest key (folded
// name); two author rows whose names fold together are one harvest.
type ScopeAuthor struct {
	Key  string
	Name string
	// OwnedASINs are the ASINs of this author's in-scope books, sorted.
	OwnedASINs []string
	Books      int
}

// ScopeCensus is the step-0 count the dry run reports.
type ScopeCensus struct {
	BooksScanned       int `json:"books_scanned"`
	BooksInScope       int `json:"books_in_scope"`
	BooksNoFiles       int `json:"books_no_present_files"`
	BooksNoAuthor      int `json:"books_in_scope_without_author"`
	Authors            int `json:"authors"`
	AuthorsWithASIN    int `json:"authors_with_asin_tagged_book"`
	JunkAuthorsSkipped int `json:"junk_authors_skipped"`
}

// authorRoleIncluded: author and co-author credits count, as does an
// unlabelled junction row (older rows carry no role). Narrators, editors,
// translators and the like are not the author whose catalog we want.
func authorRoleIncluded(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "author", "co-author", "coauthor", "primary":
		return true
	}
	return false
}

const scopePageSize = 5000

// BuildScope lists the authors to harvest (R10 + owner D1): authors, by
// author role, of books that are not soft-deleted and have at least one
// present book_file, excluding names authorjunk classifies as junk
// ("Various", "Unknown", publisher-as-author...). It is a plain local
// predicate on purpose: over-inclusion only costs requests.
func BuildScope(ctx context.Context, store any, rd ScopeReader) ([]ScopeAuthor, ScopeCensus, error) {
	var census ScopeCensus
	present, err := PresentFileBookIDs(store)
	if err != nil {
		return nil, census, fmt.Errorf("catalog scope: present files: %w", err)
	}

	type acc struct {
		asins map[string]bool
		books int
	}
	byID := map[int]*acc{}
	for offset := 0; ; offset += scopePageSize {
		if err := ctx.Err(); err != nil {
			return nil, census, err
		}
		page, err := rd.GetAllBooksCore(scopePageSize, offset)
		if err != nil {
			return nil, census, fmt.Errorf("catalog scope: books at %d: %w", offset, err)
		}
		for i := range page {
			b := &page[i]
			census.BooksScanned++
			if b.IsSoftDeleted() {
				continue
			}
			if !present[b.ID] {
				census.BooksNoFiles++
				continue
			}
			census.BooksInScope++
			links, err := rd.GetBookAuthors(b.ID)
			if err != nil {
				return nil, census, fmt.Errorf("catalog scope: authors of %s: %w", b.ID, err)
			}
			var ids []int
			for _, l := range links {
				if authorRoleIncluded(l.Role) && !slices.Contains(ids, l.AuthorID) {
					ids = append(ids, l.AuthorID)
				}
			}
			if len(links) == 0 && b.AuthorID != nil {
				ids = append(ids, *b.AuthorID)
			}
			if len(ids) == 0 {
				census.BooksNoAuthor++
				continue
			}
			asin := ""
			if b.ASIN != nil {
				asin = strings.ToUpper(strings.TrimSpace(*b.ASIN))
			}
			for _, id := range ids {
				a := byID[id]
				if a == nil {
					a = &acc{asins: map[string]bool{}}
					byID[id] = a
				}
				a.books++
				if asin != "" {
					a.asins[asin] = true
				}
			}
		}
		if len(page) < scopePageSize {
			break
		}
	}

	ids := make([]int, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	names, err := rd.GetAuthorsByIDs(ids)
	if err != nil {
		return nil, census, fmt.Errorf("catalog scope: author names: %w", err)
	}

	byKey := map[string]*ScopeAuthor{}
	keyASINs := map[string]map[string]bool{}
	junk := map[string]bool{}
	for id, a := range byID {
		au := names[id]
		if au == nil || strings.TrimSpace(au.Name) == "" {
			continue
		}
		if authorjunk.ClassifyName(au.Name).Junk() {
			junk[au.Name] = true
			continue
		}
		key := HarvestKey(au.Name)
		if key == "" {
			continue
		}
		sa := byKey[key]
		if sa == nil {
			sa = &ScopeAuthor{Key: key, Name: au.Name}
			byKey[key] = sa
			keyASINs[key] = map[string]bool{}
		}
		sa.Books += a.books
		for asin := range a.asins {
			keyASINs[key][asin] = true
		}
	}
	census.JunkAuthorsSkipped = len(junk)

	out := make([]ScopeAuthor, 0, len(byKey))
	for key, sa := range byKey {
		for asin := range keyASINs[key] {
			sa.OwnedASINs = append(sa.OwnedASINs, asin)
		}
		slices.Sort(sa.OwnedASINs)
		if len(sa.OwnedASINs) > 0 {
			census.AuthorsWithASIN++
		}
		out = append(out, *sa)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	census.Authors = len(out)
	return out, census, nil
}
