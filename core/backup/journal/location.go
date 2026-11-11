// file: core/backup/journal/location.go
// version: 1.0.0
// guid: 8f35a1c2-5d4e-4b7a-9c63-2e1f0d8a7b54
// last-edited: 2026-11-11

package journal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/falkcorp/mediacore/storage"
)

// LocationFile is the name of <state_dir>/backup.location.json (05-backup
// §5B.3.6 row "Where"). restoreswap saves a copy of it at step c and renames
// it back at step x under the same name.
const LocationFile = "backup.location.json"

// Location is backup.location.json: where the journal is and which store it
// describes, and which generation, content marker and committed journal head
// that store last served (05-backup §5B.3.6 rows "Where", "Head in the store",
// "Content marker"). The paths are absolute. Store, ServedBoot and Head are
// compared only when the file Describes the store at hand (SEC6-8): a state
// directory two stores share holds one file, and it describes one of them.
type Location struct {
	BackupDir  string
	Journal    string
	DBPath     string
	InstallID  string
	Store      storage.StoreIdentity
	ServedBoot ServedBoot
	Head       Head
}

// locationWire is the file's JSON form.
type locationWire struct {
	BackupDir  string                `json:"backup_dir"`
	Journal    string                `json:"journal"`
	DBPath     string                `json:"db_path"`
	InstallID  string                `json:"install_id"`
	Store      storage.StoreIdentity `json:"store"`
	ServedBoot ServedBoot            `json:"served_boot"`
	Head       headRecord            `json:"head"`
}

// Describes reports whether the file describes the store at dbPath with this
// install ID (§5B.3.6 row "Where": `db_path` equals the resolved
// core.storage.path and `install_id` equals the store's marker). Only then are
// its store, served_boot and head members evidence about that store.
func (l Location) Describes(dbPath, installID string) bool {
	return l.DBPath != "" && l.DBPath == dbPath && l.InstallID != "" && l.InstallID == installID
}

// HasServedBoot reports whether the file records a content marker (a file
// written before any journaling boot, or by a build without one, has none).
func (l Location) HasServedBoot() bool { return l.ServedBoot.N > 0 }

// ReadLocation reads <stateDir>/backup.location.json. A missing file is
// (zero, false, nil): the boot then reads "no marker" for this store and
// writes the file. A file that does not decode is an error; callers treat it
// as absent for evidence and rewrite it.
func ReadLocation(stateDir string) (Location, bool, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, LocationFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Location{}, false, nil
	case err != nil:
		return Location{}, false, fmt.Errorf("journal: read %s: %w", LocationFile, err)
	}
	var w locationWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Location{}, false, fmt.Errorf("journal: decode %s: %w", LocationFile, err)
	}
	return Location{BackupDir: w.BackupDir, Journal: w.Journal, DBPath: w.DBPath, InstallID: w.InstallID,
		Store: w.Store, ServedBoot: w.ServedBoot, Head: Head(w.Head)}, true, nil
}

// renameLocation is the rename WriteLocation ends with (a seam for the crash
// test: a crash between the temp file and the rename).
var renameLocation = os.Rename

// WriteLocation writes l to <stateDir>/backup.location.json atomically and
// durably: a temp file in the same directory (0600) is written and fsynced,
// renamed over the file, and the directory is fsynced. A crash anywhere before
// the rename leaves the previous file intact; one after it leaves the new one.
//
// It does no locking: in a running process only the journal component writes
// the file and it holds one mutex across every read-modify-write (DATA7-14),
// so the callers outside this package are the component's methods.
func WriteLocation(stateDir string, l Location) error {
	raw, err := json.Marshal(locationWire{BackupDir: l.BackupDir, Journal: l.Journal, DBPath: l.DBPath, InstallID: l.InstallID,
		Store: l.Store, ServedBoot: l.ServedBoot, Head: headRecord(l.Head)})
	if err != nil {
		return fmt.Errorf("journal: encode %s: %w", LocationFile, err)
	}
	raw = append(raw, '\n')
	final := filepath.Join(stateDir, LocationFile)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("journal: create %s: %w", tmp, err)
	}
	n, err := f.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return errors.Join(fmt.Errorf("journal: write %s: %w", tmp, err), os.Remove(tmp))
	}
	if err := renameLocation(tmp, final); err != nil {
		return errors.Join(fmt.Errorf("journal: rename %s: %w", tmp, err), os.Remove(tmp))
	}
	return syncDir(stateDir)
}

// ReadServedBoot reads the committed c/backup/served_boot through ks: the zero
// pair when the store has none (it predates the marker, or is an older
// archive).
func ReadServedBoot(ctx context.Context, ks storage.Keyspace) (ServedBoot, error) {
	return decodeServedBoot(ks.Get(ctx, []byte(servedBootKey)))
}

func decodeServedBoot(raw []byte, err error) (ServedBoot, error) {
	var p ServedBoot
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return p, nil
	case err != nil:
		return p, err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("decode %s: %w", servedBootKey, err)
	}
	return p, nil
}

// NextServedBoot writes the store's next content marker and returns it:
// n = max(the store's n, fileN) + 1 with a fresh random nonce (§5B.3.6 row
// "Content marker"), read with GetForUpdate and committed synchronously
// (c/backup/served_boot is Serialized, every write of it a GetForUpdate,
// COR6-6), retried on a conflict. fileN is backup.location.json's n when the
// file describes this store, 0 otherwise. Create calls it right after its
// checkpoint (through the component's AdvanceServedBoot), and a journaling
// boot at the end of P4d step 2 (on a restore boot after its hooks).
func NextServedBoot(ctx context.Context, ks storage.Keyspace, fileN uint64) (ServedBoot, error) {
	var next ServedBoot
	err := storage.RetryOnConflict(ctx, 5, func(ctx context.Context) (err error) {
		b := ks.NewBatch()
		defer func() { err = errors.Join(err, b.Close()) }()
		cur, err := decodeServedBoot(b.GetForUpdate(ctx, ks, []byte(servedBootKey)))
		if err != nil {
			return fmt.Errorf("journal: read %s: %w", servedBootKey, err)
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return fmt.Errorf("journal: a nonce for %s: %w", servedBootKey, err)
		}
		next = ServedBoot{N: max(cur.N, fileN) + 1, Nonce: hex.EncodeToString(nonce[:])}
		v, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := b.In(ks).Set([]byte(servedBootKey), v); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		return ServedBoot{}, fmt.Errorf("journal: write %s: %w", servedBootKey, err)
	}
	return next, nil
}
