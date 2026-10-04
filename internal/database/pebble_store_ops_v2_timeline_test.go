// file: internal/database/pebble_store_ops_v2_timeline_test.go
// version: 1.4.0
// guid: bf8efde3-0111-470c-a4c5-473eea3696e9
// last-edited: 2026-10-04

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
)

// ── helpers ─────────────────────────────────────────────────────────────────

func newTimelineStore(t testing.TB) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// rngReader feeds ULID entropy from the seeded RNG so a failing seed
// reproduces exactly.
type rngReader struct{ r *rand.Rand }

func (x rngReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(x.r.Uint32())
	}
	return len(b), nil
}

func timelineKeys(t testing.TB, p *PebbleStore, prefix string) []string {
	t.Helper()
	pre := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: pre, UpperBound: prefixUpperBound(pre)})
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	var out []string
	for iter.First(); iter.Valid(); iter.Next() {
		out = append(out, string(iter.Key()))
	}
	if err := iter.Error(); err != nil {
		t.Fatal(err)
	}
	return out
}

func rowIDs(rows []OperationV2Row) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].ID
	}
	return out
}

func firstDiff(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}

// compareTimeline asserts the scan and the indexed read agree for since and
// limit, and returns a description of the first mismatch or "".
func compareTimeline(p *PebbleStore, since time.Time, limit int) (string, error) {
	scan, err := p.listOperationsV2SinceScan(since, limit)
	if err != nil {
		return "", err
	}
	idx, err := p.listOperationsV2SinceIndexed(since, limit)
	if err != nil {
		return "", err
	}
	a, b := rowIDs(scan), rowIDs(idx)
	if d := firstDiff(a, b); d >= 0 {
		get := func(s []string) string {
			if d < len(s) {
				return s[d]
			}
			return "<end>"
		}
		return fmt.Sprintf("since=%s limit=%d: scan len %d, indexed len %d, first differing position %d (scan %s, indexed %s)",
			since.Format(time.RFC3339Nano), limit, len(a), len(b), d, get(a), get(b)), nil
	}
	return "", nil
}

func rawSetOp(t testing.TB, p *PebbleStore, row OperationV2Row) {
	t.Helper()
	data, err := json.Marshal(&row)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv2OpKey(row.ID), data, pebble.Sync); err != nil {
		t.Fatal(err)
	}
}

func tp(t time.Time) *time.Time { return &t }

// ── random history model ────────────────────────────────────────────────────

type timelineSim struct {
	t    testing.TB
	p    *PebbleStore
	rng  *rand.Rand
	ent  rngReader
	base time.Time
	ids  []string
	qAt  map[string]time.Time
}

func newTimelineSim(t testing.TB, p *PebbleStore, seed uint64) *timelineSim {
	rng := rand.New(rand.NewPCG(seed, 0))
	return &timelineSim{t: t, p: p, rng: rng, ent: rngReader{rng}, base: time.Now().UTC(), qAt: map[string]time.Time{}}
}

// between draws a time in [lo, hi].
func (s *timelineSim) between(lo, hi time.Time) time.Time {
	if !hi.After(lo) {
		return lo
	}
	return lo.Add(time.Duration(s.rng.Int64N(int64(hi.Sub(lo)) + 1)))
}

func (s *timelineSim) newID(queuedAt time.Time) string {
	id := ulid.MustNew(ulid.Timestamp(queuedAt), s.ent).String()
	s.ids = append(s.ids, id)
	s.qAt[id] = queuedAt
	return id
}

// drawQueuedAt draws from [base-30d, base]; 10% are truncated to the minute
// so ops collide on QueuedAt and the tie-break is exercised.
func (s *timelineSim) drawQueuedAt() time.Time {
	q := s.between(s.base.Add(-30*24*time.Hour), s.base)
	if s.rng.IntN(10) == 0 {
		q = q.Truncate(time.Minute)
	}
	return q
}

// drawCompletedAt draws from [queuedAt, base]; 5% are BEFORE queuedAt (clock
// skew).
func (s *timelineSim) drawCompletedAt(queuedAt time.Time) time.Time {
	if s.rng.IntN(20) == 0 {
		return queuedAt.Add(-time.Duration(1 + s.rng.Int64N(int64(48*time.Hour))))
	}
	return s.between(queuedAt, s.base)
}

func (s *timelineSim) insert(status string, queuedAt time.Time) string {
	id := s.newID(queuedAt)
	row := OperationV2Row{ID: id, DefID: "test.def", Plugin: "test", Status: status, Params: "{}", QueuedAt: queuedAt}
	if status == "running" {
		row.StartedAt = tp(s.between(queuedAt, s.base))
	}
	_ = s.p.InsertOperationV2(row)
	return id
}

var terminalStatuses = []string{"completed", "failed", "canceled"}
var interruptedStatuses = []string{"interrupted_quiesced", "interrupted_ask", "interrupted_restart"}

// history writes one operation's whole life through the real writers.
func (s *timelineSim) history() {
	q := s.drawQueuedAt()
	switch k := s.rng.IntN(100); {
	case k < 35: // queued → running → terminal
		id := s.insert("queued", q)
		started := s.between(q, s.base)
		_ = s.p.UpdateOperationV2Status(id, "running", &started, nil, nil)
		s.noise(id)
		c := s.drawCompletedAt(q)
		_ = s.p.UpdateOperationV2Status(id, terminalStatuses[s.rng.IntN(3)], nil, &c, nil)
		if s.rng.IntN(20) == 0 {
			_, _, _ = s.p.DeleteOperationV2(id, terminalStatuses)
		}
	case k < 45: // waiting_deps, maybe promoted and finished
		id := s.insert("waiting_deps", q)
		if s.rng.IntN(2) == 0 {
			_ = s.p.PromoteToQueued(id)
			started := s.between(q, s.base)
			_ = s.p.UpdateOperationV2Status(id, "running", &started, nil, nil)
			c := s.drawCompletedAt(q)
			_ = s.p.UpdateOperationV2Status(id, "completed", nil, &c, nil)
		}
	case k < 55: // interrupted with a completion time (not terminal)
		id := s.insert("queued", q)
		started := s.between(q, s.base)
		_ = s.p.UpdateOperationV2Status(id, "running", &started, nil, nil)
		c := s.drawCompletedAt(q)
		_ = s.p.UpdateOperationV2Status(id, interruptedStatuses[s.rng.IntN(3)], nil, &c, nil)
	case k < 67: // terminal → reset → running → terminal later
		id := s.insert("queued", q)
		started := s.between(q, s.base)
		_ = s.p.UpdateOperationV2Status(id, "running", &started, nil, nil)
		c1 := s.drawCompletedAt(q)
		_ = s.p.UpdateOperationV2Status(id, terminalStatuses[s.rng.IntN(3)], nil, &c1, nil)
		_ = s.p.ResetOperationV2ForResume(id)
		_ = s.p.IncrementResumeCountV2(id)
		started2 := s.between(started, s.base)
		_ = s.p.UpdateOperationV2Status(id, "running", &started2, nil, nil)
		if s.rng.IntN(3) > 0 {
			c2 := s.between(c1, s.base)
			_ = s.p.UpdateOperationV2Status(id, "completed", nil, &c2, nil)
		}
	case k < 77: // long-running: queued 20-30 days ago
		lq := s.between(s.base.Add(-30*24*time.Hour), s.base.Add(-20*24*time.Hour))
		id := s.insert("running", lq)
		s.noise(id)
		if s.rng.IntN(2) == 0 {
			c := s.between(s.base.Add(-time.Hour), s.base)
			_ = s.p.UpdateOperationV2Status(id, "completed", nil, &c, nil)
		}
	case k < 85: // canceled while queued
		id := s.insert("queued", q)
		_, _ = s.p.SetOperationV2StatusIfQueued(id, "canceled")
	case k < 95: // still queued or running
		id := s.insert([]string{"queued", "running"}[s.rng.IntN(2)], q)
		s.noise(id)
	default: // terminal without an explicit completedAt (store stamps now)
		id := s.insert("queued", q)
		_ = s.p.UpdateOperationV2Status(id, "failed", nil, nil, nil)
	}
}

