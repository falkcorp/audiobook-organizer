// file: internal/database/pebble_store_authors.go
// version: 1.1.0
// guid: 1f8b9fd2-e424-4a09-9ee4-7b5b64660605
// last-edited: 2026-07-05

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func (p *PebbleStore) GetAllAuthors() ([]Author, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthors()
	}
	var authors []Author
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author:0"),
		UpperBound: []byte("author:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys
		if strings.Contains(string(iter.Key()), ":name:") {
			continue
		}

		var author Author
		if err := json.Unmarshal(iter.Value(), &author); err != nil {
			return nil, err
		}
		authors = append(authors, author)
	}

	return authors, nil
}

func (p *PebbleStore) GetAuthorByID(id int) (*Author, error) {
	key := []byte(fmt.Sprintf("author:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		// Check for tombstone redirect
		canonicalID, tErr := p.GetAuthorTombstone(id)
		if tErr != nil || canonicalID == 0 {
			return nil, nil
		}
		return p.GetAuthorByID(canonicalID)
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var author Author
	if err := json.Unmarshal(value, &author); err != nil {
		return nil, err
	}
	return &author, nil
}

// GetAuthorsByIDs returns a map from authorID → *Author for the given IDs.
// Deduplicates IDs before fetching; missing IDs are absent from the result map.
func (p *PebbleStore) GetAuthorsByIDs(ids []int) (map[int]*Author, error) {
	result := make(map[int]*Author, len(ids))
	for _, id := range ids {
		if _, already := result[id]; already {
			continue
		}
		a, err := p.GetAuthorByID(id)
		if err != nil {
			return nil, err
		}
		if a != nil {
			result[id] = a
		}
	}
	return result, nil
}

func (p *PebbleStore) GetAuthorByName(name string) (*Author, error) {
	// Use lowercase for case-insensitive lookup
	indexKey := []byte(fmt.Sprintf("author:name:%s", util.NormalizeAuthor(name)))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return nil, err
	}

	return p.GetAuthorByID(id)
}

func (p *PebbleStore) CreateAuthor(name string) (*Author, error) {
	// Check if author already exists
	existing, err := p.GetAuthorByName(name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	id, err := p.nextID("author")
	if err != nil {
		return nil, err
	}

	author := &Author{ID: id, Name: name}
	data, err := json.Marshal(author)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("author:%d", id))
	// Use lowercase for case-insensitive lookup
	indexKey := []byte(fmt.Sprintf("author:name:%s", util.NormalizeAuthor(name)))

	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return nil, err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	p.UpsertAuthorToMemDB(author)
	return author, nil
}

func (p *PebbleStore) DeleteAuthor(id int) error {
	// Get the author to find name for index cleanup
	author, err := p.GetAuthorByID(id)
	if err != nil {
		return err
	}
	if author == nil {
		return nil
	}

	batch := p.db.NewBatch()
	if err := batch.Delete([]byte(fmt.Sprintf("author:%d", id)), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author:%d: %w", id, err)
	}
	if err := batch.Delete([]byte(fmt.Sprintf("author:name:%s", util.NormalizeAuthor(author.Name))), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author:name: %w", err)
	}

	// Delete aliases for this author (cascade)
	if err := p.deleteAuthorAliases(batch, id); err != nil {
		batch.Close()
		return fmt.Errorf("delete author aliases: %w", err)
	}

	// Delete book_author entries for this author
	iter, iterErr := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_author:"),
		UpperBound: []byte("book_author;"),
	})
	if iterErr == nil {
		defer iter.Close()
		for iter.First(); iter.Valid(); iter.Next() {
			val, valErr := iter.ValueAndErr()
			if valErr != nil {
				continue
			}
			var ba BookAuthor
			if json.Unmarshal(val, &ba) == nil && ba.AuthorID == id {
				if err := batch.Delete(iter.Key(), nil); err != nil {
					batch.Close()
					return fmt.Errorf("pebble Delete book_author entry: %w", err)
				}
			}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteAuthorFromMemDB(id)
	p.DeleteAuthorAliasesByAuthorIDFromMemDB(id)
	return nil
}

func (p *PebbleStore) UpdateAuthorName(id int, name string) error {
	author, err := p.GetAuthorByID(id)
	if err != nil {
		return err
	}
	if author == nil {
		return fmt.Errorf("author %d not found", id)
	}

	batch := p.db.NewBatch()
	// Remove old name index
	if err := batch.Delete([]byte(fmt.Sprintf("author:name:%s", util.NormalizeAuthor(author.Name))), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author:name: %w", err)
	}

	// Update author record
	author.Name = name
	data, err := json.Marshal(author)
	if err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set([]byte(fmt.Sprintf("author:%d", id)), data, nil); err != nil {
		batch.Close()
		return err
	}
	// Add new name index
	if err := batch.Set([]byte(fmt.Sprintf("author:name:%s", util.NormalizeAuthor(name))), []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.UpsertAuthorToMemDB(author)
	return nil
}

func (p *PebbleStore) GetAuthorAliases(authorID int) ([]AuthorAlias, error) {
	prefix := []byte(fmt.Sprintf("author_alias:author:%d:", authorID))
	upper := []byte(fmt.Sprintf("author_alias:author:%d;", authorID))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var aliases []AuthorAlias
	for iter.First(); iter.Valid(); iter.Next() {
		var alias AuthorAlias
		if err := json.Unmarshal(iter.Value(), &alias); err != nil {
			// Fallback for legacy format: iter.Value() is just an alias ID
			if aliasID, err := strconv.Atoi(string(iter.Value())); err == nil {
				if legacyAlias, err := p.getAuthorAliasByID(aliasID); err == nil && legacyAlias != nil {
					alias = *legacyAlias
				} else {
					continue
				}
			} else {
				continue
			}
		}
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].AliasName < aliases[j].AliasName })
	return aliases, nil
}

