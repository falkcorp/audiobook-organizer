// file: internal/database/pebble_store_name_index.go
// version: 1.0.0
// guid: 8c0380c7-4e20-40bb-b2a0-a5b6e840b5be
// last-edited: 2026-09-12

package database

import (
	"bytes"
	"strconv"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Read-compat and ownership-checked deletes for the name indexes keyed by
// util.NormalizeAuthor: author:name:, author_alias:name:, narrator_name: and
// series:name:<name>:<author_id>.
//
// WHY THIS EXISTS. On 2026-09-12 NormalizeAuthor started collapsing internal
// whitespace (TASK-086). Index entries already on disk were written with the
// old key (util.NormalizeAuthorLegacy: trim + lowercase only), so a name with
// a double space or a tab is stored under a key the new function never
// produces. Two consequences, each handled here:
//
//  1. LOOKUPS would miss those entries and the caller (CreateAuthor,
//     CreateSeries, ...) would mint a duplicate row. nameIndexGet tries the new
//     key and then the legacy key, so every lookup that succeeded before the
//     change still succeeds: the legacy attempt is byte-identical to the old
//     single attempt.
//
//  2. DELETES could hit another row's entry. Before the change "john smith"
//     and "john  smith" were different keys; now both rows compute
//     "john smith", so deleting or renaming the double-spaced row would remove
//     the single-spaced row's entry and make IT unfindable by name.
//     deleteNameIndexIfOwned deletes an entry only while it still points at
//     the row being deleted.
//
// The existing legacy entries are deliberately NOT re-keyed here. A re-key has
// to pick a winner for every group of rows that collide under the new key, and
// that choice waits on maintenance.author-whitespace-collision-report.

// nameIndexKeyFunc builds a full index key from a normalized name.
type nameIndexKeyFunc func(norm string) string

func authorNameIndexKey(norm string) string   { return "author:name:" + norm }
func aliasNameIndexKey(norm string) string    { return "author_alias:name:" + norm }
func narratorNameIndexKey(norm string) string { return "narrator_name:" + norm }

// seriesNameIndexKey returns the key builder for one author scope ("nil" or a
// decimal author id), matching series:name:<name>:<author_id>.
func seriesNameIndexKey(authorIDStr string) nameIndexKeyFunc {
	return func(norm string) string { return "series:name:" + norm + ":" + authorIDStr }
}

// nameIndexKeys returns the keys name can be stored under: the current key
// and, when it differs, the legacy key.
func nameIndexKeys(keyFor nameIndexKeyFunc, name string) []string {
	cur := util.NormalizeAuthor(name)
	keys := []string{keyFor(cur)}
	if legacy := util.NormalizeAuthorLegacy(name); legacy != cur {
		keys = append(keys, keyFor(legacy))
	}
	return keys
}

// getValueCopy reads key and returns a copy of its value, or (nil, nil) when
// the key is absent.
func (p *PebbleStore) getValueCopy(key []byte) ([]byte, error) {
	v, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), v...)
	closer.Close()
	return out, nil
}

// nameIndexGet resolves name through a NormalizeAuthor-keyed index: the
// current key first, then the legacy key. Returns (nil, nil) when neither is
// present.
func (p *PebbleStore) nameIndexGet(keyFor nameIndexKeyFunc, name string) ([]byte, error) {
	for _, k := range nameIndexKeys(keyFor, name) {
		v, err := p.getValueCopy([]byte(k))
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v, nil
		}
	}
	return nil, nil
}

// nameIndexDeleter is satisfied by both *pebble.Batch and *pebble.DB.
type nameIndexDeleter interface {
	Delete(key []byte, opts *pebble.WriteOptions) error
}

// deleteNameIndexIfOwned deletes the current and legacy index entries for
// name, each ONLY if its value is still owner. An entry owned by another row
// (one whose name collapses to the same key) is left alone.
//
// The ownership read goes to the committed database, not to a pending batch:
// none of the callers write the same index key earlier in the batch they pass.
func (p *PebbleStore) deleteNameIndexIfOwned(w nameIndexDeleter, opts *pebble.WriteOptions, keyFor nameIndexKeyFunc, name string, owner []byte) error {
	for _, k := range nameIndexKeys(keyFor, name) {
		v, err := p.getValueCopy([]byte(k))
		if err != nil {
			return err
		}
		if v == nil || !bytes.Equal(v, owner) {
			continue
		}
		if err := w.Delete([]byte(k), opts); err != nil {
			return err
		}
	}
	return nil
}

// nameIndexOwner is the value an int-id index entry holds. author:name:,
// author_alias:name: and series:name: store strconv.Itoa(id); narrator_name:
// stores json.Marshal(id), which is the same bytes for an int.
func nameIndexOwner(id int) []byte { return []byte(strconv.Itoa(id)) }
