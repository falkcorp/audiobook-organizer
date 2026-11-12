// file: internal/books/cutover/m3/delete.go
// version: 1.0.0
// guid: 35a41446-3e37-40f3-9e23-46dd90379dda
// last-edited: 2026-11-12

package m3

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/storage"
)

// rowSet collects the keys one delete removes, each once.
type rowSet struct {
	b    *storage.Batch
	seen map[string]struct{}
}

func newRowSet(b *storage.Batch) *rowSet { return &rowSet{b: b, seen: map[string]struct{}{}} }

func (s *rowSet) delete(key string) error {
	if _, dup := s.seen[key]; dup {
		return nil
	}
	s.seen[key] = struct{}{}
	return s.b.Delete([]byte(key))
}

// get reads one legacy row, nil when absent.
func (m *Migration) get(ctx context.Context, key string) ([]byte, error) {
	raw, err := m.legacy().Get(ctx, []byte(key))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return raw, err
}

// deleteKeyRows stages the deletion of one API key's row and its index rows:
// the per-user entry and the hash entry, found from the row when it decodes
// and by scanning the index when it does not.
func (m *Migration) deleteKeyRows(ctx context.Context, b *storage.Batch, id string) error {
	return m.deleteKeyRowsIn(ctx, newRowSet(b), id)
}

func (m *Migration) deleteKeyRowsIn(ctx context.Context, set *rowSet, id string) error {
	if err := set.delete(prefixKey + id); err != nil {
		return err
	}
	raw, err := m.get(ctx, prefixKey+id)
	if err != nil {
		return err
	}
	if h, derr := decodeKey(prefixKey+id, raw); raw != nil && derr == nil {
		if err := set.delete(idxKeyByUser + h.UserID + ":" + id); err != nil {
			return err
		}
		if h.TokenHash != "" {
			if err := set.delete(idxKeyByHash + h.TokenHash); err != nil {
				return err
			}
		}
	}
	// The index is derived data: whatever entries name this key go too.
	rows, err := m.scan(ctx, idxKey)
	if err != nil {
		return err
	}
	for _, kv := range rows {
		k := string(kv.Key)
		if (strings.HasPrefix(k, idxKeyByUser) && strings.HasSuffix(k, ":"+id)) || (strings.HasPrefix(k, idxKeyByHash) && string(kv.Value) == id) {
			if err := set.delete(k); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteIdentityRows stages the deletion of one OAuth identity's row and its
// per-user and (provider, subject) index rows.
func (m *Migration) deleteIdentityRows(ctx context.Context, b *storage.Batch, id string) error {
	return m.deleteIdentityRowsIn(ctx, newRowSet(b), id)
}

func (m *Migration) deleteIdentityRowsIn(ctx context.Context, set *rowSet, id string) error {
	if err := set.delete(prefixIdentity + id); err != nil {
		return err
	}
	raw, err := m.get(ctx, prefixIdentity+id)
	if err != nil {
		return err
	}
	if h, derr := decodeIdentity(prefixIdentity+id, raw); raw != nil && derr == nil {
		if err := set.delete(idxOAuthUser + h.UserID + ":" + id); err != nil {
			return err
		}
		if err := set.delete(idxOAuthSub + h.Provider + ":" + h.Subject); err != nil {
			return err
		}
	}
	rows, err := m.scan(ctx, idxOAuth)
	if err != nil {
		return err
	}
	for _, kv := range rows {
		k := string(kv.Key)
		if (strings.HasPrefix(k, idxOAuthUser) && strings.HasSuffix(k, ":"+id)) || (strings.HasPrefix(k, idxOAuthSub) && string(kv.Value) == id) {
			if err := set.delete(k); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteUserRows stages the deletion of a user's row, its index rows and
// every key and identity the user owns (by row or by index entry), appending
// their IDs to ids.
func (m *Migration) deleteUserRows(ctx context.Context, b *storage.Batch, id string, ids *[]string) error {
	set := newRowSet(b)
	if err := set.delete(prefixUser + id); err != nil {
		return err
	}
	idx, err := m.scan(ctx, idxUser)
	if err != nil {
		return err
	}
	for _, kv := range idx {
		if string(kv.Value) == id {
			if err := set.delete(string(kv.Key)); err != nil {
				return err
			}
		}
	}
	keyIDs := map[string]struct{}{}
	krows, err := m.scan(ctx, prefixKey)
	if err != nil {
		return err
	}
	for _, kv := range krows {
		if h, derr := decodeKey(string(kv.Key), kv.Value); derr == nil && h.UserID == id {
			keyIDs[h.ID] = struct{}{}
		}
	}
	kidx, err := m.scan(ctx, idxKeyByUser+id+":")
	if err != nil {
		return err
	}
	for _, kv := range kidx {
		keyIDs[strings.TrimPrefix(string(kv.Key), idxKeyByUser+id+":")] = struct{}{}
	}
	identIDs := map[string]struct{}{}
	irows, err := m.scan(ctx, prefixIdentity)
	if err != nil {
		return err
	}
	for _, kv := range irows {
		if h, derr := decodeIdentity(string(kv.Key), kv.Value); derr == nil && h.UserID == id {
			identIDs[h.ID] = struct{}{}
		}
	}
	iidx, err := m.scan(ctx, idxOAuthUser+id+":")
	if err != nil {
		return err
	}
	for _, kv := range iidx {
		identIDs[strings.TrimPrefix(string(kv.Key), idxOAuthUser+id+":")] = struct{}{}
	}
	for _, kid := range sortedKeys(keyIDs) {
		if err := m.deleteKeyRowsIn(ctx, set, kid); err != nil {
			return err
		}
		*ids = append(*ids, kid)
	}
	for _, iid := range sortedKeys(identIDs) {
		if err := m.deleteIdentityRowsIn(ctx, set, iid); err != nil {
			return err
		}
		*ids = append(*ids, iid)
	}
	return nil
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
