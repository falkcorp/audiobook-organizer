// file: core/internal/outboxstore/dead_counts_test.go
// version: 1.1.0
// guid: 3f6d2a9e-5b1c-4e7a-9d84-1c0b7e52a6f3
// last-edited: 2026-11-15

package outboxstore_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

func deadAt(part int) outboxstore.Dead {
	return outboxstore.Dead{Msg: outboxstore.Msg{ID: "01J0000000000000000000BAT1", Key: "wayne-manor", Payload: []byte(`{}`)}, Part: part, Token: "error", Attempts: 3, At: time.Now()}
}

// stageDeads stages the letters of seqs on (consumer, topic, part) in ONE batch,
// the way a round of an unordered consumer does.
func stageDeads(t testing.TB, s *outboxstore.Store, consumer string, part int, seqs ...uint64) {
	t.Helper()
	ctx := context.Background()
	b := s.OpenBatch()
	defer b.Close()
	for _, seq := range seqs {
		must(t, s.StageDead(ctx, b, consumer, topic, seq, deadAt(part)))
	}
	must(t, b.Commit(ctx, storage.Sync))
}

func deleteDead(t testing.TB, s *outboxstore.Store, consumer string, seq uint64) bool {
	t.Helper()
	ctx := context.Background()
	b := s.OpenBatch()
	defer b.Close()
	found, err := s.StageDeleteDead(ctx, b, consumer, topic, seq)
	must(t, err)
	must(t, b.Commit(ctx, storage.Sync))
	return found
}

func totals(t testing.TB, s *outboxstore.Store) map[string]int {
	t.Helper()
	got, err := s.DeadTotals(context.Background())
	must(t, err)
	return got.ByConsumer
}

func putRaw(t testing.TB, ks storage.Keyspace, key, raw []byte) {
	t.Helper()
	ctx := context.Background()
	b := ks.NewIndexedBatch()
	defer b.Close()
	b.Bind(ks)
	if _, err := b.GetForUpdate(ctx, ks, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	must(t, b.In(ks).Set(key, raw))
	must(t, b.Commit(ctx, storage.Sync))
}

func mustCount(t testing.TB, n int) []byte {
	t.Helper()
	raw, err := outboxstore.EncodeDeadCountForTest(n)
	must(t, err)
	return raw
}

func countRows(t testing.TB, ks storage.Keyspace, root []byte) int {
	t.Helper()
	n := 0
	for _, err := range ks.Scan(context.Background(), storage.ScanOptions{Start: root, End: keys.PrefixEnd(root), KeysOnly: true}) {
		must(t, err)
		n++
	}
	return n
}

// The counters move in the batch of the letter they count, never on their own:
// two letters of one batch both count, a letter staged again over itself does
// not count twice, a delete lowers the shard the letter named, a missing letter
// is not an error and counts nothing, and a shard that reaches zero has no row.
func TestDeadCounts_FollowEveryLetterWrite(t *testing.T) {
	s, ks := openStore(t)
	stageDeads(t, s, consumer, 1, 3, 5, 9)
	stageDeads(t, s, consumer, 0, 11)
	stageDeads(t, s, "wayne.vault.guard", 1, 1)
	if got := totals(t, s); got[consumer] != 4 || got["wayne.vault.guard"] != 1 || len(got) != 2 {
		t.Fatalf("DeadTotals = %v, want %s 4 and wayne.vault.guard 1", got, consumer)
	}
	if n := countRows(t, ks, outboxstore.DeadCountRoot()); n != 3 {
		t.Fatalf("%d counter rows, want 3 (two shards of %s, one of the guard)", n, consumer)
	}

	stageDeads(t, s, consumer, 1, 5) // the same key again: replaced, not added
	if got := totals(t, s); got[consumer] != 4 {
		t.Fatalf("a letter staged over itself changed the count to %d, want 4", got[consumer])
	}

	if !deleteDead(t, s, consumer, 9) || totals(t, s)[consumer] != 3 {
		t.Fatalf("after deleting one letter, DeadTotals = %v, want %s 3", totals(t, s), consumer)
	}
	if deleteDead(t, s, consumer, 9) || totals(t, s)[consumer] != 3 {
		t.Fatalf("deleting a letter that is gone changed DeadTotals to %v", totals(t, s))
	}
	deleteDead(t, s, consumer, 11)
	if n := countRows(t, ks, outboxstore.DeadCountRoot()); n != 2 {
		t.Fatalf("%d counter rows after the last letter of a shard went, want 2: a zero count has no row", n)
	}
	deleteDead(t, s, consumer, 3)
	deleteDead(t, s, consumer, 5)
	deleteDead(t, s, "wayne.vault.guard", 1)
	if got := totals(t, s); len(got) != 0 {
		t.Fatalf("DeadTotals = %v with no letters left", got)
	}
	if n := countRows(t, ks, outboxstore.DeadCountRoot()); n != 0 {
		t.Fatalf("%d counter rows with no letters left, want 0", n)
	}
	rep, err := s.RepairDeadCounts(context.Background())
	must(t, err)
	if len(rep.Fixes) != 0 {
		t.Fatalf("a repair after only counted writes changed %+v, want nothing", rep.Fixes)
	}
}

// The counter is guarded like the letter: two batches that both bump one pair's
// counter from the same value cannot both commit, or the count would lose one
// of the two letters.
func TestDeadCounts_TwoBatchesOnOnePairCannotBothCommit(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	a, b := s.OpenBatch(), s.OpenBatch()
	defer a.Close()
	defer b.Close()
	must(t, s.StageDead(ctx, a, consumer, topic, 1, deadAt(1)))
	must(t, s.StageDead(ctx, b, consumer, topic, 2, deadAt(1)))
	must(t, a.Commit(ctx, storage.Sync))
	if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("the second commit = %v, want ErrConflict", err)
	}
	if got := totals(t, s)[consumer]; got != 1 {
		t.Fatalf("DeadTotals = %d, want 1 (only the first batch landed)", got)
	}
}

