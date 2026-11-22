// file: core/backup/journal/hookrun.go
// version: 1.0.1
// guid: 615fe5fe-cd99-4083-af97-16b095ea11d6
// last-edited: 2026-11-12

package journal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// HookRunFile is <state_dir>/restore_hook_run.json: the ID of the restore hook
// run in progress when the run has no restore intent of its own (a replaced or
// external store). It lives beside backup.location.json and the restore journal,
// on the live host and outside the store, because the keys derived from it let a
// run SKIP work (auth's credential-epoch bump, a journal line, audit's records).
// A store, an archive or a checkpoint carries only what is in the keyspace, so no
// archive can hand a run this ID back; the earlier home of the ID, the c/backup/
// keyspace, could be carried by an archive taken mid-run.
const HookRunFile = "restore_hook_run.json"

// legacyHookRunKey is c/backup/restore_run, where builds before F4-08 B10m kept
// the run ID. Nothing reads it for a key any more: a row is cleared
// (ClearLegacyHookRun) and never believed.
const legacyHookRunKey = "restore_run"

// hookRunPrefix starts every run ID. A restore line's seq is a decimal number and
// a restore intent's key starts "restore:", so no run ID is ever equal to either.
const hookRunPrefix = "run:"

// hookRunRecord is restore_hook_run.json.
type hookRunRecord struct {
	ID string `json:"id"`
	// Binding is the SHA-256 (hex) of what the run was minted for: the caller's
	// description of the replaced store's state (EnsureHookRun's binding). A run
	// that finds the file with another binding is not the run the file was made
	// for, and gets a new ID.
	Binding string    `json:"binding"`
	At      time.Time `json:"at"`
}

// EnsureHookRun returns the ID of the restore hook run in progress for a run
// with no restore intent, recording a new one (synchronously: a temporary file
// written and fsynced, renamed into place, the state directory fsynced) when there
// is none. The ID is minted here, on the live host, from crypto/rand.
//
// binding says what the run is for: everything about the replaced store that is
// the same on every attempt of this run and that a finished run changes (the
// external restore lines whose hooks are owed, or, with none, the location file's
// content marker). The file is honoured only when it was written for the same
// binding. That is what makes the ID safe to find again after a crash and unsafe
// to find in any other state: a file left behind by a run that a build which does
// not know it (an older one, after a rollback) finished is never the run now
// starting, so it is replaced and never reused. A file that is unreadable as a
// record or names another binding is replaced; only a file that cannot be read at
// all is an error. A doubtful file leads to a new ID, which bumps and journals
// again, and never to a skip.
func EnsureHookRun(stateDir, binding string, at time.Time, opts ...FSOption) (string, error) {
	if binding == "" {
		return "", errors.New("journal: a restore hook run needs a binding")
	}
	fsys := fsOf(opts...)
	path := filepath.Join(stateDir, HookRunFile)
	want := bindingDigest(binding)
	raw, err := fsys.ReadFile(path)
	switch {
	case err == nil:
		var rec hookRunRecord
		if json.Unmarshal(raw, &rec) == nil && strings.HasPrefix(rec.ID, hookRunPrefix) && rec.Binding == want {
			return rec.ID, nil
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("journal: read %s: %w", HookRunFile, err)
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("journal: make a restore run ID: %w", err)
	}
	rec := hookRunRecord{ID: hookRunPrefix + hex.EncodeToString(rnd[:]), Binding: want, At: at.UTC()}
	val, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("journal: encode %s: %w", HookRunFile, err)
	}
	tmp := path + ".tmp"
	if err := writeStateTmp(fsys, LabelHookRunTmp, LabelHookRunCleanup, tmp, append(val, '\n')); err != nil {
		return "", err
	}
	if err := renameStateTmp(fsys, LabelHookRunRename, LabelHookRunCleanup, tmp, path); err != nil {
		return "", err
	}
	if err := syncDir(fsys, LabelHookRunDirSync, stateDir); err != nil {
		return "", fmt.Errorf("journal: sync the state directory after %s: %w", HookRunFile, err)
	}
	return rec.ID, nil
}

// bindingDigest is how a binding is kept: hashed, so the file holds no journal
// line's MAC or marker.
func bindingDigest(binding string) string {
	sum := sha256.Sum256([]byte(binding))
	return hex.EncodeToString(sum[:])
}

// ClearHookRun ends the run in progress, and removes a file no run is using: the
// next run with no restore intent gets a new ID. No file is not an error and does
// nothing (no operation, no fsync), so a boot with nothing to clear writes
// nothing.
func ClearHookRun(stateDir string, opts ...FSOption) error {
	fsys := fsOf(opts...)
	path := filepath.Join(stateDir, HookRunFile)
	if _, err := fsys.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("journal: look for %s: %w", HookRunFile, err)
	}
	if err := removeLabelled(fsys, LabelHookRunClear, path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("journal: remove %s: %w", HookRunFile, err)
	}
	if err := syncDir(fsys, LabelHookRunClearDirSync, stateDir); err != nil {
		return fmt.Errorf("journal: sync the state directory after removing %s: %w", HookRunFile, err)
	}
	return nil
}

// ClearLegacyHookRun deletes c/backup/restore_run, the run ID a build before
// F4-08 B10m kept in the store, if the store has it. Such a row can arrive in an
// archive or be left by a rollback's build; this build never reads it for a key
// and removes it so that no build can take it for one. No row is not an error and
// commits nothing.
func ClearLegacyHookRun(ctx context.Context, ks storage.Keyspace) (err error) {
	if _, err := ks.Get(ctx, []byte(legacyHookRunKey)); errors.Is(err, storage.ErrNotFound) {
		return nil
	} else if err != nil {
		return fmt.Errorf("journal: look for %s: %w", legacyHookRunKey, err)
	}
	b := ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := b.In(ks).Delete([]byte(legacyHookRunKey)); err != nil {
		return err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("journal: clear %s: %w", legacyHookRunKey, err)
	}
	return nil
}
