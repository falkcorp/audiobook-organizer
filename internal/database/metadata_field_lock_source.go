// file: internal/database/metadata_field_lock_source.go
// version: 1.4.0
// guid: bee79451-3875-44ce-b66f-6dda1128e3ce
// last-edited: 2026-10-03

package database

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// A field lock is normally a person's: they edited the field, or pressed the
// lock. The Repairs lane also locks the fields it corrects (the title repairs
// lock the title and author they wrote), so a forced rescan cannot write the
// file tags' junk back. Those locks carry their source in
// MetadataFieldState.LockSource so that:
//
//   - the operation revert lifts only the lock its own operation set, never
//     one a person set or touched since (undo.CheckFieldLockCurrent);
//   - a metadata apply a person picked by hand (the search dialog, the manual
//     apply path) may overwrite a repair-locked field: the lock exists to stop
//     machines (the scanner, auto-fetch, the nightly upgrade), not the owner
//     (FieldLocks.WithoutRepairLocks);
//   - a fixer's "user override" hold says the lock is a repair's.

// repairLockPrefix starts every repair lock source.
const repairLockPrefix = "repair:"

// RepairLockSource is the LockSource a Repairs apply operation writes.
func RepairLockSource(opID string) string { return repairLockPrefix + opID }

// RepairLockOp returns the operation id a repair lock source names.
func RepairLockOp(src string) (string, bool) {
	op, ok := strings.CutPrefix(src, repairLockPrefix)
	return op, ok && op != ""
}

// IsRepairLockSource reports whether src names a Repairs apply.
func IsRepairLockSource(src string) bool { return strings.HasPrefix(src, repairLockPrefix) }

// IsRepairLock reports whether the field carries a bare lock a repair set: no
// override value, and a repair lock source.
func (s MetadataFieldState) IsRepairLock() bool {
	return s.OverrideLocked && s.OverrideValue == nil && IsRepairLockSource(s.LockSource)
}

// metadataStateLocks serialize every read-modify-write of one book's field
// state rows in this process. The metadata services' whole-snapshot change
// (metafetch.WithStateSnapshot) holds a book's stripe from its read through
// its save, which rewrites every row and deletes the ones the change removed;
// the Repairs lock write (repairs.Writer.LockFields), RecordUserOverrides,
// ClaimRepairLocks and the revert's lock lift take the same stripe. A snapshot
// read outside it and saved later erased whatever landed in between: a
// person's override, a clear, a repair lock. Striped by book id like the book
// write stripes; no code path holds two at once.
//
// LOCK ORDER: field-state stripe, then book write stripe -- never the other
// way. The operation revert's metadata_update restore
// (audiobooks.RevertService.revertMetadataUpdate) holds a book's field-state
// stripe across its paired-lock check and the ModifyBook that puts the value
// back, so a person's lock take-over cannot land between the two. Nothing may
// take one of these stripes inside a ModifyBook/UpdateBook callback or from
// the book change observer: with both kinds hashed into buckets, two
// different books can share stripes, so a book->state path anywhere would
// deadlock against the revert. Audited 2026-10-03: every caller of
// RecordUserOverrides, ClaimRepairLocks, repairs.Writer.LockFields,
// metafetch.WithStateSnapshot and the revert's lock lift runs after its book
// write returns, and the observer (realtime.BookChangeCoalescer) only records
// the id.
var metadataStateLocks [256]sync.Mutex

// LockMetadataState takes bookID's field-state stripe and returns its
// release. Nothing slow may run under it: Pebble reads and writes only (the
// operation revert's single-book ModifyBook is the one book write that does,
// in the lock order above).
func LockMetadataState(bookID string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(bookID))
	mu := &metadataStateLocks[h.Sum32()%uint32(len(metadataStateLocks))]
	mu.Lock()
	return mu.Unlock
}

// LegacyMetadataStateStore is what MigrateLegacyMetadataState needs.
type LegacyMetadataStateStore interface {
	MetadataFieldStateReader
	UpsertMetadataFieldState(state *MetadataFieldState) error
	DeleteUserPreference(key string) error
}