// noise interleaves writes that never change CompletedAt.
func (s *timelineSim) noise(id string) {
	for range s.rng.IntN(4) {
		switch s.rng.IntN(6) {
		case 0:
			_ = s.p.UpdateOpProgressV2(id, s.rng.IntN(100), 100, "tick")
		case 1:
			ph := "phase"
			_ = s.p.UpdateOpPhaseV2(id, &ph)
		case 2:
			_ = s.p.UpdateOperationV2Params(id, []byte(`{"x":1}`))
		case 3:
			_ = s.p.SetOperationV2Result(id, "ok")
		case 4:
			_ = s.p.IncrementResumeCountV2(id)
		case 5:
			_ = s.p.UpdateOpCheckpointV2(id, s.rng.IntN(50))
		}
	}
}

// legacy writes a raw pre-A5 row with no index key.
func (s *timelineSim) legacy() {
	q := s.drawQueuedAt()
	id := s.newID(q)
	row := OperationV2Row{ID: id, DefID: "legacy", Plugin: "test", Status: "running", QueuedAt: q, StartedAt: tp(s.between(q, s.base))}
	if s.rng.IntN(3) > 0 {
		row.Status = "completed"
		row.CompletedAt = tp(s.drawCompletedAt(q))
	}
	rawSetOp(s.t, s.p, row)
}

// randomWrite applies one random write to a random existing op (or inserts
// a new one). Errors are ignored: many transitions are invalid on purpose.
func (s *timelineSim) randomWrite() {
	if len(s.ids) == 0 || s.rng.IntN(8) == 0 {
		s.history()
		return
	}
	id := s.ids[s.rng.IntN(len(s.ids))]
	q := s.qAt[id]
	switch s.rng.IntN(12) {
	case 0:
		started := s.between(q, s.base)
		_ = s.p.UpdateOperationV2Status(id, "running", &started, nil, nil)
	case 1:
		c := s.drawCompletedAt(q)
		_ = s.p.UpdateOperationV2Status(id, terminalStatuses[s.rng.IntN(3)], nil, &c, nil)
	case 2:
		c := s.drawCompletedAt(q)
		_ = s.p.UpdateOperationV2Status(id, interruptedStatuses[s.rng.IntN(3)], nil, &c, nil)
	case 3:
		_ = s.p.ResetOperationV2ForResume(id)
	case 4:
		_ = s.p.PromoteToQueued(id)
	case 5:
		_, _ = s.p.SetOperationV2StatusIfQueued(id, "canceled")
	case 6:
		_, _, _ = s.p.DeleteOperationV2(id, terminalStatuses)
	case 7:
		_, _ = s.p.RepairOpsV2MissingCompletedAt()
	case 8:
		_, _ = s.p.SetOpQueuedProgressV2(id, 1, 2, "queued est")
	case 9:
		_ = s.p.MarkOperationV2ManualRetry(id)
	default:
		s.noise(id)
	}
}

var timelineSinces = func(base time.Time) []time.Time {
	return []time.Time{{}, base.Add(-30 * 24 * time.Hour), base.Add(-48 * time.Hour), base.Add(-time.Hour), base.Add(time.Hour)}
}

var timelineLimits = []int{1, 7, 200, 5000}

// assertTimelineEquivalent compares the scan with the public
// ListOperationsV2Since for every since and limit. Trust is asserted first,
// so the public call IS the indexed read (and is the production
// entry point); the direct indexed call is compared once per since as well.
func assertTimelineEquivalent(t *testing.T, p *PebbleStore, seed uint64, base time.Time, phase string) {
	t.Helper()
	if !p.OpsV2TimelineIndexTrusted() {
		t.Fatalf("seed %d %s: index not trusted; the public read would be the scan", seed, phase)
	}
	for _, since := range timelineSinces(base) {
		if diff, err := compareTimeline(p, since, 5000); err != nil || diff != "" {
			t.Fatalf("seed %d %s: %v %s", seed, phase, err, diff)
		}
		// The oracle for smaller limits is a prefix of the 5000-row scan
		// (truncation is the shared sortAndLimitTimeline, and no window here
		// holds 5000 rows); the production read still runs at every limit.
		full, err := p.listOperationsV2SinceScan(since, 5000)
		if err != nil {
			t.Fatalf("seed %d %s: scan: %v", seed, phase, err)
		}
		for _, limit := range timelineLimits {
			scan := full[:min(limit, len(full))]
			pub, err := p.ListOperationsV2Since(since, limit)
			if err != nil {
				t.Fatalf("seed %d %s: ListOperationsV2Since: %v", seed, phase, err)
			}
			a, b := rowIDs(scan), rowIDs(pub)
			if d := firstDiff(a, b); d >= 0 {
				t.Fatalf("seed %d %s: since=%s limit=%d: scan len %d, indexed len %d, first differing position %d",
					seed, phase, since.Format(time.RFC3339Nano), limit, len(a), len(b), d)
			}
		}
	}
}

