// file: core/library/journal.go
// version: 1.3.0
// guid: 643a2716-2f53-438c-aa2c-943654d74242
// last-edited: 2026-11-12

package library

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// rowStore is one c/library/ sub-keyspace of JSON rows (the transfer journal,
// the quarantine records). It is an interface only so tests can kill the
// process between two durable steps: a wrapper counts the writes and fails
// every one after the kill point, like a process that is gone.
type rowStore interface {
	put(ctx context.Context, id string, v any) error // durable (Sync) on return
	remove(ctx context.Context, id string) error
	get(ctx context.Context, id string, into any) (found bool, err error)
	// all calls fn for each row in key order; decoding is the caller's.
	all(ctx context.Context, fn func(id string, raw []byte) error) error
	// stageRemove stages the delete of id into b, binding this keyspace to it.
	stageRemove(b *storage.Batch, id string) error
}

type ksStore struct{ ks storage.Keyspace }

func newRowStore(ks storage.Keyspace) rowStore { return ksStore{ks: ks} }

func (s ksStore) put(ctx context.Context, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.ks.Set(ctx, []byte(id), b)
}

func (s ksStore) remove(ctx context.Context, id string) error {
	return s.ks.Delete(ctx, []byte(id))
}

func (s ksStore) get(ctx context.Context, id string, into any) (bool, error) {
	b, err := s.ks.Get(ctx, []byte(id))
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, into)
}

func (s ksStore) all(ctx context.Context, fn func(id string, raw []byte) error) error {
	for kv, err := range s.ks.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			return err
		}
		if err := fn(string(kv.Key), kv.Value); err != nil {
			return err
		}
	}
	return nil
}

func (s ksStore) stageRemove(b *storage.Batch, id string) error {
	return b.Bind(s.ks).In(s.ks).Delete([]byte(id))
}

// rowState is a transfer's last durable step (05-library 5L.3.3).
type rowState string

const (
	// stBegun: the row exists; nothing else is promised. A temp file may exist.
	stBegun rowState = "begun"
	// stCopied: the temp file is complete, fsynced and verified, and SrcSHA
	// names its bytes. The final name is not yet published.
	stCopied rowState = "copied"
	// stReplacing: a Replace is under way; Aside names where the old
	// destination goes (or went).
	stReplacing rowState = "replacing"
	// stPublished: the final name holds the verified bytes. What is left is the
	// temp's removal, the directory fsync and (Move) the source's removal.
	stPublished rowState = "published"
	// stDone: a TransferIn's file work finished; only the caller's batch is
	// pending. Recovery never undoes it.
	stDone rowState = "done"
)

// transferRow is one c/library/transfer/<id> row.
type transferRow struct {
	ID        string        `json:"id"`
	State     rowState      `json:"state"`
	Mode      TransferMode  `json:"mode"` // as requested
	Did       TransferMode  `json:"did"`  // as resolved; 0 until known
	Owner     storage.Owner `json:"owner"`
	In        bool          `json:"in,omitempty"` // a TransferIn
	Src       Path          `json:"src"`
	Dst       Path          `json:"dst"`
	Tmp       string        `json:"tmp,omitempty"`   // destination-root-relative temp name
	Aside     string        `json:"aside,omitempty"` // destination-root-relative name of the replaced file
	Expect    string        `json:"expect_sha256,omitempty"`
	SrcSHA    string        `json:"src_sha256,omitempty"`
	Size      int64         `json:"size,omitempty"`
	RemoveSrc bool          `json:"remove_src,omitempty"` // the source's removal is owed after publish
	// The source as it was before the first byte was read (Lstat): its device and
	// inode and its modification time in ns. The cross-device Move's source is
	// removed only while it is still this file; zero means "not recorded".
	SrcDev   uint64 `json:"src_dev,omitempty"`
	SrcIno   uint64 `json:"src_ino,omitempty"`
	SrcMTime int64  `json:"src_mtime_ns,omitempty"`
	// The facts that tell a file from a later one that was given the same inode
	// number (libfs.FileFacts): the inode change time and creation time in ns, and
	// the inode generation where the filesystem has one. Zero or nil means the
	// platform did not report it, and the check skips it.
	SrcCTime int64   `json:"src_ctime_ns,omitempty"`
	SrcBirth int64   `json:"src_birth_ns,omitempty"`
	SrcGen   *uint32 `json:"src_gen,omitempty"`
	// The temp file's device and inode, so recovery can tell that the final name
	// is the temp (published by link or by rename) without reading it again.
	TmpDev uint64 `json:"tmp_dev,omitempty"`
	TmpIno uint64 `json:"tmp_ino,omitempty"`
	// Placing: a rename onto Dst is under way through a placeholder (the filesystem
	// has neither an atomic no-replace rename nor hard links), and names the path
	// being renamed there. While it is set, Dst may hold an empty entry this
	// transfer made; recovery removes that entry when the path is still where it
	// was, and never otherwise.
	Placing *Path       `json:"placing,omitempty"`
	Verify  VerifyLevel `json:"verify"`
	At      time.Time   `json:"at"`
}

func (r transferRow) record() TransferRecord {
	return TransferRecord{ID: r.ID, Src: r.Src, Dst: r.Dst, Did: r.Did, SHA256: r.SrcSHA, At: r.At}
}

// listTransfers decodes every transfer row; a row that does not decode is
// reported in the error and skipped, never dropped silently.
func (m *manager) listTransfers(ctx context.Context) ([]transferRow, error) {
	var rows []transferRow
	var bad []error
	err := m.jrn.all(ctx, func(id string, raw []byte) error {
		var r transferRow
		if err := json.Unmarshal(raw, &r); err != nil || r.ID != id {
			bad = append(bad, fmt.Errorf("library: transfer row %q does not decode", id))
			return nil
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, errors.Join(bad...)
}

// ---- ULID ----

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID is a 26-character ULID: 48 bits of milliseconds then 80 random bits,
// Crockford base32. IDs sort by creation time and name temp files safely.
func newULID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:]) // never fails since Go 1.24
	hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- { // the 128-bit value in 5-bit groups, low first
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// validULID reports whether s has the shape newULID produces; quarantine IDs
// from a caller are checked with it before they name a key or a directory.
func validULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(crockford, s[i]) < 0 {
			return false
		}
	}
	return true
}