// LegacyMetadataEntry is one field of the pre-migration blob, as the
// metadata services wrote it.
type LegacyMetadataEntry struct {
	FetchedValue   any       `json:"fetched_value,omitempty"`
	OverrideValue  any       `json:"override_value,omitempty"`
	OverrideLocked bool      `json:"override_locked"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// MigrateLegacyMetadataState moves a book's pre-migration state blob into
// field-state rows and deletes the blob, the migration the metadata services
// run when a book is opened. It does nothing (false, nil) when the book has
// rows already or no blob. Call it under LockMetadataState(bookID).
//
// A writer that is about to create a book's FIRST row must migrate first:
// database.LockedUserFields reads the blob only while a book has no rows, so a
// first row written over an unmigrated blob hides every lock the blob holds.
// A blob that does not parse is returned as an error and nothing is written.
func MigrateLegacyMetadataState(store LegacyMetadataStateStore, bookID string) (bool, error) {
	rows, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return false, fmt.Errorf("read field states of %s: %w", bookID, err)
	}
	if len(rows) > 0 {
		return false, nil
	}
	entries, err := ParseLegacyMetadataState(store, bookID)
	if err != nil || len(entries) == 0 {
		return false, err
	}
	now := time.Now()
	for field, e := range entries {
		fetched, err := metastate.Encode(e.FetchedValue)
		if err != nil {
			return false, fmt.Errorf("encode legacy %s of %s: %w", field, bookID, err)
		}
		override, err := metastate.Encode(e.OverrideValue)
		if err != nil {
			return false, fmt.Errorf("encode legacy %s of %s: %w", field, bookID, err)
		}
		at := e.UpdatedAt
		if at.IsZero() {
			at = now
		}
		if err := store.UpsertMetadataFieldState(&MetadataFieldState{BookID: bookID, Field: field,
			FetchedValue: fetched, OverrideValue: override, OverrideLocked: e.OverrideLocked, UpdatedAt: at}); err != nil {
			return false, fmt.Errorf("migrate legacy %s of %s: %w", field, bookID, err)
		}
	}
	if err := DeleteLegacyMetadataState(store, bookID); err != nil {
		return false, fmt.Errorf("retire legacy metadata state of %s: %w", bookID, err)
	}
	return true, nil
}

// ParseLegacyMetadataState reads and parses bookID's pre-migration blob: nil
// when there is none, an error when it does not parse (such a book cannot be
// migrated, so a writer that needs its rows must hold it).
func ParseLegacyMetadataState(reader MetadataFieldStateReader, bookID string) (map[string]LegacyMetadataEntry, error) {
	pref, err := reader.GetUserPreference(metastate.Key(bookID))
	if err != nil {
		return nil, fmt.Errorf("read legacy metadata state of %s: %w", bookID, err)
	}
	if pref == nil || pref.Value == nil || *pref.Value == "" {
		return nil, nil
	}
	var entries map[string]LegacyMetadataEntry
	if err := json.Unmarshal([]byte(*pref.Value), &entries); err != nil {
		return nil, fmt.Errorf("legacy metadata state of %s does not parse: %w", bookID, err)
	}
	return entries, nil
}

// RepairLockClaimStore is what ClaimRepairLocks needs.
type RepairLockClaimStore interface {
	GetMetadataFieldStates(bookID string) ([]MetadataFieldState, error)
	UpsertMetadataFieldState(state *MetadataFieldState) error
}

// ClaimRepairLocks turns the repair locks on keys into a person's locks
// (LockSource cleared, the lock kept), under the book's field-state stripe.
// A metadata apply a person picked by hand calls it for the repair-locked
// fields it was allowed to write: the value is now the person's choice, so
// reverting the repair must not lift the lock and let a rescan restore the
// junk over it. A key whose row is not a repair lock is left alone.
func ClaimRepairLocks(store RepairLockClaimStore, bookID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	unlock := LockMetadataState(bookID)
	defer unlock()
	rows, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return fmt.Errorf("read field states of %s: %w", bookID, err)
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	for i := range rows {
		if !want[rows[i].Field] || !rows[i].IsRepairLock() {
			continue
		}
		row := rows[i]
		row.LockSource = ""
		if err := store.UpsertMetadataFieldState(&row); err != nil {
			return fmt.Errorf("claim the %s lock of %s: %w", row.Field, bookID, err)
		}
	}
	return nil
}
