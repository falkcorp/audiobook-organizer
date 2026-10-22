// file: internal/server/absauth/lru.go
// version: 1.1.0
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
// PINNING. Some entries must outlive a stream of new keys: a username that has
// already failed enough times to be refused, a visitor with a login in the
// handlers, a visitor refused for the rest of the window. SetPinned gives the
// table a predicate for them; pinned entries live on a second list, and the bound
// evicts the coldest UNPINNED entry first, and a pinned one only when every entry
// is pinned. Both lists are kept in write order, so this stays O(1). The predicate
// is applied when an entry is stored (Put) or re-filed (Touch), so a caller that
// changes a value in place calls Touch afterwards, exactly as it does to mark the
// write.
//
// Not safe for concurrent use: callers hold their own lock.
type LRU[V any] struct {
	max    int
	pinned func(V) bool
	cold   *list.List // unpinned entries; front = most recently written
	hot    *list.List // pinned entries; front = most recently written
	m      map[string]*list.Element
}

type lruEntry[V any] struct {
	key string
	val V
	hot bool
}

// NewLRU returns an empty table holding at most size entries (at least 1).
func NewLRU[V any](size int) *LRU[V] {
	return &LRU[V]{max: max(size, 1), cold: list.New(), hot: list.New(), m: make(map[string]*list.Element)}
}

// SetPinned sets the predicate that marks an entry as worth keeping over others
// when the table is full. nil (the default) pins nothing. Set it before use.
func (t *LRU[V]) SetPinned(fn func(V) bool) { t.pinned = fn }

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
// coldest unpinned entry (or, if all are pinned, the coldest) when the table is
// full and key is new.
func (t *LRU[V]) Put(key string, val V) {
	if e, ok := t.m[key]; ok {
		e.Value.(*lruEntry[V]).val = val
		t.refile(e)
		return
	}
	if len(t.m) >= t.max {
		victim := t.cold.Back()
		if victim == nil {
			victim = t.hot.Back()
		}
		t.removeElement(victim)
	}
	ent := &lruEntry[V]{key: key, val: val}
	ent.hot = t.isPinned(val)
	t.m[key] = t.listFor(ent.hot).PushFront(ent)
}

// Touch marks key as the most recently written entry (its value was mutated in
// place) and files it according to the value it now holds.
func (t *LRU[V]) Touch(key string) {
	if e, ok := t.m[key]; ok {
		t.refile(e)
	}
}

// Delete removes key.
func (t *LRU[V]) Delete(key string) {
	if e, ok := t.m[key]; ok {
		t.removeElement(e)
	}
}

// Sweep removes entries from the cold end of each list while expired reports true
// for them, stopping at the first live entry of that list, and returns how many it
// removed. Each list is in write order, so for an expiry that grows with age
// nothing past its first live entry can be expired. An entry the caller keeps
// alive for another reason (a login still in flight) is filed on the pinned list
// and never stands in front of the unpinned ones.
func (t *LRU[V]) Sweep(expired func(V) bool) int {
	n := 0
	for _, l := range []*list.List{t.cold, t.hot} {
		for e := l.Back(); e != nil; e = l.Back() {
			if !expired(e.Value.(*lruEntry[V]).val) {
				break
			}
			t.removeElement(e)
			n++
		}
	}
	return n
}

func (t *LRU[V]) isPinned(v V) bool { return t.pinned != nil && t.pinned(v) }

func (t *LRU[V]) listFor(hot bool) *list.List {
	if hot {
		return t.hot
	}
	return t.cold
}

// refile moves e to the front of the list its current value belongs on.
func (t *LRU[V]) refile(e *list.Element) {
	ent := e.Value.(*lruEntry[V])
	hot := t.isPinned(ent.val)
	if hot == ent.hot {
		t.listFor(hot).MoveToFront(e)
		return
	}
	t.listFor(ent.hot).Remove(e)
	ent.hot = hot
	t.m[ent.key] = t.listFor(hot).PushFront(ent)
}

func (t *LRU[V]) removeElement(e *list.Element) {
	if e == nil {
		return
	}
	ent := e.Value.(*lruEntry[V])
	t.listFor(ent.hot).Remove(e)
	delete(t.m, ent.key)
}
