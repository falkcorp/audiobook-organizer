// file: internal/database/pebble_store_ops_v3_test.go
// version: 1.11.0
// guid: 721ba5ca-742e-43be-9064-b4c8bcba0578
// last-edited: 2026-10-25

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// ── helpers ─────────────────────────────────────────────────────────────────

// v3Digest hashes every key and value under each prefix, so a test can prove
// a failed write changed nothing.
func v3Digest(t testing.TB, p *PebbleStore, prefixes ...string) string {
	t.Helper()
	h := sha256.New()
	for _, pre := range prefixes {
		b := []byte(pre)
		iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: b, UpperBound: prefixEnd(b)})
		if err != nil {
			t.Fatal(err)
		}
		for iter.First(); iter.Valid(); iter.Next() {
			h.Write(iter.Key())
			h.Write([]byte{0})
			h.Write(iter.Value())
			h.Write([]byte{0})
		}
		if err := iter.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func mustRun(t testing.TB, p *PebbleStore, id string) *RunRecord {
	t.Helper()
	r, err := p.GetRunV3(id)
	if err != nil {
		t.Fatalf("GetRunV3(%s): %v", id, err)
	}
	if r == nil {
		t.Fatalf("GetRunV3(%s): no record", id)
	}
	return r
}

// transition applies ev to id with the stored CAS fields.
func transition(t testing.TB, p *PebbleStore, id string, ev state.Event, mut func(*TransitionRequest)) *RunRecord {
	t.Helper()
	cur := mustRun(t, p, id)
	req := TransitionRequest{ID: id, ExpectState: cur.State, ExpectAttempt: cur.Attempt, ExpectFenceEpoch: cur.FenceEpoch, Event: ev}
	if mut != nil {
		mut(&req)
	}
	out, err := p.TransitionRunV3(req)
	if err != nil {
		t.Fatalf("TransitionRunV3(%s, %s): %v", id, ev, err)
	}
	return out
}

func newRunID() string { return ulid.Make().String() }

func insertV3(t testing.TB, p *PebbleStore, def, wait string) string {
	t.Helper()
	id := newRunID()
	if err := p.InsertRunV3(RunRecord{ID: id, DefID: def, WaitReason: wait, Priority: 5}); err != nil {
		t.Fatalf("InsertRunV3: %v", err)
	}
	return id
}

// setOpsV2MirrorEnabled flips the opv2 mirror of native writes. Test-only
// until 05-PR16 adds the production switch.
func (p *PebbleStore) setOpsV2MirrorEnabled(on bool) { p.opsV2MirrorOff.Store(!on) }

// reachableV3States is every v3 state, derived from the production
// transition table: everything Next reaches from "" through every event and
// stop reason (every one of the ten states is reachable, so a state added to
// the table without a transition into it is a bug this also exposes).
func reachableV3States() []state.V3 {
	seen := map[state.V3]bool{}
	var out []state.V3
	frontier := []state.V3{""}
	for len(frontier) > 0 {
		from := frontier[0]
		frontier = frontier[1:]
		for _, ev := range state.AllEvents {
			for _, reason := range append([]string{""}, state.StopReasons...) {
				to, err := state.Next(from, ev, reason)
				if err != nil || seen[to] {
					continue
				}
				seen[to] = true
				out = append(out, to)
				frontier = append(frontier, to)
			}
		}
	}
	return out
}

// checkV3Consistency recomputes every index key and the counters from a full
// scan of opv3:run: and compares them to what is stored, and checks that
// every run's mirrored opv2 row reads in the status family the record maps
// to.
func checkV3Consistency(t testing.TB, p *PebbleStore) {
	t.Helper()
	runs := map[string]RunRecord{}
	want := map[string]bool{}
	counts := OpCounts{ByState: map[state.V3]int64{}, ByDef: map[string]int64{}}
	pre := []byte(opv3RunPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: pre, UpperBound: prefixEnd(pre)})
	if err != nil {
		t.Fatal(err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		var r RunRecord
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			t.Fatalf("%s: %v", iter.Key(), err)
		}
		runs[r.ID] = r
		for _, k := range opv3IndexKeys(&r) {
			want[k] = true
		}
		counts.ByState[r.State]++
		counts.ByDef[r.DefID]++
		counts.Total++
	}
	_ = iter.Close()
	got := map[string]bool{}
	for _, fam := range []string{opv3IxStatePrefix, opv3IxDefPrefix, opv3IxDonePrefix, opv3IxParentPrefix, opv3QueuePrefix} {
		for _, k := range timelineKeys(t, p, fam) {
			got[k] = true
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("index key missing: %s", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("orphan index key: %s", k)
		}
	}
	// Every run record has its opv3:prog: half and every opv3:prog: key has a
	// run: no write or delete path leaves one without the other.
	progs := map[string]bool{}
	for _, k := range timelineKeys(t, p, opv3ProgPrefix) {
		id := strings.TrimPrefix(k, opv3ProgPrefix)
		progs[id] = true
		if _, ok := runs[id]; !ok {
			t.Errorf("orphan opv3:prog: key for %s", id)
		}
	}
	for id := range runs {
		if !progs[id] {
			t.Errorf("run %s has no opv3:prog: key", id)
		}
	}
	stored, err := p.OpCountsV3()
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range counts.ByState {
		if stored.ByState[k] != v {
			t.Errorf("meta:counts by_state[%s] = %d, full scan says %d", k, stored.ByState[k], v)
		}
	}
	for k, v := range stored.ByState {
		if counts.ByState[k] != v {
			t.Errorf("meta:counts by_state[%s] = %d, full scan says %d", k, v, counts.ByState[k])
		}
	}
	for k, v := range counts.ByDef {
		if stored.ByDef[k] != v {
			t.Errorf("meta:counts by_def[%s] = %d, full scan says %d", k, stored.ByDef[k], v)
		}
	}
	if stored.Total != counts.Total {
		t.Errorf("meta:counts total = %d, full scan says %d", stored.Total, counts.Total)
	}
	for _, s := range reachableV3States() {
		n := int64(len(timelineKeys(t, p, opv3IxStatePrefix+string(s)+":")))
		if n != counts.ByState[s] {
			t.Errorf("ix:state:%s has %d keys, full scan says %d", s, n, counts.ByState[s])
		}
	}
	for id, r := range runs {
		row, err := p.GetOperationV2(id)
		if err != nil || row == nil {
			t.Errorf("run %s (%s) has no readable opv2 mirror row: %v", id, r.State, err)
			continue
		}
		// The v2 row is in the record's status family: either the exact
		// status the record mirrors as, or a v2 spelling (a legacy one, an
		// unmapped one) that the forward map sends to the record's state.
		fwd, _, _ := state.FromV2Live(row.Status, derefStr(row.ErrorMessage))
		mig, _, _ := state.FromV2(row.Status, derefStr(row.ErrorMessage))
		if r.FromV2 == nil {
			mig = fwd // only a migrated run may read "running" as interrupted{crash}
		}
		if wantS := v2StatusOf(&r, false); row.Status != wantS && fwd != r.State && mig != r.State {
			t.Errorf("run %s is %s in v3 but %q in v2 (want %q)", id, r.State, row.Status, wantS)
		}
	}
}

// ── TestStageRunV3KeepsIndexesConsistent ────────────────────────────────────