// A store written before the counters existed holds letters and no counters:
// RepairDeadCounts builds them, says so, and a second run changes nothing. It
// also rewrites a counter that is wrong, one that does not decode, and one whose
// letters are gone, and books a letter that does not decode under UnreadShard,
// where it counts as a letter of its consumer.
func TestRepairDeadCounts_BuildsAndCorrectsCounters(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	letter := func(c string, seq uint64, part int) {
		raw, err := outboxstore.EncodeDeadForTest(deadAt(part))
		must(t, err)
		putRaw(t, ks, outboxstore.DeadKey(c, topic, seq), raw)
	}
	letter(consumer, 3, 1)
	letter(consumer, 5, 1)
	letter(consumer, 7, 0)
	letter("wayne.vault.guard", 2, 4)
	putRaw(t, ks, outboxstore.DeadKey(consumer, topic, 8), []byte(`j{"v":2}`)) // a row of a version this build cannot read
	if got := totals(t, s); len(got) != 0 {
		t.Fatalf("DeadTotals = %v before any counter exists", got)
	}

	rep, err := s.RepairDeadCounts(ctx)
	must(t, err)
	fixes := rep.Fixes
	if len(fixes) != 4 {
		t.Fatalf("first repair changed %d counters (%+v), want 4: %s 01 and 00, the guard's 04, and %s's unreadable shard", len(fixes), fixes, consumer, consumer)
	}
	for _, f := range fixes {
		if !f.Absent || f.Want == 0 {
			t.Fatalf("fix %+v: a counter built from letters that predate it should be Absent with a count", f)
		}
	}
	if got := totals(t, s); got[consumer] != 4 || got["wayne.vault.guard"] != 1 {
		t.Fatalf("DeadTotals after repair = %v, want %s 4 (3 readable, 1 unreadable) and the guard 1", got, consumer)
	}
	if rep, err = s.RepairDeadCounts(ctx); err != nil || len(rep.Fixes) != 0 {
		t.Fatalf("a second repair = %+v, %v; want nothing to change", rep, err)
	}

	putRaw(t, ks, outboxstore.DeadCountKey(consumer, topic, 1), []byte(`not a counter`))
	putRaw(t, ks, outboxstore.DeadCountKey("wayne.vault.guard", topic, 4), mustCount(t, 9))
	putRaw(t, ks, outboxstore.DeadCountKey("wayne.vault.joker", topic, 2), mustCount(t, 2)) // letters long gone
	rep, err = s.RepairDeadCounts(ctx)
	must(t, err)
	fixes = rep.Fixes
	byKey := map[string]outboxstore.DeadCountFix{}
	for _, f := range fixes {
		byKey[f.Consumer+"/"+f.Shard] = f
	}
	if f := byKey[consumer+"/01"]; len(fixes) != 3 || !f.Unreadable || f.Want != 2 {
		t.Fatalf("an unreadable counter: fixes = %+v", fixes)
	}
	if f := byKey["wayne.vault.guard/04"]; f.Had != 9 || f.Want != 1 || f.Absent || f.Unreadable {
		t.Fatalf("a wrong counter: %+v, want Had 9 Want 1", f)
	}
	if f := byKey["wayne.vault.joker/02"]; f.Had != 2 || f.Want != 0 {
		t.Fatalf("a counter with no letters: %+v, want Had 2 Want 0", f)
	}
	if got := totals(t, s); got[consumer] != 4 || got["wayne.vault.guard"] != 1 || got["wayne.vault.joker"] != 0 {
		t.Fatalf("DeadTotals after the second repair = %v", got)
	}
	if n := countRows(t, ks, outboxstore.DeadCountRoot()); n != 4 {
		t.Fatalf("%d counter rows, want 4: the joker's stale counter must be deleted", n)
	}
}