func (p *PebbleStore) GetAllAuthorAliases() ([]AuthorAlias, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthorAliases()
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author_alias:0"),
		UpperBound: []byte("author_alias:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var aliases []AuthorAlias
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Only match primary records (author_alias:<digits>), skip index keys
		if strings.Contains(key, ":author:") || strings.Contains(key, ":name:") {
			continue
		}
		var a AuthorAlias
		if err := json.Unmarshal(iter.Value(), &a); err != nil {
			return nil, err
		}
		aliases = append(aliases, a)
	}
	return aliases, nil
}

func (p *PebbleStore) CreateAuthorAlias(authorID int, aliasName string, aliasType string) (*AuthorAlias, error) {
	if aliasType == "" {
		aliasType = "alias"
	}

	// Check for duplicate
	nameKey := fmt.Sprintf("author_alias:name:%s", util.NormalizeAuthor(aliasName))
	if _, closer, err := p.db.Get([]byte(nameKey)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("alias %q already exists", aliasName)
	}

	id, err := p.nextID("author_alias")
	if err != nil {
		return nil, err
	}

	alias := AuthorAlias{
		ID:        id,
		AuthorID:  authorID,
		AliasName: aliasName,
		AliasType: aliasType,
		CreatedAt: time.Now(),
	}

	data, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()
	if err := batch.Set([]byte(fmt.Sprintf("author_alias:%d", id)), data, nil); err != nil {
		batch.Close()
		return nil, fmt.Errorf("pebble Set author_alias:%d: %w", id, err)
	}
	if err := batch.Set([]byte(fmt.Sprintf("author_alias:author:%d:%d", authorID, id)), data, nil); err != nil {
		batch.Close()
		return nil, fmt.Errorf("pebble Set author_alias:author index: %w", err)
	}
	if err := batch.Set([]byte(nameKey), []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return nil, fmt.Errorf("pebble Set author_alias name index: %w", err)
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		batch.Close()
		return nil, err
	}
	p.UpsertAuthorAliasToMemDB(&alias)
	return &alias, nil
}

