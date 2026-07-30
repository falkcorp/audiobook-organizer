// file: internal/database/pebble_store_syncfile.go
// version: 1.0.0
// guid: 92ebd115-9f89-4400-ac26-bf9a065b6153
// last-edited: 2026-07-30

package database

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// SyncFile is the durable per-file identity record backing the ABS
// contentUrl file addressing scheme (/api/items/{itemId}/file/{ino}). See
// docs/specs/2026-07-29-abs-sync-api-design.md §4.2b.
//
// ino must survive file replacement (same logical track, new physical
// BookFile row -- a remux or a quality upgrade), which is why it is
// indirected through this keyspace instead of exposing BookFile.ID
// directly: BookFile.ID is stable across in-place updates but a genuinely
// new row (delete+create) mints a fresh ULID with nothing to preserve an
// offline client's cached download URL.
type SyncFile struct {
	SyncFileID    string    `json:"sync_file_id"`
	BookID        string    `json:"book_id"`
	CurrentFileID string    `json:"current_file_id"` // the live BookFile.ID
	CreatedAt     time.Time `json:"created_at"`
}

// syncFileMintMu guards the check-then-mint race in MintOrGetSyncFileID.
// Separate from TASK-01's syncIDMintMu (sync_item keyspace): the two
// keyspaces are independent, so there is no reason to serialize unrelated
// work behind a shared lock.
var syncFileMintMu sync.Mutex

// SyncFileStore is a small additive capability interface for the sync_file
// keyspace. It is intentionally NOT added to the Store interface in
// store.go -- callers type-assert via AsSyncFileStore instead, so adding
// this capability never requires touching every other Store implementation
// (mocks included).
type SyncFileStore interface {
	MintOrGetSyncFileID(bookID, fileID string) (string, error)
	GetSyncFileID(bookID, fileID string) (string, bool, error)
	ListSyncFilesForBook(bookID string) ([]SyncFile, error)
	RepointSyncFile(bookID, oldFileID, newFileID string) error
}

// AsSyncFileStore type-asserts s into a SyncFileStore, returning nil if s is
// nil or does not implement the capability.
func AsSyncFileStore(s any) SyncFileStore {
	if s == nil {
		return nil
	}
	if sf, ok := s.(SyncFileStore); ok {
		return sf
	}
	return nil
}

func syncFileLookupKey(bookID, fileID string) []byte {
	return []byte(fmt.Sprintf("sync_file:lookup:%s:%s", bookID, fileID))
}

func syncFileBookKey(bookID, syncFileID string) []byte {
	return []byte(fmt.Sprintf("sync_file:book:%s:%s", bookID, syncFileID))
}

func syncFileRecordKey(syncFileID string) []byte {
	return []byte(fmt.Sprintf("sync_file:%s", syncFileID))
}

