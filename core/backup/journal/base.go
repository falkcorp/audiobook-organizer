// file: core/backup/journal/base.go
// version: 1.0.0
// guid: d1e85b74-2c39-4a06-9f7b-8a3e0c5d1f62
// last-edited: 2026-11-11

package journal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// CreateBased creates a new journal file at path whose header continues after
// base: base_seq = base.Seq and base_mac = base.MAC, so the first line it takes
// is base.Seq+1 and the store's committed journal_head still lies on its chain.
// It is the one way to start a journal below a store whose head is above zero:
// the boot sets another install's journal aside and starts a new one with a
// `gap` line (05-backup §5B.3.6 "Boot re-apply" (0)), and OpenFile, which
// refuses an empty file under a store with a head (a deleted or truncated
// journal is a broken chain, never a fresh start), then finds the base.
//
// The file is created exclusively (0600) and made durable with its directory;
// an existing file is an error and is never touched. The header is signed under
// the active derive key.
func CreateBased(path string, keys Keys, hdr Header, base Head) error {
	if path == "" || keys == nil || hdr.InstallID == "" {
		return errors.New("journal: CreateBased needs a path, Keys and an install ID")
	}
	hdr.KID, hdr.BaseSeq, hdr.BaseMAC = keys.ActiveDeriveID(), base.Seq, base.MAC
	line, _, err := sealHeader(keys, hdr)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("journal: create %s: %w", path, err)
	}
	n, err := f.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return errors.Join(fmt.Errorf("journal: write the header of %s: %w", path, err), os.Remove(path))
	}
	return syncDir(filepath.Dir(path))
}

// Fingerprint identifies a broken journal state: where the chain breaks (or
// that the store's committed line is not on it), the last mac it verified,
// where it claims to end and the store's head. The boot fails closed on a
// broken journal once per fingerprint, so the same broken journal does not
// bump every credential epoch at every restart.
func (c *Chain) Fingerprint(head Head, forked bool) string {
	seq, reason := uint64(0), BreakReason("")
	if c.Break != nil {
		seq, reason = c.Break.Seq, c.Break.Reason
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("v1|%d|%s|%s|%d|%d|%t", seq, reason, c.PrevMAC(), c.ClaimedEnd, head.Seq, forked)))
	return hex.EncodeToString(sum[:])
}

type latchRecord struct {
	Fingerprint string    `json:"fingerprint"`
	At          time.Time `json:"at"`
}

// ReadBrokenLatch reads the fingerprint of the broken journal state the boot
// already failed closed on ("" for none).
func ReadBrokenLatch(ctx context.Context, ks storage.Reader) (string, error) {
	raw, err := ks.Get(ctx, []byte(brokenLatchKey))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return "", nil
	case err != nil:
		return "", err
	}
	var r latchRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("decode %s: %w", brokenLatchKey, err)
	}
	return r.Fingerprint, nil
}

// WriteBrokenLatch records, synchronously, that the boot failed closed on the
// broken journal state fp.
func WriteBrokenLatch(ctx context.Context, ks storage.Keyspace, fp string, at time.Time) error {
	raw, err := json.Marshal(latchRecord{Fingerprint: fp, At: at.UTC()})
	if err != nil {
		return err
	}
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	if err := b.In(ks).Set([]byte(brokenLatchKey), raw); err != nil {
		return err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("journal: record %s: %w", brokenLatchKey, err)
	}
	return nil
}
