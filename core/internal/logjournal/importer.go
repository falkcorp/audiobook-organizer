// file: core/internal/logjournal/importer.go
// version: 1.0.0
// guid: 92d5a1c3-7e08-4b64-a3f9-0d1c6b8e4a25
// last-edited: 2026-11-13

package logjournal

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/storage"
)

// ErrNoSourceID is an imported record without the app's ID of its source row:
// the key is derived from it, so without one a re-import could not land on the
// same key and would write the record twice.
var ErrNoSourceID = errors.New("logjournal: an imported record needs a SourceID")

// importDomain separates import keys' hash from any other use of the digest.
const importDomain = "mediacore/logjournal/import\x00"

// Importer is log.Importer over the native store. Its key for a record is the
// ULID of the record's Time (milliseconds) with an entropy part that is a hash
// of the SourceID, so the same ImportedRecord always lands on the same key and
// a step that resumes after a crash between an import and the deletion of its
// source row rewrites that key instead of adding a second record. Two rows of
// one Time and different SourceIDs hash to different entropy and are two
// records.
type Importer struct{ s *Store }

// NewImporter returns the importer over s.
func NewImporter(s *Store) *Importer { return &Importer{s: s} }

// importULID is the deterministic ULID of an imported record.
func importULID(ms uint64, sourceID string) string {
	sum := sha256.Sum256([]byte(importDomain + sourceID))
	var e [entropyLen]byte
	copy(e[:], sum[:entropyLen])
	return makeULID(ms, e)
}

// Import writes recs in one durable batch. Every record needs a usable Time
// (the zero time and times outside 1970 to year 9999 are ErrInvalidTime) and a
// SourceID; one bad record rejects the whole call before anything is written.
// A record already imported is overwritten with itself: an import is a plain
// Set of a derived key, so repeating it is a no-op.
func (im *Importer) Import(ctx context.Context, recs []log.ImportedRecord) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}
	b := im.s.ks.NewBatch()
	defer discard(b, &err)
	for i, r := range recs {
		if r.SourceID == "" {
			return fmt.Errorf("record %d: %w", i, ErrNoSourceID)
		}
		if r.Time.IsZero() {
			return fmt.Errorf("record %d: %w", i, ErrInvalidTime)
		}
		ms, err := msOf(r.Time)
		if err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		rw, err := toRow(r.Record, r.Time)
		if err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		raw, err := rowCodec().Encode(rw)
		if err != nil {
			return fmt.Errorf("logjournal: encode: %w", err)
		}
		if err := b.Set(journalKey(importULID(ms, r.SourceID)), raw); err != nil {
			return err
		}
	}
	return b.Commit(ctx, storage.Sync)
}
