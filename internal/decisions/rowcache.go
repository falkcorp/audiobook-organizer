// file: internal/decisions/rowcache.go
// version: 1.0.1
// guid: c27f4e90-3b58-4a1d-8e06-d4a91b73f625
// last-edited: 2026-10-28

package decisions

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// logGenerations counts the writes to each engine's VETO rows (the active,
// pair-edge and membership keys), by lock scope (the engine behind the KV). It
// is package-level for the reason subjectLocks is: every Store over one engine
// writes the same keys, so the count must be shared by all of them, not kept
// per Store.
//
// It deliberately does NOT count a commit that writes only labels: a record,
// a verdict and their lookup indexes (an applied, undone, approve, skip or
// reopened decision). The RowCache holds the veto rows alone, so a label-only
// commit cannot make it stale, and the retired-versions apply writes one such
// record per row. RowCache is the only reader of this count.
var logGenerations sync.Map // lock scope -> *atomic.Uint64

func logCounter(scope string) *atomic.Uint64 {
	if c, ok := logGenerations.Load(scope); ok {
		return c.(*atomic.Uint64)
	}
	c, _ := logGenerations.LoadOrStore(scope, new(atomic.Uint64))
	return c.(*atomic.Uint64)
}

func logGeneration(scope string) uint64 { return logCounter(scope).Load() }

// bumpLogGeneration records that the veto rows of the engine may have changed.
// Store.commit is the only writer of the decision keys and calls it after every
// commit it attempts that touches a veto row (TestDecisionKeysHaveOneWriter
// holds the package, and the repository, to that).
func bumpLogGeneration(scope string) { logCounter(scope).Add(1) }

// writesVetoRows reports whether the batch writes or deletes any row a
// RowCache holds: an active row, a pair edge or a membership row. Those are
// exactly the prefixes scanRows reads, so a new scanned prefix must be added
// here (TestRowCache_OnlyVetoRowWritesInvalidate holds the two together).
func writesVetoRows(writes []database.RawWrite) bool {
	for _, w := range writes {
		if strings.HasPrefix(w.Key, prefixActive) || strings.HasPrefix(w.Key, prefixEdge) || strings.HasPrefix(w.Key, prefixMember) {
			return true
		}
	}
	return false
}

// RowCache lets the Loads of many Stores share one read of the decision log:
// the raw rows (live rejects, pair edges, set members) of the last scan, valid
// while no veto row has been written since. A Load still RESOLVES every stored id
// afresh each time; only the scan of the log is saved.
//
// That is the whole of what may be cached. A resolved snapshot depends on book
// state (a shell merged, retired or purged since changes where a stored id
// leads), and the retired-versions apply changes exactly that between the
// re-plans of one run: reusing a snapshot resolved before one row's retire
// would let a veto naming that row's shell miss the next row's join, a dropped
// owner reject. The rows are pure log data, so the count of writes to those rows is a
// sound key for them.
//
// One RowCache serves any number of Stores and is safe for concurrent use. Give
// it to NewStore through WithRowCache. nil (no cache) is valid everywhere.
type RowCache struct {
	mu    sync.Mutex
	scope string
	gen   uint64
	rows  *loadedRows
}

// NewRowCache returns an empty RowCache.
func NewRowCache() *RowCache { return &RowCache{} }

// WithRowCache makes the Store's Load read the log through c.
func WithRowCache(c *RowCache) Option { return func(s *Store) { s.cache = c } }

func (c *RowCache) get(scope string, gen uint64) *loadedRows {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows != nil && c.scope == scope && c.gen == gen {
		return c.rows
	}
	return nil
}

func (c *RowCache) put(scope string, gen uint64, rows *loadedRows) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scope, c.gen, c.rows = scope, gen, rows
}
