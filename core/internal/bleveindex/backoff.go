// file: core/internal/bleveindex/backoff.go
// version: 1.0.1
// guid: 9c502189-c888-4f53-a272-3dbeab8f20b7
// last-edited: 2026-11-12

package bleveindex

import (
	"bytes"
	"time"

	"github.com/falkcorp/mediacore/storage/index"
)

// A mark whose document cannot be loaded or indexed stays pending, and so it
// would be retried, and logged, by every pass. Each failure of the same id
// instead delays its next attempt, doubling from failBackoffBase to
// failBackoffMax: a poison mark costs one attempt per window, not one per pass,
// and the healthy marks around it are not delayed. The state is in memory (a
// restart retries everything once) and holds one small entry per failing mark.
//
// An entry belongs to the mark it failed on, not to the id: it records the
// mark's token, and a mark with another token (the entity was written again,
// which is the owner fixing the data) is never held back by it. The table is
// bounded by Options.MaxBackoffEntries. When it is full, the entries whose
// window is over are dropped first; if it is still full, a new failing mark is
// not recorded (it is retried every pass, like a mark that never failed before)
// and every entry already there keeps backing off.
const (
	failBackoffBase = 2 * time.Second
	failBackoffMax  = 5 * time.Minute
	// DefaultMaxBackoffEntries bounds the table of backing-off marks of one index
	// (Options.MaxBackoffEntries).
	DefaultMaxBackoffEntries = 1 << 16
)

type failure struct {
	n     int       // consecutive failures of this mark
	until time.Time // not retried before
	token []byte    // the token of the mark that failed
}

// clock is the engine's time source (a test replaces it).
func (e *Engine) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

func (e *Engine) maxBackoffEntries() int {
	if e.o.MaxBackoffEntries > 0 {
		return e.o.MaxBackoffEntries
	}
	return DefaultMaxBackoffEntries
}

// recordFailure delays the next attempt at the mark m. A failure of a mark with
// another token than the entry's starts the count again: it is new data.
func (i *idx) recordFailure(m index.Mark) {
	i.failMu.Lock()
	defer i.failMu.Unlock()
	now := i.e.clock()
	f, known := i.fails[m.ID]
	if known && !bytes.Equal(f.token, m.Token) {
		f = failure{}
	}
	if !known {
		if i.fails == nil {
			i.fails = map[string]failure{}
		}
		if len(i.fails) >= i.e.maxBackoffEntries() && !i.makeRoom(now) {
			return // full of live entries: this mark is retried every pass
		}
	}
	f.n++
	d := failBackoffMax
	if f.n <= 18 { // 2s << 17 is already past the cap; avoid overflowing the shift
		d = min(failBackoffBase<<(f.n-1), failBackoffMax)
	}
	f.until = now.Add(d)
	f.token = bytes.Clone(m.Token)
	i.fails[m.ID] = f
}

// makeRoom drops the entries whose window is over and reports whether the table
// has a free slot afterwards. The sweep walks the whole table, so it runs at
// most once per failBackoffBase (an entry cannot expire sooner than that after
// it was recorded, and a pass over a table of live entries must not rescan it
// for every failing mark). Called with failMu held.
func (i *idx) makeRoom(now time.Time) bool {
	limit := i.e.maxBackoffEntries()
	if i.sweptAt.IsZero() || now.Sub(i.sweptAt) >= failBackoffBase {
		i.sweptAt = now
		for id, f := range i.fails {
			if !now.Before(f.until) {
				delete(i.fails, id)
			}
		}
		if len(i.fails) >= limit {
			i.log.Warn("failure backoff table is full of live entries; marks past it are retried on every pass",
				"module", i.module, "index", i.name, "entries", len(i.fails))
		}
	}
	return len(i.fails) < limit
}

// recordSuccess forgets id's failures, whatever mark they were of.
func (i *idx) recordSuccess(id string) {
	i.failMu.Lock()
	defer i.failMu.Unlock()
	delete(i.fails, id)
}

// forgetFailures drops the whole table; a full pass over an empty dirty set
// calls it.
func (i *idx) forgetFailures() {
	i.failMu.Lock()
	defer i.failMu.Unlock()
	i.fails = nil
}

// due is the marks of a page that are not backing off. A mark is held back only
// by an entry of its own token: a rewritten entity is retried at once.
func (i *idx) due(marks []index.Mark, now time.Time) []index.Mark {
	i.failMu.Lock()
	defer i.failMu.Unlock()
	if len(i.fails) == 0 {
		return marks
	}
	out := make([]index.Mark, 0, len(marks))
	for _, m := range marks {
		if f, ok := i.fails[m.ID]; ok && now.Before(f.until) && bytes.Equal(f.token, m.Token) {
			continue
		}
		out = append(out, m)
	}
	return out
}
