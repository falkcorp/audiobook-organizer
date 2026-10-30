// file: internal/database/tombstone_file_stamp.go
// version: 1.0.0
// guid: 5b0e7c92-3d14-4a68-b1f7-92c4e8a06d35
// last-edited: 2026-10-30

package database

import (
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// TombstoneFileStamp is what a purge saw of a book's file when it committed to
// removing it: the inode (where the platform reports one), size and modification
// time. It is kept with the book's tombstone so that the tombstone sweeper
// (internal/sweep) unlinks a file only when the file now at the path is the one
// the purge decided on, and never a different file that appeared there later.
//
// It mirrors audiobooks.fileIdentity field for field: audiobooks owns the
// comparison rule (and the reasons the device number is left out), and converts
// to and from this type. It lives here only because the store has to carry it
// and database cannot import audiobooks.
//
// The zero value carries nothing. A tombstone with no stamp (every tombstone
// written before 2026-10-30, and any purge that did not commit to removing the
// file) is never swept for its file: the sweeper keeps the file and reports why.
type TombstoneFileStamp struct {
	// HasInode says Ino is real (the platform reports it).
	HasInode bool   `json:"has_inode,omitempty"`
	Ino      uint64 `json:"ino,omitempty"`
	Size     int64  `json:"size"`
	MtimeNS  int64  `json:"mtime_ns"`
	// IsDir: the purge decided on an empty directory, not a file.
	IsDir bool `json:"is_dir,omitempty"`
}

// StampedTombstoneWriter writes a book tombstone together with the stamp of the
// file the purge committed to removing. It is a capability, not a Store method:
// nothing but the purge writes it, and Store's width is a ratchet. Resolve it
// with AsCapability.
type StampedTombstoneWriter interface {
	// CreateStampedBookTombstone is CreateBookTombstone plus the stamp, in the
	// same value under the same key, so the two are written and deleted
	// together (DeleteBookTombstone removes both) and a reader can never see a
	// tombstone paired with another purge's stamp.
	CreateStampedBookTombstone(book *Book, stamp TombstoneFileStamp) error
}

// TombstoneStampReader reads the stamp CreateStampedBookTombstone stored.
type TombstoneStampReader interface {
	// GetBookTombstoneStamp returns the stamp kept with tombstone id, or nil
	// when the tombstone has none (it was written by CreateBookTombstone, or
	// before stamps existed) or does not exist.
	GetBookTombstoneStamp(id string) (*TombstoneFileStamp, error)
}

var (
	_ StampedTombstoneWriter = (*PebbleStore)(nil)
	_ TombstoneStampReader   = (*PebbleStore)(nil)
	_ StampedTombstoneWriter = (*MockStore)(nil)
	_ TombstoneStampReader   = (*MockStore)(nil)
)

// stampedTombstone is the stored form of a tombstone that carries a stamp: the
// Book's own JSON, flattened, plus one more key. A plain Book decode (every
// existing reader: GetBookTombstone, ListBookTombstones) ignores the extra key,
// and a row written before stamps existed decodes here with a nil stamp.
type stampedTombstone struct {
	Book
	RemovalStamp *TombstoneFileStamp `json:"removal_stamp,omitempty"`
}

// CreateStampedBookTombstone implements StampedTombstoneWriter.
func (p *PebbleStore) CreateStampedBookTombstone(book *Book, stamp TombstoneFileStamp) error {
	if book == nil {
		return fmt.Errorf("create stamped tombstone: nil book")
	}
	data, err := json.Marshal(stampedTombstone{Book: *book, RemovalStamp: &stamp})
	if err != nil {
		return err
	}
	return p.db.Set([]byte(fmt.Sprintf("tombstone:%s", book.ID)), data, pebble.Sync)
}

// GetBookTombstoneStamp implements TombstoneStampReader.
func (p *PebbleStore) GetBookTombstoneStamp(id string) (*TombstoneFileStamp, error) {
	val, closer, err := p.db.Get([]byte(fmt.Sprintf("tombstone:%s", id)))
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()
	var rec stampedTombstone
	if err := json.Unmarshal(val, &rec); err != nil {
		return nil, fmt.Errorf("decode tombstone %s: %w", id, err)
	}
	return rec.RemovalStamp, nil
}
