// file: internal/repairs/standdown_lease_test.go
// version: 1.0.0
// guid: 0cc1976b-de55-4933-9769-7d7d9ae3ff38
// last-edited: 2026-10-01

package repairs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// ttlStandDown is a lease with the registry's renewal rule
// (scan_standdown.go RenewScanStandDown): a renewal after the expiry fails
// and drops the holder, and nothing resurrects it. Its clock only moves when
// a test advances it, so "a row that writes for longer than the TTL" is
// deterministic.
type ttlStandDown struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     time.Time
	expiry  map[string]time.Time
	renews  int
	refused int
}

func newTTLStandDown(ttl time.Duration) *ttlStandDown {
	return &ttlStandDown{ttl: ttl, now: time.Unix(1_700_000_000, 0), expiry: map[string]time.Time{}}
}

func (s *ttlStandDown) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

func (s *ttlStandDown) AcquireScanStandDown(_ context.Context, holder, _ string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiry[holder] = s.now.Add(s.ttl)
	return func() {
		s.mu.Lock()
		delete(s.expiry, holder)
		s.mu.Unlock()
	}, nil
}

func (s *ttlStandDown) RenewScanStandDown(holder string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renews++
	exp, ok := s.expiry[holder]
	if !ok || s.now.After(exp) {
		delete(s.expiry, holder)
		s.refused++
		return false
	}
	s.expiry[holder] = s.now.Add(s.ttl)
	return true
}

func (s *ttlStandDown) ScanStandDownValid(holder string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.expiry[holder]
	return ok && !s.now.After(exp)
}

// chainFixer trims the title of every book of a row, one write per book, the
// way fragment consolidation retires its books one by one. between runs
// before each write (with the row id and the book's position) so a test can
// move the lease clock while the row is being written. A failure after the
// first write is reported partial, wrapped with %w as the real fixers do.
type chainFixer struct {
	s       *memStore
	groups  map[string][]string
	order   []string
	between func(rowID string, i int)
}

func (f *chainFixer) ID() string          { return "chain-trim" }
func (f *chainFixer) Title() string       { return "Chain trim" }
func (f *chainFixer) Description() string { return "test fixer" }

func (f *chainFixer) row(id string) Row {
	return Row{RowID: id, BookIDs: f.groups[id], Title: id, Reason: "untrimmed", Risk: RiskLow, Fingerprint: "fp:" + id}
}

func (f *chainFixer) Plan(_ context.Context, _ json.RawMessage, _ registry.Reporter) ([]Row, error) {
	var rows []Row
	for _, id := range f.order {
		rows = append(rows, f.row(id))
	}
	return rows, nil
}

func (f *chainFixer) Replan(_ context.Context, _ json.RawMessage, planned Row, _ registry.Reporter) (Row, error) {
	return f.row(planned.RowID), nil
}

func (f *chainFixer) Apply(_ context.Context, w *Writer, fresh Row) error {
	steps := 0
	for i, id := range fresh.BookIDs {
		if f.between != nil {
			f.between(fresh.RowID, i)
		}
		if _, err := w.Modify(id, func(b *database.Book) error {
			b.Title = strings.TrimSpace(b.Title)
			return nil
		}); err != nil {
			if steps == 0 {
				return err
			}
			return fmt.Errorf("%w: after %d step(s): %w", ErrPartiallyApplied, steps, err)
		}
		steps++
	}
	return nil
}

// leaseFixture: row "a" has n books, row "b" has 2; both untrimmed.
func leaseFixture(n int) (*memStore, *chainFixer) {
	s := newMemStore()
	f := &chainFixer{s: s, groups: map[string][]string{}, order: []string{"a", "b"}}
	for _, row := range []struct {
		id string
		n  int
	}{{"a", n}, {"b", 2}} {
		for i := 0; i < row.n; i++ {
			id := fmt.Sprintf("%s%02d", row.id, i)
			s.add(id, " "+id+" ", "/lib/A/"+id+"/x.m4b", nil)
			f.groups[row.id] = append(f.groups[row.id], id)
		}
	}
	return s, f
}

func trimmed(s *memStore, ids []string) int {
	n := 0
	for _, id := range ids {
		if s.title(id) == id {
			n++
		}
	}
	return n
}

// A row that writes for far longer than the lease, making steady progress,
// keeps the lease: each write renews it. Before the fix the lease was renewed
// only at the row's start, so this row (12 writes a minute apart against a 5m
// lease) outlived it and row b was aborted with ErrStandDownLost, exactly as
// the 315-book fragment row did in prod on 2026-10-01.
func TestRunApply_LongRowRenewsLeaseOnEachWrite(t *testing.T) {
	s, f := leaseFixture(12)
	sd := newTTLStandDown(5 * time.Minute)
	f.between = func(string, int) { sd.advance(time.Minute) }
	plan := planFor(t, s, f)
	d := deps(s, sd)
	d.Concurrency = 1
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"a", "b"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Empty(t, res.Aborted)
	require.Equal(t, 2, res.ByOutcome[OutcomeApplied], "both rows written: %+v", res.Rows)
	require.Equal(t, 12, trimmed(s, f.groups["a"]))
	require.Equal(t, 2, trimmed(s, f.groups["b"]))
	require.Zero(t, sd.refused)
}

// A write that stalls past the lease (a wedged step) loses it: the next write
// is refused, nothing after it is written, the row is reported aborted with
// the steps it got through, and the remaining rows are not written.
func TestRunApply_LeaseLostMidRowStopsTheRowAndTheRun(t *testing.T) {
	s, f := leaseFixture(6)
	sd := newTTLStandDown(5 * time.Minute)
	f.between = func(row string, i int) {
		if row == "a" && i == 3 {
			sd.advance(6 * time.Minute)
		}
	}
	plan := planFor(t, s, f)
	d := deps(s, sd)
	d.Concurrency = 1
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"a", "b"}, false, d, nopReporter{})
	require.ErrorIs(t, err, ErrStandDownLost)
	require.Equal(t, ErrStandDownLost.Error(), res.Aborted)
	require.Equal(t, 2, res.ByOutcome[OutcomeAborted], "%+v", res.Rows)
	require.Zero(t, res.Partial, "a lease-lost row is aborted (re-run on resume), not settled as partial")
	byID := map[string]RowResult{}
	for _, r := range res.Rows {
		byID[r.RowID] = r
	}
	require.Contains(t, byID["a"].Error, "after 3 step(s)")
	require.Contains(t, byID["a"].Error, "scan stand-down lease lost")
	require.Equal(t, 3, trimmed(s, f.groups["a"]), "the writes before the lapse stand; none after")
	require.Equal(t, 0, trimmed(s, f.groups["b"]))
	require.Equal(t, 3, res.BookWrites)
	require.Equal(t, 1, sd.refused, "a lost lease is never renewed again")
}

// The Writer latches a failed renewal: later writes are refused without
// calling the gate again, and without a lease installed it never beats.
func TestWriter_BeatLatchesALostLease(t *testing.T) {
	s := newMemStore()
	s.add("x", " x ", "/lib/A/x/x.m4b", nil)
	w := NewWriter(s, s, "t", "bulk_update", "rp-")
	_, err := w.Modify("x", func(*database.Book) error { return nil })
	require.NoError(t, err, "no lease installed: no beat")
	calls := 0
	w.setLease(func() bool { calls++; return false })
	_, err = w.Modify("x", func(*database.Book) error { return nil })
	require.ErrorIs(t, err, ErrStandDownLost)
	require.ErrorIs(t, w.SetPrimaryAuthor("x", 1, nil), ErrStandDownLost)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, w.Writes())
}
