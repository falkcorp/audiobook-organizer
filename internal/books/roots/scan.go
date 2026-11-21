// file: internal/books/roots/scan.go
// version: 0.1.0
// guid: ef0df0f9-0bbf-4ae7-90b1-ffae8730bfeb
// last-edited: 2026-11-20

package roots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/storage"
)

// PrefixRootScan is the key prefix of books' per-root scan state, the family M6
// creates (05-library 5L.10). The key is PrefixRootScan + the root ID.
const PrefixRootScan = "rootscan:"

// ErrBadScanRow is wrapped by every error about a rootscan row that does not
// decode or whose key is not a root ID. Writers refuse to overwrite such a row.
var ErrBadScanRow = errors.New("roots: undecodable rootscan row")

// ScanState is the row of the rootscan:<root ID> family: the four fields of
// HEAD's import_path row that are books' scan state and not library's. The JSON
// form is the stored one and is byte-identical to what M6 writes.
type ScanState struct {
	Enabled   bool       `json:"enabled"`
	CreatedAt time.Time  `json:"created_at"`
	LastScan  *time.Time `json:"last_scan,omitempty"`
	BookCount int        `json:"book_count"`
}

// Rows is the part of storage.LegacyRange the store calls. books' LegacyRange
// satisfies it; the fold declares the rootscan: claim that lets it write.
type Rows interface {
	Get(ctx context.Context, key []byte) ([]byte, error)
	Set(ctx context.Context, key, value []byte) error
	Delete(ctx context.Context, key []byte) error
	Scan(ctx context.Context, opts storage.ScanOptions) iter.Seq2[storage.KV, error]
}

var _ Rows = storage.LegacyRange{}

// ScanStore reads and writes the rootscan: family. Every read-modify-write of a
// row holds that root's lock, so two writers of different fields of one root
// (a scan recording its count, the UI switching the root off) never lose each
// other's update. The lock is in-process, which is all there is: one process
// owns the store.
type ScanStore struct {
	rows Rows
	now  func() time.Time

	mu    sync.Mutex
	locks map[library.RootID]*sync.Mutex
}

// NewScanStore builds a store over rows.
func NewScanStore(rows Rows) *ScanStore {
	return &ScanStore{rows: rows, now: time.Now, locks: map[library.RootID]*sync.Mutex{}}
}

// lock is the root's mutex. Roots are few and admin-registered, so the table is
// not pruned.
func (s *ScanStore) lock(id library.RootID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[id]
	if !ok {
		l = new(sync.Mutex)
		s.locks[id] = l
	}
	return l
}

func scanKey(id library.RootID) []byte { return []byte(PrefixRootScan + string(id)) }

// Get is the root's scan state; false when it has no row (books-library has
// none by design, and a root created after R2 has none until Create or a
// writer makes one).
func (s *ScanStore) Get(ctx context.Context, id library.RootID) (ScanState, bool, error) {
	raw, err := s.rows.Get(ctx, scanKey(id))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return ScanState{}, false, nil
	case err != nil:
		return ScanState{}, false, fmt.Errorf("roots: read the scan state of %s: %w", id, err)
	}
	st, err := decodeScan(id, raw)
	return st, err == nil, err
}

func decodeScan(id library.RootID, raw []byte) (ScanState, error) {
	var st ScanState
	if err := json.Unmarshal(raw, &st); err != nil {
		return ScanState{}, fmt.Errorf("%w: %s: %v", ErrBadScanRow, id, err)
	}
	return st, nil
}

// ScanEntry is one row of All: its state, or why it could not be read.
type ScanEntry struct {
	State ScanState
	Err   error
}