func (p *PebbleStore) DeleteAuthorAlias(id int) error {
	alias, err := p.getAuthorAliasByID(id)
	if err != nil {
		return err
	}
	if alias == nil {
		return nil
	}

	batch := p.db.NewBatch()
	if err := batch.Delete([]byte(fmt.Sprintf("author_alias:%d", id)), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author_alias:%d: %w", id, err)
	}
	if err := batch.Delete([]byte(fmt.Sprintf("author_alias:author:%d:%d", alias.AuthorID, id)), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author_alias:author index: %w", err)
	}
	if err := batch.Delete([]byte(fmt.Sprintf("author_alias:name:%s", util.NormalizeAuthor(alias.AliasName))), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author_alias:name index: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteAuthorAliasFromMemDB(id)
	return nil
}

func (p *PebbleStore) FindAuthorByAlias(aliasName string) (*Author, error) {
	nameKey := []byte(fmt.Sprintf("author_alias:name:%s", util.NormalizeAuthor(aliasName)))
	value, closer, err := p.db.Get(nameKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	aliasID, _ := strconv.Atoi(string(value))
	closer.Close()

	alias, err := p.getAuthorAliasByID(aliasID)
	if err != nil || alias == nil {
		return nil, err
	}
	return p.GetAuthorByID(alias.AuthorID)
}

func (p *PebbleStore) getAuthorAliasByID(id int) (*AuthorAlias, error) {
	key := []byte(fmt.Sprintf("author_alias:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var alias AuthorAlias
	if err := json.Unmarshal(value, &alias); err != nil {
		return nil, err
	}
	return &alias, nil
}

// deleteAuthorAliases removes all aliases for an author (cascade on delete).
func (p *PebbleStore) deleteAuthorAliases(batch *pebble.Batch, authorID int) error {
	prefix := []byte(fmt.Sprintf("author_alias:author:%d:", authorID))
	upper := []byte(fmt.Sprintf("author_alias:author:%d;", authorID))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		aliasID, _ := strconv.Atoi(string(iter.Value()))
		alias, err := p.getAuthorAliasByID(aliasID)
		if err != nil {
			return err
		}
		if alias != nil {
			if err := batch.Delete([]byte(fmt.Sprintf("author_alias:%d", aliasID)), nil); err != nil {
				return fmt.Errorf("pebble Delete author_alias:%d: %w", aliasID, err)
			}
			if err := batch.Delete([]byte(fmt.Sprintf("author_alias:name:%s", util.NormalizeAuthor(alias.AliasName))), nil); err != nil {
				return fmt.Errorf("pebble Delete author_alias:name index: %w", err)
			}
		}
		if err := batch.Delete(iter.Key(), nil); err != nil {
			return fmt.Errorf("pebble Delete author_alias:author index: %w", err)
		}
	}
	return nil
}

func (p *PebbleStore) GetBookAuthors(bookID string) ([]BookAuthor, error) {
	key := []byte(fmt.Sprintf("book_authors:%s", bookID))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	var authors []BookAuthor
	if err := json.Unmarshal(val, &authors); err != nil {
		return nil, err
	}
	return authors, nil
}

func (p *PebbleStore) SetBookAuthors(bookID string, authors []BookAuthor) error {
	key := []byte(fmt.Sprintf("book_authors:%s", bookID))
	data, err := json.Marshal(authors)
	if err != nil {
		return err
	}
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return err
	}
	p.ReplaceBookAuthorsInMemDB(bookID, authors)
	return nil
}

func (p *PebbleStore) GetAllAuthorBookCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthorBookCounts()
	}
	// Full Pebble book scan combined with junction table scan.
	counts := make(map[int]int)

	// Pass 1: scan book_authors junction table (multi-author associations).
	// Track which books have junction entries so we don't double-count.
	bookHasJunction := make(map[string]bool)
	jIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_authors:"),
		UpperBound: []byte("book_authors:~"),
	})
	if err != nil {
		return nil, err
	}
	for jIter.First(); jIter.Valid(); jIter.Next() {
		var authors []BookAuthor
		if json.Unmarshal(jIter.Value(), &authors) != nil {
			continue
		}
		key := string(jIter.Key())
		bookID := strings.TrimPrefix(key, "book_authors:")
		// Look up the book to check primary/deletion flags.
		book, _ := p.GetBookByID(bookID)
		if book == nil {
			continue
		}
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		bookHasJunction[bookID] = true
		for _, a := range authors {
			counts[a.AuthorID]++
		}
	}
	jIter.Close()

	// Pass 2: scan books for the legacy AuthorID field (for books without junction entries).
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if bookHasJunction[b.ID] {
			continue // already counted via junction
		}
		if b.AuthorID == nil {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		counts[*b.AuthorID]++
	}

	return counts, nil
}

// GetAllAuthorFileCounts returns the number of audio files per author.
// Uses the in-memory query layer when enabled, otherwise the Pebble fallback.
func (p *PebbleStore) GetAllAuthorFileCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthorFileCounts()
	}
	return p.GetAllAuthorFileCounts_Pebble()
}

