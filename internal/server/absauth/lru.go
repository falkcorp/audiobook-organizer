// file: internal/server/absauth/lru.go
// version: 1.0.0
// guid: 0d3a0cfe-fa8f-4b61-b380-4a0f67d40fa0
// last-edited: 2026-10-22

package absauth

import "container/list"

// LRU is a string-keyed table bounded to a maximum size that remembers the order
// in which its entries were last written. It exists for the login failure tables
// (Throttle here, the login ceiling in the server package), which an attacker
// rotating addresses or usernames can fill:
//
//   - at the bound, Put evicts the least recently written entry in O(1) (the old
//     tables scanned every entry under their lock for each insertion, which turned
//     a flood of new keys into a CPU cost for every login), and
//   - Sweep drops expired entries from the cold end only, so a periodic sweep costs
//     as much as it removes, never a walk of the whole table.
//
// Not safe for concurrent use: callers hold their own lock.
type LRU[V any] struct {
	max int
	ll  *list.List // front = most recently written
	m   map[string]*list.Element
}

type lruEntry[V any] struct {
	key string
	val V
}

// NewLRU returns an empty table holding at most size entries (at least 1).
func NewLRU[V any](size int) *LRU[V] {
	return &LRU[V]{max: max(size, 1), ll: list.New(), m: make(map[string]*list.Element)}
}

// Len is the number of entries.
func (t *LRU[V]) Len() int { return len(t.m) }

// Get returns the entry for key without changing its position.
func (t *LRU[V]) Get(key string) (V, bool) {
	if e, ok := t.m[key]; ok {
		return e.Value.(*lruEntry[V]).val, true
	}
	var zero V
	return zero, false
}

// Put stores val under key as the most recently written entry, evicting the
// least recently written one when the table is full and key is new.
func (t *LRU[V]) Put(key string, val V) {
	if e, ok := t.m[key]; ok {
		e.Value.(*lruEntry[V]).val = val
		t.ll.MoveToFront(e)
		return
	}
	if len(t.m) >= t.max {
		t.removeElement(t.ll.Back())
	}
	t.m[key] = t.ll.PushFront(&lruEntry[V]{key: key, val: val})
}

// Touch marks key as the most recently written entry (its value was mutated in
// place).
func (t *LRU[V]) Touch(key string) {
	if e, ok := t.m[key]; ok {
		t.ll.MoveToFront(e)
	}
}

// Delete removes key.
func (t *LRU[V]) Delete(key string) {
	if e, ok := t.m[key]; ok {
		t.removeElement(e)
	}
}

// Sweep removes entries from the cold end while expired reports true for them,
// stopping at the first entry that is not expired, and returns how many it
// removed. Entries are in write order, so for an expiry that grows with age
// nothing past the first live entry can be expired.
func (t *LRU[V]) Sweep(expired func(V) bool) int {
	n := 0
	for e := t.ll.Back(); e != nil; e = t.ll.Back() {
		if !expired(e.Value.(*lruEntry[V]).val) {
			break
		}
		t.removeElement(e)
		n++
	}
	return n
}

func (t *LRU[V]) removeElement(e *list.Element) {
	if e == nil {
		return
	}
	t.ll.Remove(e)
	delete(t.m, e.Value.(*lruEntry[V]).key)
}