// assertTimelineBoundaries compares scan and indexed reads at the window
// edges where an off-by-one in the index would show: for each row, since at
// CompletedAt and QueuedAt exactly and minus/plus 1 ns (the exact value is
// the one a >= vs > slip changes), and QueuedAt plus 1 min (just
// past the ULID-seek slack). allRows checks every row; otherwise a random
// sample of 6 rows keeps the per-seed cost bounded.
func assertTimelineBoundaries(t *testing.T, p *PebbleStore, seed uint64, sim *timelineSim, allRows bool, phase string) {
	t.Helper()
	rows, err := p.listOperationsV2SinceScan(time.Time{}, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// One decode of every row is the oracle for every boundary below: the
	// scan for a window is exactly these rows filtered by opV2InWindow and
	// sorted, so re-reading the store per boundary would only add cost.
	all := append([]OperationV2Row(nil), rows...)
	oracle := func(since time.Time) []string {
		var in []OperationV2Row
		for i := range all {
			if opV2InWindow(&all[i], since) {
				in = append(in, all[i])
			}
		}
		return rowIDs(sortAndLimitTimeline(in, 5000))
	}
	if !allRows && len(rows) > 6 {
		sim.rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		rows = rows[:6]
	}
	for _, r := range rows {
		sinces := []time.Time{r.QueuedAt.Add(-time.Nanosecond), r.QueuedAt, r.QueuedAt.Add(time.Nanosecond), r.QueuedAt.Add(time.Minute)}
		if r.CompletedAt != nil {
			sinces = append(sinces, r.CompletedAt.Add(-time.Nanosecond), *r.CompletedAt, r.CompletedAt.Add(time.Nanosecond))
		}
		for _, since := range sinces {
			idx, err := p.listOperationsV2SinceIndexed(since, 5000)
			if err != nil {
				t.Fatalf("seed %d %s: %v", seed, phase, err)
			}
			want, got := oracle(since), rowIDs(idx)
			if d := firstDiff(want, got); d >= 0 {
				t.Fatalf("seed %d %s: boundary around row %s: since=%s: scan len %d, indexed len %d, first differing position %d",
					seed, phase, r.ID, since.Format(time.RFC3339Nano), len(want), len(got), d)
			}
		}
	}
}

// ── tests ───────────────────────────────────────────────────────────────────

// TestOpsV2Timeline_EquivalenceRandomHistories is the property test (and the
// anti-over-suppression test): over random histories the indexed read must
// return exactly the scan's rows, in the scan's order, for every window and
// limit. An index that hides a row the scan returns fails it.
func TestOpsV2Timeline_EquivalenceRandomHistories(t *testing.T) {
	// 200 seeds in the non-race full suite; 50 when built with -race (200
	// took ~9 min there, against go test's 10 min default); 25 under -short,
	// where CI's race job runs. Seeds 1 and 101 check every row's window
	// boundaries. OPSV2_TIMELINE_SEEDS overrides all three.
	seeds := uint64(200)
	if opsV2TimelineRaceBuild {
		seeds = 50
	}
	if testing.Short() {
		seeds = 25
	}
	if v := os.Getenv("OPSV2_TIMELINE_SEEDS"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &seeds)
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		// Not newTimelineStore: 200 stores must not all stay open until the
		// test's cleanup runs.
		p, err := NewPebbleStoreInMemory(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		sim := newTimelineSim(t, p, seed)
		for range 150 {
			sim.history()
		}
		for range 10 {
			sim.legacy()
		}
		res, err := p.ReconcileOpsV2TimelineIndex(context.Background())
		if err != nil {
			t.Fatalf("seed %d: reconcile: %v", seed, err)
		}
		if res.Missing < 10 {
			t.Fatalf("seed %d: reconcile found %d missing keys; the 10 legacy rows have none", seed, res.Missing)
		}
		assertTimelineEquivalent(t, p, seed, sim.base, "after reconcile")
		assertTimelineBoundaries(t, p, seed, sim, seed%100 == 1, "after reconcile")

		for range 100 {
			sim.randomWrite()
		}
		assertTimelineEquivalent(t, p, seed, sim.base, "after 100 more writes (no reconcile)")
		assertTimelineBoundaries(t, p, seed, sim, seed%100 == 1, "after 100 more writes (no reconcile)")
		// Readers tolerate a stale extra key (they re-check every row), so
		// equivalence alone cannot see a writer that leaves one behind. A
		// second reconcile must find the index exact: nothing missing,
		// nothing orphaned.
		res, err = p.ReconcileOpsV2TimelineIndex(context.Background())
		if err != nil {
			t.Fatalf("seed %d: second reconcile: %v", seed, err)
		}
		if res.Missing != 0 || res.Orphans != 0 {
			t.Fatalf("seed %d: writers let the index drift without a reconcile: %+v", seed, res)
		}
		_ = p.Close()
	}
}

func TestOpsV2Timeline_ZeroSinceReturnsAll(t *testing.T) {
	p := newTimelineStore(t)
	sim := newTimelineSim(t, p, 7)
	for range 60 {
		sim.history()
	}
	if _, err := p.ReconcileOpsV2TimelineIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	all, err := p.listOperationsV2SinceIndexed(time.Time{}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	n := len(timelineKeys(t, p, opv2OpPrefix))
	if len(all) != n {
		t.Fatalf("zero since returned %d rows, store holds %d", len(all), n)
	}
	for _, limit := range timelineLimits {
		if diff, err := compareTimeline(p, time.Time{}, limit); err != nil || diff != "" {
			t.Fatalf("zero since: %v %s", err, diff)
		}
	}
}

// TestOpsV2Timeline_ReadBeforeReconcileUsesScan: until this boot's reconcile
// completes, ListOperationsV2Since must return the scan's rows, including a
// row the index does not cover yet.
func TestOpsV2Timeline_ReadBeforeReconcileUsesScan(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC()
	q := now.Add(-3 * time.Hour)
	// Id minted at QueuedAt, so the ULID-seek leg does not cover it and only
	// an index key could.
	id := ulid.MustNew(ulid.Timestamp(q), rngReader{rand.New(rand.NewPCG(3, 0))}).String()
	rawSetOp(t, p, OperationV2Row{ID: id, Status: "completed", QueuedAt: q, CompletedAt: tp(now.Add(-10 * time.Minute))})

	if p.OpsV2TimelineIndexTrusted() {
		t.Fatal("trusted before any reconcile")
	}
	rows, err := p.ListOperationsV2Since(now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("before reconcile: got %v, want [%s] (the scan's result)", rowIDs(rows), id)
	}
	if idx, _ := p.listOperationsV2SinceIndexed(now.Add(-time.Hour), 100); len(idx) != 0 {
		t.Fatalf("probe is void: the unrepaired index already returns %v", rowIDs(idx))
	}
	if _, err := p.ReconcileOpsV2TimelineIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.OpsV2TimelineIndexTrusted() {
		t.Fatal("not trusted after a completed reconcile")
	}
	rows, err = p.ListOperationsV2Since(now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("after reconcile (indexed path): got %v, want [%s]", rowIDs(rows), id)
	}
}
func TestOpsV2Timeline_ReconcileRepairsMissingAndOrphans(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	tA, tB, tC, tD := now.Add(-4*time.Hour), now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Hour)

	// A: completed row with no done key.
	rawSetOp(t, p, OperationV2Row{ID: "A", Status: "completed", QueuedAt: tA.Add(-time.Minute), CompletedAt: &tA})
	// B: completed row (indexed) plus a stray open key.
	if err := p.InsertOperationV2(OperationV2Row{ID: "B", Status: "completed", QueuedAt: tB.Add(-time.Minute), CompletedAt: &tB}); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv2OpenKey("B"), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	// C: completed row (indexed) plus a done key with the wrong nanos.
	if err := p.InsertOperationV2(OperationV2Row{ID: "C", Status: "completed", QueuedAt: tC.Add(-time.Minute), CompletedAt: &tC}); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv2DoneKey(tC.Add(5*time.Second), "C"), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	// D: open and done keys for a row that does not exist.
	if err := p.db.Set(opv2OpenKey("D"), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv2DoneKey(tD, "D"), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	// E: a correctly indexed queued row.
	if err := p.InsertOperationV2(OperationV2Row{ID: "E", Status: "queued", QueuedAt: now}); err != nil {
		t.Fatal(err)
	}

	oldWorkers := opsV2TimelineReconcileWorkers
	opsV2TimelineReconcileWorkers = 3
	t.Cleanup(func() { opsV2TimelineReconcileWorkers = oldWorkers })
	res, err := p.ReconcileOpsV2TimelineIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 4 || res.Missing != 1 || res.Orphans != 4 || res.Fixed != 5 {
		t.Fatalf("first reconcile = %+v, want rows 4, missing 1, orphans 4, fixed 5", res)
	}
	wantOpen := []string{string(opv2OpenKey("E"))}
	wantDone := []string{string(opv2DoneKey(tA, "A")), string(opv2DoneKey(tB, "B")), string(opv2DoneKey(tC, "C"))}
	sort.Strings(wantDone)
	if got := timelineKeys(t, p, opv2OpenPrefix); strings.Join(got, ",") != strings.Join(wantOpen, ",") {
		t.Fatalf("open keys = %v, want %v", got, wantOpen)
	}
	if got := timelineKeys(t, p, opv2DonePrefix); strings.Join(got, ",") != strings.Join(wantDone, ",") {
		t.Fatalf("done keys = %v, want %v", got, wantDone)
	}

	res, err = p.ReconcileOpsV2TimelineIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Fixed != 0 || res.Missing != 0 || res.Orphans != 0 {
		t.Fatalf("second reconcile = %+v, want all zero", res)
	}
}

func TestOpsV2Timeline_ProgressWritesStageNoIndexKeys(t *testing.T) {
	p := newTimelineStore(t)
	id := ulid.Make().String()
	now := time.Now().UTC()
	if err := p.InsertOperationV2(OperationV2Row{ID: id, Status: "queued", QueuedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateOperationV2Status(id, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	before := len(timelineKeys(t, p, opv2OpenPrefix)) + len(timelineKeys(t, p, opv2DonePrefix))
	for i := range 1000 {
		if err := p.UpdateOpProgressV2(id, i, 1000, "tick"); err != nil {
			t.Fatal(err)
		}
	}
	after := len(timelineKeys(t, p, opv2OpenPrefix)) + len(timelineKeys(t, p, opv2DonePrefix))
	if before != after || before != 1 {
		t.Fatalf("index keys before=%d after=%d, want 1 and 1", before, after)
	}

	// The delta itself: same CompletedAt stages nothing.
	c := now.Add(-time.Minute)
	for _, pair := range [][2]*OperationV2Row{
		{{ID: id}, {ID: id, ProgressCurrent: 5}},
		{{ID: id, CompletedAt: &c}, {ID: id, CompletedAt: tp(c)}},
		{nil, nil},
	} {
		b := p.db.NewBatch()
		if err := stageOpRow(b, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
		if b.Count() != 0 {
			t.Fatalf("stageOpRow(%+v, %+v) staged %d ops, want 0", pair[0], pair[1], b.Count())
		}
		_ = b.Close()
	}
}

func TestOpsV2Timeline_FailedCommitLeavesRowAndIndexAgreeing(t *testing.T) {
	p := newTimelineStore(t)
	if _, err := p.ReconcileOpsV2TimelineIndex(context.Background()); err != nil {
		t.Fatal(err) // empty store: just marks the index trusted
	}
	sim := newTimelineSim(t, p, 99)
	var calls, failures int
	injected := errors.New("injected commit failure")
	hook := func(s *PebbleStore) error {
		if s != p {
			return nil
		}
		calls++
		if calls%4 == 0 {
			failures++
			return injected
		}
		return nil
	}
	opsV2BeforeCommitHook.Store(&hook)
	t.Cleanup(func() { opsV2BeforeCommitHook.Store(nil) })

	for step := range 400 {
		sim.randomWrite()
		for _, since := range []time.Time{{}, sim.base.Add(-48 * time.Hour), sim.base.Add(-time.Hour)} {
			diff, err := compareTimeline(p, since, 5000)
			if err != nil {
				t.Fatal(err)
			}
			if diff != "" {
				t.Fatalf("step %d (after %d injected failures): %s", step, failures, diff)
			}
		}
	}
	if failures == 0 {
		t.Fatal("the hook never failed a commit; the test proves nothing")
	}
	opsV2BeforeCommitHook.Store(nil)
	res, err := p.ReconcileOpsV2TimelineIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Missing != 0 || res.Orphans != 0 {
		t.Fatalf("after %d injected failures the index drifted: %+v", failures, res)
	}
}

// TestOpsV2Timeline_RollbackForward is the review's rollback/roll-forward
// probe. Boot 1 (A5) builds and trusts the index. A pre-A5 binary then
// resumes an A5-era op (leaving its stale done key and writing no open key)
// and inserts a waiting_deps op with no index key. Boot 3 (A5 again, a fresh
// process: a new store over the same files) must return all three ops from
// the 24 h read both before its reconcile (scan) and after it (index), with
// no operator step.
func TestOpsV2Timeline_RollbackForward(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	ent := rngReader{rand.New(rand.NewPCG(11, 0))}
	mint := func(q time.Time) string { return ulid.MustNew(ulid.Timestamp(q), ent).String() }
	qResumed, qWaiting, qDone := now.Add(-72*time.Hour), now.Add(-60*time.Hour), now.Add(-50*time.Hour)
	resumed, waiting, done := mint(qResumed), mint(qWaiting), mint(qDone)

	// Boot 1: A5.
	p1, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []OperationV2Row{
		{ID: resumed, Status: "queued", QueuedAt: qResumed},
		{ID: done, Status: "queued", QueuedAt: qDone},
	} {
		if err := p1.InsertOperationV2(r); err != nil {
			t.Fatal(err)
		}
	}
	oldC := now.Add(-48 * time.Hour)
	if err := p1.UpdateOperationV2Status(resumed, "failed", nil, &oldC, nil); err != nil {
		t.Fatal(err)
	}
	newC := now.Add(-2 * time.Hour)
	if err := p1.UpdateOperationV2Status(done, "completed", nil, &newC, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p1.ReconcileOpsV2TimelineIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p1.Close(); err != nil {
		t.Fatal(err)
	}

	// Boot 2: a pre-A5 binary, simulated with raw row writes and no index keys.
	p2, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rawSetOp(t, p2, OperationV2Row{ID: resumed, Status: "queued", QueuedAt: qResumed})
	rawSetOp(t, p2, OperationV2Row{ID: waiting, Status: "waiting_deps", QueuedAt: qWaiting})
	if err := p2.Close(); err != nil {
		t.Fatal(err)
	}

	// Boot 3: A5 again.
	p3, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p3.Close() })
	since := now.Add(-24 * time.Hour)
	want := []string{done, waiting, resumed} // started_at all nil: queued_at DESC
	check := func(phase string) {
		t.Helper()
		rows, err := p3.ListOperationsV2Since(since, 200)
		if err != nil {
			t.Fatal(err)
		}
		if got := rowIDs(rows); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: 24 h read = %v, want %v", phase, got, want)
		}
	}
	if p3.OpsV2TimelineIndexTrusted() {
		t.Fatal("boot 3 trusts the index before its own reconcile")
	}
	if idx, _ := p3.listOperationsV2SinceIndexed(since, 200); len(idx) == len(want) {
		t.Fatalf("probe is void: the unrepaired index already returns all rows %v", rowIDs(idx))
	}
	check("boot 3 before reconcile (scan)")
	res, err := p3.ReconcileOpsV2TimelineIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Missing != 2 || res.Orphans != 1 {
		t.Fatalf("boot 3 reconcile = %+v, want missing 2 (resumed open key, waiting open key), orphans 1 (resumed stale done key)", res)
	}
	if !p3.OpsV2TimelineIndexTrusted() {
		t.Fatal("boot 3 not trusted after its reconcile")
	}
	check("boot 3 after reconcile (index)")
}
func TestOpsV2Timeline_SweepHollowRemovesDoneKey(t *testing.T) {
	p := newTimelineStore(t)
	const hollow = "01HOLLOW00000000000000000000"
	if err := p.db.Set(opv2OpKey(hollow), []byte("not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv2DoneKey(time.Now(), hollow), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv2OpenKey(hollow), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	keep := ulid.Make().String()
	c := time.Now().UTC()
	if err := p.InsertOperationV2(OperationV2Row{ID: keep, Status: "completed", QueuedAt: c, CompletedAt: &c}); err != nil {
		t.Fatal(err)
	}
	n, err := p.SweepHollowOperationsV2()
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1", n, err)
	}
	for _, fam := range []string{opv2OpenPrefix, opv2DonePrefix} {
		for _, k := range timelineKeys(t, p, fam) {
			if strings.HasSuffix(k, hollow) {
				t.Fatalf("index key %s still names the swept row", k)
			}
		}
	}
	if got := timelineKeys(t, p, opv2DonePrefix); len(got) != 1 || !strings.HasSuffix(got[0], keep) {
		t.Fatalf("done keys = %v, want only the kept row", got)
	}
}

func TestOpsV2Timeline_ResumeMovesDoneToOpen(t *testing.T) {
	p := newTimelineStore(t)
	id := ulid.Make().String()
	now := time.Now().UTC()
	t1, t2 := now.Add(-time.Hour), now.Add(-time.Minute)
	if err := p.InsertOperationV2(OperationV2Row{ID: id, Status: "queued", QueuedAt: now.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateOperationV2Status(id, "failed", nil, &t1, nil); err != nil {
		t.Fatal(err)
	}
	assertKeys := func(step string, open, done []string) {
		t.Helper()
		if got := timelineKeys(t, p, opv2OpenPrefix); strings.Join(got, ",") != strings.Join(open, ",") {
			t.Fatalf("%s: open = %v, want %v", step, got, open)
		}
		if got := timelineKeys(t, p, opv2DonePrefix); strings.Join(got, ",") != strings.Join(done, ",") {
			t.Fatalf("%s: done = %v, want %v", step, got, done)
		}
	}
	assertKeys("complete T1", nil, []string{string(opv2DoneKey(t1, id))})
	if err := p.ResetOperationV2ForResume(id); err != nil {
		t.Fatal(err)
	}
	assertKeys("reset", []string{string(opv2OpenKey(id))}, nil)
	if err := p.UpdateOperationV2Status(id, "completed", nil, &t2, nil); err != nil {
		t.Fatal(err)
	}
	assertKeys("complete T2", nil, []string{string(opv2DoneKey(t2, id))})
}

func TestOpsV2Timeline_DeleteRemovesIndexKeys(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC()
	done, open, broken := ulid.Make().String(), ulid.Make().String(), ulid.Make().String()
	if err := p.InsertOperationV2(OperationV2Row{ID: done, Status: "completed", QueuedAt: now, CompletedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := p.InsertOperationV2(OperationV2Row{ID: open, Status: "queued", QueuedAt: now}); err != nil {
		t.Fatal(err)
	}
	// An undecodable row whose CompletedAt cannot be derived, with a done key.
	if err := p.db.Set(opv2OpKey(broken), []byte("{"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv2DoneKey(now, broken), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{done, open, broken} {
		if _, deleted, err := p.DeleteOperationV2(id, []string{"completed", "queued"}); err != nil || !deleted {
			t.Fatalf("delete %s: deleted=%v err=%v", id, deleted, err)
		}
	}
	if got := append(timelineKeys(t, p, opv2OpenPrefix), timelineKeys(t, p, opv2DonePrefix)...); len(got) != 0 {
		t.Fatalf("index keys remain after delete: %v", got)
	}
}

// getOpLogsV2Full reproduces the pre-tail-read GetOpLogsV2: decode every row,
// then keep the last limit.
func getOpLogsV2Full(p *PebbleStore, opID string, limit int) ([]OpLogV2Row, error) {
	prefix := []byte("opv2:log:" + opID + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var result []OpLogV2Row
	for iter.First(); iter.Valid(); iter.Next() {
		var row OpLogV2Row
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			continue
		}
		result = append(result, row)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	if limit > 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, nil
}

func TestGetOpLogsV2_TailMatchesFull(t *testing.T) {
	p := newTimelineStore(t)
	op := ulid.Make().String()
	base := time.Now().UTC()
	rows := make([]OpLogV2Row, 1200)
	for i := range rows {
		rows[i] = OpLogV2Row{OperationID: op, Level: "info", Message: fmt.Sprintf("line %04d", i), CreatedAt: base.Add(time.Duration(i) * time.Millisecond)}
	}
	if err := p.AppendOpLogsV2(rows); err != nil {
		t.Fatal(err)
	}
	// 5 undecodable values interleaved, one at the very end.
	for i, at := range []int{0, 300, 600, 900, 1199} {
		k := opv2LogKey(op, base.Add(time.Duration(at)*time.Millisecond), int64(9_000_000+i))
		if err := p.db.Set(k, []byte("x"), pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{1, 10, 1000, 1195, 5000, 0} {
		got, err := p.GetOpLogsV2(op, limit)
		if err != nil {
			t.Fatal(err)
		}
		want, err := getOpLogsV2Full(p, op, limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("limit %d: len %d, want %d", limit, len(got), len(want))
		}
		for i := range got {
			if got[i].Message != want[i].Message || !got[i].CreatedAt.Equal(want[i].CreatedAt) {
				t.Fatalf("limit %d: row %d = %q, want %q", limit, i, got[i].Message, want[i].Message)
			}
		}
	}
}

// opv2OpKeyAllowlist is every non-test function, and every named top-level
// const/var, in the whole module that may mention an opv2:op: row key at all:
// the opv2OpKey builder, the opv2OpPrefix constant, or a string literal
// containing "opv2:op:". Keyed "<module-relative file>:<func>" for plain
// functions, "<file>:(*Recv).<method>" for methods, and "<file>:const <name>"
// / "<file>:var <name>" for declarations (there is no wildcard, so
// `var f = opv2OpKey` anywhere is caught).
//
// The role says what the function may do with the key:
//   - "stage": writes or deletes an opv2:op: row and must call stageOpRow
//     (the timeline index delta) in the same batch;
//   - "sweep": deletes rows whose CompletedAt is unknown (undecodable) and
//     must delete their opv2OpenKey and the done keys found by doneKeysForIDs;
//   - "read": may build the key only to read it. On p.db it may use only
//     Get, NewIter and NewSnapshot (called or as a method value); every
//     pebble write method (Set, Delete, SingleDelete, DeleteSized,
//     DeleteRange, Merge, SetDeferred, Apply, Commit, New*Batch*, RangeKey*,
//     Ingest*, LogData) and pebbleSetJSON is refused anywhere in the
//     function, except inside the extra func passed to commitOpV2Row (which
//     stages queue/act keys into commitOpV2Row's own batch).
//
// In every role a row-key value (an opv2OpKey(...) call, opv2OpPrefix, the
// literal, or a variable assigned or ranged from one) may flow only into the
// callees in opv2KeySinks for that role, or into another allowlisted
// function; never into any other helper (which could write it) and never
// into commitOpV2Row's extra func. opv2OpKey may only be called, not taken
// as a value.
//
// A NEW function that needs the key must be added here with the right role,
// and a new writer must go through commitOpV2Row or call stageOpRow. That is
// the point: review sees every place that can touch the row family. The
// runtime half is rejectOpv2OpRawKey on SetRaw/DeleteRaw/DeleteRawBatch, for
// keys built where no AST check can follow them.
var opv2OpKeyAllowlist = map[string]string{
	"internal/database/pebble_store_ops_v2_timeline.go:const opv2OpPrefix":                          "read",
	"internal/database/pebble_store_ops_v2.go:opv2OpKey":                                            "read",
	"internal/database/pebble_store_ops_v2_timeline.go:var opv2RawRefusedPrefixes":                  "read", // the raw-key guard's refused families
	"internal/database/pebble_store_ops_v2_timeline.go:(*PebbleStore).commitOpV2Row":                "stage",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).UpdateOperationV2Status":               "stage",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).ResetOperationV2ForResume":             "stage",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).SetOperationV2StatusIfQueued":          "stage",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).DeleteOperationV2":                     "stage",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).SweepHollowOperationsV2":               "sweep",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).InsertOperationV2":                     "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).PromoteToQueued":                       "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).SetOperationV2Result":                  "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).SetOpQueuedProgressV2":                 "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).UpdateOperationV2Params":               "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).stampCompletedAtIfPhantom":             "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).getExistingOpV2":                       "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).GetOperationV2":                        "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).ListActiveOperationsV2":                "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).ListQueuedOperationsV2":                "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).ListResumableOperationsV2":             "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).ListWaitingDepsOps":                    "read",
	"internal/database/pebble_store_ops_v2.go:(*PebbleStore).RepairOpsV2MissingCompletedAt":         "read",
	"internal/database/pebble_store_ops_v2_timeline.go:getOpV2FromSnapshot":                         "read",
	"internal/database/pebble_store_ops_v2_timeline.go:(*PebbleStore).listOperationsV2SinceIndexed": "read",
	"internal/database/pebble_store_ops_v2_timeline.go:(*PebbleStore).listOperationsV2SinceScan":    "read",
	"internal/database/pebble_store_ops_v2_timeline.go:opv2ULIDSeekBound":                           "read",
	"internal/database/pebble_store_ops_v2_timeline.go:(*PebbleStore).detectOpsV2TimelineDrift":     "read",
	"internal/database/pebble_store_ops_v2_timeline.go:(*PebbleStore).readOpV2Raw":                  "read",
}

// opv2PebbleWrites are pebble DB/Batch write methods (and pebbleSetJSON); a
// "read" function may not mention any of them.
var opv2PebbleWrites = map[string]bool{
	"Set": true, "Delete": true, "SingleDelete": true, "DeleteSized": true,
	"DeleteRange": true, "Merge": true, "SetDeferred": true, "Apply": true,
	"Commit": true, "NewBatch": true, "NewBatchWithSize": true,
	"NewIndexedBatch": true, "NewIndexedBatchWithSize": true,
	"RangeKeySet": true, "RangeKeyUnset": true, "RangeKeyDelete": true,
	"Ingest": true, "IngestAndExcise": true, "IngestWithStats": true,
	"Excise": true, "LogData": true, "pebbleSetJSON": true,
}

// opv2DBReads are the only p.db selectors a "read" function may use.
var opv2DBReads = map[string]bool{"Get": true, "NewIter": true, "NewSnapshot": true}

// opv2KeySinks are the callees a row-key value may be passed to, by role
// (in addition to any allowlisted function). Read-only plumbing only;
// stage/sweep add the batch writes they exist to make.
var opv2KeySinks = map[string]map[string]bool{
	"read": {
		"Get": true, "NewIter": true, "pebbleGetJSON": true, "prefixEnd": true,
		"prefixUpperBound": true, "append": true, "string": true,
		"Compare": true, "HasPrefix": true, "TrimPrefix": true,
		"forEachSnapKey": true, "runOpsV2TimelinePool": true,
		"Errorf": true, // an error message, not a write
	},
}

func init() {
	for _, role := range []string{"stage", "sweep"} {
		m := map[string]bool{"Set": true, "Delete": true}
		for k := range opv2KeySinks["read"] {
			m[k] = true
		}
		opv2KeySinks[role] = m
	}
}

func opv2CalleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	case *ast.ArrayType:
		return "string" // a []byte(...) conversion
	}
	return ""
}

// opv2IsRowKeyToken reports whether n is itself a row-key token.
func opv2IsRowKeyToken(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.Ident:
		return x.Name == "opv2OpKey" || x.Name == "opv2OpPrefix"
	case *ast.BasicLit:
		return x.Kind == token.STRING && strings.Contains(x.Value, "opv2:op:")
	}
	return false
}

// opv2KeyReturners are the allowlisted functions that may RETURN a row-key
// value (the key builder and the timeline seek bound). Any other allowlisted
// function returning one is a violation: the caller could write it.
var opv2KeyReturners = map[string]bool{
	"internal/database/pebble_store_ops_v2.go:opv2OpKey":                  true,
	"internal/database/pebble_store_ops_v2_timeline.go:opv2ULIDSeekBound": true,
}

// opv2FuncKey is a FuncDecl's allowlist key: file, receiver and name.
func opv2FuncKey(rel string, fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return rel + ":" + fd.Name.Name
	}
	var recv string
	switch t := fd.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			recv = "(*" + id.Name + ")"
		}
	case *ast.Ident:
		recv = "(" + t.Name + ")"
	}
	return rel + ":" + recv + "." + fd.Name.Name
}

// TestOpsV2RowWritesGoThroughStageOpRow is the CI ratchet that keeps every
// opv2:op: row write staging the timeline index. It parses EVERY non-test Go
// file in the module and fails on any mention of the row key outside
// opv2OpKeyAllowlist, then holds each allowlisted function to its role and
// to the key-flow rule (see the allowlist). Stale entries fail too.
//
// A call counts as "into an allowlisted function" only when EVERY function
// or method in the module with that name is allowlisted (file + receiver +
// name), so a second, unlisted method of the same name cannot launder a key.
//
// Known limits:
//   - a key spelled without any of the three tokens (for example
//     strings.Join([]string{"opv2", "op", id}, ":")) is invisible here; the
//     raw entry points refuse it at runtime (rejectOpv2OpRawKey), and a
//     direct p.db write of such a key in an unlisted function is left to
//     review;
//   - taint is tracked by variable name within one function (assignments,
//     element and field assignments, range); it does not follow values
//     through channels, closures' captured writes, struct fields read back
//     in another function, or interface method calls resolved at run time.
func TestOpsV2RowWritesGoThroughStageOpRow(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	fset := token.NewFileSet()
	type parsedFile struct {
		rel string
		f   *ast.File
	}
	var files []parsedFile
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			n := d.Name()
			if path != root && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" || n == "web" || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		af, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		files = append(files, parsedFile{filepath.ToSlash(rel), af})
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	// Every function/method in the module by bare name, for callee resolution.
	byName := map[string][]string{}
	for _, pf := range files {
		for _, decl := range pf.f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				byName[fd.Name.Name] = append(byName[fd.Name.Name], opv2FuncKey(pf.rel, fd))
			}
		}
	}
	allowlistedCallee := func(name string) bool {
		keys := byName[name]
		if len(keys) == 0 {
			return false
		}
		for _, k := range keys {
			if _, ok := opv2OpKeyAllowlist[k]; !ok {
				return false
			}
		}
		return true
	}

	var violations []string
	seen := map[string]bool{}
	check := func(key string, node ast.Node, fd *ast.FuncDecl) {
		var refs []string
		ast.Inspect(node, func(n ast.Node) bool {
			if n != nil && opv2IsRowKeyToken(n) {
				refs = append(refs, fset.Position(n.Pos()).String())
			}
			return true
		})
		if len(refs) == 0 {
			return
		}
		role, ok := opv2OpKeyAllowlist[key]
		if !ok {
			violations = append(violations, fmt.Sprintf("%s mentions the opv2:op: row key but is not in opv2OpKeyAllowlist (at %s); write through commitOpV2Row, or add it with its role after review", key, strings.Join(refs, ", ")))
			return
		}
		seen[key] = true
		if fd != nil && fd.Body != nil {
			violations = append(violations, opv2CheckRole(fset, key, role, fd, allowlistedCallee)...)
		}
	}
	for _, pf := range files {
		for _, decl := range pf.f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				check(opv2FuncKey(pf.rel, d), d, d)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch sp := spec.(type) {
					case *ast.ValueSpec:
						for _, name := range sp.Names {
							check(fmt.Sprintf("%s:%s %s", pf.rel, d.Tok, name.Name), sp, nil)
						}
					default:
						check(fmt.Sprintf("%s:%s <spec>", pf.rel, d.Tok), sp, nil)
					}
				}
			}
		}
	}
	for key := range opv2OpKeyAllowlist {
		if !seen[key] {
			violations = append(violations, fmt.Sprintf("allowlisted %s no longer mentions the opv2:op: row key; remove it from opv2OpKeyAllowlist", key))
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("opv2:op: row-key references must stay reviewed and keep the timeline index in the same batch:\n  %s", strings.Join(violations, "\n  "))
	}
}

// opv2CheckRole holds one allowlisted function to its role and to the
// key-flow rule.
func opv2CheckRole(fset *token.FileSet, key, role string, fd *ast.FuncDecl, allowlistedCallee func(string) bool) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, key+": "+fmt.Sprintf(format, args...)) }
	pos := func(n ast.Node) string { return fset.Position(n.Pos()).String() }

	// The commitOpV2Row extra func(s): writes are allowed inside, row keys are not.
	extras := map[ast.Node]bool{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && opv2CalleeName(c) == "commitOpV2Row" && len(c.Args) == 3 {
			extras[c.Args[2]] = true
		}
		return true
	})
	inExtra := func(n ast.Node) bool {
		for e := range extras {
			if n.Pos() >= e.Pos() && n.End() <= e.End() {
				return true
			}
		}
		return false
	}

	// Taint: variables assigned or ranged from a row-key value, to a fixpoint.
	tainted := map[string]bool{}
	// hasTaint reports whether e evaluates to (or builds) a row-key value. It
	// looks through concatenation, composite literals, slicing, &, and the
	// key-preserving calls (opv2OpKey, append, conversions); the result of any
	// other call is not a key (that call is checked as a sink in its own
	// right), and a closure body is checked at its own calls.
	keyPreserving := map[string]bool{"opv2OpKey": true, "append": true, "string": true}
	hasTaint := func(e ast.Node) bool {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			if found || n == nil {
				return false
			}
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "opv2OpKey" {
					found = true
					return false
				}
				if !keyPreserving[opv2CalleeName(c)] {
					return false
				}
			}
			if opv2IsRowKeyToken(n) {
				found = true
				return false
			}
			if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
				found = true
				return false
			}
			return true
		})
		return found
	}
	for changed := true; changed; {
		changed = false
		mark := func(lhs []ast.Expr) {
			for _, l := range lhs {
				// keys[0] = ..., s.k = ..., *p = ...: taint the base variable.
				for {
					switch x := l.(type) {
					case *ast.IndexExpr:
						l = x.X
						continue
					case *ast.SelectorExpr:
						l = x.X
						continue
					case *ast.StarExpr:
						l = x.X
						continue
					case *ast.ParenExpr:
						l = x.X
						continue
					}
					break
				}
				if id, ok := l.(*ast.Ident); ok && id.Name != "_" && !tainted[id.Name] {
					tainted[id.Name] = true
					changed = true
				}
			}
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				for _, r := range x.Rhs {
					if hasTaint(r) {
						mark(x.Lhs)
						break
					}
				}
			case *ast.ValueSpec:
				for _, v := range x.Values {
					if hasTaint(v) {
						var lhs []ast.Expr
						for _, nm := range x.Names {
							lhs = append(lhs, nm)
						}
						mark(lhs)
						break
					}
				}
			case *ast.RangeStmt:
				if hasTaint(x.X) {
					mark([]ast.Expr{x.Key, x.Value})
				}
			}
			return true
		})
	}

	// opv2OpKey only as a call.
	calledFun := map[ast.Node]bool{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			calledFun[c.Fun] = true
		}
		return true
	})
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "opv2OpKey" && !calledFun[id] {
			add("opv2OpKey taken as a value at %s; call it where the key is used", pos(id))
		}
		return true
	})

	// Flow: row-key values reach only the role's sinks or allowlisted funcs,
	// and never the extra func.
	containsKey := func(e ast.Node) bool {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			if n == nil || found {
				return false
			}
			if opv2IsRowKeyToken(n) {
				found = true
			} else if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
				found = true
			}
			return !found
		})
		return found
	}
	for e := range extras {
		if containsKey(e) {
			add("the commitOpV2Row extra func at %s uses an opv2:op: row key; it may stage only queue/act/index keys", pos(e))
		}
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := opv2CalleeName(c)
		if name == "len" || name == "opv2OpKey" || opv2KeySinks[role][name] || allowlistedCallee(name) {
			return true
		}
		for _, a := range c.Args {
			if extras[a] {
				continue
			}
			if hasTaint(a) {
				add("an opv2:op: row key flows into %s at %s, which is not an allowed sink for role %s", name, pos(c), role)
				break
			}
		}
		return true
	})

	// Returning a row key hands it to a caller the ratchet does not see.
	if !opv2KeyReturners[key] {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false // a closure's return goes to its own caller, checked as a sink
			}
			if r, ok := n.(*ast.ReturnStmt); ok {
				for _, res := range r.Results {
					if hasTaint(res) {
						add("returns an opv2:op: row key at %s; only %v may", pos(r), "opv2OpKey/opv2ULIDSeekBound")
						break
					}
				}
			}
			return true
		})
	}

	switch role {
	case "stage":
		if !opv2Calls(fd.Body, "stageOpRow") {
			add("(role stage) writes opv2:op: rows but no longer calls stageOpRow")
		}
	case "sweep":
		usesOpen := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "opv2OpenKey" {
				usesOpen = true
			}
			return true
		})
		if !opv2Calls(fd.Body, "doneKeysForIDs") || !usesOpen {
			add("(role sweep) must delete opv2OpenKey and the doneKeysForIDs keys of the rows it removes")
		}
	case "read":
		allowedDB := map[ast.Node]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && opv2DBReads[sel.Sel.Name] {
				allowedDB[sel.X] = true
			}
			return true
		})
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name == "db" && !allowedDB[x] {
					add("(role read) uses p.db other than Get/NewIter/NewSnapshot at %s", pos(x))
				}
				if opv2PebbleWrites[x.Sel.Name] && !inExtra(x) {
					add("(role read) mentions write method %s at %s outside the commitOpV2Row extra func", x.Sel.Name, pos(x))
				}
			case *ast.Ident:
				if x.Name == "pebbleSetJSON" && !inExtra(x) {
					add("(role read) mentions pebbleSetJSON at %s", pos(x))
				}
			}
			return true
		})
	default:
		add("unknown role %q", role)
	}
	return out
}