// A repair that read the counters, and then lost a pair to a writer, reads
// again instead of overwriting the count that moved.
func TestRepairDeadCounts_NeverOverwritesACountThatMoved(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	raw, err := outboxstore.EncodeDeadForTest(deadAt(1))
	must(t, err)
	putRaw(t, ks, outboxstore.DeadKey(consumer, topic, 3), raw) // a letter with no counter
	var once sync.Once
	outboxstore.SetRepairHook(s, func() {
		// Between the repair's snapshot and its commit a real writer lands a
		// letter on the same pair, counter and all.
		once.Do(func() { stageDeads(t, s, consumer, 1, 4) })
	})
	rep, err := s.RepairDeadCounts(ctx)
	must(t, err)
	fixes := rep.Fixes
	outboxstore.SetRepairHook(s, nil)
	if got := totals(t, s)[consumer]; got != 2 {
		t.Fatalf("DeadTotals = %d, want 2 (letters 3 and 4); the repair wrote a count from before letter 4", got)
	}
	if len(fixes) != 1 || fixes[0].Want != 2 {
		t.Fatalf("the repair that committed re-read after losing the race: fixes = %+v, want one fix to 2", fixes)
	}
}

// Dead-lettering a backlog costs a number of key reads linear in its size. The
// relay recounts after every dead letter; counting by scanning the letters
// made that quadratic (a backlog of ten thousand read about fifty million keys
// and held the recount lock for the whole time), counting the per-pair counters
// reads at most one row per partition each time. This stages two thousand
// letters (a run with ten thousand read 159,880 keys, 16.0 per recount, against
// the 50 million of the scan), one batch each across sixteen partitions the way
// an ordered consumer's workers do, and recounts after each, as the relay does.
func TestDeadCounts_RecountCostDoesNotGrowWithTheLetters(t *testing.T) {
	const (
		letters    = 2_000
		partitions = 16
	)
	s, _ := openStore(t)
	ctx := context.Background()
	start := time.Now()
	before := s.KeysScanned()
	for i := range letters {
		stageDeads(t, s, consumer, i%partitions, uint64(i+1))
		if _, err := s.DeadTotals(ctx); err != nil {
			t.Fatal(err)
		}
	}
	read := s.KeysScanned() - before
	t.Logf("%d letters over %d partitions: %d keys read by %d recounts (%.1f per recount), %v", letters, partitions, read, letters, float64(read)/letters, time.Since(start).Round(time.Millisecond))
	if limit := int64(letters * partitions); read > limit {
		t.Fatalf("%d recounts read %d keys, want at most %d (one row per partition each): a recount that reads the letters is quadratic", letters, read, limit)
	}
	if got := totals(t, s)[consumer]; got != letters {
		t.Fatalf("DeadTotals = %d, want %d", got, letters)
	}

	// The scan the counters replaced, for contrast: it reads every letter, every time.
	const sample = 300
	before = s.KeysScanned()
	for range sample {
		if _, _, err := s.DeadCounts(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if per := (s.KeysScanned() - before) / sample; per != letters {
		t.Fatalf("a scan of the letters read %d keys, want all %d: the counter does not see the cost it is meant to show", per, letters)
	}
}

// letterOnly plants the letter of (consumer, seq) on part without its counter,
// the way a store from before the counters existed holds it.
func letterOnly(t testing.TB, ks storage.Keyspace, c string, seq uint64, part int) {
	t.Helper()
	raw, err := outboxstore.EncodeDeadForTest(deadAt(part))
	must(t, err)
	putRaw(t, ks, outboxstore.DeadKey(c, topic, seq), raw)
}

// A delete against a counter that is absent (the letter predates the counters,
// so the relay or an operator removes it before the build ran) leaves the
// counter where it was: nothing. A repair that read the store just before that
// delete must still lose its race to it, or it writes a count that still
// includes the letter. The delete therefore leaves a row of its own, which the
// repair's byte comparison sees; the next repair deletes that zero row.
//
// Before this change the delete clamped at zero and deleted an absent key, so
// the repair's precondition (absent, as read) passed and it wrote 2 for one
// letter.
func TestRepairDeadCounts_SeesADeleteAgainstAnAbsentCounter(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	letterOnly(t, ks, consumer, 3, 1)
	letterOnly(t, ks, consumer, 5, 1)
	var once sync.Once
	outboxstore.SetRepairHook(s, func() {
		once.Do(func() {
			if !deleteDead(t, s, consumer, 3) {
				t.Error("the letter to delete was not found")
			}
		})
	})
	rep, err := s.RepairDeadCounts(ctx)
	must(t, err)
	outboxstore.SetRepairHook(s, nil)
	if got := totals(t, s)[consumer]; got != 1 {
		t.Fatalf("DeadTotals = %d, want 1: the repair wrote the count of a letter that was deleted while it ran", got)
	}
	if len(rep.Fixes) != 1 || rep.Fixes[0].Want != 1 {
		t.Fatalf("fixes = %+v, want one fix to 1 after re-reading", rep.Fixes)
	}

	// A zero row, the trace of a delete against nothing, is itself corrected.
	putRaw(t, ks, outboxstore.DeadCountKey("wayne.vault.guard", topic, 2), mustCount(t, 0))
	rep, err = s.RepairDeadCounts(ctx)
	must(t, err)
	if len(rep.Fixes) != 1 || rep.Fixes[0].Consumer != "wayne.vault.guard" || rep.Fixes[0].Want != 0 {
		t.Fatalf("a zero row: fixes = %+v, want the guard's counter deleted", rep.Fixes)
	}
	if n := countRows(t, ks, outboxstore.DeadCountRoot()); n != 1 {
		t.Fatalf("%d counter rows, want 1", n)
	}
}

// Letters written before the counters are built are safe: the write bumps an
// absent counter to 1, the repair then reads the letters and corrects it, and
// a second run changes nothing. This is why the relay does not refuse to write
// a dead letter while the build is failing.
func TestRepairDeadCounts_CorrectsACounterStartedByALetterWrittenBeforeTheBuild(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	letterOnly(t, ks, consumer, 3, 1)
	letterOnly(t, ks, consumer, 5, 1)
	stageDeads(t, s, consumer, 1, 7) // the relay's dead letter, before any build
	if got := totals(t, s)[consumer]; got != 1 {
		t.Fatalf("DeadTotals = %d before the build, want 1 (only the new letter is counted)", got)
	}
	rep, err := s.RepairDeadCounts(ctx)
	must(t, err)
	if len(rep.Fixes) != 1 || rep.Fixes[0].Had != 1 || rep.Fixes[0].Want != 3 || rep.Fixes[0].Absent {
		t.Fatalf("fixes = %+v, want the counter corrected from 1 to 3", rep.Fixes)
	}
	if got := totals(t, s)[consumer]; got != 3 {
		t.Fatalf("DeadTotals = %d after the build, want 3", got)
	}
	if rep, err = s.RepairDeadCounts(ctx); err != nil || len(rep.Fixes) != 0 {
		t.Fatalf("a second repair = %+v, %v; want nothing", rep, err)
	}
}

// Keys that belong to no letter or counter do not stop the repair: it skips and
// counts them, repairs everything else, and leaves them where they are.
func TestRepairDeadCounts_SkipsAndCountsKeysThatAreNoLettersOrCounters(t *testing.T) {
	s, ks := openStore(t)
	ctx := context.Background()
	letterOnly(t, ks, consumer, 3, 1)
	putRaw(t, ks, append(outboxstore.DeadRoot(), 'j'), []byte(`x`))
	putRaw(t, ks, append(outboxstore.DeadCountRoot(), 'j'), []byte(`x`))
	rep, err := s.RepairDeadCounts(ctx)
	if err != nil {
		t.Fatalf("RepairDeadCounts = %v: a key that is no letter's must not abort the repair", err)
	}
	if rep.Unparseable != 2 || len(rep.Fixes) != 1 {
		t.Fatalf("repair = %+v, want 2 unparseable keys named and one counter built", rep)
	}
	if got := totals(t, s)[consumer]; got != 1 {
		t.Fatalf("DeadTotals = %d, want 1", got)
	}
	if n := countRows(t, ks, outboxstore.DeadRoot()); n != 2 {
		t.Fatalf("%d rows under dead/, want 2: the stray key is left in place", n)
	}
	letters, bad, err := s.DeadCounts(ctx)
	must(t, err)
	if bad != 1 || letters[consumer] != 1 {
		t.Fatalf("DeadCounts = %v, %d unparseable; want 1 letter and 1 stray key", letters, bad)
	}
	dt, err := s.DeadTotals(ctx)
	must(t, err)
	if dt.Unparseable != 1 {
		t.Fatalf("DeadTotals reports %d stray counter keys, want 1", dt.Unparseable)
	}
}

// A counter that does not decode is named, not an error: DeadTotals leaves it
// out of the sums and the repair rewrites it.
func TestDeadTotals_NamesAnUnreadableCounterInsteadOfFailing(t *testing.T) {
	s, ks := openStore(t)
	stageDeads(t, s, consumer, 1, 3)
	putRaw(t, ks, outboxstore.DeadCountKey(consumer, topic, 1), []byte(`not a counter`))
	dt, err := s.DeadTotals(context.Background())
	if err != nil {
		t.Fatalf("DeadTotals = %v, want the counter named instead", err)
	}
	if len(dt.Unreadable) != 1 || dt.Unreadable[0].Consumer != consumer || dt.Unreadable[0].Shard != "01" {
		t.Fatalf("Unreadable = %+v, want %s shard 01", dt.Unreadable, consumer)
	}
	if dt.ByConsumer[consumer] != 0 {
		t.Fatalf("an unreadable counter was summed as %d", dt.ByConsumer[consumer])
	}
}
