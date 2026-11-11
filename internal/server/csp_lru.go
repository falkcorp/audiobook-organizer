// file: internal/server/csp_lru.go
// version: 2.0.0
// guid: 0d3a0cfe-fa8f-4b61-b380-4a0f67d40fa0
// last-edited: 2026-11-10

package server

import "container/list"

// visitorLRU is the CSP reporter's per-visitor table: string-keyed, bounded to
// a maximum size, and kept in the order its entries were last written, so a
// caller rotating addresses cannot grow it:
//
//   - at the bound, put evicts the least recently written entry in O(1), and
//   - sweep drops expired entries from the cold end only, so a periodic sweep
//     costs as much as it removes, never a walk of the whole table.
//
// It was absauth.LRU, which also served the login failure tables with a
// pinned tier (an entry that must outlive a stream of new keys). Those tables
// moved into core/auth with their own copy (F4-02), and the CSP table pins
// nothing, so the pinned tier went with them.
//
// Not safe for concurrent use: the caller holds its own lock.
type visitorLRU[V any] struct {
	max  int
	l    *list.List // front = most recently written
	m    map[string]*list.Element
	zero V
}

type visitorEntry[V any] struct {
	key string
	val V
}

// newVisitorLRU returns an empty table holding at most size entries (at least
// 1).
func newVisitorLRU[V any](size int) *visitorLRU[V] {
	return &visitorLRU[V]{max: max(size, 1), l: list.New(), m: make(map[string]*list.Element)}
}

// get returns the entry for key without changing its position.
func (t *visitorLRU[V]) get(key string) (V, bool) {
	if e, ok := t.m[key]; ok {
		return e.Value.(*visitorEntry[V]).val, true
	}
	return t.zero, false
}

// put stores val under key as the most recently written entry, evicting the
// coldest entry when the table is full and key is new.
func (t *visitorLRU[V]) put(key string, val V) {
	if e, ok := t.m[key]; ok {
		e.Value.(*visitorEntry[V]).val = val
		t.l.MoveToFront(e)
		return
	}
	if len(t.m) >= t.max {
		t.remove(t.l.Back())
	}
	t.m[key] = t.l.PushFront(&visitorEntry[V]{key: key, val: val})
}

// sweep removes entries from the cold end while expired reports true for
// them, stopping at the first live entry, and returns how many it removed.
// The list is in write order, so for an expiry that grows with age nothing
// past its first live entry can be expired.
func (t *visitorLRU[V]) sweep(expired func(V) bool) int {
	n := 0
	for e := t.l.Back(); e != nil; e = t.l.Back() {
		if !expired(e.Value.(*visitorEntry[V]).val) {
			break
		}
		t.remove(e)
		n++
	}
	return n
}

func (t *visitorLRU[V]) remove(e *list.Element) {
	if e == nil {
		return
	}
	t.l.Remove(e)
	delete(t.m, e.Value.(*visitorEntry[V]).key)
}