func opv2Calls(body ast.Node, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && opv2CalleeName(c) == name {
			found = true
		}
		return !found
	})
	return found
}

// TestRawKeyEntryPointsRefuseOpRows is the runtime half of the ratchet:
// SetRaw, DeleteRaw and DeleteRawBatch refuse an opv2:op: key however it was
// built, and DeleteRawBatch refuses the whole batch before deleting anything.
func TestRawKeyEntryPointsRefuseOpRows(t *testing.T) {
	p := newTimelineStore(t)
	refused := []string{
		strings.Join([]string{"opv2", "op", "01RAW0000000000000000000000"}, ":"),
		strings.Join([]string{"opv2", "open", "01RAW0000000000000000000000"}, ":"),
		string(opv2DoneKey(time.Now(), "01RAW0000000000000000000000")),
	}
	for _, key := range refused {
		if err := p.SetRaw(key, []byte("{}")); !errors.Is(err, errOpv2OpRawKey) {
			t.Fatalf("SetRaw(%q) = %v, want errOpv2OpRawKey", key, err)
		}
		if err := p.DeleteRaw(key); !errors.Is(err, errOpv2OpRawKey) {
			t.Fatalf("DeleteRaw(%q) = %v, want errOpv2OpRawKey", key, err)
		}
		if err := p.SetRaw("unrelated:k", []byte("v")); err != nil {
			t.Fatal(err)
		}
		if err := p.DeleteRawBatch([]string{"unrelated:k", key}); !errors.Is(err, errOpv2OpRawKey) {
			t.Fatalf("DeleteRawBatch(%q) = %v, want errOpv2OpRawKey", key, err)
		}
		if v, err := p.GetRaw("unrelated:k"); err != nil || string(v) != "v" {
			t.Fatalf("a refused DeleteRawBatch deleted another key: %q %v", v, err)
		}
		if _, closer, err := p.db.Get([]byte(key)); err == nil {
			closer.Close()
			t.Fatalf("a refused SetRaw wrote %q", key)
		}
	}
	// Exact prefixes only: neighbouring families are not refused.
	for _, key := range []string{"opv2:opx:1", "opv2:openly:1", "opv2:doner:1", "opv2:q:1", "opv2:op"} {
		if err := p.SetRaw(key, []byte("v")); err != nil {
			t.Fatalf("SetRaw(%q) refused, want allowed: %v", key, err)
		}
		if err := p.DeleteRaw(key); err != nil {
			t.Fatalf("DeleteRaw(%q) refused, want allowed: %v", key, err)
		}
	}
}