// MintOrGetSyncFileID returns the durable syncFileID for the (bookID,
// fileID) pair, minting one on first encounter. Concurrent callers racing
// on the same pair all observe the same winning ID.
func (s *PebbleStore) MintOrGetSyncFileID(bookID, fileID string) (string, error) {
	if bookID == "" {
		return "", fmt.Errorf("MintOrGetSyncFileID: bookID must not be empty")
	}
	if fileID == "" {
		return "", fmt.Errorf("MintOrGetSyncFileID: fileID must not be empty")
	}

	syncFileMintMu.Lock()
	defer syncFileMintMu.Unlock()

	lookupKey := syncFileLookupKey(bookID, fileID)
	existing, closer, err := s.db.Get(lookupKey)
	if err == nil {
		id := string(existing)
		closer.Close()
		return id, nil
	}
	if err != pebble.ErrNotFound {
		return "", err
	}

	syncFileID, err := newULID()
	if err != nil {
		return "", err
	}

	record := SyncFile{
		SyncFileID:    syncFileID,
		BookID:        bookID,
		CurrentFileID: fileID,
		CreatedAt:     time.Now(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("failed to marshal sync_file record: %w", err)
	}

	batch := s.db.NewBatch()
	if err := batch.Set(syncFileRecordKey(syncFileID), data, nil); err != nil {
		batch.Close()
		return "", err
	}
	if err := batch.Set(lookupKey, []byte(syncFileID), nil); err != nil {
		batch.Close()
		return "", err
	}
	if err := batch.Set(syncFileBookKey(bookID, syncFileID), []byte(fileID), nil); err != nil {
		batch.Close()
		return "", err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return "", err
	}

	return syncFileID, nil
}

// GetSyncFileID performs a read-only lookup of the syncFileID for a
// (bookID, fileID) pair. Returns ("", false, nil) when no entry exists.
func (s *PebbleStore) GetSyncFileID(bookID, fileID string) (string, bool, error) {
	value, closer, err := s.db.Get(syncFileLookupKey(bookID, fileID))
	if err == pebble.ErrNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	id := string(value)
	closer.Close()
	return id, true, nil
}

// ListSyncFilesForBook returns every sync_file entry minted for bookID, by
// prefix-scanning the enumerable sync_file:book:<bookID>: index and
// materializing each referenced SyncFile record.
func (s *PebbleStore) ListSyncFilesForBook(bookID string) ([]SyncFile, error) {
	prefix := []byte(fmt.Sprintf("sync_file:book:%s:", bookID))

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []SyncFile
	for iter.First(); iter.Valid(); iter.Next() {
		syncFileID := string(iter.Key()[len(prefix):])

		recData, closer, err := s.db.Get(syncFileRecordKey(syncFileID))
		if err != nil {
			if err == pebble.ErrNotFound {
				continue
			}
			return nil, err
		}
		var rec SyncFile
		unmarshalErr := json.Unmarshal(recData, &rec)
		closer.Close()
		if unmarshalErr != nil {
			return nil, fmt.Errorf("failed to unmarshal sync_file record %q: %w", syncFileID, unmarshalErr)
		}
		out = append(out, rec)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	return out, nil
}

// RepointSyncFile moves the sync_file identity for bookID from oldFileID to
// newFileID -- the file-level analogue of TASK-01's RepointSyncItem, for a
// future file-replacement path (remux, quality upgrade) that does not exist
// yet. If no sync_file entry is registered for (bookID, oldFileID), this is
// a no-op returning nil.
func (s *PebbleStore) RepointSyncFile(bookID, oldFileID, newFileID string) error {
	if bookID == "" {
		return fmt.Errorf("RepointSyncFile: bookID must not be empty")
	}
	if oldFileID == "" {
		return fmt.Errorf("RepointSyncFile: oldFileID must not be empty")
	}
	if newFileID == "" {
		return fmt.Errorf("RepointSyncFile: newFileID must not be empty")
	}

	syncFileMintMu.Lock()
	defer syncFileMintMu.Unlock()

	oldLookupKey := syncFileLookupKey(bookID, oldFileID)
	existing, closer, err := s.db.Get(oldLookupKey)
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	syncFileID := string(existing)
	closer.Close()

	recData, recCloser, err := s.db.Get(syncFileRecordKey(syncFileID))
	if err != nil {
		return err
	}
	var rec SyncFile
	unmarshalErr := json.Unmarshal(recData, &rec)
	recCloser.Close()
	if unmarshalErr != nil {
		return fmt.Errorf("failed to unmarshal sync_file record %q: %w", syncFileID, unmarshalErr)
	}
	rec.CurrentFileID = newFileID

	updatedData, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("failed to marshal sync_file record: %w", err)
	}

	batch := s.db.NewBatch()
	if err := batch.Delete(oldLookupKey, nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Delete(syncFileBookKey(bookID, syncFileID), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set(syncFileBookKey(bookID, syncFileID), []byte(newFileID), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set(syncFileLookupKey(bookID, newFileID), []byte(syncFileID), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set(syncFileRecordKey(syncFileID), updatedData, nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}

	return nil
}
