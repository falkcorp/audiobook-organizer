// file: internal/database/pebble_store_sync_alias_use.go
// version: 1.1.0
// guid: d69a8939-cf20-4007-a46e-2e439f5957e1
// last-edited: 2026-09-25

// Package database: sync_alias_use keyspace. It records which merge-loser
// libraryItemIds (aliases) each user's ABS client has actually addressed.
//
// A merge leaves the loser's syncID redirecting to the survivor (sync_item:,
// RecordSyncMerge). A client that still holds the loser id keys its local
// progress row by it, so the ABS /api/me list must carry a row under that id
// or the client deletes it (abs/userdata.go ClientMediaProgress). Sending one
// for EVERY alias makes a client count a finished merged book once per id
// (AudioBooth's Stats "items finished"). This keyspace narrows the alias rows
// to ids the user's client has used: the ABS handlers record an alias here
// whenever a request addresses one.
//
// Keys:
//   - sync_alias_use:<userID>:<aliasSyncID> -> JSON syncAliasUse
//   - sync_alias_use_seeded:<userID>        -> "1": the one-time seed ran to
//     completion (SeedSyncAliasUses with complete=true). A different prefix,
//     so the per-user scan of sync_alias_use:<userID>: never sees it.
package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// SyncAliasUseStore is implemented by PebbleStore. Obtain it with
// AsSyncAliasUseStore; store.go stays untouched.
type SyncAliasUseStore interface {
	// RecordSyncAliasUse records that userID's client addressed aliasSyncID.
	// Idempotent: an existing record is left as it is.
	RecordSyncAliasUse(userID, aliasSyncID string) error
	// ListSyncAliasUses returns every alias recorded for userID, sorted, and
	// whether the one-time seed has run for that user.
	ListSyncAliasUses(userID string) (aliases []string, seeded bool, err error)
	// SeedSyncAliasUses records aliases and, when complete, marks userID as
	// seeded, in one batch. complete=false records the aliases only, so the
	// caller's next attempt runs the seed again (a partial seed: some
	// lookups failed). Aliases already recorded are left as they are.
	SeedSyncAliasUses(userID string, aliases []string, complete bool) error
}

// AsSyncAliasUseStore returns s as a SyncAliasUseStore, looking through the
// indexedStore decorator (store_capability.go), or nil.
func AsSyncAliasUseStore(s any) SyncAliasUseStore {
	if s == nil {
		return nil
	}
	if as, ok := AsCapability[SyncAliasUseStore](s); ok {
		return as
	}
	return nil
}

var _ SyncAliasUseStore = (*PebbleStore)(nil)

// syncAliasUse is the stored record. FirstUsedAt is diagnostic only.
type syncAliasUse struct {
	FirstUsedAt time.Time `json:"first_used_at"`
}

func syncAliasUsePrefix(userID string) []byte {
	return []byte("sync_alias_use:" + userID + ":")
}

func syncAliasUseKey(userID, aliasSyncID string) []byte {
	return append(syncAliasUsePrefix(userID), aliasSyncID...)
}

func syncAliasUseSeededKey(userID string) []byte {
	return []byte("sync_alias_use_seeded:" + userID)
}

// validSyncAliasUseIDs rejects ids that would make a key ambiguous: a ':' in
// the user id would let one user's prefix cover another's keys.
func validSyncAliasUseIDs(userID, aliasSyncID string) error {
	if userID == "" || strings.Contains(userID, ":") {
		return fmt.Errorf("sync alias use: invalid user id %q", userID)
	}
	if aliasSyncID == "" || strings.Contains(aliasSyncID, ":") {
		return fmt.Errorf("sync alias use: invalid alias id %q", aliasSyncID)
	}
	return nil
}

// RecordSyncAliasUse implements SyncAliasUseStore.
func (p *PebbleStore) RecordSyncAliasUse(userID, aliasSyncID string) error {
	if err := validSyncAliasUseIDs(userID, aliasSyncID); err != nil {
		return err
	}
	key := syncAliasUseKey(userID, aliasSyncID)
	_, closer, err := p.db.Get(key)
	if err == nil {
		closer.Close()
		return nil
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	// A concurrent first use can write twice; both values are equivalent, so
	// no lock is needed.
	value, err := json.Marshal(syncAliasUse{FirstUsedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	return p.db.Set(key, value, pebble.Sync)
}

// ListSyncAliasUses implements SyncAliasUseStore. The alias id is the key and
// the value holds only a diagnostic timestamp, so the value is not decoded: a
// corrupt one cannot hide a recorded use.
func (p *PebbleStore) ListSyncAliasUses(userID string) ([]string, bool, error) {
	if userID == "" || strings.Contains(userID, ":") {
		return nil, false, fmt.Errorf("sync alias use: invalid user id %q", userID)
	}
	prefix := syncAliasUsePrefix(userID)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, false, err
	}
	var aliases []string
	for iter.First(); iter.Valid(); iter.Next() {
		aliases = append(aliases, string(iter.Key()[len(prefix):]))
	}
	if err := iter.Close(); err != nil {
		return nil, false, err
	}
	sort.Strings(aliases)

	_, closer, err := p.db.Get(syncAliasUseSeededKey(userID))
	switch {
	case err == nil:
		closer.Close()
		return aliases, true, nil
	case errors.Is(err, pebble.ErrNotFound):
		return aliases, false, nil
	default:
		return nil, false, err
	}
}

// SeedSyncAliasUses implements SyncAliasUseStore.
func (p *PebbleStore) SeedSyncAliasUses(userID string, aliases []string, complete bool) error {
	if userID == "" || strings.Contains(userID, ":") {
		return fmt.Errorf("sync alias use: invalid user id %q", userID)
	}
	batch := p.db.NewBatch()
	defer batch.Close()
	now := time.Now().UTC()
	for _, alias := range aliases {
		if err := validSyncAliasUseIDs(userID, alias); err != nil {
			return err
		}
		key := syncAliasUseKey(userID, alias)
		_, closer, err := p.db.Get(key)
		if err == nil {
			closer.Close()
			continue
		}
		if !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
		value, err := json.Marshal(syncAliasUse{FirstUsedAt: now})
		if err != nil {
			return err
		}
		if err := batch.Set(key, value, nil); err != nil {
			return err
		}
	}
	if complete {
		if err := batch.Set(syncAliasUseSeededKey(userID), []byte("1"), nil); err != nil {
			return err
		}
	}
	if batch.Empty() {
		return nil
	}
	return batch.Commit(pebble.Sync)
}
