// file: internal/database/book_file_live_sample.go
// version: 1.1.0
// guid: 3b0e6f94-71d2-4c58-9a1e-c5d7284f0b63
// last-edited: 2026-10-26

package database

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-memdb"
)

// LiveBookFileSampler is the read the purge's library-root check is built on: a
// spread-out sample of the files the database says belong to live books under a
// folder. It is NOT part of Store (Store's flattened width is a ratchet,
// store_width_test.go, and exactly one consumer needs this), so it is found with
// AsCapability, which looks through decorators that opt in via StoreUnwrapper.
// *PebbleStore and MockStore implement it.
type LiveBookFileSampler interface {
	// SampleLiveBookFilePathsUnder returns up to k book_file paths that lie
	// inside root and belong to live books: the book is not in the trash, the
	// row is not retired, and no two paths are of the same book, so the sample
	// spreads over books. It reads the library from a few places in path order,
	// each wrapping around, and stops as soon as it has k. The places are the
	// folders that really exist under root (their first characters are read from
	// the index, never guessed), taken in a random order, so the reads spread over
	// the library instead of crowding at its start.
	//
	// Fewer than k paths come back only when that is every such path there is:
	// the answer is never short because of where a read happened to start, and an
	// empty answer means no live book has a file under root at all. A caller that
	// has to decide from the sample (a mount that is not up looks like an empty
	// folder) can therefore treat a short answer as the whole population.
	//
	// The work is proportional to the rows it visits, not to the library: it
	// does not copy books or files out of the store. A store that cannot answer
	// completely (memdb not serving) returns ErrLiveSampleUnavailable, and the
	// caller must fail closed.
	SampleLiveBookFilePathsUnder(root string, k int) ([]string, error)
}

// ErrLiveSampleUnavailable is returned by SampleLiveBookFilePathsUnder when the
// store cannot sample (memdb is not serving: a Pebble scan of every book_file
// is not a fallback, it is an outage).
var ErrLiveSampleUnavailable = errors.New("cannot sample the live book files")

var (
	_ LiveBookFileSampler = (*PebbleStore)(nil)
	_ LiveBookFileSampler = (*MockStore)(nil)
)

const (
	// liveSampleChunks is how many places the sample is drawn from. One
	// contiguous run of 50 paths is a few author folders, and a nested mount
	// that is not up under another folder would never be looked at.
	liveSampleChunks = 5
	// liveSampleMaxStarts bounds the hops that find the first characters under a
	// root: a first byte has 256 values, so it is a bound that is never reached.
	liveSampleMaxStarts = 256
)

// SampleLiveBookFilePathsUnder implements LiveBookFileSampler from memdb.
func (p *PebbleStore) SampleLiveBookFilePathsUnder(root string, k int) ([]string, error) {
	if k <= 0 || root == "" {
		return nil, nil
	}
	m := p.memOrRefuse("SampleLiveBookFilePathsUnder")
	if m == nil {
		return nil, fmt.Errorf("%w: memdb is not serving", ErrLiveSampleUnavailable)
	}
	return m.sampleLiveBookFilePathsUnder(root, k)
}

// liveSample is one sampling read's state.
type liveSample struct {
	txn       *memdb.Txn
	out       []string
	seenPaths map[string]struct{}
	seenBooks map[string]struct{}
}

// take adds bf's path when it is evidence: a path not yet taken, of a book not
// yet represented, that is live. It reports whether it was added.
func (s *liveSample) take(bf *BookFile) (bool, error) {
	if bf.Retired || bf.FilePath == "" {
		return false, nil
	}
	if _, dup := s.seenPaths[bf.FilePath]; dup {
		return false, nil
	}
	if _, dup := s.seenBooks[bf.BookID]; dup {
		return false, nil
	}
	raw, err := s.txn.First(memTableBooks, memIdxID, bf.BookID)
	if err != nil {
		// A failed lookup is not "the book is gone": it would shrink the sample
		// toward "ready".
		return false, fmt.Errorf("memdb book lookup (book_id=%s): %w", bf.BookID, err)
	}
	b, ok := raw.(*Book)
	if !ok || b == nil {
		return false, nil // a file row with no book: not evidence of a live one
	}
	if bookIsSoftDeleted(b) || (b.LibraryState != nil && *b.LibraryState == "deleted") {
		return false, nil
	}
	s.seenPaths[bf.FilePath] = struct{}{}
	s.seenBooks[bf.BookID] = struct{}{}
	s.out = append(s.out, bf.FilePath)
	return true, nil
}