// TestStageRunV3KeepsIndexesConsistent drives 10,000 random writes across 50
// defs through both paths -- the v2 façade (insert, status writes including
// the off-table ones the v2 registry makes, resume reset, promote, progress,
// discard) and the native v3 methods (insert, every legal transition,
// discard) -- and after each 1,000 checks that ix:state, ix:def, ix:done,
// ix:parent, q and meta:counts all equal a full scan of opv3:run:, and that
// every mirrored v2 row reads in the right status family.
func TestStageRunV3KeepsIndexesConsistent(t *testing.T) {
	p := newTimelineStore(t)
	rng := rand.New(rand.NewPCG(20261011, 0))
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	entropy := ulid.Monotonic(rngReader{rng}, 0)
	var ids []string
	newID := func() string {
		clock = clock.Add(time.Duration(1+rng.IntN(1000)) * time.Millisecond)
		id := ulid.MustNew(ulid.Timestamp(clock), entropy).String()
		ids = append(ids, id)
		return id
	}
	pick := func() string {
		if len(ids) == 0 {
			return ""
		}
		return ids[rng.IntN(len(ids))]
	}
	def := func() string { return fmt.Sprintf("test.def-%02d", rng.IntN(50)) }
	v2Statuses := []string{"queued", "running", "completed", "failed", "canceled", "interrupted_quiesced",
		"interrupted_ask", "interrupted_dropped", "waiting_deps", "interrupted", "interrupted_restart", "made_up"}
	allDiscard := append(state.WithProp(func(state.Props) bool { return true }), "queued", "running", "waiting_deps", "made_up")

	for i := 1; i <= 10000; i++ {
		id := pick()
		switch op := rng.IntN(12); {
		case op == 0 || id == "":
			row := OperationV2Row{ID: newID(), DefID: def(), Status: "queued", Priority: rng.IntN(10), QueuedAt: clock}
			if rng.IntN(4) == 0 {
				row.Status = "waiting_deps"
			}
			if rng.IntN(5) == 0 && len(ids) > 1 {
				parent := ids[rng.IntN(len(ids)-1)]
				row.ParentID = &parent
			}
			_ = p.InsertOperationV2(row)
		case op == 1:
			if err := p.InsertRunV3(RunRecord{ID: newID(), DefID: def(), Priority: rng.IntN(10),
				WaitReason: []string{"", "", state.WaitDeps, state.WaitPause}[rng.IntN(4)]}); err != nil {
				t.Fatal(err)
			}
		case op <= 4:
			now := clock
			msg := "x"
			if rng.IntN(3) == 0 {
				msg = "timeout: run exceeded 1m"
			}
			_ = p.UpdateOperationV2Status(id, v2Statuses[rng.IntN(len(v2Statuses))], &now, &now, &msg)
		case op == 5:
			_ = p.ResetOperationV2ForResume(id)
		case op == 6:
			_ = p.PromoteToQueued(id)
		case op == 7:
			_ = p.UpdateOpProgressV2(id, rng.IntN(100), 100, "tick")
		case op == 8:
			if rng.IntN(4) == 0 {
				_, _, _ = p.DeleteOperationV2(id, allDiscard)
			} else {
				_, _ = p.DeleteRunV3(id, reachableV3States())
			}
		default:
			cur, err := p.GetRunV3(id)
			if err != nil {
				t.Fatal(err)
			}
			if cur == nil {
				continue
			}
			type cand struct {
				ev     state.Event
				reason string
			}
			var cands []cand
			for _, ev := range state.AllEvents {
				for _, r := range append([]string{""}, state.StopReasons...) {
					if _, err := state.Next(cur.State, ev, r); err == nil && ev != state.EvEnqueue {
						cands = append(cands, cand{ev, r})
					}
				}
			}
			if len(cands) == 0 {
				continue
			}
			c := cands[rng.IntN(len(cands))]
			_, err = p.TransitionRunV3(TransitionRequest{ID: id, ExpectState: cur.State, ExpectAttempt: cur.Attempt,
				ExpectFenceEpoch: cur.FenceEpoch, Event: c.ev, StopReason: c.reason, Zombie: rng.IntN(2) == 0})
			if err != nil && !errors.Is(err, state.ErrTransition) {
				t.Fatalf("TransitionRunV3(%s %s %s): %v", id, cur.State, c.ev, err)
			}
		}
		if i%1000 == 0 {
			checkV3Consistency(t, p)
			if t.Failed() {
				t.Fatalf("inconsistent after %d writes", i)
			}
		}
	}
	// Every write path above stamped V2RowHash with the bytes it stored, so
	// the boot reconcile finds nothing to rebuild and writes nothing.
	assertReconcileNoop(t, p, "10,000 random writes")
}

// ── TestTransitionCAS ───────────────────────────────────────────────────────

