// file: internal/database/pebble_store_authors.go
// version: 1.9.0
// guid: 1f8b9fd2-e424-4a09-9ee4-7b5b64660605
// last-edited: 2026-09-12

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

	sortByLowerName(authors, func(a Author) string { return a.Name })
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
	// Case- and whitespace-insensitive lookup; falls back to the pre-2026-09-12
	// key so entries written before NormalizeAuthor collapsed whitespace still
	// resolve (pebble_store_name_index.go).
	value, err := p.nameIndexGet(authorNameIndexKey, name)
	if err != nil || value == nil {
		return nil, err
	}

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return nil, err
	}

	return p.GetAuthorByID(id)
}

// CreateAuthor returns the author with this name, creating it if absent.
//
// The lookup and the insert are serialized, because they used to not be and the
// window was not narrow: measured on 2026-08-25, 24 concurrent calls with an
// IDENTICAL name produced 24 distinct author rows, reproducibly. The dedup check
// essentially never observed a concurrent write.
//
// That is worse than a few redundant rows. The author:name:<normalized> index maps
// one name to exactly ONE id, so every duplicate beyond the indexed one is
// UNREACHABLE by name lookup -- any code resolving a name to an id is then
// silently working on a different row than the one the books hang off. Production
// shows the shape already: two rows both named "Unknown Author", one with 0 books
// and one with 2,128.
//
// It bites in normal operation rather than under stress: the scanner resolves
// authors from inside its worker pool, once per book, so an import that first
// meets an author across several books at once mints a row per worker.
//
// This mirrors reviewMu, which exists in this same store for exactly this failure
// on review items: concurrent same-key writes duplicating rows. The lock is
// nameIdx.author, which every author:name: writer holds (UpdateAuthorName and
// DeleteAuthor too), so a create can also not interleave with a rename or a
// delete of the same key; see nameIndexLocks for the lock order.
func (p *PebbleStore) CreateAuthor(name string) (*Author, error) {
	// Fast path: an existing author needs no lock. This is the overwhelmingly
	// common case -- authors are resolved once per book but created once per
	// author -- so the lock must not sit on every resolve.
	existing, err := p.GetAuthorByName(name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	p.nameIdx.author.Lock()
	defer p.nameIdx.author.Unlock()

	// Re-check UNDER the lock. This is the entire fix: another goroutine may have
	// created this author between the fast-path miss above and acquiring the lock,
	// and without this re-read we would mint a second row for it. GetAuthorByName
	// reads the author:name index straight from Pebble, so it sees any committed
	// write; and because UpsertAuthorToMemDB below runs before the lock is
	// released, a waiter observes both the durable row and the memdb projection.
	existing, err = p.GetAuthorByName(name)
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
	// Held from before the row read until after the commit, so the name read
	// here and the ownership checks below cannot go stale: the author lock for
	// author:name:, then the alias lock for the alias cascade's
	// author_alias:name: deletes (lock order in nameIndexLocks).
	p.nameIdx.author.Lock()
	defer p.nameIdx.author.Unlock()
	p.nameIdx.alias.Lock()
	defer p.nameIdx.alias.Unlock()

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
	// Ownership-checked: another row whose name collapses to the same key may
	// own the entry, and deleting it would make that row unfindable by name.
	// The check stays true until the commit below only because nameIdx.author
	// is held: no rename or create can take the key over in between.
	if err := p.deleteNameIndexIfOwned(batch, nil, authorNameIndexKey, author.Name, nameIndexOwner(id)); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author:name: %w", err)
	}

	// Delete aliases for this author (cascade)
	if err := p.deleteAuthorAliases(batch, id); err != nil {
		batch.Close()
		return fmt.Errorf("delete author aliases: %w", err)
	}

	// Drop this author from the book_authors junction table.
	//
	// The junction is stored one row per book — key "book_authors:<bookID>",
	// value a JSON array of BookAuthor — so a row cannot simply be deleted:
	// it may still carry co-authors that must survive. Each matching row is
	// rewritten without this author, and only deleted outright when the author
	// was its sole entry. Both writes join the batch the author row is deleted
	// in, so the junction never outlives the author it points at.
	//
	// Cost: one full scan of the junction keyspace per delete. There is no
	// author -> books reverse index in Pebble, and building one needs a
	// backfill migration. Bulk callers (the purge-empty-authors maintenance
	// task) pay this per author.
	affected, sweepErr := p.sweepAuthorFromBookAuthors(batch, id)
	if sweepErr != nil {
		batch.Close()
		return sweepErr
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteAuthorFromMemDB(id)
	p.DeleteAuthorAliasesByAuthorIDFromMemDB(id)
	// Mirror the junction rewrites into memdb. Pebble is the source of truth,
	// but the query layer reads memdb when it is enabled, so skipping this
	// leaves the orphaned association visible to every reader that matters.
	for bookID, remaining := range affected {
		p.ReplaceBookAuthorsInMemDB(bookID, remaining)
	}
	return nil
}

// sweepAuthorFromBookAuthors stages, on batch, the removal of authorID from
// every book_authors:<bookID> row that references it. It returns the surviving
// association slice for each row it touched, keyed by book ID, so the caller
// can replay the same edit into memdb once the batch commits.
func (p *PebbleStore) sweepAuthorFromBookAuthors(batch *pebble.Batch, authorID int) (map[string][]BookAuthor, error) {
	// prefixUpperBound, not a hand-written "book_authors:~" sentinel. The key
	// is book_authors:<bookID> and bookID is an opaque string: a literal '~'
	// (0x7E) upper bound silently excludes any id whose first byte is higher,
	// which includes every non-ASCII id (UTF-8 continuation bytes start at
	// 0xC2). Rows skipped by the sweep are exactly the orphaned junction rows
	// this function exists to remove, so a too-low bound reintroduces the bug
	// in a narrower, harder-to-see form.
	prefix := []byte("book_authors:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("pebble iterate book_authors: %w", err)
	}
	defer iter.Close()

	affected := make(map[string][]BookAuthor)
	for iter.First(); iter.Valid(); iter.Next() {
		val, valErr := iter.ValueAndErr()
		if valErr != nil {
			return nil, fmt.Errorf("pebble read book_authors row: %w", valErr)
		}
		var authors []BookAuthor
		if json.Unmarshal(val, &authors) != nil {
			// A row we cannot parse is a row we cannot safely rewrite; leaving
			// it alone is strictly better than dropping other authors' links.
			continue
		}
		remaining := make([]BookAuthor, 0, len(authors))
		for _, a := range authors {
			if a.AuthorID != authorID {
				remaining = append(remaining, a)
			}
		}
		if len(remaining) == len(authors) {
			continue // author not in this book
		}
		// iter.Key() is invalidated by Next(); string() copies.
		key := string(iter.Key())
		bookID := strings.TrimPrefix(key, "book_authors:")
		if len(remaining) == 0 {
			if err := batch.Delete([]byte(key), nil); err != nil {
				return nil, fmt.Errorf("pebble Delete %s: %w", key, err)
			}
			affected[bookID] = nil
			continue
		}
		data, mErr := json.Marshal(remaining)
		if mErr != nil {
			return nil, fmt.Errorf("marshal remaining book_authors for %s: %w", bookID, mErr)
		}
		if err := batch.Set([]byte(key), data, nil); err != nil {
			return nil, fmt.Errorf("pebble Set %s: %w", key, err)
		}
		affected[bookID] = remaining
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("pebble iterate book_authors: %w", err)
	}
	return affected, nil
}

func (p *PebbleStore) UpdateAuthorName(id int, name string) error {
	// Held from before the row read: the old name read here decides which key
	// the ownership-checked delete targets, so a concurrent rename or delete of
	// this row, or a create claiming either key, must not interleave.
	p.nameIdx.author.Lock()
	defer p.nameIdx.author.Unlock()

	author, err := p.GetAuthorByID(id)
	if err != nil {
		return err
	}
	if author == nil {
		return fmt.Errorf("author %d not found", id)
	}

	batch := p.db.NewBatch()
	// Remove old name index, only where it still points at this author.
	if err := p.deleteNameIndexIfOwned(batch, nil, authorNameIndexKey, author.Name, nameIndexOwner(id)); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author:name: %w", err)
	}
	newIndexKey := []byte(authorNameIndexKey(util.NormalizeAuthor(name)))
	if prev, err := p.getValueCopy(newIndexKey); err != nil {
		batch.Close()
		return err
	} else if prev != nil && string(prev) != strconv.Itoa(id) {
		// Pre-existing behavior, kept: the rename takes the key over. Logged
		// because the other author stops resolving by name from here on.
		slog.Warn("UpdateAuthorName: name index already held by another author; repointing it",
			"author_id", id, "new_name", name, "previous_owner", string(prev))
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
	if err := batch.Set(newIndexKey, []byte(strconv.Itoa(id)), nil); err != nil {
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
	sortAuthorAliases(aliases)
	return aliases, nil
}

func (p *PebbleStore) CreateAuthorAlias(authorID int, aliasName string, aliasType string) (*AuthorAlias, error) {
	if aliasType == "" {
		aliasType = "alias"
	}

	// The duplicate check and the commit that claims the key are one step
	// with respect to every other author_alias:name: writer; without the lock
	// two concurrent calls could both see the name free and both mint an alias.
	p.nameIdx.alias.Lock()
	defer p.nameIdx.alias.Unlock()

	// Check for duplicate, under the current and the legacy key.
	nameKey := aliasNameIndexKey(util.NormalizeAuthor(aliasName))
	if existing, err := p.nameIndexGet(aliasNameIndexKey, aliasName); err != nil {
		return nil, err
	} else if existing != nil {
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
	// Held across the row read, the ownership check and the commit; see
	// nameIndexLocks.
	p.nameIdx.alias.Lock()
	defer p.nameIdx.alias.Unlock()

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
	if err := p.deleteNameIndexIfOwned(batch, nil, aliasNameIndexKey, alias.AliasName, nameIndexOwner(id)); err != nil {
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
	value, err := p.nameIndexGet(aliasNameIndexKey, aliasName)
	if err != nil || value == nil {
		return nil, err
	}
	aliasID, _ := strconv.Atoi(string(value))

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
// The caller must hold nameIdx.alias until batch commits, because the
// ownership checks on the author_alias:name: entries are only sound under it.
// It does not lock itself: its one caller, DeleteAuthor, already holds
// nameIdx.author and nameIdx.alias, and sync.Mutex is not reentrant.
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
			if err := p.deleteNameIndexIfOwned(batch, nil, aliasNameIndexKey, alias.AliasName, nameIndexOwner(aliasID)); err != nil {
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
	// The key names the book; a row stored before 2026-09-12 may not. Stamp it
	// so no reader -- above all UpsertBookToMemDB's reload, whose memdb insert
	// rejects an empty BookID and aborts the whole book -- sees the gap. See
	// junction_bookid.go.
	stampBookAuthorsInPlace(bookID, authors)
	return authors, nil
}

// SetBookAuthors replaces the book's author credits. The store owns the
// row's BookID: every row is stamped with bookID before it is persisted, since
// bookID is the key the rows live under and a row carrying anything else would
// be indexed under the wrong book (or rejected) by memdb. A caller-supplied
// BookID naming a DIFFERENT book is overridden, not rejected, and logged: the
// call says "these are bookID's credits", every copy-shaped call site already
// stamps the target, and rejecting would turn a caller bug into a lost write.
func (p *PebbleStore) SetBookAuthors(bookID string, authors []BookAuthor) error {
	key := []byte(fmt.Sprintf("book_authors:%s", bookID))
	// Copy before stamping: the caller may still own and reuse the slice.
	authors = append([]BookAuthor(nil), authors...)
	if n := stampBookAuthorsInPlace(bookID, authors); n > 0 {
		slog.Warn("SetBookAuthors: overriding caller-supplied book_id that names a different book",
			"book_id", bookID, "mismatched_rows", n)
	}
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
		if bookIsSoftDeleted(book) {
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
		if bookIsSoftDeleted(&b) {
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
		if bookIsSoftDeleted(&b) {
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
	// Fast path: return the existing narrator without taking the counter lock.
	existing, err := p.GetNarratorByName(name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	// Allocation path. nameIdx.narrator makes the name check and the commit
	// that claims narrator_name:<norm> one step with respect to every other
	// writer of that key (DeleteNarrator included; see nameIndexLocks for the
	// lock order). counterMu, nested inside it, serializes the narrator_counter
	// read-modify-write -- the same mutex nextID uses for every other ID -- so
	// concurrent CreateNarrator calls can't allocate a duplicate/lost narrator
	// ID. Record + name index + counter commit in a SINGLE batch so a crash
	// can't leave a record with no name index (or an un-bumped counter).
	// NOTE: we keep the legacy narrator_counter key rather than switching to
	// nextID("narrator"), which uses a different (uninitialized) counter:narrator
	// key that would collide with / orphan the existing narrator numbering. Both
	// locks are held across the pebble.Sync commit, which is acceptable on this
	// cold create path.
	p.nameIdx.narrator.Lock()
	defer p.nameIdx.narrator.Unlock()
	p.counterMu.Lock()
	defer p.counterMu.Unlock()

	// Re-check under the lock: another goroutine may have created it between our
	// fast-path check and acquiring the lock.
	if existing, err := p.GetNarratorByName(name); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	counterKey := []byte("narrator_counter")
	var nextID int
	if val, closer, err := p.db.Get(counterKey); err == nil {
		_ = json.Unmarshal(val, &nextID)
		closer.Close()
	}
	nextID++

	narrator := &Narrator{ID: nextID, Name: name, CreatedAt: time.Now()}
	data, err := json.Marshal(narrator)
	if err != nil {
		return nil, err
	}
	idData, err := json.Marshal(nextID)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()
	if err := batch.Set([]byte(fmt.Sprintf("narrator:%d", nextID)), data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	nameKey := []byte(narratorNameIndexKey(util.NormalizeAuthor(name)))
	if err := batch.Set(nameKey, idData, nil); err != nil {
		batch.Close()
		return nil, fmt.Errorf("pebble Set narrator name index: %w", err)
	}
	if err := batch.Set(counterKey, idData, nil); err != nil {
		batch.Close()
		return nil, fmt.Errorf("pebble Set narrator counter: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
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
	val, err := p.nameIndexGet(narratorNameIndexKey, name)
	if err != nil || val == nil {
		return nil, err
	}

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
	// Key-authoritative stamp; see GetBookAuthors and junction_bookid.go.
	stampBookNarratorsInPlace(bookID, narrators)
	return narrators, nil
}

// SetBookNarrators replaces the book's narrator credits, stamping bookID onto
// every row before it is persisted. Same contract and same reasoning as
// SetBookAuthors: before this, POST /operations/optimize-database and PUT
// /audiobooks/:id/narrators could store rows with an empty book_id, which
// memdb rejects -- and every later memdb upsert of the book then failed on it.
func (p *PebbleStore) SetBookNarrators(bookID string, narrators []BookNarrator) error {
	key := []byte(fmt.Sprintf("book_narrators:%s", bookID))
	narrators = append([]BookNarrator(nil), narrators...)
	if n := stampBookNarratorsInPlace(bookID, narrators); n > 0 {
		slog.Warn("SetBookNarrators: overriding caller-supplied book_id that names a different book",
			"book_id", bookID, "mismatched_rows", n)
	}
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

// DeleteNarrator removes a narrator and every reference the store holds to it,
// mirroring DeleteAuthor: the narrator:<id> record, its narrator_name:<norm>
// index entries (underscore, not colon — see CreateNarrator; current and
// legacy key, each only while this row still owns it), and its entries in
// the book_narrators junction all go in one Pebble batch, then memdb is
// brought in line. A missing id is a no-op returning nil, as with DeleteAuthor.
// nameIdx.narrator is held from the row read through the commit, which is what
// keeps the ownership check true until the delete lands.
func (p *PebbleStore) DeleteNarrator(id int) error {
	p.nameIdx.narrator.Lock()
	defer p.nameIdx.narrator.Unlock()

	narrator, err := p.GetNarratorByID(id)
	if err != nil {
		return err
	}
	if narrator == nil {
		return nil
	}

	batch := p.db.NewBatch()
	if err := batch.Delete([]byte(fmt.Sprintf("narrator:%d", id)), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete narrator:%d: %w", id, err)
	}
	// Ownership-checked, as in DeleteAuthor: another narrator whose name
	// collapses to the same key may own the current entry, and deleting it
	// would make that narrator unfindable by name (the next CreateNarrator
	// would then mint a duplicate). This row's own legacy entry, if it has
	// one, is removed too. The check holds until the commit only because
	// nameIdx.narrator is held.
	if err := p.deleteNameIndexIfOwned(batch, nil, narratorNameIndexKey, narrator.Name, nameIndexOwner(id)); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete narrator_name: %w", err)
	}

	// Drop this narrator from the book_narrators junction in the same batch,
	// so no book keeps a NarratorID that no longer resolves. Same shape and
	// cost as DeleteAuthor's sweep: one full scan of the junction keyspace,
	// because there is no narrator -> books reverse index in Pebble.
	affected, sweepErr := p.sweepNarratorFromBookNarrators(batch, id)
	if sweepErr != nil {
		batch.Close()
		return sweepErr
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteNarratorFromMemDB(id)
	// Mirror the junction rewrites into memdb; Pebble is the source of truth,
	// but readers running with memdb enabled answer from memdb.
	for bookID, remaining := range affected {
		p.ReplaceBookNarratorsInMemDB(bookID, remaining)
	}
	return nil
}

// sweepNarratorFromBookNarrators stages, on batch, the removal of narratorID
// from every book_narrators:<bookID> row that references it. It returns the
// surviving association slice for each row it touched, keyed by book ID, so the
// caller can replay the same edit into memdb once the batch commits. It is the
// narrator twin of sweepAuthorFromBookAuthors; see that function for the
// reasoning behind the upper bound and the skip-unparseable-row rule.
func (p *PebbleStore) sweepNarratorFromBookNarrators(batch *pebble.Batch, narratorID int) (map[string][]BookNarrator, error) {
	prefix := []byte("book_narrators:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("pebble iterate book_narrators: %w", err)
	}
	defer iter.Close()

	affected := make(map[string][]BookNarrator)
	for iter.First(); iter.Valid(); iter.Next() {
		val, valErr := iter.ValueAndErr()
		if valErr != nil {
			return nil, fmt.Errorf("pebble read book_narrators row: %w", valErr)
		}
		var narrators []BookNarrator
		if json.Unmarshal(val, &narrators) != nil {
			// A row we cannot parse is a row we cannot safely rewrite.
			continue
		}
		// iter.Key() is invalidated by Next(); string() copies.
		key := string(iter.Key())
		bookID := strings.TrimPrefix(key, "book_narrators:")
		remaining := make([]BookNarrator, 0, len(narrators))
		for _, n := range narrators {
			if n.NarratorID == narratorID {
				continue
			}
			// memdb's book_narrators primary index is a non-AllowMissing
			// compound on {BookID, NarratorID}, and ReplaceBookNarratorsInMemDB
			// (unlike ReplaceBookAuthorsInMemDB) does not backfill BookID. A
			// surviving row with an empty BookID would abort the memdb replay
			// below and drop every row for this book from memdb, so fill it
			// from the key — which is where the BookID is authoritatively held.
			// This only fires on rows the sweep is already rewriting; it is not
			// a general repair of rows that never referenced this narrator.
			if n.BookID == "" {
				n.BookID = bookID
			}
			remaining = append(remaining, n)
		}
		if len(remaining) == len(narrators) {
			continue // narrator not on this book; leave the row untouched
		}
		if len(remaining) == 0 {
			if err := batch.Delete([]byte(key), nil); err != nil {
				return nil, fmt.Errorf("pebble Delete %s: %w", key, err)
			}
			affected[bookID] = nil
			continue
		}
		data, mErr := json.Marshal(remaining)
		if mErr != nil {
			return nil, fmt.Errorf("marshal remaining book_narrators for %s: %w", bookID, mErr)
		}
		if err := batch.Set([]byte(key), data, nil); err != nil {
			return nil, fmt.Errorf("pebble Set %s: %w", key, err)
		}
		affected[bookID] = remaining
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("pebble iterate book_narrators: %w", err)
	}
	return affected, nil
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
