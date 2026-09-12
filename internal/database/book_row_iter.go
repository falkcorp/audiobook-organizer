// file: internal/database/book_row_iter.go
// version: 1.1.0
// guid: 9c032ba7-3cab-4fd8-9e0b-b08a0939dd0b
// last-edited: 2026-09-12

package database

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

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
//  3. A scan that stops because of a read error must FAIL, never return a short
//     result. A Pebble iterator that hits an error mid-range simply reports
//     Valid() == false, exactly as it does at the end of the range, and only
//     Error() (or Close()) tells the two apart. Until 2026-09-12 this file
//     handed callers a raw iterator and trusted each of them to check Error();
//     34 of the 44 did not, ListBookIDs and GetAllBooksFullFrom among them. So
//     the helpers below are visitors that OWN the iterator: they read every
//     value through ValueAndErr, check Error() after the loop, fold in Close(),
//     and return the result. A caller cannot forget a check it never sees.
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

// errStopScan ends a visitor scan early without an error: a visit callback
// returns it once it has what it needs (a page is full, a match was found), and
// the scan helper returns nil. It is the analogue of fs.SkipAll.
var errStopScan = errors.New("stop row scan")

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

// forEachBookRow calls visit with the ID and value of every book:<id> record
// row, in key (plain byte) order. See forEachBareRow for the contract.
func forEachBookRow(r bareRowIterReader, visit func(id string, value []byte) error) error {
	return scanBareRows(r, bookRowPrefix, "", visit)
}

// forEachBookRowAfter is forEachBookRow starting at the first book row whose
// ID sorts strictly after afterID ("" means the start). Whether or not afterID
// still exists, the scan resumes at its successor.
func forEachBookRowAfter(r bareRowIterReader, afterID string, visit func(id string, value []byte) error) error {
	return scanBareRows(r, bookRowPrefix, afterID, visit)
}

// forEachBareRow calls visit with the ID and value of every "<prefix><id>"
// record row of the family named by prefix, which must end in ':'.
//
// Contract:
//   - value is valid only for the duration of the callback; copy it to keep it.
//   - visit returning errStopScan ends the scan and forEachBareRow returns nil.
//   - visit returning any other error ends the scan and is returned unchanged.
//   - a read error (per-value or range-level) or a Close error is returned, so
//     a nil result means every record row in the range was visited.
func forEachBareRow(r bareRowIterReader, prefix string, visit func(id string, value []byte) error) error {
	return scanBareRows(r, prefix, "", visit)
}

func scanBareRows(r bareRowIterReader, prefix, afterID string, visit func(id string, value []byte) error) error {
	what := "scan of " + prefix + " rows"
	c, err := openRowCursor(r, &pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: bareRowUpperBound(prefix),
	})
	if err != nil {
		return fmt.Errorf("%s: open iterator: %w", what, err)
	}
	b := bareRowIter{it: c.it, prefix: []byte(prefix)}
	plen := len(prefix)
	return c.run(
		func() bool { return b.SeekAfter(afterID) },
		b.Next,
		func(key, value []byte) error { return visit(string(key[plen:]), value) },
		what,
	)
}

// forEachKeyInRange calls visit with every key and value in [lower, upper), in
// key order, under the same contract as forEachBareRow (key and value are valid
// only for the callback; errStopScan stops cleanly; read and Close errors are
// returned). It is for raw-range scans such as book_file:<bookID>:<fileID>,
// where the caller applies its own key filter.
func forEachKeyInRange(r bareRowIterReader, lower, upper []byte, visit func(key, value []byte) error) error {
	what := fmt.Sprintf("scan of [%q, %q)", lower, upper)
	c, err := openRowCursor(r, &pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return fmt.Errorf("%s: open iterator: %w", what, err)
	}
	return c.run(c.it.First, c.it.Next, visit, what)
}

// rowCursor is the one place a scan helper's loop, error check and Close live.
type rowCursor struct {
	it *pebble.Iterator

	// Test seam, nil in production: see setRowScanFault.
	fault   *rowScanFault
	rows    int
	faulted error
}

func openRowCursor(r bareRowIterReader, o *pebble.IterOptions) (*rowCursor, error) {
	it, err := r.NewIter(o)
	if err != nil {
		return nil, err
	}
	c := &rowCursor{it: it}
	if f, ok := rowScanFaults.Load(r); ok {
		c.fault = f.(*rowScanFault)
	}
	return c, nil
}

// admit gates each positioning result. With a fault armed it makes the scan
// end after fault.afterRows rows exactly the way Pebble does on a read error:
// the move reports "not valid" and the error is visible only through err().
func (c *rowCursor) admit(valid bool) bool {
	if !valid {
		return false
	}
	if c.fault != nil && c.rows >= c.fault.afterRows {
		c.faulted = c.fault.err
		return false
	}
	c.rows++
	return true
}

// err is the iterator's accumulated error (Pebble's Error()), or the injected
// fault when one tripped.
func (c *rowCursor) err() error {
	if c.faulted != nil {
		return c.faulted
	}
	return c.it.Error()
}

func (c *rowCursor) run(start, next func() bool, visit func(key, value []byte) error, what string) (err error) {
	defer func() {
		if cerr := c.it.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("%s: closing iterator: %w", what, cerr)
		}
	}()
	for ok := c.admit(start()); ok; ok = c.admit(next()) {
		key := c.it.Key()
		value, verr := c.it.ValueAndErr()
		if verr != nil {
			return fmt.Errorf("%s: reading %q: %w", what, key, verr)
		}
		if verr := visit(key, value); verr != nil {
			if errors.Is(verr, errStopScan) {
				return nil
			}
			return verr
		}
	}
	// The loop ends on end-of-range OR on a read error, and nothing but this
	// check tells them apart. Without it a partial scan reads as complete.
	if ierr := c.err(); ierr != nil {
		return fmt.Errorf("%s truncated, refusing to answer from a partial result: %w", what, ierr)
	}
	return nil
}

// rowScanFault makes every scan opened on one reader fail after afterRows rows.
// Tests only: production never stores into rowScanFaults, so the per-scan cost
// is one Load on an empty sync.Map. It is keyed by reader (a test's own
// *pebble.DB), so parallel tests on other stores are unaffected.
type rowScanFault struct {
	afterRows int
	err       error
}

var rowScanFaults sync.Map // bareRowIterReader -> *rowScanFault

// setRowScanFault arms a fault on r and returns the function that disarms it.
func setRowScanFault(r bareRowIterReader, afterRows int, err error) (clear func()) {
	rowScanFaults.Store(r, &rowScanFault{afterRows: afterRows, err: err})
	return func() { rowScanFaults.Delete(r) }
}

// bareRowIter positions a *pebble.Iterator on the record rows "<prefix><id>"
// of a key family, in key (plain byte) order. It is the engine under
// scanBareRows and is deliberately not handed to callers: see rule 3 above.
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
