// file: internal/database/book_row_iter.go
// version: 1.0.0
// guid: 9c032ba7-3cab-4fd8-9e0b-b08a0939dd0b
// last-edited: 2026-09-12

package database

import (
	"bytes"

	"github.com/cockroachdb/pebble/v2"
)

// Bare-row iteration over a Pebble key family laid out as
//
//	<family>:<id>                  the record row (JSON value)
//	<family>:<index>:<...>         secondary indexes sharing the prefix
//	<family>:<id>:<...>            per-record sub-keys
//
// "book:" is the family that matters: it holds book:<id> rows AND book:asin:,
// book:path:, book:hash:, book:isbn13:, book:organizedhash:,
// book:originalhash:, book:versiongroup:, book:work: indexes, roughly 6.5
// index keys per book row on production.
//
// THE RULE, in one place so no scan can get it wrong again:
//
//  1. The range is the whole prefix: ["book:", "book;"). ';' is the byte after
//     ':', so that is exactly the set of keys starting with "book:". Until
//     2026-09-12 some thirty scans used ["book:0", "book:;") instead, which
//     admits only '0'-'9' and ':' as the first ID byte. Every book whose ID
//     starts with a letter, '_' or '~' was silently skipped. ULIDs start with a
//     digit, so this was latent, but CreateBook keeps a caller-supplied ID,
//     the seed data mints "seed_<ULID>", and older rows cannot be ruled out.
//     One of those scans (getAllBooksCoreFromPebble) is the membership set the
//     orphan book_file sweep hard-deletes against, so a skipped book lost its
//     file rows.
//  2. Inside that range a record row is the key with NO further ':' after the
//     prefix. The narrow bound used to keep the letter-leading index families
//     out by accident; with the full range this structural filter is what does
//     it. The two halves are one rule and must never be separated: widening
//     without the filter feeds index values (bare IDs or empty bytes) to JSON
//     decoders, several of which fail the whole scan on a decode error.
//
// Record IDs containing ':' are therefore not iterable. That is the same
// contract warmIter's book callback (memdb_warmup.go) and every structural
// "strings.Count(key, \":\") == 1" check in this package already apply.
//
// warmIter is deliberately NOT built on this: it is a generic prefix scanner
// that reports every key it visits (its `scanned` count includes index keys by
// design), and its callers apply their own row filters.

// bookRowPrefix is the key prefix of every book record and book index key.
const bookRowPrefix = "book:"

// bareRowUpperBound returns the exclusive upper bound of the full key range of
// a "<family>:" prefix: the prefix with its trailing ':' replaced by ';'.
func bareRowUpperBound(prefix string) []byte {
	upper := []byte(prefix)
	upper[len(upper)-1] = ';'
	return upper
}

// bareRowIterReader is satisfied by *pebble.DB and *pebble.Snapshot.
type bareRowIterReader interface {
	NewIter(o *pebble.IterOptions) (*pebble.Iterator, error)
}

// bareRowIter walks only the record rows "<prefix><id>" of a key family, in
// key (plain byte) order. It is used exactly like a *pebble.Iterator:
//
//	it, err := newBookRowIter(p.db)
//	if err != nil { ... }
//	defer it.Close()
//	for it.First(); it.Valid(); it.Next() { ... it.Value() ... }
//	if err := it.Error(); err != nil { ... }
//
// Index and sub-key subtrees are stepped over with one SeekGE each rather than
// visited key by key: every key under "<prefix><seg>:" has a second colon, so
// seeking to "<prefix><seg>;" skips exactly that subtree and nothing else. A
// record whose ID is "<seg>" sorts before its subtree and one whose ID starts
// "<seg>;" sorts after the seek target, so neither is lost.
type bareRowIter struct {
	it     *pebble.Iterator
	prefix []byte
}

// newBookRowIter opens a bareRowIter over book:<id> rows.
func newBookRowIter(r bareRowIterReader) (*bareRowIter, error) {
	return newBareRowIter(r, bookRowPrefix)
}

// newBareRowIter opens a bareRowIter over the family named by prefix, which
// must end in ':'.
func newBareRowIter(r bareRowIterReader, prefix string) (*bareRowIter, error) {
	it, err := r.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: bareRowUpperBound(prefix),
	})
	if err != nil {
		return nil, err
	}
	return &bareRowIter{it: it, prefix: []byte(prefix)}, nil
}

// First positions the iterator at the first record row.
func (b *bareRowIter) First() bool { return b.settle(b.it.First()) }

// Next advances to the next record row.
func (b *bareRowIter) Next() bool { return b.settle(b.it.Next()) }

// SeekAfter positions the iterator at the first record row whose ID sorts
// strictly after afterID ("" means the start). The smallest key greater than
// "<prefix><afterID>" is that key plus a 0x00 byte, so this works whether or
// not afterID still exists.
func (b *bareRowIter) SeekAfter(afterID string) bool {
	if afterID == "" {
		return b.First()
	}
	target := make([]byte, 0, len(b.prefix)+len(afterID)+1)
	target = append(append(append(target, b.prefix...), afterID...), 0)
	return b.settle(b.it.SeekGE(target))
}

// Valid reports whether the iterator is positioned at a record row.
func (b *bareRowIter) Valid() bool { return b.it.Valid() }

// Key returns the current record row's full key. Valid until the next move.
func (b *bareRowIter) Key() []byte { return b.it.Key() }

// ID returns the current record row's ID (the key with the prefix stripped).
func (b *bareRowIter) ID() string { return string(b.it.Key()[len(b.prefix):]) }

// Value returns the current record row's value. Valid until the next move.
func (b *bareRowIter) Value() []byte { return b.it.Value() }

// ValueAndErr returns the current record row's value and any error reading it.
func (b *bareRowIter) ValueAndErr() ([]byte, error) { return b.it.ValueAndErr() }

// Error returns any accumulated iteration error. A scan that ends because of
// an error looks exactly like one that reached the end of the range, so
// callers that must not answer from a partial set check this.
func (b *bareRowIter) Error() error { return b.it.Error() }

// Close releases the iterator.
func (b *bareRowIter) Close() error { return b.it.Close() }

// settle moves the underlying iterator forward until it rests on a record row
// or runs off the range, and reports which.
func (b *bareRowIter) settle(valid bool) bool {
	for valid {
		// Every key in [prefix, prefix-with-';') starts with prefix.
		rest := b.it.Key()[len(b.prefix):]
		colon := bytes.IndexByte(rest, ':')
		if colon < 0 {
			if len(rest) > 0 {
				return true
			}
			// A key equal to the bare prefix names no record.
			valid = b.it.Next()
			continue
		}
		// An index or sub-key subtree "<prefix><seg>:...": jump past all of it.
		skip := make([]byte, 0, len(b.prefix)+colon+1)
		skip = append(append(append(skip, b.prefix...), rest[:colon]...), ';')
		valid = b.it.SeekGE(skip)
	}
	return false
}
