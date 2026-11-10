// file: core/secrets/importer.go
// version: 1.0.0
// guid: 0fe45662-890d-4ebd-b671-62d3c038a3e5
// last-edited: 2026-11-10

package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/storage"
)

// Importer is secrets' write side for a one-way cutover step (03 §3.3; 05-config
// §1.12.2), handed to the app's steps as module.Importers.Secrets (06 §6.4.2)
// and to storage's quarantine sealing (05-storage §5S.3.13). It follows
// 05-storage's importer rules: it seals every value with the keyring's active
// data key (key ID, AAD), stages it into the CALLER's batch, never commits,
// and is create-only: an existing record with other content fails with
// storage.ErrImportConflict, while one that already holds the same value (or
// the same unreadable marker) is already current and stages nothing, so a
// re-run of a step that committed is a no-op. It is a thin layer over Stage
// and StageUnreadable.
type Importer interface {
	ImportSecret(ctx context.Context, b *storage.Batch, w Write) error
	ImportUnreadable(ctx context.Context, b *storage.Batch, n Name, reason, by string) error
}

// importer is the Store's Importer.
type importer struct{ s *Store }

// Importer returns the store's importer; valid after Open, which runs before
// the migration list (05-config §2.4).
func (s *Store) Importer() Importer { return importer{s: s} }

// checkOpen is errClosed after Close.
func (s *Store) checkOpen() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errClosed
	}
	return nil
}

// ImportSecret seals w.Value under w.Name into b, create-only. A delete is
// not importable. An operator value does not refuse it: the cutover moves
// the stored value, which the operator value hides while it is set.
func (im importer) ImportSecret(ctx context.Context, b *storage.Batch, w Write) error {
	s := im.s
	if b == nil {
		return errors.New("secrets: ImportSecret needs the caller's batch")
	}
	if w.Delete {
		return fmt.Errorf("secrets: an import of %s carries a value; a delete is not importable", w.Name)
	}
	if err := checkWrite(w); err != nil {
		return err
	}
	if err := s.checkOpen(); err != nil {
		return err
	}
	unlock := s.writes.Lock(string(w.Name))
	defer unlock()
	raw, err := s.bindForUpdate(ctx, b, w.Name)
	if err != nil {
		return err
	}
	if raw == nil {
		_, err := s.put(b, w)
		return err
	}
	same, err := s.holdsValue(w.Name, raw, w.Value.b)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	return fmt.Errorf("%w: secret %s holds another value, a marker, or a record that cannot be read", storage.ErrImportConflict, w.Name)
}

// ImportUnreadable stages n's unreadable marker into b, create-only: a record
// that is already the same marker is current; any other record conflicts.
func (im importer) ImportUnreadable(ctx context.Context, b *storage.Batch, n Name, reason, by string) error {
	s := im.s
	if err := checkUnreadable(b, n, reason, by); err != nil {
		return err
	}
	if err := s.checkOpen(); err != nil {
		return err
	}
	unlock := s.writes.Lock(string(n))
	defer unlock()
	raw, err := s.bindForUpdate(ctx, b, n)
	if err != nil {
		return err
	}
	if raw == nil {
		_, err := s.putMarker(b, n, reason, by)
		return err
	}
	if rec, derr := decodeRecord(raw); derr == nil && rec.Unreadable == reason {
		return nil
	}
	return fmt.Errorf("%w: secret %s already has a record other than the %q unreadable marker", storage.ErrImportConflict, n, reason)
}

// holdsValue reports whether raw is a sealed record of n that decrypts to
// value. A marker, a record that does not decode or decrypt, or another
// value is false; only a closed store is an error.
func (s *Store) holdsValue(n Name, raw, value []byte) (bool, error) {
	rec, err := decodeRecord(raw)
	if err != nil || !rec.sealed() {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false, errClosed
	}
	pt, err := s.ring.open(s.appID, n, rec)
	if err != nil {
		return false, nil
	}
	defer clear(pt)
	return equalValue(pt, value), nil
}