// All reads every rootscan row. One undecodable row is reported in its own
// entry (wrapping ErrBadScanRow) and does not hide the others.
func (s *ScanStore) All(ctx context.Context) (map[library.RootID]ScanEntry, error) {
	out := map[library.RootID]ScanEntry{}
	start, end := []byte(PrefixRootScan), []byte(strings.TrimSuffix(PrefixRootScan, ":")+";")
	for kv, err := range s.rows.Scan(ctx, storage.ScanOptions{Start: start, End: end}) {
		if err != nil {
			return nil, fmt.Errorf("roots: scan the rootscan rows: %w", err)
		}
		id := library.RootID(strings.TrimPrefix(string(kv.Key), PrefixRootScan))
		if !id.Valid() {
			out[id] = ScanEntry{Err: fmt.Errorf("%w: key %q is not a root ID", ErrBadScanRow, kv.Key)}
			continue
		}
		st, derr := decodeScan(id, kv.Value)
		out[id] = ScanEntry{State: st, Err: derr}
	}
	return out, nil
}

// update is the one read-modify-write: it holds the root's lock, reads the row
// (absent is the zero state and f is told so), applies
// f and writes the result. A row that does not decode is never overwritten.
func (s *ScanStore) update(ctx context.Context, id library.RootID, f func(st *ScanState, existed bool)) error {
	if !id.Valid() {
		return fmt.Errorf("roots: %q is not a root ID", id)
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	st, ok, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	f(&st, ok)
	val, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("roots: encode the scan state of %s: %w", id, err)
	}
	if err := s.rows.Set(ctx, scanKey(id), val); err != nil {
		return fmt.Errorf("roots: write the scan state of %s: %w", id, err)
	}
	return nil
}

// Create writes the row of a root registered after R2: enabled, created now,
// never scanned. A row that already exists is left as it is, so Create after a
// scan, or twice, changes nothing.
func (s *ScanStore) Create(ctx context.Context, id library.RootID) error {
	if !id.Valid() {
		return fmt.Errorf("roots: %q is not a root ID", id)
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	if _, ok, err := s.Get(ctx, id); err != nil || ok {
		return err
	}
	st := ScanState{Enabled: true, CreatedAt: s.now()}
	val, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("roots: encode the scan state of %s: %w", id, err)
	}
	if err := s.rows.Set(ctx, scanKey(id), val); err != nil {
		return fmt.Errorf("roots: write the scan state of %s: %w", id, err)
	}
	return nil
}

// RecordScan sets the root's BookCount and LastScan, the two fields HEAD's
// scanner wrote after a scan, and nothing else. A root with no row gets one
// (enabled, created at the scan's time): the scan happened, whatever made the
// row.
func (s *ScanStore) RecordScan(ctx context.Context, id library.RootID, bookCount int, at time.Time) error {
	if bookCount < 0 {
		return fmt.Errorf("roots: a book count of %d", bookCount)
	}
	return s.update(ctx, id, func(st *ScanState, existed bool) {
		if !existed {
			st.Enabled, st.CreatedAt = true, at
		}
		t := at
		st.LastScan, st.BookCount = &t, bookCount
	})
}

// SetEnabled sets the root's Enabled and nothing else; a root with no row gets
// one created now.
func (s *ScanStore) SetEnabled(ctx context.Context, id library.RootID, enabled bool) error {
	now := s.now()
	return s.update(ctx, id, func(st *ScanState, existed bool) {
		if !existed {
			st.CreatedAt = now
		}
		st.Enabled = enabled
	})
}

// Delete removes the root's row; absent is success. It takes the root's lock,
// so a writer that is mid-update finishes first and the row is then gone.
func (s *ScanStore) Delete(ctx context.Context, id library.RootID) error {
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	if err := s.rows.Delete(ctx, scanKey(id)); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("roots: delete the scan state of %s: %w", id, err)
	}
	return nil
}

// NextImportN is the n of the next import root over the registered roots and
// every rootscan row (see the package function NextImportN).
func (s *ScanStore) NextImportN(ctx context.Context, registered []library.RootID) (int, error) {
	all, err := s.All(ctx)
	if err != nil {
		return 0, err
	}
	ids := append([]library.RootID(nil), registered...)
	for id := range all {
		ids = append(ids, id)
	}
	return NextImportN(ids...), nil
}