// collect adds up to quota paths read from start, in path order to the end of
// the prefix range and then from its beginning up to start. It reports true when
// it went all the way round: every row under prefix was looked at.
func (s *liveSample) collect(prefix, start string, quota int) (exhausted bool, err error) {
	added := 0
	it, err := s.txn.LowerBound(memTableBookFiles, memIdxFilePath, start)
	if err != nil {
		return false, fmt.Errorf("memdb book_files from %q: %w", start, err)
	}
	for obj := it.Next(); obj != nil; obj = it.Next() {
		bf, ok := obj.(*BookFile)
		if !ok {
			continue
		}
		if !strings.HasPrefix(bf.FilePath, prefix) {
			break
		}
		took, err := s.take(bf)
		if err != nil {
			return false, err
		}
		if took {
			added++
			if added >= quota {
				return false, nil
			}
		}
	}
	wrap, err := s.txn.Get(memTableBookFiles, memIdxFilePath+"_prefix", prefix)
	if err != nil {
		return false, fmt.Errorf("memdb book_files under %q: %w", prefix, err)
	}
	for obj := wrap.Next(); obj != nil; obj = wrap.Next() {
		bf, ok := obj.(*BookFile)
		if !ok {
			continue
		}
		if bf.FilePath >= start {
			break // back where the walk began
		}
		took, err := s.take(bf)
		if err != nil {
			return false, err
		}
		if took {
			added++
			if added >= quota {
				return false, nil
			}
		}
	}
	return true, nil
}

// firstBytesUnder returns the distinct first bytes of the paths that lie under
// prefix, in order, found by hopping the path index: seek to the prefix, read
// the first path's byte after it, seek past every path that starts with that
// byte, repeat. The cost is one index seek per distinct first byte (at most
// liveSampleMaxStarts), not a visit of every row.
//
// A chunk of the sample starts at the beginning of one of these groups. The start
// was once drawn from a fixed 62-character alphabet; a library whose folders
// start with "G" and "W" then began 58 percent of its chunks at the beginning of
// the library (every character below "G" seeks to the first folder at or after
// it), and a folder that no drawn character landed on first was reached only by
// wrapping.
func (s *liveSample) firstBytesUnder(prefix string) ([]byte, error) {
	var firsts []byte
	seek := prefix
	for range liveSampleMaxStarts {
		it, err := s.txn.LowerBound(memTableBookFiles, memIdxFilePath, seek)
		if err != nil {
			return nil, fmt.Errorf("memdb book_files from %q: %w", seek, err)
		}
		obj := it.Next()
		if obj == nil {
			break
		}
		bf, ok := obj.(*BookFile)
		if !ok || !strings.HasPrefix(bf.FilePath, prefix) {
			break
		}
		rest := bf.FilePath[len(prefix):]
		if rest == "" {
			// A path that is the prefix itself has no first byte after it.
			seek = prefix + "\x00"
			continue
		}
		b := rest[0]
		firsts = append(firsts, b)
		if b == 0xff {
			break
		}
		seek = prefix + string([]byte{b + 1})
	}
	return firsts, nil
}

// sampleLiveBookFilePathsUnder is the memdb body of SampleLiveBookFilePathsUnder.
func (m *MemStore) sampleLiveBookFilePathsUnder(root string, k int) ([]string, error) {
	prefix := filepath.Clean(root)
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	s := &liveSample{txn: txn, seenPaths: map[string]struct{}{}, seenBooks: map[string]struct{}{}}
	firsts, err := s.firstBytesUnder(prefix)
	if err != nil {
		return nil, err
	}
	if len(firsts) == 0 {
		return nil, nil // nothing under the root at all
	}
	rand.Shuffle(len(firsts), func(i, j int) { firsts[i], firsts[j] = firsts[j], firsts[i] })
	quota := (k + liveSampleChunks - 1) / liveSampleChunks
	for c := 0; c < liveSampleChunks && len(s.out) < k; c++ {
		// Distinct places while there are distinct first characters, then round
		// again: further chunks continue past the paths already taken.
		start := prefix + string([]byte{firsts[c%len(firsts)]})
		exhausted, err := s.collect(prefix, start, min(quota, k-len(s.out)))
		if err != nil {
			return nil, err
		}
		if exhausted {
			// Every row under the prefix was looked at and what was live is in
			// out: that is the whole population, and it is short of k.
			break
		}
	}
	return s.out, nil
}
