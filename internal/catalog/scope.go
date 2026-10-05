// file: internal/catalog/scope.go
// version: 1.1.0
// guid: 5b9d3f27-8c1a-4e6b-9f42-1d7c0a8e3b56
// last-edited: 2026-10-05

package catalog

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
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
	// PublisherShapedSkipped: names with a studio word authorjunk's whole
	// list does not cover ("Big Finish Production").
	PublisherShapedSkipped int `json:"publisher_shaped_skipped"`
	// RoleMarkedSkipped: names carrying a contributor role ("Jay Rubin -
	// translator", or a credit list with one). A credit list's unmarked
	// person parts are harvested on their own (RoleMarkedSplit).
	RoleMarkedSkipped int `json:"role_marked_skipped"`
	RoleMarkedSplit   int `json:"role_marked_split"`
}

// scopePublisherWords mark a studio or company anywhere in a name. They
// extend authorjunk's publisher rule (which knows "productions" but not the
// singular, and whole publisher names only) for the harvest scope alone,
// where skipping a name costs nothing but a request it would have wasted.
var scopePublisherWords = map[string]bool{
	"production": true, "productions": true, "studio": true, "studios": true,
	"publishing": true, "publisher": true, "publishers": true,
	"entertainment": true, "llc": true, "inc": true, "ltd": true,
}

// creditPieceRe splits a credit list on its list separators (not "and",
// which joins names inside one credit as often as it joins two credits).
var creditPieceRe = regexp.MustCompile(`\s*[,;/&]\s*`)

// Scope skip reasons.
const (
	scopeSkipJunk      = "junk"
	scopeSkipPublisher = "publisher_shaped"
	scopeSkipRole      = "role_marked"
)

// scopeNames decides what one author row contributes to the harvest scope:
// the names to harvest, or why it is skipped. A name with a role marker in
// any of its credit-list pieces ("Haruki Murakami, Jay Rubin - translator,
// Philip Gabriel - translator") is never harvested whole; its unmarked
// pieces are, when each is person-shaped and not junk ("Haruki Murakami"),
// and split is set. Otherwise the whole name is skipped as role-marked.
func scopeNames(name string) (names []string, skip string, split bool) {
	name = strings.TrimSpace(name)
	if authorjunk.ClassifyName(name).Junk() {
		return nil, scopeSkipJunk, false
	}
	if publisherShaped(name) {
		return nil, scopeSkipPublisher, false
	}
	pieces := creditPieceRe.Split(name, -1)
	var plain []string
	marked := false
	for _, p := range pieces {
		bare, role := metadata.ClassifyContributor(p)
		if role != metadata.RoleAuthor {
			marked = true
			continue
		}
		if bare = strings.TrimSpace(bare); bare != "" {
			plain = append(plain, bare)
		}
	}
	if !marked {
		return []string{name}, "", false
	}
	if len(pieces) == 1 {
		return nil, scopeSkipRole, false
	}
	for _, p := range plain {
		if !personname.LooksLikePersonName(p) || authorjunk.ClassifyName(p).Junk() || publisherShaped(p) {
			return nil, scopeSkipRole, false
		}
	}
	if len(plain) == 0 {
		return nil, scopeSkipRole, false
	}
	return plain, "", true
}

func publisherShaped(name string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if scopePublisherWords[w] {
			return true
		}
	}
	return false
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
	skipped := map[string]map[string]bool{}
	splitNames := map[string]bool{}
	for id, a := range byID {
		au := names[id]
		if au == nil || strings.TrimSpace(au.Name) == "" {
			continue
		}
		harvest, skip, split := scopeNames(au.Name)
		if skip != "" {
			if skipped[skip] == nil {
				skipped[skip] = map[string]bool{}
			}
			skipped[skip][au.Name] = true
			continue
		}
		if split {
			splitNames[au.Name] = true
		}
		for _, name := range harvest {
			key := HarvestKey(name)
			if key == "" {
				continue
			}
			sa := byKey[key]
			if sa == nil {
				sa = &ScopeAuthor{Key: key, Name: name}
				byKey[key] = sa
				keyASINs[key] = map[string]bool{}
			}
			sa.Books += a.books
			for asin := range a.asins {
				keyASINs[key][asin] = true
			}
		}
	}
	census.JunkAuthorsSkipped = len(skipped[scopeSkipJunk])
	census.PublisherShapedSkipped = len(skipped[scopeSkipPublisher])
	census.RoleMarkedSkipped = len(skipped[scopeSkipRole])
	census.RoleMarkedSplit = len(splitNames)

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
