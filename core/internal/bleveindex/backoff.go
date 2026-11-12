// file: core/internal/bleveindex/backoff.go
// version: 1.0.0
// guid: 9c502189-c888-4f53-a272-3dbeab8f20b7
// last-edited: 2026-11-11

package bleveindex

import (
	"time"

	"github.com/falkcorp/mediacore/storage/index"
)

// A mark whose document cannot be loaded or indexed stays pending, and so it
// would be retried, and logged, by every pass. Each failure of the same id
// instead delays its next attempt, doubling from failBackoffBase to
// failBackoffMax: a poison mark costs one attempt per window, not one per pass,
// and the healthy marks around it are not delayed. The state is in memory (a
// restart retries everything once) and holds one small entry per failing mark.
const (
	failBackoffBase = 2 * time.Second
	failBackoffMax  = 5 * time.Minute
	// maxBackoffEntries bounds the table: past it the table is dropped, which
	// only means the marks in it are retried at once.
	maxBackoffEntries = 1 << 16
)

type failure struct {
	n     int       // consecutive failures
	until time.Time // not retried before
}

// clock is the engine's time source (a test replaces it).
func (e *Engine) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// recordFailure delays the next attempt at id.
func (i *idx) recordFailure(id string) {
	i.failMu.Lock()
	defer i.failMu.Unlock()
	if i.fails == nil || len(i.fails) >= maxBackoffEntries {
		i.fails = map[string]failure{}
	}
	f := i.fails[id]
	f.n++
	d := failBackoffMax
	if f.n <= 18 { // 2s << 17 is already past the cap; avoid overflowing the shift
		d = min(failBackoffBase<<(f.n-1), failBackoffMax)
	}
	f.until = i.e.clock().Add(d)
	i.fails[id] = f
}

// recordSuccess forgets id's failures.
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

// due is the marks of a page that are not backing off.
func (i *idx) due(marks []index.Mark, now time.Time) []index.Mark {
	i.failMu.Lock()
	defer i.failMu.Unlock()
	if len(i.fails) == 0 {
		return marks
	}
	out := make([]index.Mark, 0, len(marks))
	for _, m := range marks {
		if f, ok := i.fails[m.ID]; ok && now.Before(f.until) {
			continue
		}
		out = append(out, m)
	}
	return out
}
