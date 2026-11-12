// file: core/backup/journal/hookrun.go
// version: 1.0.1
// guid: 615fe5fe-cd99-4083-af97-16b095ea11d6
// last-edited: 2026-11-12

package journal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// hookRunKey is c/backup/restore_run: the ID of the restore hook run in progress
// when no restore line could be journaled for it (a plain write, like
// restore_hooked/<seq>).
const hookRunKey = "restore_run"

// hookRunPrefix starts every run ID. A restore line's seq is a decimal number, so
// no run ID is ever equal to one.
const hookRunPrefix = "run:"

type hookRunRecord struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

// EnsureHookRun returns the ID of the restore hook run in progress, recording a
// new one (synchronously) when there is none. A restore whose journal line could
// not be written has no seq to tell its hook run from the run of another
// restore, and the records the hooks leave (audit's stage, revoke and apply) are
// keyed on that identity so a re-run after a crash stays idempotent: the ID is
// the same for every re-run until ClearHookRun, and different for the next
// restore. A record that is present and does not decode is an error, not a
// reason to mint a second ID next to it.
func EnsureHookRun(ctx context.Context, ks storage.Keyspace, at time.Time) (string, error) {
	id, err := HookRunID(ctx, ks)
	if err != nil || id != "" {
		return id, err
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("journal: make a restore run ID: %w", err)
	}
	id = hookRunPrefix + hex.EncodeToString(raw[:])
	val, err := json.Marshal(hookRunRecord{ID: id, At: at.UTC()})
	if err != nil {
		return "", err
	}
	b := ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := b.In(ks).Set([]byte(hookRunKey), val); err != nil {
		return "", err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return "", fmt.Errorf("journal: record %s: %w", hookRunKey, err)
	}
	return id, nil
}

// HookRunID is the ID EnsureHookRun recorded, "" when none is in progress.
func HookRunID(ctx context.Context, ks storage.Reader) (string, error) {
	raw, err := ks.Get(ctx, []byte(hookRunKey))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("journal: read %s: %w", hookRunKey, err)
	}
	var rec hookRunRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return "", fmt.Errorf("journal: %s does not decode: %w", hookRunKey, err)
	}
	if rec.ID == "" {
		return "", fmt.Errorf("journal: %s holds no run ID", hookRunKey)
	}
	return rec.ID, nil
}

// ClearHookRun ends the run in progress: the next restore without a line gets a
// new ID. A run that is not recorded is not an error.
func ClearHookRun(ctx context.Context, ks storage.Keyspace) (err error) {
	b := ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := b.In(ks).Delete([]byte(hookRunKey)); err != nil {
		return err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("journal: clear %s: %w", hookRunKey, err)
	}
	return nil
}