// A trust flip after Close is refused (N5): a closed store never reports
// trusted, and the reconcile reports the refusal.
func TestOpsV2Timeline_TrustRefusedAfterClose(t *testing.T) {
	p, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.setOpsV2TimelineTrusted(true) {
		t.Fatal("setOpsV2TimelineTrusted(true) succeeded on a closed store")
	}
	if p.OpsV2TimelineIndexTrusted() {
		t.Fatal("a closed store reports the index trusted")
	}
	if _, err := p.ReconcileOpsV2TimelineIndex(context.Background()); err == nil {
		t.Fatal("reconcile on a closed store returned nil")
	}
	if p.OpsV2TimelineIndexTrusted() {
		t.Fatal("a reconcile on a closed store left it trusted")
	}
}

// Close racing a finishing reconcile: whichever lands first, the closed
// store ends untrusted.
func TestOpsV2Timeline_CloseRacesReconcile(t *testing.T) {
	for i := range 20 {
		p, err := NewPebbleStoreInMemory(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		sim := newTimelineSim(t, p, uint64(1000+i))
		for range 20 {
			sim.history()
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = p.ReconcileOpsV2TimelineIndex(context.Background())
		}()
		_ = p.Close()
		<-done
		if p.OpsV2TimelineIndexTrusted() {
			t.Fatalf("iteration %d: closed store reports trusted after a racing reconcile", i)
		}
	}
}

// ── benchmark ───────────────────────────────────────────────────────────────

// BenchmarkListOperationsV2Since: 50,000 ops, 2,000 of them completed in the
// last 24 h and the rest spread over the 90 days before that. Each row
// carries ~600 bytes of params, roughly a prod row, so the decode cost the
// scan pays is realistic.
func BenchmarkListOperationsV2Since(b *testing.B) {
	p := newTimelineStore(b)
	base := time.Now().UTC()
	rng := rand.New(rand.NewPCG(1, 0))
	ent := rngReader{rng}
	params := `{"pad":"` + strings.Repeat("x", 600) + `"}`
	const total, recent = 50_000, 2_000
	batch := p.db.NewBatch()
	for i := range total {
		var q time.Time
		if i < recent {
			q = base.Add(-time.Duration(rng.Int64N(int64(24 * time.Hour))))
		} else {
			q = base.Add(-24*time.Hour - time.Duration(rng.Int64N(int64(89*24*time.Hour))))
		}
		started := q.Add(time.Second)
		row := OperationV2Row{
			ID: ulid.MustNew(ulid.Timestamp(q), ent).String(), DefID: "bench.def", Plugin: "bench",
			Status: "completed", Params: params, QueuedAt: q, StartedAt: &started,
		}
		c := q.Add(time.Duration(rng.Int64N(int64(10 * time.Minute))))
		if c.After(base) {
			c = base
		}
		row.CompletedAt = &c
		data, _ := json.Marshal(&row)
		_ = batch.Set(opv2OpKey(row.ID), data, nil)
		if err := stageOpRow(batch, nil, &row); err != nil {
			b.Fatal(err)
		}
		if batch.Count() > 10_000 {
			if err := batch.Commit(pebble.Sync); err != nil {
				b.Fatal(err)
			}
			batch = p.db.NewBatch()
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name   string
		since  time.Time
		reader func(time.Time, int) ([]OperationV2Row, error)
	}{
		{"scan_24h", base.Add(-24 * time.Hour), p.listOperationsV2SinceScan},
		{"indexed_24h", base.Add(-24 * time.Hour), p.listOperationsV2SinceIndexed},
		{"scan_90d", base.Add(-90 * 24 * time.Hour), p.listOperationsV2SinceScan},
		{"indexed_90d", base.Add(-90 * 24 * time.Hour), p.listOperationsV2SinceIndexed},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				rows, err := c.reader(c.since, 5000)
				if err != nil {
					b.Fatal(err)
				}
				if len(rows) == 0 {
					b.Fatal("no rows")
				}
			}
		})
	}
}

