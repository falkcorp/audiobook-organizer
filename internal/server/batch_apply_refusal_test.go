// file: internal/server/batch_apply_refusal_test.go
// version: 1.0.0
// guid: 25c4fe3e-8309-4f4c-ae42-148ff9105880
// last-edited: 2026-10-30
//
// metadata.batch-apply-cached remembers structural gate refusals and does not
// re-offer a book whose inputs are unchanged. The tests drive the same three
// calls the op's per-book closure makes -- Skip, applyCachedCandidateForBookTimed,
// Settle -- against a real in-memory key-value store.

package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// memKV is an in-memory database.RawKVStore.
type memKV struct {
	m       map[string][]byte
	getErr  error
	setErr  error
	deletes int
}

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) SetRaw(key string, v []byte) error {
	if k.setErr != nil {
		return k.setErr
	}
	k.m[key] = append([]byte(nil), v...)
	return nil
}
func (k *memKV) GetRaw(key string) ([]byte, error) {
	if k.getErr != nil {
		return nil, k.getErr
	}
	return k.m[key], nil
}
func (k *memKV) DeleteRaw(key string) error { k.deletes++; delete(k.m, key); return nil }
func (k *memKV) DeleteRawBatch(keys []string) error {
	for _, key := range keys {
		delete(k.m, key)
	}
	return nil
}
func (k *memKV) CountPrefix(p string) (int64, error) {
	pairs, _ := k.ScanPrefix(p)
	return int64(len(pairs)), nil
}
func (k *memKV) ScanPrefix(p string) ([]database.KVPair, error) {
	var out []database.KVPair
	for key, v := range k.m {
		if strings.HasPrefix(key, p) {
			out = append(out, database.KVPair{Key: key, Value: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (k *memKV) ScanPrefixPage(p, after string, limit int) ([]database.KVPair, string, error) {
	all, _ := k.ScanPrefix(p)
	var page []database.KVPair
	for _, kv := range all {
		if kv.Key > after && len(page) < limit {
			page = append(page, kv)
		}
	}
	next := ""
	if len(page) == limit && page[len(page)-1].Key != all[len(all)-1].Key {
		next = page[len(page)-1].Key
	}
	return page, next, nil
}

// refusalBooks is fakeBooks with a file set.
type refusalBooks struct {
	fakeBooks
	files []database.BookFile
}

func (f *refusalBooks) GetBookFiles(string) ([]database.BookFile, error) { return f.files, nil }

// refusalFixture is a book the gate refuses for a structural reason: a
// Google Books candidate (review-only source), which nothing about the book
// can change.
func refusalFixture(t *testing.T) (*refusalBooks, *fakeApplySvc, metafetch.MetadataCandidate) {
	t.Helper()
	dur := 3600
	books := &refusalBooks{
		fakeBooks: fakeBooks{"b1": {ID: "b1", Title: "The Long Halloween", FilePath: "/lib/Jeph Loeb/The Long Halloween/The Long Halloween.m4b", Duration: &dur}},
		files:     []database.BookFile{{ID: "f1", BookID: "b1", FilePath: "/lib/Jeph Loeb/The Long Halloween/The Long Halloween.m4b", FileSize: 1000, Duration: 3600}},
	}
	cand := metafetch.MetadataCandidate{Title: "The Long Halloween", Author: "Jeph Loeb", Source: "Google Books", ISBN13: "9780000000001", Score: 0.95}
	return books, &fakeApplySvc{candidates: candidateJSON(t, cand)}, cand
}

// onePass is the op's per-book closure minus the write-back gate and counters.
func onePass(t *testing.T, l *applyRefusalLedger, svc *fakeApplySvc, books bookReader, id string, pin *metafetch.CandidatePin) (out applyOutcome, skipped bool) {
	t.Helper()
	_, had, skip := l.Skip(svc, books, id, pin)
	if skip {
		return applyOutcome{}, true
	}
	out = applyCachedCandidateForBookTimed(context.Background(), svc, books, id, false, nil, metafetch.NewApplyPhaseTimings(), nil, pin, "")
	l.Settle(id, out, had, pin)
	return out, false
}

func newTestLedger(kv *memKV) *applyRefusalLedger {
	l := newApplyRefusalLedger(kv)
	l.build = "build-A"
	return l
}

func TestBatchApplyRefusal_UnchangedStructuralRefusalIsSkippedOnSecondRun(t *testing.T) {
	books, svc, _ := refusalFixture(t)
	kv := newMemKV()
	l := newTestLedger(kv)

	out, skipped := onePass(t, l, svc, books, "b1", nil)
	if skipped || out.Applied || out.Reason != applySkipGateBlocked || out.Gate == nil || !applygate.StructuralRefusal(out.Gate.Reason) {
		t.Fatalf("first run: skipped=%v outcome %+v; want a structural gate refusal", skipped, out)
	}
	if _, ok := kv.m[applyRefusalKey("b1")]; !ok {
		t.Fatalf("first run wrote no apply_refusal: record; keys %v", kv.m)
	}

	if _, skipped = onePass(t, l, svc, books, "b1", nil); !skipped {
		t.Fatal("second run with identical inputs re-evaluated the book; it must be skipped")
	}
	if len(svc.appliedIDs) != 0 {
		t.Fatalf("applied %v", svc.appliedIDs)
	}
}

// Anything the gate reads changing clears the suppression with no bookkeeping.
func TestBatchApplyRefusal_ChangedInputsReofferTheBook(t *testing.T) {
	cases := map[string]func(*testing.T, *refusalBooks, *fakeApplySvc, *metafetch.MetadataCandidate){
		"a new candidate": func(t *testing.T, _ *refusalBooks, svc *fakeApplySvc, c *metafetch.MetadataCandidate) {
			c.Score = 0.97
			svc.candidates = candidateJSON(t, *c)
		},
		"the file set": func(_ *testing.T, b *refusalBooks, _ *fakeApplySvc, _ *metafetch.MetadataCandidate) {
			b.files = append(b.files, database.BookFile{ID: "f2", BookID: "b1", FilePath: "/lib/Jeph Loeb/The Long Halloween/The Long Halloween 2.m4b", FileSize: 1000, Duration: 3600})
		},
		"a file duration backfilled": func(_ *testing.T, b *refusalBooks, _ *fakeApplySvc, _ *metafetch.MetadataCandidate) {
			b.files = []database.BookFile{{ID: "f1", BookID: "b1", FilePath: b.files[0].FilePath, FileSize: 1000, Duration: 7200}}
		},
		"the transcribed title": func(_ *testing.T, b *refusalBooks, _ *fakeApplySvc, _ *metafetch.MetadataCandidate) {
			tt := "Dark Victory"
			b.fakeBooks["b1"].TranscribedTitle = &tt
		},
		"the narrator": func(_ *testing.T, b *refusalBooks, _ *fakeApplySvc, _ *metafetch.MetadataCandidate) {
			n := "Richard Ferrone"
			b.fakeBooks["b1"].Narrator = &n
		},
		"the path": func(_ *testing.T, b *refusalBooks, _ *fakeApplySvc, _ *metafetch.MetadataCandidate) {
			b.fakeBooks["b1"].FilePath = "/lib/Jeph Loeb/Batman The Long Halloween/The Long Halloween.m4b"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			books, svc, cand := refusalFixture(t)
			l := newTestLedger(newMemKV())
			if _, skipped := onePass(t, l, svc, books, "b1", nil); skipped {
				t.Fatal("first run skipped")
			}
			if _, skipped := onePass(t, l, svc, books, "b1", nil); !skipped {
				t.Fatal("control: unchanged inputs must be skipped")
			}
			mutate(t, books, svc, &cand)
			if _, skipped := onePass(t, l, svc, books, "b1", nil); skipped {
				t.Fatalf("%s changed but the book stayed suppressed", name)
			}
		})
	}
}

// A new build may have relaxed a gate rule: nothing in the inputs would show it.
func TestBatchApplyRefusal_NewBuildReevaluates(t *testing.T) {
	books, svc, _ := refusalFixture(t)
	kv := newMemKV()
	l := newTestLedger(kv)
	onePass(t, l, svc, books, "b1", nil)
	if _, skipped := onePass(t, l, svc, books, "b1", nil); !skipped {
		t.Fatal("control: same build must skip")
	}
	l.build = "build-B"
	if _, skipped := onePass(t, l, svc, books, "b1", nil); skipped {
		t.Fatal("a record written by another build suppressed the book")
	}
	// Re-evaluation rewrote the record for the new build.
	if _, skipped := onePass(t, l, svc, books, "b1", nil); !skipped {
		t.Fatal("the new build's own record must suppress")
	}
}

// Refusals that are not verdicts about the book are never remembered.
func TestBatchApplyRefusal_TransientRefusalIsNotSuppressed(t *testing.T) {
	t.Run("owner rejection check failed", func(t *testing.T) {
		books, svc, cand := refusalFixture(t)
		kv := newMemKV()
		l := newTestLedger(kv)
		out := applyOutcome{Reason: applySkipGateBlocked, Gate: &applygate.Verdict{Reason: applygate.ReasonOwnerRejectionCheckFailed},
			Judged: &applyRefusalInputs{RawCandidate: svc.candidates[0], Book: books.fakeBooks["b1"]}}
		l.Settle("b1", out, false, nil)
		if len(kv.m) != 0 {
			t.Fatalf("a transient gate refusal was recorded: %v", kv.m)
		}
		_ = cand
	})
	t.Run("authors unreadable and apply failed", func(t *testing.T) {
		_, _, _ = refusalFixture(t)
		kv := newMemKV()
		l := newTestLedger(kv)
		for _, reason := range []string{applySkipAuthorsUnreadable, applySkipApplyFailed, applySkipNoCachedCandidates, applySkipFileWorkWouldFail, applySkipDecodeFailed} {
			l.Settle("b1", applyOutcome{Reason: reason, Judged: &applyRefusalInputs{}}, false, nil)
		}
		if len(kv.m) != 0 {
			t.Fatalf("a non-gate skip was recorded: %v", kv.m)
		}
	})
	t.Run("a stored non-structural reason never suppresses", func(t *testing.T) {
		books, svc, _ := refusalFixture(t)
		kv := newMemKV()
		l := newTestLedger(kv)
		in, ok := gatherApplyRefusalInputs(svc, books, "b1")
		if !ok {
			t.Fatal("gather failed")
		}
		// Hand-written record: right fingerprint and build, reason is a
		// partial_book (depends on sibling books, not this one).
		l.Settle("b1", applyOutcome{Reason: applySkipGateBlocked, Gate: &applygate.Verdict{Reason: applygate.ReasonPartialBook}, Judged: &in}, false, nil)
		if len(kv.m) != 0 {
			t.Fatalf("partial_book was recorded: %v", kv.m)
		}
		rec := applyRefusalRecord{Reason: applygate.ReasonPartialBook, Fingerprint: in.fingerprint(), FingerprintVersion: applyRefusalFingerprintVersion, Build: l.build, RecordedAt: time.Now()}
		if l.unchanged(rec, svc, books, "b1") {
			t.Fatal("a non-structural stored reason suppressed the book")
		}
	})
	t.Run("unreadable record or store fails open", func(t *testing.T) {
		books, svc, _ := refusalFixture(t)
		kv := newMemKV()
		l := newTestLedger(kv)
		onePass(t, l, svc, books, "b1", nil)
		kv.getErr = fmt.Errorf("pebble: closed")
		if _, skipped := onePass(t, l, svc, books, "b1", nil); skipped {
			t.Fatal("a failing record read suppressed the book")
		}
		kv.getErr = nil
		kv.m[applyRefusalKey("b1")] = []byte("not json")
		if _, skipped := onePass(t, l, svc, books, "b1", nil); skipped {
			t.Fatal("an undecodable record suppressed the book")
		}
	})
}

// An owner-review pin is the owner choosing to apply over the gate: a stored
// refusal for the same inputs must not swallow the click.
func TestBatchApplyRefusal_PinnedBookBypassesTheMemory(t *testing.T) {
	books, svc, cand := refusalFixture(t)
	// The shape TestOwnerReview_MatchingPinAppliesAndRecordsOverride pins as
	// lifted by a row review: no file rows and a ten-hour stored runtime.
	tenHours := 36000
	books.files, books.fakeBooks["b1"].Duration = nil, &tenHours
	l := newTestLedger(newMemKV())
	onePass(t, l, svc, books, "b1", nil)
	if _, skipped := onePass(t, l, svc, books, "b1", nil); !skipped {
		t.Fatal("control: the unpinned book must be suppressed")
	}
	out, skipped := onePass(t, l, svc, books, "b1", rowPin(cand))
	if skipped {
		t.Fatal("a row-pinned book was suppressed by the refusal memory")
	}
	if !out.Applied || !out.OwnerReviewed {
		t.Fatalf("pinned outcome %+v, want applied as owner-reviewed", out)
	}
}

// A successful apply drops the record, paying a delete only when one existed.
func TestBatchApplyRefusal_AppliedBookDropsItsRecord(t *testing.T) {
	books, svc, _ := refusalFixture(t)
	kv := newMemKV()
	l := newTestLedger(kv)
	onePass(t, l, svc, books, "b1", nil)
	// The review-only source is now the owner's problem solved: a different,
	// passing candidate arrives, which changes the fingerprint.
	svc.candidates = oneCandidate(t)
	out, skipped := onePass(t, l, svc, fakeBooks{}, "b1", nil)
	if skipped || !out.Applied {
		t.Fatalf("skipped=%v outcome %+v, want applied", skipped, out)
	}
	if len(kv.m) != 0 || kv.deletes != 1 {
		t.Fatalf("records %v deletes %d, want the one stale record deleted", kv.m, kv.deletes)
	}

	// No record, no delete: the hot path of an ordinary apply pays no write.
	kv2 := newMemKV()
	l2 := newTestLedger(kv2)
	if out, _ = onePass(t, l2, &fakeApplySvc{candidates: oneCandidate(t)}, fakeBooks{}, "b2", nil); !out.Applied {
		t.Fatalf("outcome %+v", out)
	}
	if kv2.deletes != 0 {
		t.Fatalf("an applied book with no record issued %d deletes", kv2.deletes)
	}
}

func TestBatchApplyRefusal_NilLedgerRemembersNothing(t *testing.T) {
	var l *applyRefusalLedger
	books, svc, _ := refusalFixture(t)
	if _, _, skip := l.Skip(svc, books, "b1", nil); skip {
		t.Fatal("nil ledger skipped")
	}
	l.Settle("b1", applyOutcome{}, false, nil)
	if newApplyRefusalLedger(nil) != nil {
		t.Fatal("a nil store must give a nil ledger")
	}
}

func TestResetApplyRefusals(t *testing.T) {
	kv := newMemKV()
	for i := 0; i < 1203; i++ { // more than one scan page
		kv.m[applyRefusalKey(fmt.Sprintf("book%04d", i))] = []byte("{}")
	}
	kv.m["metadata_cache:keep"] = []byte("x")
	ctx := context.Background()

	if n, err := resetApplyRefusals(ctx, kv, nil, true); err != nil || n != 1203 || len(kv.m) != 1204 {
		t.Fatalf("dry run: n=%d err=%v remaining=%d", n, err, len(kv.m))
	}
	if n, err := resetApplyRefusals(ctx, kv, []string{"book0000", "nope", " "}, false); err != nil || n != 1 || len(kv.m) != 1203 {
		t.Fatalf("by id: n=%d err=%v remaining=%d", n, err, len(kv.m))
	}
	if n, err := resetApplyRefusals(ctx, kv, nil, false); err != nil || n != 1202 {
		t.Fatalf("reset all: n=%d err=%v", n, err)
	}
	if _, ok := kv.m["metadata_cache:keep"]; len(kv.m) != 1 || !ok {
		t.Fatalf("reset touched another key family: %d keys left", len(kv.m))
	}
}