// GetAllAuthorFileCounts_Pebble returns the number of audio files per author using Pebble iteration.
// Full book scan fallback after book:author index removal (Task 3.4).
func (p *PebbleStore) GetAllAuthorFileCounts_Pebble() (map[int]int, error) {
	counts := make(map[int]int)

	// Phase 1: Full scan of primary book records to collect author-book relationships.
	// book:author prefix index removed in Task 3.4; must iterate all books instead.
	type AuthorBook struct {
		AuthorID int
		BookID   string
	}
	var authorBooks []AuthorBook

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}

		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.AuthorID == nil {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		authorBooks = append(authorBooks, AuthorBook{AuthorID: *b.AuthorID, BookID: b.ID})
	}
	iter.Close()

	// Phase 2: Batch-load all files for all books at once
	bookIDs := make([]string, len(authorBooks))
	for i, ab := range authorBooks {
		bookIDs[i] = ab.BookID
	}

	filesMap := make(map[string][]BookFileCore)
	if len(bookIDs) > 0 {
		if bfm, err := p.GetBookFilesForIDsCore(bookIDs); err == nil {
			filesMap = bfm
		}
	}

	// Phase 3: Count files per author
	for _, ab := range authorBooks {
		files := filesMap[ab.BookID]
		if len(files) == 0 {
			counts[ab.AuthorID]++
			continue
		}
		activeCount := 0
		for _, f := range files {
			if !f.Missing {
				activeCount++
			}
		}
		if activeCount > 0 {
			counts[ab.AuthorID] += activeCount
		} else {
			counts[ab.AuthorID]++
		}
	}

	return counts, nil
}

func (p *PebbleStore) CreateNarrator(name string) (*Narrator, error) {
	// Check if narrator already exists
	existing, err := p.GetNarratorByName(name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	// Generate a new ID by incrementing a counter
	counterKey := []byte("narrator_counter")
	var nextID int
	if val, closer, err := p.db.Get(counterKey); err == nil {
		json.Unmarshal(val, &nextID)
		closer.Close()
	}
	nextID++

	narrator := &Narrator{ID: nextID, Name: name, CreatedAt: time.Now()}
	data, err := json.Marshal(narrator)
	if err != nil {
		return nil, err
	}

	key := []byte(fmt.Sprintf("narrator:%d", nextID))
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return nil, err
	}

	// Save name index
	nameKey := []byte(fmt.Sprintf("narrator_name:%s", util.NormalizeAuthor(name)))
	idData, _ := json.Marshal(nextID)
	if err := p.db.Set(nameKey, idData, pebble.Sync); err != nil {
		return nil, fmt.Errorf("pebble Set narrator name index: %w", err)
	}

	// Update counter
	counterData, _ := json.Marshal(nextID)
	if err := p.db.Set(counterKey, counterData, pebble.Sync); err != nil {
		return nil, fmt.Errorf("pebble Set narrator counter: %w", err)
	}

	p.UpsertNarratorToMemDB(narrator)
	return narrator, nil
}

func (p *PebbleStore) GetNarratorByID(id int) (*Narrator, error) {
	key := []byte(fmt.Sprintf("narrator:%d", id))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	var narrator Narrator
	if err := json.Unmarshal(val, &narrator); err != nil {
		return nil, err
	}
	return &narrator, nil
}

func (p *PebbleStore) GetNarratorByName(name string) (*Narrator, error) {
	nameKey := []byte(fmt.Sprintf("narrator_name:%s", util.NormalizeAuthor(name)))
	val, closer, err := p.db.Get(nameKey)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	var id int
	if err := json.Unmarshal(val, &id); err != nil {
		return nil, err
	}
	return p.GetNarratorByID(id)
}

func (p *PebbleStore) ListNarrators() ([]Narrator, error) {
	var narrators []Narrator
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("narrator:"),
		UpperBound: []byte("narrator;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var n Narrator
		if err := json.Unmarshal(iter.Value(), &n); err == nil {
			narrators = append(narrators, n)
		}
	}
	// Sort alphabetically by name to match SQLiteStore ordering behaviour.
	sort.Slice(narrators, func(i, j int) bool {
		return narrators[i].Name < narrators[j].Name
	})
	return narrators, nil
}

func (p *PebbleStore) GetBookNarrators(bookID string) ([]BookNarrator, error) {
	key := []byte(fmt.Sprintf("book_narrators:%s", bookID))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	var narrators []BookNarrator
	if err := json.Unmarshal(val, &narrators); err != nil {
		return nil, err
	}
	return narrators, nil
}

func (p *PebbleStore) SetBookNarrators(bookID string, narrators []BookNarrator) error {
	key := []byte(fmt.Sprintf("book_narrators:%s", bookID))
	data, err := json.Marshal(narrators)
	if err != nil {
		return err
	}
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return err
	}
	p.ReplaceBookNarratorsInMemDB(bookID, narrators)
	return nil
}