func TestTransitionCAS(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.cas", "")
	cur := mustRun(t, p, id)
	before := v3Digest(t, p, "opv2:", "opv3:")
	for name, req := range map[string]TransitionRequest{
		"stale attempt": {ID: id, ExpectState: cur.State, ExpectAttempt: cur.Attempt + 1, ExpectFenceEpoch: cur.FenceEpoch, Event: state.EvDispatch},
		"stale epoch":   {ID: id, ExpectState: cur.State, ExpectAttempt: cur.Attempt, ExpectFenceEpoch: cur.FenceEpoch + 7, Event: state.EvDispatch},
		"stale state":   {ID: id, ExpectState: state.V3Running, ExpectAttempt: cur.Attempt, ExpectFenceEpoch: cur.FenceEpoch, Event: state.EvReturnNil},
	} {
		if _, err := p.TransitionRunV3(req); !errors.Is(err, ErrStaleTransition) {
			t.Errorf("%s: err = %v, want ErrStaleTransition", name, err)
		}
	}
	if _, err := p.TransitionRunV3(TransitionRequest{ID: id, ExpectState: cur.State, Event: state.EvReturnNil}); !errors.Is(err, state.ErrTransition) {
		t.Errorf("queued --return_nil--> err = %v, want state.ErrTransition", err)
	}
	if after := v3Digest(t, p, "opv2:", "opv3:"); after != before {
		t.Fatal("a refused transition changed the store")
	}

	// Two concurrent dispatches of one run: exactly one wins.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, stale := 0, 0
	for range 8 {
		wg.Go(func() {
			_, err := p.TransitionRunV3(TransitionRequest{ID: id, ExpectState: cur.State, ExpectAttempt: cur.Attempt,
				ExpectFenceEpoch: cur.FenceEpoch, Event: state.EvDispatch})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrStaleTransition):
				stale++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()
	if wins != 1 || stale != 7 {
		t.Fatalf("concurrent dispatch: %d won, %d stale; want 1 and 7", wins, stale)
	}
	r := mustRun(t, p, id)
	if r.State != state.V3Running || r.Attempt != 1 || r.FenceEpoch != 1 {
		t.Fatalf("after dispatch: %s attempt %d epoch %d, want running 1 1", r.State, r.Attempt, r.FenceEpoch)
	}
}

// ── TestOneBatchTwoFamilies ─────────────────────────────────────────────────

// TestOneBatchTwoFamilies injects a failing commit into every write path and
// requires that neither opv3: nor opv2: changed.
func TestOneBatchTwoFamilies(t *testing.T) {
	p := newTimelineStore(t)
	v2id := newRunID()
	if err := p.InsertOperationV2(OperationV2Row{ID: v2id, DefID: "test.batch", Status: "queued", QueuedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	v3id := insertV3(t, p, "test.batch", "")
	transition(t, p, v3id, state.EvDispatch, nil)

	before := v3Digest(t, p, "opv2:", "opv3:")
	boom := errors.New("injected commit failure")
	hook := func(s *PebbleStore) error {
		if s == p {
			return boom
		}
		return nil
	}
	opsV2BeforeCommitHook.Store(&hook)
	t.Cleanup(func() { opsV2BeforeCommitHook.Store(nil) })

	now := time.Now().UTC()
	writes := map[string]func() error{
		"UpdateOperationV2Status": func() error { return p.UpdateOperationV2Status(v2id, "running", &now, nil, nil) },
		"UpdateOpProgressV2":      func() error { return p.UpdateOpProgressV2(v2id, 1, 2, "x") },
		"ResetOperationV2ForResume": func() error {
			return p.ResetOperationV2ForResume(v2id)
		},
		"DeleteOperationV2": func() error {
			_, _, err := p.DeleteOperationV2(v2id, []string{"queued"})
			return err
		},
		"TransitionRunV3": func() error {
			_, err := p.TransitionRunV3(TransitionRequest{ID: v3id, ExpectState: state.V3Running, ExpectAttempt: 1, ExpectFenceEpoch: 1, Event: state.EvReturnNil})
			return err
		},
		"InsertRunV3":    func() error { return p.InsertRunV3(RunRecord{ID: newRunID(), DefID: "test.batch"}) },
		"DeleteRunV3":    func() error { _, err := p.DeleteRunV3(v3id, reachableV3States()); return err },
		"CommitLedgerV3": func() error { return p.CommitLedgerV3(v3id, LedgerBatch{Ledger: Ledger{ChunkCount: 1}}) },
	}
	for name, w := range writes {
		if err := w(); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want the injected failure", name, err)
		}
		if after := v3Digest(t, p, "opv2:", "opv3:"); after != before {
			t.Fatalf("%s: a failed commit changed opv2:/opv3:", name)
		}
	}
}

// ── TestTerminalDeletesSnapAndLedger / TestCommitLedgerOneBatch ─────────────

func TestTerminalDeletesSnapAndLedger(t *testing.T) {
	for _, path := range []string{"native", "v2 façade"} {
		t.Run(path, func(t *testing.T) {
			p := newTimelineStore(t)
			id := insertV3(t, p, "test.ledger", "")
			transition(t, p, id, state.EvDispatch, nil)
			for page := range 3 {
				if err := p.PutSnapshotPageV3(id, page, []string{"a", "b"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.PutChunkOffsetsV3(id, []int32{0, 256}); err != nil {
				t.Fatal(err)
			}
			total := int64(512)
			if err := p.CommitLedgerV3(id, LedgerBatch{
				Ledger:   Ledger{ChunkSize: 256, ChunkCount: 2, Done: []byte{3}, Items: LedgerItems{Done: 512}},
				Progress: ProgressSnapshot{Done: 512, Total: &total, Unit: "books"},
			}); err != nil {
				t.Fatal(err)
			}
			if path == "native" {
				transition(t, p, id, state.EvReturnNil, nil)
			} else {
				now := time.Now().UTC()
				if err := p.UpdateOperationV2Status(id, "completed", nil, &now, nil); err != nil {
					t.Fatal(err)
				}
			}
			if keys := timelineKeys(t, p, string(opv3SnapRunPrefix(id))); len(keys) != 0 {
				t.Errorf("snapshot keys survive the terminal transition: %v", keys)
			}
			if l, err := p.GetLedgerV3(id); err != nil || l != nil {
				t.Errorf("ledger survives the terminal transition: %+v %v", l, err)
			}
			r := mustRun(t, p, id)
			if r.State != state.V3Succeeded {
				t.Fatalf("state = %s", r.State)
			}
			if path == "native" && (r.Progress.Done != 512 || r.Progress.Total == nil || *r.Progress.Total != 512) {
				t.Errorf("final counters lost: %+v", r.Progress)
			}
			if err := p.CommitLedgerV3(id, LedgerBatch{}); !errors.Is(err, ErrStaleTransition) {
				t.Errorf("a ledger commit after the terminal transition: err = %v, want ErrStaleTransition", err)
			}
		})
	}
}

func TestCommitLedgerOneBatch(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.ledger", "")
	transition(t, p, id, state.EvDispatch, nil)
	if err := p.SaveCheckpointV3(id, Checkpoint{SchemaVersion: 1, Phase: "scan"}); err != nil {
		t.Fatal(err)
	}
	lease := Lease{Chunk: 1, Worker: 0, Item: "item-7", ItemIndex: 7}
	if err := p.CommitLedgerV3(id, LedgerBatch{
		Ledger:   Ledger{ChunkSize: 10, ChunkCount: 3, Done: []byte{1}},
		Leases:   []Lease{lease},
		Progress: ProgressSnapshot{Done: 10, Phase: "scan"},
		ItemRows: []ItemOutcome{{Key: "item-3", Outcome: "failed", Error: "nope"}},
	}); err != nil {
		t.Fatal(err)
	}
	l, _ := p.GetLedgerV3(id)
	ck, _ := p.GetCheckpointV3(id)
	r := mustRun(t, p, id)
	items, _ := p.ListItemOutcomesV3(id, ItemQuery{})
	if l == nil || l.ChunkCount != 3 || ck == nil || len(ck.Leases) != 1 || ck.Leases[0].Item != "item-7" || ck.Phase != "scan" ||
		r.Progress.Done != 10 || items.Total != 1 {
		t.Fatalf("ledger commit incomplete: ledger %+v ckpt %+v progress %+v items %+v", l, ck, r.Progress, items)
	}

	before := v3Digest(t, p, "opv3:")
	boom := errors.New("injected")
	hook := func(s *PebbleStore) error {
		if s == p {
			return boom
		}
		return nil
	}
	opsV2BeforeCommitHook.Store(&hook)
	defer opsV2BeforeCommitHook.Store(nil)
	if err := p.CommitLedgerV3(id, LedgerBatch{Ledger: Ledger{ChunkCount: 9}, Progress: ProgressSnapshot{Done: 99},
		ItemRows: []ItemOutcome{{Key: "item-4", Outcome: "skipped:dup"}}}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if v3Digest(t, p, "opv3:") != before {
		t.Fatal("a failed ledger commit wrote part of its batch")
	}
}

// ── TestListRunsTotalIsExact ────────────────────────────────────────────────

func TestListRunsTotalIsExact(t *testing.T) {
	p := newTimelineStore(t)
	for range 25 {
		insertV3(t, p, "test.a", "")
	}
	for range 10 {
		id := insertV3(t, p, "test.b", "")
		transition(t, p, id, state.EvDispatch, nil)
	}
	for range 3 {
		id := insertV3(t, p, "test.a", "")
		transition(t, p, id, state.EvDispatch, nil)
	}
	check := func(name string, q RunQuery, wantTotal int64, wantPages []int) {
		t.Helper()
		var sizes []int
		seen := map[string]bool{}
		for {
			page, err := p.ListRunsV3(q)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if page.Total != wantTotal {
				t.Errorf("%s: Total = %d, want %d (page length %d)", name, page.Total, wantTotal, len(page.Runs))
			}
			sizes = append(sizes, len(page.Runs))
			for _, r := range page.Runs {
				if seen[r.ID] {
					t.Errorf("%s: run %s on two pages", name, r.ID)
				}
				seen[r.ID] = true
				if (q.DefID != "" && r.DefID != q.DefID) || (q.State != "" && r.State != q.State) {
					t.Errorf("%s: run %s (%s, %s) does not match the query", name, r.ID, r.DefID, r.State)
				}
			}
			if page.Next == "" {
				break
			}
			q.Cursor = page.Next
		}
		if fmt.Sprint(sizes) != fmt.Sprint(wantPages) {
			t.Errorf("%s: page sizes %v, want %v", name, sizes, wantPages)
		}
	}
	check("def a", RunQuery{DefID: "test.a", Limit: 10}, 28, []int{10, 10, 8})
	check("queued", RunQuery{State: state.V3Queued, Limit: 10}, 25, []int{10, 10, 5})
	check("def a running", RunQuery{DefID: "test.a", State: state.V3Running, Limit: 2}, 3, []int{2, 1})
	check("all", RunQuery{Limit: 20}, 38, []int{20, 18})
	c, err := p.OpCountsV3()
	if err != nil || c.Total != 38 || c.ByState[state.V3Running] != 13 || c.ByDef["test.a"] != 28 {
		t.Fatalf("OpCountsV3 = %+v, %v", c, err)
	}
	q, err := p.ListQueuedRunsV3(0)
	if err != nil || len(q) != 25 {
		t.Fatalf("ListQueuedRunsV3 = %d runs, %v; want 25", len(q), err)
	}
}

// ── TestListRunsKeepIsOnePass ───────────────────────────────────────────────

// TestListRunsKeepIsOnePass: RunQuery.Keep narrows a listing inside the store's
// own pass, so Total counts only the runs it accepts, every page is full while
// accepted runs remain (no short or empty page for a run Keep refused, and no
// page after the last accepted one), and one call judges each record under the
// index once however small the limit is. Covered for each index the listing can
// walk: every state, one state, one def, and a def with a state.
func TestListRunsKeepIsOnePass(t *testing.T) {
	p := newTimelineStore(t)
	for range 7 {
		insertV3(t, p, "test.show", "")
	}
	for range 6 {
		insertV3(t, p, "test.hide", "")
	}
	for range 3 {
		id := insertV3(t, p, "test.hide", "")
		transition(t, p, id, state.EvDispatch, nil)
	}
	for range 4 {
		id := insertV3(t, p, "test.show", "")
		transition(t, p, id, state.EvDispatch, nil)
	}
	cases := []struct {
		name      string
		q         RunQuery
		total     int64
		pages     []int
		candidate int // records Keep is asked about: those the def and state already accept
	}{
		{"every state", RunQuery{Limit: 4}, 11, []int{4, 4, 3}, 20},
		{"one state", RunQuery{State: state.V3Queued, Limit: 3}, 7, []int{3, 3, 1}, 13},
		{"one def", RunQuery{DefID: "test.show", Limit: 5}, 11, []int{5, 5, 1}, 11},
		{"a def Keep refuses whole", RunQuery{DefID: "test.hide", Limit: 2}, 0, []int{0}, 9},
		{"def and state", RunQuery{DefID: "test.show", State: state.V3Running, Limit: 3}, 4, []int{3, 1}, 4},
		{"limit one", RunQuery{Limit: 1}, 11, []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, 20},
	}
	for _, c := range cases {
		calls := 0
		c.q.Keep = func(r *RunRecord) bool {
			calls++
			return r.DefID != "test.hide"
		}
		var sizes []int
		seen := map[string]bool{}
		for {
			calls = 0
			page, err := p.ListRunsV3(c.q)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if page.Total != c.total {
				t.Errorf("%s: Total = %d, want %d", c.name, page.Total, c.total)
			}
			if calls != c.candidate {
				t.Errorf("%s: one call judged %d records, want %d (one pass)", c.name, calls, c.candidate)
			}
			sizes = append(sizes, len(page.Runs))
			for _, r := range page.Runs {
				if r.DefID == "test.hide" {
					t.Errorf("%s: a run Keep refused was returned: %s", c.name, r.ID)
				}
				if seen[r.ID] {
					t.Errorf("%s: run %s on two pages", c.name, r.ID)
				}
				seen[r.ID] = true
			}
			if page.Next == "" {
				break
			}
			c.q.Cursor = page.Next
		}
		if fmt.Sprint(sizes) != fmt.Sprint(c.pages) {
			t.Errorf("%s: page sizes %v, want %v", c.name, sizes, c.pages)
		}
		if int64(len(seen)) != c.total {
			t.Errorf("%s: the walk met %d runs, Total says %d", c.name, len(seen), c.total)
		}
	}
}

// ── TestMirrorEveryState ────────────────────────────────────────────────────

// TestMirrorEveryState writes each of the ten v3 states natively and reads
// the run back through the v2 API (what GET /api/v1/operations/v2/:id
// serves): the status families must agree, and a v2 progress tick on the row
// must not move the v3 state.
func TestMirrorEveryState(t *testing.T) {
	if n := len(reachableV3States()); n != 10 {
		t.Fatalf("the transition table reaches %d v3 states, want the ten of state-and-persistence.md section 1", n)
	}
	type step struct {
		ev  state.Event
		mut func(*TransitionRequest)
	}
	stop := func(reason string) step {
		return step{state.EvStopRequested, func(r *TransitionRequest) { r.StopReason = reason; r.Zombie = true }}
	}
	plain := func(ev state.Event) step { return step{ev, nil} }
	cases := []struct {
		want   state.V3
		wait   string
		steps  []step
		v2     string
		msgPfx string
	}{
		{state.V3Queued, "", nil, "queued", ""},
		{state.V3Queued, state.WaitDeps, nil, "waiting_deps", ""},
		{state.V3Running, "", []step{plain(state.EvDispatch)}, "running", ""},
		{state.V3Stopping, "", []step{plain(state.EvDispatch), stop(state.StopWatchdog)}, "running", ""},
		{state.V3Interrupted, "", []step{plain(state.EvDispatch), plain(state.EvBootSweep)}, "interrupted_quiesced", ""},
		{state.V3AwaitingDecision, "", []step{plain(state.EvDispatch), plain(state.EvBootSweep), plain(state.EvAsk)}, "interrupted_ask", ""},
		{state.V3Succeeded, "", []step{plain(state.EvDispatch), plain(state.EvReturnNil)}, "completed", ""},
		{state.V3Failed, "", []step{plain(state.EvDispatch), {state.EvReturnErr, func(r *TransitionRequest) { r.Error = "boom" }}}, "failed", "boom"},
		{state.V3Canceled, "", []step{plain(state.EvDispatch), stop(state.StopUser), {state.EvGoroutineReturned, func(r *TransitionRequest) { r.StopReason = state.StopUser }}}, "canceled", ""},
		{state.V3TimedOut, "", []step{plain(state.EvDispatch), stop(state.StopTimeout), {state.EvGoroutineReturned, func(r *TransitionRequest) { r.StopReason = state.StopTimeout }}}, "canceled", "timeout:"},
		{state.V3Dropped, "", []step{plain(state.EvDispatch), plain(state.EvBootSweep), plain(state.EvDecideDrop)}, "interrupted_dropped", ""},
	}
	covered := map[state.V3]bool{}
	p := newTimelineStore(t)
	for _, c := range cases {
		id := insertV3(t, p, "test.mirror", c.wait)
		for _, s := range c.steps {
			transition(t, p, id, s.ev, s.mut)
		}
		r := mustRun(t, p, id)
		row, err := p.GetOperationV2(id)
		if err != nil || row == nil {
			t.Fatalf("%s: no v2 row: %v", c.want, err)
		}
		if r.State != c.want || row.Status != c.v2 || !strings.HasPrefix(derefStr(row.ErrorMessage), c.msgPfx) {
			t.Errorf("%s: v3 %s / v2 %q (%q), want v3 %s / v2 %q (%q...)", c.want, r.State, row.Status, derefStr(row.ErrorMessage), c.want, c.v2, c.msgPfx)
		}
		live := c.v2 == "queued" || c.v2 == "running" || c.v2 == "waiting_deps"
		if live == (row.CompletedAt != nil) {
			t.Errorf("%s: v2 completed_at = %v for status %s", c.want, row.CompletedAt, row.Status)
		}
		// A v2 write that keeps the status (a progress tick) keeps the v3 state.
		if err := p.UpdateOpProgressV2(id, 3, 10, "tick"); err != nil {
			t.Fatal(err)
		}
		if again := mustRun(t, p, id); again.State != c.want || again.Progress.Done != 3 {
			t.Errorf("%s: after a v2 progress tick the run is %s (done %d)", c.want, again.State, again.Progress.Done)
		}
		covered[c.want] = true
	}
	for _, s := range reachableV3States() {
		if !covered[s] {
			t.Errorf("state %s has no mirror row", s)
		}
	}
	checkV3Consistency(t, p)
}

// TestFacadeOffTableMovesAreRecorded: the v2 registry's moves the v3 table
// does not have are mirrored, never refused, and say so on the audit trail.
func TestFacadeOffTableMovesAreRecorded(t *testing.T) {
	p := newTimelineStore(t)
	id := newRunID()
	if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: "test.facade", Status: "queued", QueuedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	ok, err := p.UpdateOperationV2StatusIf(id, "queued", "canceled", nil, nil, nil)
	if err != nil || !ok {
		t.Fatalf("cancel of a queued row: %v %v", ok, err)
	}
	evts, err := p.ListRunEventsV3(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 2 || evts[0].Event != string(state.EvEnqueue) || evts[1].Event != string(state.EvV2Write) ||
		evts[1].From != state.V3Queued || evts[1].To != state.V3Canceled {
		t.Fatalf("events = %+v", evts)
	}
	if mustRun(t, p, id).State != state.V3Canceled {
		t.Fatal("not canceled")
	}
}

// ── TestLogReadThrough ──────────────────────────────────────────────────────

func TestLogReadThrough(t *testing.T) {
	p := newTimelineStore(t)
	id := newRunID()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: "test.log", Status: "completed", QueuedAt: t0}); err != nil {
		t.Fatal(err)
	}
	var v2 []OpLogV2Row
	for i := range 4 {
		v2 = append(v2, OpLogV2Row{OperationID: id, Level: "info", Message: fmt.Sprintf("v2-%d", i),
			Attrs: `{"n":` + fmt.Sprint(i) + `}`, CreatedAt: t0.Add(time.Duration(2*i) * time.Second)})
	}
	if err := p.AppendOpLogsV2(v2); err != nil {
		t.Fatal(err)
	}
	// Pretend the run predates the façade, then migrate it.
	wipeOpv3(t, p)
	if _, err := p.MigrateOpsV2ToV3(500, nil); err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, p, id); r.FromV2 == nil {
		t.Fatal("not a migrated run")
	}
	for i := range 3 {
		if err := p.AppendLogV3(id, LogLine{At: t0.Add(time.Duration(2*i+1) * time.Second), Level: "info", Message: fmt.Sprintf("v3-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := p.ListLogV3(id, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, l := range all {
		msgs = append(msgs, l.Message)
	}
	want := "v2-0 v3-0 v2-1 v3-1 v2-2 v3-2 v2-3"
	if strings.Join(msgs, " ") != want {
		t.Fatalf("merged log = %v, want %s", msgs, want)
	}
	if all[0].Fields["n"] != "0" {
		t.Errorf("v2 attrs not carried: %+v", all[0].Fields)
	}
	// Page through it two at a time across both families.
	var paged []string
	after := ""
	for {
		page, err := p.ListLogV3(id, 2, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, l := range page {
			paged = append(paged, l.Message)
		}
		after = page[len(page)-1].Key
	}
	if strings.Join(paged, " ") != want {
		t.Fatalf("paged log = %v, want %s", paged, want)
	}
}

// wipeOpv3 deletes every opv3: key, turning the store into one written by a
// binary that predates 05-PR4.
func wipeOpv3(t testing.TB, p *PebbleStore) {
	t.Helper()
	pre := []byte(opv3Prefix)
	if err := p.db.DeleteRange(pre, prefixEnd(pre), pebble.Sync); err != nil {
		t.Fatal(err)
	}
}

// ── discard ─────────────────────────────────────────────────────────────────

// TestDeleteRunV3LeavesNoV2Keys holds DeleteRunV3's mirror delete to
// DeleteOperationV2's key set: after either, no opv2: or opv3: key names the
// run.
func TestDeleteRunV3LeavesNoV2Keys(t *testing.T) {
	for _, via := range []string{"DeleteRunV3", "DeleteOperationV2"} {
		t.Run(via, func(t *testing.T) {
			p := newTimelineStore(t)
			id := newRunID()
			now := time.Now().UTC()
			if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: "test.discard", Status: "queued", Priority: 3, QueuedAt: now}); err != nil {
				t.Fatal(err)
			}
			_ = p.UpsertOpStateV2(OpStateV2Row{OperationID: id, StateBlob: []byte("{}")})
			_ = p.AppendOpLogsV2([]OpLogV2Row{{OperationID: id, Message: "x", CreatedAt: now}})
			_ = p.InsertOpErrorV2(OpErrorV2Row{OperationID: id, OccurredAt: now})
			_ = p.AppendLogV3(id, LogLine{Message: "y"})
			_ = p.PutIntentV3(id, Intent{Seq: 1})
			if err := p.UpdateOperationV2Status(id, "failed", &now, &now, nil); err != nil {
				t.Fatal(err)
			}
			var deleted bool
			var err error
			if via == "DeleteRunV3" {
				deleted, err = p.DeleteRunV3(id, []state.V3{state.V3Failed})
			} else {
				_, deleted, err = p.DeleteOperationV2(id, []string{"failed"})
			}
			if err != nil || !deleted {
				t.Fatalf("delete: %v %v", deleted, err)
			}
			for _, fam := range []string{"opv2:", "opv3:"} {
				for _, k := range timelineKeys(t, p, fam) {
					if strings.Contains(k, id) {
						t.Errorf("key survives the discard: %s", k)
					}
				}
			}
			checkV3Consistency(t, p)
		})
	}
}

// ── item outcomes ───────────────────────────────────────────────────────────

func TestItemOutcomesCapAndPaging(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.items", "")
	var rows []ItemOutcome
	for i := range opv3ItemCap + 5 {
		outcome := "failed"
		if i%2 == 1 {
			outcome = "skipped:dup"
		}
		rows = append(rows, ItemOutcome{Key: fmt.Sprintf("item-%05d", i), Outcome: outcome})
	}
	if err := p.PutItemOutcomesV3(id, rows); err != nil {
		t.Fatal(err)
	}
	page, err := p.ListItemOutcomesV3(id, ItemQuery{Limit: 7})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != opv3ItemCap || !page.Truncated || len(page.Rows) != 7 || page.Next == "" {
		t.Fatalf("page = total %d truncated %v rows %d next %q", page.Total, page.Truncated, len(page.Rows), page.Next)
	}
	sk, err := p.ListItemOutcomesV3(id, ItemQuery{Outcome: "skipped", Limit: 3})
	if err != nil || sk.Total != opv3ItemCap/2 || sk.Rows[0].Outcome != "skipped:dup" {
		t.Fatalf("skipped page = %+v %v", sk.Total, err)
	}
	// Re-putting a stored key overwrites it without counting it again.
	if err := p.PutItemOutcomesV3(id, rows[:1]); err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, p, id); r.ItemOutcomes.Stored != opv3ItemCap || r.ItemOutcomes.Dropped != 5 {
		t.Fatalf("counts = %+v", r.ItemOutcomes)
	}
}

// TestSnapshotsIntentsAndCheckpoints round-trips the small per-run families.
func TestSnapshotsIntentsAndCheckpoints(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.small", "")
	if err := p.PutSnapshotPageV3(id, 2, []string{"k1", "k2"}); err != nil {
		t.Fatal(err)
	}
	if keys, ok, err := p.GetSnapshotPageV3(id, 2); err != nil || !ok || len(keys) != 2 {
		t.Fatalf("snapshot page: %v %v %v", keys, ok, err)
	}
	if _, ok, _ := p.GetSnapshotPageV3(id, 3); ok {
		t.Fatal("missing page reported found")
	}
	if err := p.PutChunkOffsetsV3(id, []int32{0, 10, 25}); err != nil {
		t.Fatal(err)
	}
	if offs, ok, err := p.GetChunkOffsetsV3(id); err != nil || !ok || len(offs) != 3 {
		t.Fatalf("chunk offsets: %v %v %v", offs, ok, err)
	}
	for seq := uint64(1); seq <= 3; seq++ {
		if err := p.PutIntentV3(id, Intent{Seq: seq, Subject: "book:x", Kind: "modify"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.ClearIntentV3(id, 2); err != nil {
		t.Fatal(err)
	}
	ins, err := p.ListIntentsV3(id)
	if err != nil || len(ins) != 2 || ins[0].Seq != 1 || ins[1].Seq != 3 {
		t.Fatalf("intents = %+v %v", ins, err)
	}
	if err := p.SaveCheckpointV3(id, Checkpoint{SchemaVersion: 1, Phase: "p", State: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	ck, err := p.GetCheckpointV3(id)
	if err != nil || ck == nil || ck.Phase != "p" || mustRun(t, p, id).LastCheckpointAt == nil {
		t.Fatalf("checkpoint = %+v %v", ck, err)
	}
	child := newRunID()
	if err := p.InsertRunV3(RunRecord{ID: child, DefID: "test.small", ParentID: id}); err != nil {
		t.Fatal(err)
	}
	kids, err := p.ListChildRunsV3(id)
	if err != nil || len(kids) != 1 || kids[0].ID != child {
		t.Fatalf("children = %+v %v", kids, err)
	}
	transition(t, p, child, state.EvDispatch, nil)
	transition(t, p, child, state.EvReturnNil, nil)
	done, complete, err := p.ListDoneRunsSinceV3(time.Now().Add(-time.Hour), 10)
	if err != nil || !complete || len(done) != 1 || done[0].ID != child {
		t.Fatalf("done window = %d runs complete=%v %v", len(done), complete, err)
	}
	if _, complete, _ := p.ListDoneRunsSinceV3(time.Time{}, 0); !complete {
		t.Fatal("a window under the default limit reported incomplete")
	}
	evts, err := p.ListRunEventsV3(child, 2)
	if err != nil || len(evts) != 2 || evts[1].Event != string(state.EvReturnNil) || evts[1].Seq != 3 {
		t.Fatalf("last two events = %+v %v", evts, err)
	}
}

// TestOpv3KeysAllInRegisteredFamilies writes every opv3: family the store
// writes and requires each key to land in a registered CHILD family (not the
// opv3: parent, and never "(unregistered)"), which is what db-census-exact
// reports per family.
func TestOpv3KeysAllInRegisteredFamilies(t *testing.T) {
	p := newTimelineStore(t)
	parent := insertV3(t, p, "test.census", "")
	child := newRunID()
	if err := p.InsertRunV3(RunRecord{ID: child, DefID: "test.census", ParentID: parent}); err != nil {
		t.Fatal(err)
	}
	transition(t, p, child, state.EvDispatch, nil)
	_ = p.PutSnapshotPageV3(child, 0, []string{"a"})
	_ = p.PutChunkOffsetsV3(child, []int32{0})
	_ = p.PutIntentV3(child, Intent{Seq: 1})
	// child is dispatched, not terminal: a terminal transition deletes its
	// row markers, and a refused put would only show up as "not exercised".
	if err := p.PutRowMarkV3(child, RowMarkV3{RowID: "r"}); err != nil {
		t.Fatal(err)
	}
	_ = p.AppendLogV3(child, LogLine{Message: "m"})
	if err := p.CommitLedgerV3(child, LedgerBatch{Ledger: Ledger{ChunkCount: 1},
		ItemRows: []ItemOutcome{{Key: "k", Outcome: "skipped:dup"}}, Marks: map[string][]string{"applied": {"k"}}}); err != nil {
		t.Fatal(err)
	}
	done := insertV3(t, p, "test.census", "")
	transition(t, p, done, state.EvDispatch, nil)
	transition(t, p, done, state.EvReturnNil, nil)
	_ = p.pebbleSetJSON([]byte(opv3SchedPrefix+"test.census"), SchedState{DefID: "test.census"})
	_ = p.pebbleSetJSON([]byte(opv3DefPrefix+"test.gone"), DefSnapshot{DefID: "test.gone"})
	ranges := keyFamilyRanges(keyFamilies)
	seen := map[string]bool{}
	for _, k := range timelineKeys(t, p, opv3Prefix) {
		fam := familyForKey(ranges, []byte(k))
		if fam == unregisteredFamily || fam == opv3Prefix {
			t.Errorf("key %s is counted as %q, not in a registered opv3 child family", k, fam)
		}
		seen[fam] = true
	}
	for _, f := range keyFamilies {
		if strings.HasPrefix(f.Prefix, opv3Prefix) && f.Prefix != opv3Prefix && !seen[f.Prefix] {
			t.Errorf("registered family %s was not exercised", f.Prefix)
		}
	}
}

// TestInterruptedLedgerRunMirrorsAsDroppedAndStays: a v2 binary cannot resume
// a chunk ledger, so an interrupted ledger run mirrors as interrupted_dropped;
// a later v2 write on that row (a progress tick) must not read that as a
// drop.
func TestInterruptedLedgerRunMirrorsAsDroppedAndStays(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.ledger-mirror", "")
	transition(t, p, id, state.EvDispatch, nil)
	if err := p.CommitLedgerV3(id, LedgerBatch{Ledger: Ledger{ChunkCount: 4, Done: []byte{1}}}); err != nil {
		t.Fatal(err)
	}
	transition(t, p, id, state.EvBootSweep, nil)
	row, err := p.GetOperationV2(id)
	if err != nil || row == nil || row.Status != "interrupted_dropped" {
		t.Fatalf("interrupted ledger run mirrors as %+v (%v), want interrupted_dropped", row, err)
	}
	if err := p.UpdateOpProgressV2(id, 1, 4, "tick"); err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, p, id); r.State != state.V3Interrupted {
		t.Fatalf("after a v2 progress tick the ledger run is %s, want interrupted", r.State)
	}
	if l, _ := p.GetLedgerV3(id); l == nil {
		t.Fatal("the ledger of a non-terminal run was deleted")
	}
}

// TestMirrorOffSwitch is the D25 switch 05-PR16 flips: with the mirror off,
// native v3 writes leave opv2: alone, while the v2 façade (which IS the v2
// API) keeps writing both families.
func TestMirrorOffSwitch(t *testing.T) {
	p := newTimelineStore(t)
	facade := newRunID()
	if err := p.InsertOperationV2(OperationV2Row{ID: facade, DefID: "test.mirror-off", Status: "queued", QueuedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	p.setOpsV2MirrorEnabled(false)
	t.Cleanup(func() { p.setOpsV2MirrorEnabled(true) })
	if p.opsV2MirrorEnabled() {
		t.Fatal("the switch did not turn the mirror off")
	}

	native := insertV3(t, p, "test.mirror-off", "")
	transition(t, p, native, state.EvDispatch, nil)
	for _, k := range timelineKeys(t, p, "opv2:") {
		if strings.Contains(k, native) {
			t.Errorf("mirror off, but a native write left %s", k)
		}
	}

	// The façade still writes both families.
	now := time.Now().UTC()
	if err := p.UpdateOperationV2Status(facade, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, p, facade); r.State != state.V3Running {
		t.Fatalf("façade write with the mirror off: v3 %s, want running", r.State)
	}

	// A native discard leaves the v2 row alone with the mirror off.
	transition(t, p, facade, state.EvReturnNil, nil)
	before := v3Digest(t, p, "opv2:")
	if deleted, err := p.DeleteRunV3(facade, []state.V3{state.V3Succeeded}); err != nil || !deleted {
		t.Fatalf("DeleteRunV3: %v %v", deleted, err)
	}
	if v3Digest(t, p, "opv2:") != before {
		t.Fatal("mirror off, but DeleteRunV3 changed opv2:")
	}
	if r, _ := p.GetRunV3(facade); r != nil {
		t.Fatal("the v3 record survived its discard")
	}
}

// TestPerRunWritersRefuseMissingOrTerminalRun: the per-run writers 05-PR5
// gives callers (snapshot pages, chunk offsets, intents) refuse a run that
// does not exist or is terminal, and write nothing. A write that landed after
// a terminal transition or a discard would re-create per-run keys that no
// pass ever cleans up. Log lines are accepted on a terminal run
// (TestLogAcceptedAfterTerminalRefusedAfterDiscard).
func TestPerRunWritersRefuseMissingOrTerminalRun(t *testing.T) {
	p := newTimelineStore(t)
	done := insertV3(t, p, "test.closed", "")
	transition(t, p, done, state.EvDispatch, nil)
	transition(t, p, done, state.EvReturnNil, nil)
	open := insertV3(t, p, "test.open", "")
	transition(t, p, open, state.EvDispatch, nil)
	writers := map[string]func(id string) error{
		"PutSnapshotPageV3": func(id string) error { return p.PutSnapshotPageV3(id, 0, []string{"a"}) },
		"PutChunkOffsetsV3": func(id string) error { return p.PutChunkOffsetsV3(id, []int32{0}) },
		"PutIntentV3":       func(id string) error { return p.PutIntentV3(id, Intent{Seq: 1, Subject: "book:x"}) },
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			before := v3Digest(t, p, "opv3:")
			if err := write(newRunID()); !errors.Is(err, ErrRunNotFoundV3) {
				t.Errorf("missing run: err = %v, want ErrRunNotFoundV3", err)
			}
			if err := write(done); !errors.Is(err, ErrStaleTransition) {
				t.Errorf("terminal run: err = %v, want ErrStaleTransition", err)
			}
			if v3Digest(t, p, "opv3:") != before {
				t.Fatal("a refused write changed opv3:")
			}
			if err := write(open); err != nil {
				t.Fatalf("running run: %v", err)
			}
		})
	}
}

// TestLogAcceptedAfterTerminalRefusedAfterDiscard: a run's log outlives its
// terminal transition, so a line written as the run finishes is kept; a
// discard deletes the log, and a line after it is refused and writes nothing,
// so no log key is left that nothing deletes.
func TestLogAcceptedAfterTerminalRefusedAfterDiscard(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.log-closed", "")
	transition(t, p, id, state.EvDispatch, nil)
	if err := p.AppendLogV3(id, LogLine{Message: "running"}); err != nil {
		t.Fatal(err)
	}
	transition(t, p, id, state.EvReturnNil, nil)
	if err := p.AppendLogV3(id, LogLine{Message: "finished"}); err != nil {
		t.Fatalf("terminal run: %v", err)
	}
	lines, err := p.ListLogV3(id, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[1].Message != "finished" {
		t.Fatalf("log after terminal = %+v", lines)
	}
	if deleted, err := p.DeleteRunV3(id, reachableV3States()); err != nil || !deleted {
		t.Fatalf("DeleteRunV3: %v %v", deleted, err)
	}
	if n := countPrefix(t, p, opv3LogPrefix+id+":", ""); n != 0 {
		t.Fatalf("%d log keys left after discard", n)
	}
	before := v3Digest(t, p, "opv3:")
	if err := p.AppendLogV3(id, LogLine{Message: "late"}); !errors.Is(err, ErrRunNotFoundV3) {
		t.Fatalf("discarded run: err = %v, want ErrRunNotFoundV3", err)
	}
	if v3Digest(t, p, "opv3:") != before {
		t.Fatal("a refused log line changed opv3:")
	}
}

// TestV2WriteLeavesLedgerProgress: once the chunk ledger owns a run's
// progress (CommitLedgerV3 set Chunks), a v2 write -- the reporter's lazy
// flush, the terminal status write -- does not move Done, Total or Current
// back to the v2 row's stale numbers.
func TestV2WriteLeavesLedgerProgress(t *testing.T) {
	p := newTimelineStore(t)
	id := newRunID()
	now := time.Now().UTC()
	if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: "test.ledger-owned", Status: "queued", Priority: 1, QueuedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateOperationV2Status(id, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	total := int64(700)
	if err := p.CommitLedgerV3(id, LedgerBatch{Ledger: Ledger{ChunkSize: 256, ChunkCount: 3, Done: []byte{7}},
		Progress: ProgressSnapshot{Done: 700, Total: &total, Current: "last item", Chunks: &ChunkProgress{Done: 3, Total: 3, Size: 256}}}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateOpProgressV2(id, 0, 0, "stale"); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateOperationV2Status(id, "completed", &now, &now, nil); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, p, id)
	if r.Progress.Done != 700 || r.Progress.Total == nil || *r.Progress.Total != 700 || r.Progress.Current != "last item" {
		t.Fatalf("progress after v2 writes = %+v", r.Progress)
	}
}

// TestCommitLedgerKeepsPhase: a ledger commit carries the runner's numbers
// but no phase; the phase the run set (through the v2 row) survives it.
func TestCommitLedgerKeepsPhase(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.phase", "")
	transition(t, p, id, state.EvDispatch, nil)
	phase := "apply"
	if err := p.UpdateOpPhaseV2(id, &phase); err != nil {
		t.Fatal(err)
	}
	if err := p.CommitLedgerV3(id, LedgerBatch{Ledger: Ledger{ChunkCount: 1},
		Progress: ProgressSnapshot{Done: 3, Chunks: &ChunkProgress{Done: 1, Total: 1, Size: 3}}}); err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, p, id); r.Progress.Phase != "apply" || r.Progress.Done != 3 {
		t.Fatalf("progress after a ledger commit = %+v", r.Progress)
	}
}

// TestMarksStagedWithLedger: marks ride in the ledger commit as their own
// opv3:mark: keys (written once, never in the ledger value), list back by
// name in key order with item keys that hold ':' intact, are deleted with
// the ledger at the terminal transition, and a bad name is refused.
func TestMarksStagedWithLedger(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.marks", "")
	transition(t, p, id, state.EvDispatch, nil)
	commit := func(marks map[string][]string) error {
		return p.CommitLedgerV3(id, LedgerBatch{Ledger: Ledger{ChunkSize: 1, ChunkCount: 3, Done: []byte{0}}, Marks: marks})
	}
	if err := commit(map[string][]string{"applied": {"b:2", "a:1"}}); err != nil {
		t.Fatal(err)
	}
	if err := commit(map[string][]string{"applied": {"c"}, "held": {"x"}}); err != nil {
		t.Fatal(err)
	}
	got, err := p.ListMarksV3(id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["applied"], []string{"a:1", "b:2", "c"}) || !slices.Equal(got["held"], []string{"x"}) {
		t.Fatalf("marks = %v", got)
	}
	if led, _ := p.GetLedgerV3(id); led == nil {
		t.Fatal("no ledger")
	}
	before := v3Digest(t, p, "opv3:")
	if err := commit(map[string][]string{"bad\x00name": {"k"}}); err == nil {
		t.Fatal("a mark name holding a NUL was accepted")
	}
	if v3Digest(t, p, "opv3:") != before {
		t.Fatal("a refused commit changed opv3:")
	}
	transition(t, p, id, state.EvReturnNil, nil)
	if got, _ := p.ListMarksV3(id); len(got) != 0 {
		t.Fatalf("marks left after the terminal transition: %v", got)
	}
	// N-2: the first commit of a ledger started over drops the old ledger's
	// marks in its batch, and keeps the ones it stages itself.
	again := insertV3(t, p, "test.marks", "")
	transition(t, p, again, state.EvDispatch, nil)
	led := Ledger{ChunkSize: 1, ChunkCount: 1, Done: []byte{0}}
	if err := p.CommitLedgerV3(again, LedgerBatch{Ledger: led, Marks: map[string][]string{"applied": {"old1", "old2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := p.CommitLedgerV3(again, LedgerBatch{Ledger: led, ResetMarks: true, Marks: map[string][]string{"applied": {"new"}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.ListMarksV3(again); !slices.Equal(got["applied"], []string{"new"}) || len(got) != 1 {
		t.Fatalf("marks after a reset = %v, want only the new one", got)
	}
	// A discard of a run that never ended deletes its marks too.
	open := insertV3(t, p, "test.marks", "")
	transition(t, p, open, state.EvDispatch, nil)
	if err := p.CommitLedgerV3(open, LedgerBatch{Ledger: Ledger{ChunkSize: 1, ChunkCount: 1, Done: []byte{0}},
		Marks: map[string][]string{"applied": {"z"}}}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := p.DeleteRunV3(open, reachableV3States()); err != nil || !deleted {
		t.Fatalf("DeleteRunV3: %v %v", deleted, err)
	}
	if got, _ := p.ListMarksV3(open); len(got) != 0 {
		t.Fatalf("marks left after a discard: %v", got)
	}
}

func TestSchedStateV3RoundTrip(t *testing.T) {
	p := newTimelineStore(t)
	if got, err := p.GetSchedV3("test.sched"); err != nil || got != nil {
		t.Fatalf("empty GetSchedV3 = %+v, %v; want nil, nil", got, err)
	}
	last := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	if err := p.PutSchedV3(SchedState{DefID: "test.sched", LastFire: &last, LastRunID: "r1", Missed: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetSchedV3("test.sched")
	if err != nil || got == nil || got.LastRunID != "r1" || got.Missed != 2 || !got.LastFire.Equal(last) {
		t.Fatalf("GetSchedV3 = %+v, %v", got, err)
	}
	if err := p.PutSchedV3(SchedState{}); err == nil {
		t.Error("PutSchedV3 accepted an empty def id")
	}
}

// ── TestListRunsSkipIDAndUndecodable ────────────────────────────────────────

// TestListRunsSkipIDAndUndecodable: RunQuery.SkipID leaves a run out by its
// index key before its record is read, so a corrupt record of a skipped run
// does not fail the listing, is not counted and never reaches Keep; with
// OnUndecodable a corrupt record the caller did not skip is reported and left
// out instead of failing the page; with neither, the first corrupt record fails
// the listing (an exact census still needs that).
func TestListRunsSkipIDAndUndecodable(t *testing.T) {
	p := newTimelineStore(t)
	var hidden []string
	for range 5 {
		insertV3(t, p, "test.show", "")
	}
	for range 3 {
		hidden = append(hidden, insertV3(t, p, "test.hide", ""))
	}
	corrupt := hidden[1]
	if err := p.db.Set(opv3RunKey(corrupt), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	skip := func(id string) bool { return slices.Contains(hidden, id) }

	if _, err := p.ListRunsV3(RunQuery{Limit: 50, Keep: func(*RunRecord) bool { return true }}); !errors.Is(err, errOpsV3Undecodable) {
		t.Fatalf("a corrupt record with no handler must fail the listing, got %v", err)
	}

	kept := 0
	page, err := p.ListRunsV3(RunQuery{
		Limit: 3, SkipID: skip,
		Keep: func(r *RunRecord) bool { kept++; return true },
	})
	if err != nil {
		t.Fatalf("a corrupt record of a skipped run failed the listing: %v", err)
	}
	if page.Total != 5 || len(page.Runs) != 3 || page.Next == "" {
		t.Errorf("Total/page/next = %d/%d/%q, want 5/3/non-empty", page.Total, len(page.Runs), page.Next)
	}
	if kept != 5 {
		t.Errorf("Keep judged %d records, want the 5 shown ones only", kept)
	}
	for _, r := range page.Runs {
		if slices.Contains(hidden, r.ID) {
			t.Errorf("skipped run %s was returned", r.ID)
		}
	}

	// The key-only path (no Keep) also leaves skipped keys out of Total.
	page, err = p.ListRunsV3(RunQuery{Limit: 50, SkipID: skip})
	if err != nil || page.Total != 5 || len(page.Runs) != 5 {
		t.Errorf("no-Keep listing with SkipID = %d total, %d runs, %v; want 5, 5", page.Total, len(page.Runs), err)
	}

	// Not skipped, but a handler: the corrupt run is reported and left out; the
	// others are listed and counted.
	var reported []string
	page, err = p.ListRunsV3(RunQuery{
		Limit: 50, Keep: func(*RunRecord) bool { return true },
		OnUndecodable: func(id string, err error) {
			if !errors.Is(err, errOpsV3Undecodable) {
				t.Errorf("handler got %v", err)
			}
			reported = append(reported, id)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reported, []string{corrupt}) || page.Total != 7 || len(page.Runs) != 7 {
		t.Errorf("reported %v, total %d, runs %d; want [%s], 7, 7", reported, page.Total, len(page.Runs), corrupt)
	}
}
