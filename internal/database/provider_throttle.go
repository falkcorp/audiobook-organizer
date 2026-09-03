// file: internal/database/provider_throttle.go
// version: 1.0.0
// guid: 954d6574-128c-4af7-b839-dcd891c661bd
// last-edited: 2026-09-03

package database

import (
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// providerThrottlePfx is the Pebble keyspace for global metadata-provider
// throttles. Key layout: provider_throttle:<provider id> → opaque JSON payload.
//
// The payload is deliberately opaque here. The domain type lives in
// internal/metadata (which already imports this package, so the dependency
// cannot run the other way), and these three methods exist only so a hold set
// at 03:00 is still in force after the 04:00 restart -- prod restarted 146
// times in 30 days, and an in-memory-only hold forgets a 4-hour quota block on
// every one of them.
//
// These methods are NOT on the Store interface. internal/metadata declares a
// three-method port and capability-asserts for it, so nothing here widens a
// 398-method interface or forces a mock regeneration; a store that does not
// implement them degrades to in-memory-only throttling with one warning.
const providerThrottlePfx = "provider_throttle:"

func providerThrottleKey(providerID string) []byte {
	return []byte(providerThrottlePfx + providerID)
}

// LoadProviderThrottles returns every persisted throttle payload keyed by
// provider id.
func (p *PebbleStore) LoadProviderThrottles() (map[string][]byte, error) {
	prefix := []byte(providerThrottlePfx)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("list provider throttles: %w", err)
	}
	defer func() { _ = iter.Close() }()

	out := make(map[string][]byte)
	for iter.First(); iter.Valid(); iter.Next() {
		id := strings.TrimPrefix(string(iter.Key()), providerThrottlePfx)
		if id == "" {
			continue
		}
		// iter.Value() is only valid until the next iteration step.
		out[id] = append([]byte(nil), iter.Value()...)
	}
	return out, iter.Error()
}

// SaveProviderThrottle writes (or replaces) one provider's hold.
func (p *PebbleStore) SaveProviderThrottle(providerID string, payload []byte) error {
	if strings.TrimSpace(providerID) == "" {
		return fmt.Errorf("save provider throttle: empty provider id")
	}
	return p.db.Set(providerThrottleKey(providerID), payload, pebble.Sync)
}

// DeleteProviderThrottle removes one provider's hold. Deleting an absent key is
// not an error -- both expiry and the manual reset take this path and neither
// should care whether a row happened to be there.
func (p *PebbleStore) DeleteProviderThrottle(providerID string) error {
	return p.db.Delete(providerThrottleKey(providerID), pebble.Sync)
}