// An insert over an undecodable row overwrites it (as it always has) and
// leaves exactly the new row's index key.
func TestOpsV2Timeline_InsertOverUndecodableRow(t *testing.T) {
	p := newTimelineStore(t)
	id := ulid.Make().String()
	now := time.Now().UTC()
	if err := p.db.Set(opv2OpKey(id), []byte("{"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv2DoneKey(now.Add(-time.Hour), id), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.InsertOperationV2(OperationV2Row{ID: id, Status: "queued", QueuedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got := timelineKeys(t, p, opv2DonePrefix); len(got) != 0 {
		t.Fatalf("stale done keys remain: %v", got)
	}
	if got := timelineKeys(t, p, opv2OpenPrefix); len(got) != 1 || got[0] != string(opv2OpenKey(id)) {
		t.Fatalf("open keys = %v, want only %s", got, opv2OpenKey(id))
	}
}

// BenchmarkReconcileOpsV2Timeline measures the boot reconcile at 50k and
// 200k rows: "steady" (index already exact, the every-boot case) and "first"
// (no index keys at all, the first boot after A5 ships). ~600 B params per row.
func BenchmarkReconcileOpsV2Timeline(b *testing.B) {
	for _, n := range []int{50_000, 200_000} {
		// Not newTimelineStore: each size's store is closed before the next
		// is built, so the 200k fixture never shares memory with the 50k one.
		p, err := NewPebbleStoreInMemory(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		base := time.Now().UTC()
		rng := rand.New(rand.NewPCG(2, 0))
		ent := rngReader{rng}
		params := `{"pad":"` + strings.Repeat("x", 600) + `"}`
		batch := p.db.NewBatch()
		for i := range n {
			q := base.Add(-time.Duration(rng.Int64N(int64(90 * 24 * time.Hour))))
			row := OperationV2Row{ID: ulid.MustNew(ulid.Timestamp(q), ent).String(), Status: "completed", Params: params, QueuedAt: q}
			if i%50 != 0 {
				c := q.Add(time.Minute)
				row.CompletedAt = &c
			} else {
				row.Status = "running"
			}
			data, _ := json.Marshal(&row)
			_ = batch.Set(opv2OpKey(row.ID), data, nil)
			if batch.Count() > 10_000 {
				if err := batch.Commit(pebble.Sync); err != nil {
					b.Fatal(err)
				}
				batch = p.db.NewBatch()
			}
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			b.Fatal(err)
		}
		clearIndex := func() {
			for _, fam := range []string{opv2OpenPrefix, opv2DonePrefix} {
				if err := p.db.DeleteRange([]byte(fam), prefixUpperBound([]byte(fam)), pebble.Sync); err != nil {
					b.Fatal(err)
				}
			}
		}
		b.Run(fmt.Sprintf("first_%dk", n/1000), func(b *testing.B) {
			for b.Loop() {
				b.StopTimer()
				clearIndex()
				b.StartTimer()
				if _, err := p.ReconcileOpsV2TimelineIndex(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("steady_%dk", n/1000), func(b *testing.B) {
			for b.Loop() {
				if _, err := p.ReconcileOpsV2TimelineIndex(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
		_ = p.Close()
	}
}

// A closed store reports the timeline index untrusted (N3).
func TestOpsV2Timeline_CloseClearsTrust(t *testing.T) {
	p, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReconcileOpsV2TimelineIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.OpsV2TimelineIndexTrusted() {
		t.Fatal("not trusted after reconcile")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.OpsV2TimelineIndexTrusted() {
		t.Fatal("a closed store still reports the index trusted")
	}
}