// CreateAuthorTombstone writes a tombstone that redirects oldID to canonicalID.
func (p *PebbleStore) CreateAuthorTombstone(oldID, canonicalID int) error {
	key := []byte(fmt.Sprintf("author_tombstone:%d", oldID))
	value := []byte(strconv.Itoa(canonicalID))
	return p.db.Set(key, value, pebble.Sync)
}

// GetAuthorTombstone returns the canonical author ID for a tombstoned author.
// Returns 0 if no tombstone exists.
func (p *PebbleStore) GetAuthorTombstone(oldID int) (int, error) {
	key := []byte(fmt.Sprintf("author_tombstone:%d", oldID))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()

	canonicalID, err := strconv.Atoi(string(value))
	if err != nil {
		return 0, fmt.Errorf("invalid tombstone value for author %d: %w", oldID, err)
	}
	return canonicalID, nil
}

// ResolveTombstoneChains finds chains like A→B→C and collapses them so A→C, B→C.
// Returns the number of tombstones updated.
func (p *PebbleStore) ResolveTombstoneChains() (int, error) {
	// Collect all tombstones
	tombstones := make(map[int]int) // oldID → canonicalID
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author_tombstone:"),
		UpperBound: []byte("author_tombstone;"),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create tombstone iterator: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		parts := strings.SplitN(keyStr, ":", 2)
		if len(parts) != 2 {
			continue
		}
		oldID, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		val, valErr := iter.ValueAndErr()
		if valErr != nil {
			continue
		}
		canonicalID, err := strconv.Atoi(string(val))
		if err != nil {
			continue
		}
		tombstones[oldID] = canonicalID
	}

	// Resolve chains: follow each tombstone to its final destination
	updated := 0
	for oldID, canonicalID := range tombstones {
		finalID := canonicalID
		visited := map[int]bool{oldID: true}
		for {
			nextID, exists := tombstones[finalID]
			if !exists {
				break
			}
			if visited[finalID] {
				break // cycle detection
			}
			visited[finalID] = true
			finalID = nextID
		}
		if finalID != canonicalID {
			// Update the tombstone to point directly to the final destination
			key := []byte(fmt.Sprintf("author_tombstone:%d", oldID))
			if err := p.db.Set(key, []byte(strconv.Itoa(finalID)), pebble.Sync); err != nil {
				return updated, fmt.Errorf("failed to update tombstone %d: %w", oldID, err)
			}
			updated++
		}
	}

	return updated, nil
}

// GetAuthorsByBookIDs returns a map from bookID → []Author for all given book IDs.
func (p *PebbleStore) GetAuthorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Author, error) {
	if len(bookIDs) == 0 {
		return map[string][]Author{}, nil
	}
	result := make(map[string][]Author, len(bookIDs))
	for _, bookID := range bookIDs {
		bookAuthors, err := p.GetBookAuthors(bookID)
		if err != nil {
			return nil, fmt.Errorf("GetAuthorsByBookIDs %s: %w", bookID, err)
		}
		var authors []Author
		for _, ba := range bookAuthors {
			author, err := p.GetAuthorByID(ba.AuthorID)
			if err != nil {
				return nil, fmt.Errorf("GetAuthorsByBookIDs author lookup %d: %w", ba.AuthorID, err)
			}
			if author != nil {
				authors = append(authors, *author)
			}
		}
		result[bookID] = authors
	}
	return result, nil
}

// GetNarratorsByBookIDs returns a map from bookID → []Narrator for all given book IDs.
func (p *PebbleStore) GetNarratorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Narrator, error) {
	if len(bookIDs) == 0 {
		return map[string][]Narrator{}, nil
	}
	result := make(map[string][]Narrator, len(bookIDs))
	for _, bookID := range bookIDs {
		bookNarrators, err := p.GetBookNarrators(bookID)
		if err != nil {
			return nil, fmt.Errorf("GetNarratorsByBookIDs %s: %w", bookID, err)
		}
		var narrators []Narrator
		for _, bn := range bookNarrators {
			narrator, err := p.GetNarratorByID(bn.NarratorID)
			if err != nil {
				return nil, fmt.Errorf("GetNarratorsByBookIDs narrator lookup %d: %w", bn.NarratorID, err)
			}
			if narrator != nil {
				narrators = append(narrators, *narrator)
			}
		}
		result[bookID] = narrators
	}
	return result, nil
}
