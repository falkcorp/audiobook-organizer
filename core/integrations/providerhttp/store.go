// file: core/integrations/providerhttp/store.go
// version: 1.0.0
// guid: ec22f9ca-2b95-466e-b63c-1d0ae214f66c
// last-edited: 2026-11-11

package providerhttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// GateStore persists holds and daily quota counters (05-metadata M2.3, R4-1).
// SDK surface. Core ships its one implementation, NativeGateStore.
//
// Writes take no caller batch and carry no commit-time precondition: each
// commits its own batch. The Registry is the only caller, and it serialises
// every read-modify-write of one scope's hold and of one counter under its own
// in-process mutex across the store's commit (M2.6), so the store needs no
// Serialized declaration. (Brief step 5 mentions Expect; the spec's M2.3 says
// no commit-time check, and an Expect on an undeclared range fails with
// storage.ErrNotSerialized, so the spec is followed.)
type GateStore interface {
	// Holds lists every persisted hold.
	Holds(ctx context.Context) ([]integrations.Hold, error)
	// PutHold stores h; a zero h.Until deletes the scope's record.
	PutHold(ctx context.Context, h integrations.Hold) error
	// Quota returns the newest stored day of the counter and its count;
	// ("", 0, nil) when it has none.
	Quota(ctx context.Context, scope, counter string) (day string, used int, err error)
	// PutQuota stores the count of day and deletes days before yesterday.
	PutQuota(ctx context.Context, scope, counter, day string, used int) error
}

// Owner is the storage owner of the c/providerhttp/ keyspace (the name the
// kernel's fixed subsystem order calls "providerhttp"; 05-storage §5S.9.1).
const Owner storage.Owner = "core/providerhttp"

// StorageSpec is the spec the kernel registers for Owner: a core keyspace with
// no Serialized range (see GateStore).
func StorageSpec() storage.Spec { return storage.Spec{Keyspace: true} }

const (
	holdsSeg = "holds"
	quotaSeg = "quota"
	dayForm  = "2006-01-02"
)

// quotaCount is the value of c/providerhttp/quota/<scope>/<counter>/<day>.
type quotaCount struct {
	Used int `json:"used"`
}

// NativeGateStore is core's GateStore over its own keyspace c/providerhttp/
// (M2.4 "Native store"):
//
//	holds/<scope>                  codec.JSON[integrations.Hold](1)
//	quota/<scope>/<counter>/<day>  codec.JSON[quotaCount](1)
//
// Each segment is one escaped key segment, so a scope or counter holding any
// byte stays inside its own prefix.
func NativeGateStore(ks storage.Keyspace) GateStore { return nativeStore{ks: ks} }

type nativeStore struct{ ks storage.Keyspace }

func holdKey(scope string) []byte { return keys.Join(keys.Str(holdsSeg), keys.Str(scope)) }

func quotaPrefix(scope, counter string) []byte {
	return keys.Prefix(keys.Str(quotaSeg), keys.Str(scope), keys.Str(counter))
}

func quotaKey(scope, counter, day string) []byte {
	return keys.Join(keys.Str(quotaSeg), keys.Str(scope), keys.Str(counter), keys.Str(day))
}

