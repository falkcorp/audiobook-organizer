// file: core/audit/restore_hook_test.go
// version: 1.0.0
// guid: 2d5e89bb-8fe9-451d-b08f-dbe1d525d934
// last-edited: 2026-11-11

package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// batEvent is a Batman-fixture event: actor and target vary by i.
func batEvent(i int) Event {
	actors := []string{"bruce_wayne", "alfred_pennyworth", "commissioner_gordon"}
	targets := []string{"robin", "batgirl", "nightwing"}
	a := bruce()
	a.UserID = actors[i%len(actors)]
	return Event{
		Action:  []string{"core.role.update", "core.user.disable", "core.session.revoke"}[i%3],
		Outcome: Success, Actor: a,
		Target: entity.Ref{Module: "core", Kind: "role", ID: targets[i%len(targets)]},
	}
}

// writeN records n batman events into s.
func writeN(t *testing.T, s *Store, n int) {
	t.Helper()
	for i := range n {
		if err := s.Record(context.Background(), batEvent(i)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
}

// dumpKS is every row of ks keyed by its relative key.
func dumpKS(t *testing.T, ks storage.Keyspace) map[string]string {
	t.Helper()
	out := map[string]string{}
	for kv, err := range ks.Scan(context.Background(), storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		out[string(kv.Key)] = string(kv.Value)
	}
	return out
}

// restoreScenario is a pre-swap store holding 6 records and a live (archive)
// store holding the first 3 under a lease of 4, so the unissued seq 4 is the
// seq a hookless restore would reissue over copied record 4.
type restoreScenario struct {
	pre, live       *env
	preS, liveS     *Store
	mark            uint64
	rc              journal.RestoreContext
	preRows, before map[string]string
}

func newRestoreScenario(t *testing.T) *restoreScenario {
	t.Helper()
	pre := newEnv(t)
	live := bootEnv(t, vfs.NewMem(), t.TempDir()+"/live.pebble", 4)
	sc := &restoreScenario{pre: pre, live: live, preS: newStoreOn(t, pre, Options{}, Deps{}), liveS: newStoreOn(t, live, Options{}, Deps{}), mark: 3}
	writeN(t, sc.preS, 6)
	writeN(t, sc.liveS, 3)
	sc.rc = journal.RestoreContext{
		ArchiveID: "arch_gotham_01", Kind: journal.KindArchive, Boundary: 41, AuditHighWater: sc.mark,
		Previous: func(o storage.Owner) (storage.Reader, error) {
			if o != "core/audit" {
				return nil, fmt.Errorf("unexpected owner %s", o)
			}
			return pre.ks, nil
		},
		Journal: []restorejournal.Record{{Step: "staged", Note: "archive", Actor: "alfred_pennyworth"}},
		Counts:  map[string]int{"api_keys_revoked": 2, "passwords_blanked": 1},
	}
	sc.preRows = dumpKS(t, pre.ks)
	sc.before = dumpKS(t, live.ks)
	return sc
}

func (sc *restoreScenario) run(t *testing.T, restoreSeq uint64) map[string]int {
	t.Helper()
	counts, err := RestoreHook(RestoreDeps{Store: sc.liveS, RestoreSeq: restoreSeq})(context.Background(), sc.rc)
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	return counts
}

func restoreRecords(t *testing.T, s *Store) []Record {
	t.Helper()
	return all(t, s, Filter{ActionPrefix: "core.restore."})
}

// TestAudit_RestoreCopiesForwardRecords (05-rbac 12): every pre-swap record
// above the high-water mark lands in the live store byte-identical with its
// index rows, and the stage, revoke and apply records are appended.
func TestAudit_RestoreCopiesForwardRecords(t *testing.T) {
	sc := newRestoreScenario(t)
	counts := sc.run(t, 7)
	if counts[CountRecordsCopied] != 3 || counts[CountRecordsAppended] != 3 {
		t.Fatalf("counts = %v, want copied 3 appended 3", counts)
	}
	live := dumpKS(t, sc.live.ks)
	nCopiedRows := 0
	for k, v := range sc.preRows {
		seq, err := seqOfAnyKey(k)
		if err != nil {
			continue // the sequence's own row
		}
		if seq <= sc.mark {
			continue
		}
		nCopiedRows++
		if got, ok := live[k]; !ok || got != v {
			t.Errorf("row %q: present=%v identical=%v", k, ok, got == v)
		}
	}
	if nCopiedRows != 3*(1+3) { // record + action + actor + target index each
		t.Errorf("copied rows = %d, want 12", nCopiedRows)
	}
	for _, f := range []Filter{
		{ActorUserID: "alfred_pennyworth"}, {ActionPrefix: "core.user.disable"},
		{Target: entity.Ref{Module: "core", Kind: "role", ID: "nightwing"}},
	} {
		got := seqs(all(t, sc.liveS, f))
		want := seqs(all(t, sc.preS, f))
		for _, w := range want {
			if w > sc.mark && !slices.Contains(got, w) {
				t.Errorf("query %+v misses copied seq %d (got %v)", f, w, got)
			}
		}
	}
	rr := restoreRecords(t, sc.liveS)
	if got := len(rr); got != 3 {
		t.Fatalf("restore records = %v, want stage, revoke, apply", actions(rr))
	}
	for _, r := range rr {
		if r.Target.ID != "7" || r.Outcome != Success {
			t.Errorf("%s: target %v outcome %v", r.Action, r.Target, r.Outcome)
		}
		raw := fmt.Sprint(r.Details)
		if strings.Contains(raw, "redacted") {
			t.Errorf("%s details were redacted: %s", r.Action, raw)
		}
	}
}

// TestAudit_CopyForwardThenWriteNoOverwrite (05-rbac 12, SEC2-12): the
// sequence is advanced before the copy, so later records never reuse a copied
// seq and nothing copied changes.
func TestAudit_CopyForwardThenWriteNoOverwrite(t *testing.T) {
	// Control: without the hook the next seq is 4, a copied record's seq.
	ctl := bootEnv(t, vfs.NewMem(), t.TempDir()+"/ctl.pebble", 4)
	cs := newStoreOn(t, ctl, Options{}, Deps{})
	writeN(t, cs, 4)
	if got := seqs(all(t, cs, Filter{})); got[0] != 4 {
		t.Fatalf("control: fourth hookless seq = %d, want 4 (the reuse danger)", got[0])
	}

	sc := newRestoreScenario(t)
	sc.run(t, 7)
	afterHook := dumpKS(t, sc.live.ks)
	copied := map[string]string{}
	for k, v := range sc.preRows {
		if seq, err := seqOfAnyKey(k); err == nil && seq > sc.mark {
			copied[k] = v
		}
	}
	// AdvancePast(6) ran: a fresh allocation lands at 7 or later... and the
	// restore records already took such seqs.
	for _, r := range restoreRecords(t, sc.liveS) {
		if r.Seq <= 6 {
			t.Errorf("%s got seq %d, within the copied range", r.Action, r.Seq)
		}
	}
	writeN(t, sc.liveS, 5)
	final := dumpKS(t, sc.live.ks)
	for k, v := range copied {
		if final[k] != v {
			t.Errorf("copied row %q changed after later writes", k)
		}
	}
	for k, v := range afterHook {
		if !strings.HasPrefix(k, string(recPrefix())) && !strings.HasPrefix(k, "idx") {
			continue // the sequence's own high-water row moves
		}
		if final[k] != v {
			t.Errorf("row %q changed after later writes", k)
		}
	}
	top := seqs(all(t, sc.liveS, Filter{}))[:5]
	for _, s := range top {
		if s <= 6 {
			t.Errorf("new record seq %d not above every copied seq", s)
		}
	}
}

// TestAudit_RestoreHookIdempotent: a second run (a crash before applied)
// copies the same rows, adds no second restore record and changes nothing.
func TestAudit_RestoreHookIdempotent(t *testing.T) {
	sc := newRestoreScenario(t)
	first := sc.run(t, 7)
	rows := dumpKS(t, sc.live.ks)
	second := sc.run(t, 7)
	if second[CountRecordsCopied] != first[CountRecordsCopied] {
		t.Errorf("copied %d then %d", first[CountRecordsCopied], second[CountRecordsCopied])
	}
	if second[CountRecordsAppended] != 0 {
		t.Errorf("rerun appended %d records", second[CountRecordsAppended])
	}
	if got := len(restoreRecords(t, sc.liveS)); got != 3 {
		t.Errorf("restore records after rerun = %d, want 3", got)
	}
	for k, v := range dumpKS(t, sc.live.ks) {
		if rows[k] != v && !bytes.HasPrefix([]byte(k), []byte("seq")) {
			t.Errorf("row %q changed on rerun", k)
		}
	}
	// A different restore gets its own records.
	if third := sc.run(t, 9); third[CountRecordsAppended] != 3 {
		t.Errorf("new restore appended %d, want 3", third[CountRecordsAppended])
	}
}

// TestAudit_RestorePreviousNilAppendsOnly: no live store before the restore.
func TestAudit_RestorePreviousNilAppendsOnly(t *testing.T) {
	sc := newRestoreScenario(t)
	sc.rc.Previous = nil
	counts := sc.run(t, 5)
	if counts[CountRecordsCopied] != 0 || counts[CountRecordsAppended] != 3 {
		t.Fatalf("counts = %v", counts)
	}
	live := dumpKS(t, sc.live.ks)
	for k, v := range sc.preRows {
		if seq, err := seqOfAnyKey(k); err == nil && seq > sc.mark {
			if strings.HasPrefix(k, string(recPrefix())) && live[k] == v {
				t.Errorf("row %q copied without a Previous", k)
			}
		}
	}
	if len(restoreRecords(t, sc.liveS)) != 3 {
		t.Error("restore records missing")
	}
}

// TestAudit_RestorePreviousErrorAppendsNothing: a failing reader fails the hook.
func TestAudit_RestorePreviousErrorAppendsNothing(t *testing.T) {
	sc := newRestoreScenario(t)
	boom := errors.New("pre-swap store unreadable")
	sc.rc.Previous = func(storage.Owner) (storage.Reader, error) { return nil, boom }
	_, err := RestoreHook(RestoreDeps{Store: sc.liveS, RestoreSeq: 7})(context.Background(), sc.rc)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the reader error", err)
	}
	if got := dumpKS(t, sc.live.ks); len(got) != len(sc.before) {
		t.Errorf("store changed: %d rows, was %d", len(got), len(sc.before))
	}
	if n := len(restoreRecords(t, sc.liveS)); n != 0 {
		t.Errorf("%d restore records appended after a failure", n)
	}
}

// TestAudit_RestoreHighWaterAtTopCopiesNothing: nothing above the mark.
func TestAudit_RestoreHighWaterAtTopCopiesNothing(t *testing.T) {
	for _, mark := range []uint64{6, 99} {
		sc := newRestoreScenario(t)
		sc.rc.AuditHighWater = mark
		counts := sc.run(t, 7)
		if counts[CountRecordsCopied] != 0 {
			t.Errorf("mark %d: copied %d", mark, counts[CountRecordsCopied])
		}
		if got := len(all(t, sc.liveS, Filter{})); got != 3+3 {
			t.Errorf("mark %d: live holds %d records, want 3 own + 3 restore", mark, got)
		}
	}
}

// seqOfAnyKey is the seq of a rec/ or idx/ key, an error for any other row.
func seqOfAnyKey(k string) (uint64, error) {
	if !strings.HasPrefix(k, string(recPrefix())) && !strings.HasPrefix(k, "idx") {
		return 0, errors.New("not an audit row")
	}
	return seqOfIndexKey([]byte(k))
}