// Holds skips a row it cannot decode: holds fail open (M2.5), and one damaged
// row must not hide the rest.
func (s nativeStore) Holds(ctx context.Context) ([]integrations.Hold, error) {
	var out []integrations.Hold
	for kv, err := range s.ks.ScanPrefix(ctx, keys.Prefix(keys.Str(holdsSeg))) {
		if err != nil {
			return nil, fmt.Errorf("providerhttp: read holds: %w", err)
		}
		h, derr := codec.JSON[integrations.Hold](1).Decode(kv.Value)
		if derr != nil || h.Scope == "" {
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

func (s nativeStore) PutHold(ctx context.Context, h integrations.Hold) error {
	if h.Scope == "" {
		return errors.New("providerhttp: a hold needs a scope")
	}
	if h.Until.IsZero() {
		return s.ks.Delete(ctx, holdKey(h.Scope))
	}
	raw, err := codec.JSON[integrations.Hold](1).Encode(h)
	if err != nil {
		return err
	}
	return s.ks.Set(ctx, holdKey(h.Scope), raw)
}

// Quota reads the newest day; a row that cannot be decoded is an error (the
// counter fails closed, M2.5), never a zero.
func (s nativeStore) Quota(ctx context.Context, scope, counter string) (string, int, error) {
	var day string
	var used int
	for kv, err := range s.ks.ScanPrefix(ctx, quotaPrefix(scope, counter)) {
		if err != nil {
			return "", 0, fmt.Errorf("providerhttp: read quota: %w", err)
		}
		segs, serr := keys.Split(kv.Key)
		if serr != nil || len(segs) != 4 {
			return "", 0, fmt.Errorf("providerhttp: quota key %q is malformed", kv.Key)
		}
		q, derr := codec.JSON[quotaCount](1).Decode(kv.Value)
		if derr != nil {
			return "", 0, fmt.Errorf("providerhttp: quota row: %w", derr)
		}
		if d := string(segs[3]); d >= day {
			day, used = d, q.Used
		}
	}
	return day, used, nil
}

func (s nativeStore) PutQuota(ctx context.Context, scope, counter, day string, used int) (err error) {
	when, err := time.Parse(dayForm, day)
	if err != nil {
		return fmt.Errorf("providerhttp: quota day %q: %w", day, err)
	}
	raw, err := codec.JSON[quotaCount](1).Encode(quotaCount{Used: used})
	if err != nil {
		return err
	}
	cutoff := when.AddDate(0, 0, -1).Format(dayForm) // days before yesterday go
	b := s.ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := b.Set(quotaKey(scope, counter, day), raw); err != nil {
		return err
	}
	for kv, serr := range s.ks.ScanPrefix(ctx, quotaPrefix(scope, counter)) {
		if serr != nil {
			return fmt.Errorf("providerhttp: prune quota: %w", serr)
		}
		segs, kerr := keys.Split(kv.Key)
		if kerr != nil || len(segs) != 4 {
			continue
		}
		if d := string(segs[3]); d < cutoff && !bytes.Equal(kv.Key, quotaKey(scope, counter, day)) {
			if err := b.Delete(bytes.Clone(kv.Key)); err != nil {
				return err
			}
		}
	}
	return b.Commit(ctx, storage.Sync)
}

// memStore is the in-memory GateStore of Options.Store == nil.
type memStore struct {
	mu    sync.Mutex
	holds map[string]integrations.Hold
	quota map[string]map[string]int // scope\x00counter -> day -> used
}

func newMemStore() *memStore {
	return &memStore{holds: map[string]integrations.Hold{}, quota: map[string]map[string]int{}}
}

func (m *memStore) Holds(context.Context) ([]integrations.Hold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Collect(maps.Values(m.holds))
	slices.SortFunc(out, func(a, b integrations.Hold) int { return compareStr(a.Scope, b.Scope) })
	return out, nil
}

func (m *memStore) PutHold(_ context.Context, h integrations.Hold) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h.Until.IsZero() {
		delete(m.holds, h.Scope)
		return nil
	}
	m.holds[h.Scope] = h
	return nil
}

func (m *memStore) Quota(_ context.Context, scope, counter string) (string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var day string
	var used int
	for d, n := range m.quota[scope+"\x00"+counter] {
		if d >= day {
			day, used = d, n
		}
	}
	return day, used, nil
}

func (m *memStore) PutQuota(_ context.Context, scope, counter, day string, used int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := scope + "\x00" + counter
	if m.quota[k] == nil {
		m.quota[k] = map[string]int{}
	}
	m.quota[k][day] = used
	if when, err := time.Parse(dayForm, day); err == nil {
		cutoff := when.AddDate(0, 0, -1).Format(dayForm)
		for d := range m.quota[k] {
			if d < cutoff {
				delete(m.quota[k], d)
			}
		}
	}
	return nil
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
