// file: internal/server/author_merge_standdown_test.go
// version: 1.0.0
// guid: 7c1d4e82-3b59-4a06-9f2d-e85a10b6c437
// last-edited: 2026-10-30

package server

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
)

// entities.author-merge and ai.author-scan-apply hold the library-scan stand-down
// for their whole merge loop, as dedup.series-merge does (MergeSeriesOptions.Scan):
// a concurrent library.scan rewrites book_authors on the rows the loop repoints.
// These tests drive both registered ops against a recording gate.

// recordingStandDown is a scan stand-down that records every call in order and
// can refuse the acquire or lose the lease.
type recordingStandDown struct {
	mu         sync.Mutex
	events     []string
	acquireErr error
	// loseOnRenew makes the Nth RenewScanStandDown (1-based) and every later one
	// report a lost lease. Zero never loses it.
	loseOnRenew int
	renews      int
	// onAcquire and onRelease run at that moment, to look at the store then.
	onAcquire func()
	onRelease func()
}

func (g *recordingStandDown) AcquireScanStandDown(_ context.Context, holder, reason string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.acquireErr != nil {
		g.events = append(g.events, "acquire-refused")
		return nil, g.acquireErr
	}
	g.events = append(g.events, "acquire:"+holder+":"+reason)
	if g.onAcquire != nil {
		g.onAcquire()
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.events = append(g.events, "release")
		if g.onRelease != nil {
			g.onRelease()
		}
	}, nil
}

func (g *recordingStandDown) RenewScanStandDown(string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.renews++
	if g.loseOnRenew > 0 && g.renews >= g.loseOnRenew {
		g.events = append(g.events, "renew-lost")
		return false
	}
	g.events = append(g.events, "renew")
	return true
}

func (g *recordingStandDown) count(event string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, e := range g.events {
		if e == event {
			n++
		}
	}
	return n
}

func (g *recordingStandDown) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

// authorGone reports whether id no longer exists as its own author.
func authorGone(t *testing.T, st *database.PebbleStore, id int) bool {
	t.Helper()
	authors, err := st.GetAllAuthors()
	require.NoError(t, err)
	for _, a := range authors {
		if a.ID == id {
			return false
		}
	}
	return true
}

func runGatedAuthorMerge(t *testing.T, s *Server, gate *recordingStandDown, rep interface{ OpID() string }, p entities.AuthorMergeOpParams) error {
	t.Helper()
	s.scanStandDownGateOverride = gate
	def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	switch r := rep.(type) {
	case *opReporter:
		return def.Run(context.Background(), raw, r)
	case *relinkOpReporter:
		return def.Run(context.Background(), raw, r)
	}
	t.Fatalf("unsupported reporter %T", rep)
	return nil
}

// The gate is taken before the first write and released after the last one, with
// the lease renewed once per author in between.
func TestAuthorMergeOp_HoldsTheScanStandDownAroundTheMerge(t *testing.T) {
	st := newRelinkOpStore(t)
	keep, err := st.CreateAuthor("Selina Kyle")
	require.NoError(t, err)
	a, err := st.CreateAuthor("Selina Kyle Variant")
	require.NoError(t, err)
	b, err := st.CreateAuthor("Kyle Selina")
	require.NoError(t, err)
	bookA := mkRelinkOpBook(t, st, "stand-down-a", a.ID, false)
	bookB := mkRelinkOpBook(t, st, "stand-down-b", b.ID, false)

	var atAcquire, atRelease struct{ aGone, bGone bool }
	gate := &recordingStandDown{}
	gate.onAcquire = func() { atAcquire.aGone, atAcquire.bGone = authorGone(t, st, a.ID), authorGone(t, st, b.ID) }
	gate.onRelease = func() { atRelease.aGone, atRelease.bGone = authorGone(t, st, a.ID), authorGone(t, st, b.ID) }

	err = runGatedAuthorMerge(t, newRelinkOpServer(st), gate, &relinkOpReporter{}, entities.AuthorMergeOpParams{
		KeepID: keep.ID, MergeIDs: []int{a.ID, b.ID}, KeepName: keep.Name,
	})

	require.NoError(t, err)
	require.Equal(t, []string{"acquire:op-relink-test:author-merge apply", "renew", "renew", "release"}, gate.snapshot())
	require.False(t, atAcquire.aGone || atAcquire.bGone, "the gate must be taken before any author is merged")
	require.True(t, atRelease.aGone && atRelease.bGone, "the gate must be held until the last author is merged")
	require.Equal(t, []int{keep.ID}, junctionIDs(t, st, bookA))
	require.Equal(t, []int{keep.ID}, junctionIDs(t, st, bookB))
}

// A run that fails part way (one id does not exist) still releases the gate, once.
func TestAuthorMergeOp_ReleasesTheStandDownOnAnErrorPath(t *testing.T) {
	st := newRelinkOpStore(t)
	keep, err := st.CreateAuthor("Harvey Dent")
	require.NoError(t, err)
	a, err := st.CreateAuthor("Harvey Dent Variant")
	require.NoError(t, err)
	mkRelinkOpBook(t, st, "stand-down-err", a.ID, false)
	const missing = 999999
	gate := &recordingStandDown{}

	err = runGatedAuthorMerge(t, newRelinkOpServer(st), gate, &relinkOpReporter{}, entities.AuthorMergeOpParams{
		KeepID: keep.ID, MergeIDs: []int{a.ID, missing}, KeepName: keep.Name,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), AuthorMergeFailedIDsPrefix+strconv.Itoa(missing)+"]")
	require.Equal(t, 1, gate.count("release"), "events: %v", gate.snapshot())
	require.Equal(t, "release", gate.snapshot()[len(gate.snapshot())-1])
}

// A scan that will not park refuses the run before it writes anything.
func TestAuthorMergeOp_RefusesWhenTheScanWillNotStandDown(t *testing.T) {
	st := newRelinkOpStore(t)
	keep, err := st.CreateAuthor("Oswald Cobblepot")
	require.NoError(t, err)
	a, err := st.CreateAuthor("Oswald Cobblepot Variant")
	require.NoError(t, err)
	book := mkRelinkOpBook(t, st, "stand-down-refused", a.ID, false)
	gate := &recordingStandDown{acquireErr: errors.New("scan stand-down: scan did not park within 5m0s")}

	err = runGatedAuthorMerge(t, newRelinkOpServer(st), gate, &relinkOpReporter{}, entities.AuthorMergeOpParams{
		KeepID: keep.ID, MergeIDs: []int{a.ID}, KeepName: keep.Name,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to apply while a library.scan is running or queued")
	require.Contains(t, err.Error(), "did not park")
	require.Equal(t, []string{"acquire-refused"}, gate.snapshot(), "a refused gate has nothing to release")
	require.Equal(t, []int{a.ID}, junctionIDs(t, st, book), "nothing was moved")
	require.False(t, authorGone(t, st, a.ID))
}

// The gate is keyed by the op id, so a run with none is refused, not run ungated.
func TestAuthorMergeOp_RefusesToApplyWithoutAnOperationID(t *testing.T) {
	st := newRelinkOpStore(t)
	keep, err := st.CreateAuthor("Edward Nygma")
	require.NoError(t, err)
	a, err := st.CreateAuthor("Edward Nygma Variant")
	require.NoError(t, err)
	book := mkRelinkOpBook(t, st, "stand-down-noid", a.ID, false)
	gate := &recordingStandDown{}

	err = runGatedAuthorMerge(t, newRelinkOpServer(st), gate, &opReporter{id: ""}, entities.AuthorMergeOpParams{
		KeepID: keep.ID, MergeIDs: []int{a.ID}, KeepName: keep.Name,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "without an operation id")
	require.Empty(t, gate.snapshot(), "the gate is not even asked")
	require.Equal(t, []int{a.ID}, junctionIDs(t, st, book))
}

// A lost lease stops the loop before the next author is written, reports the ids
// it never reached in the lane's fixed form, invalidates the caches (the first
// author did merge) and still releases.
func TestAuthorMergeOp_AbortsOnALostStandDownLease(t *testing.T) {
	st := newRelinkOpStore(t)
	keep, err := st.CreateAuthor("Pamela Isley")
	require.NoError(t, err)
	a, err := st.CreateAuthor("Pamela Isley Variant")
	require.NoError(t, err)
	b, err := st.CreateAuthor("Isley Pamela")
	require.NoError(t, err)
	bookA := mkRelinkOpBook(t, st, "lease-a", a.ID, false)
	bookB := mkRelinkOpBook(t, st, "lease-b", b.ID, false)
	s := newRelinkOpServer(st)
	s.dedupCache.Set("author-duplicates", gin.H{})
	gate := &recordingStandDown{loseOnRenew: 2} // author a renews, author b does not

	err = runGatedAuthorMerge(t, s, gate, &relinkOpReporter{}, entities.AuthorMergeOpParams{
		KeepID: keep.ID, MergeIDs: []int{a.ID, b.ID}, KeepName: keep.Name,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), AuthorMergeFailedIDsPrefix+strconv.Itoa(b.ID)+"]")
	require.Contains(t, err.Error(), "scan stand-down lease lost")
	require.Equal(t, []int{keep.ID}, junctionIDs(t, st, bookA), "the first author merged before the lease went")
	require.Equal(t, []int{b.ID}, junctionIDs(t, st, bookB), "nothing is written past a lost lease")
	require.False(t, authorGone(t, st, b.ID))
	_, cached := s.dedupCache.Get("author-duplicates")
	require.False(t, cached, "the merged author is gone, so the cached list is stale")
	require.Equal(t, []string{"acquire:op-relink-test:author-merge apply", "renew", "renew-lost", "release"}, gate.snapshot())
}

// ---- ai.author-scan-apply ----------------------------------------------------

func (f *applyFixture) gated(gate *recordingStandDown) { f.s.scanStandDownGateOverride = gate }

// ONE hold spans every result of the run (a hold keyed by the op id cannot be
// re-taken after its first release), and it outlives the last result's mark.
func TestAIScanApplyOp_HoldsOneScanStandDownAcrossAllResults(t *testing.T) {
	f := newApplyFixture(t)
	keepA, fromA := f.author("Barbara Gordon"), f.author("Barbara Gordon Variant")
	keepB, fromB := f.author("Dick Grayson"), f.author("Dick Grayson Variant")
	mkRelinkOpBook(t, f.st, "ai-hold-a", fromA.ID, false)
	mkRelinkOpBook(t, f.st, "ai-hold-b", fromB.ID, false)
	r1 := f.result(database.ScanSuggestion{CanonicalName: keepA.Name, KeepID: keepA.ID, AuthorIDs: []int{keepA.ID, fromA.ID}, Source: "groups_scan"})
	r2 := f.result(database.ScanSuggestion{CanonicalName: keepB.Name, KeepID: keepB.ID, AuthorIDs: []int{keepB.ID, fromB.ID}, Source: "groups_scan"})
	var heldWhenFirstMerged, goneAtRelease bool
	gate := &recordingStandDown{}
	gate.onAcquire = func() { heldWhenFirstMerged = authorGone(t, f.st, fromA.ID) }
	gate.onRelease = func() { goneAtRelease = authorGone(t, f.st, fromA.ID) && authorGone(t, f.st, fromB.ID) }
	f.gated(gate)

	_, err := f.run(live(), r1, r2)

	require.NoError(t, err)
	require.Equal(t, []string{"acquire:op-apply-test:ai.author-scan-apply", "renew", "renew", "release"}, gate.snapshot())
	require.False(t, heldWhenFirstMerged, "taken before the first merge")
	require.True(t, goneAtRelease, "held until the last result merged")
	require.True(t, f.applied(r1))
	require.True(t, f.applied(r2))
}

// A failed result (a missing keep id) still releases the one hold.
func TestAIScanApplyOp_ReleasesTheStandDownOnAnErrorPath(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Tim Drake"), f.author("Tim Drake Variant")
	mkRelinkOpBook(t, f.st, "ai-hold-err", from.ID, false)
	ok := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})
	gate := &recordingStandDown{}
	f.gated(gate)

	_, err := f.run(live(), ok, 987654) // 987654 is not a result of the scan

	require.Error(t, err)
	require.Equal(t, 1, gate.count("release"), "events: %v", gate.snapshot())
	require.Equal(t, "release", gate.snapshot()[len(gate.snapshot())-1])
}

func TestAIScanApplyOp_RefusesWhenTheScanWillNotStandDown(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Alfred Pennyworth"), f.author("Alfred Pennyworth Variant")
	book := mkRelinkOpBook(t, f.st, "ai-hold-refused", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})
	gate := &recordingStandDown{acquireErr: errors.New("scan stand-down: scan did not park within 5m0s")}
	f.gated(gate)

	_, err := f.run(live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to apply while a library.scan is running or queued")
	require.Equal(t, []int{from.ID}, junctionIDs(t, f.st, book), "nothing was merged")
	require.False(t, f.applied(id))
	got, gerr := f.scanStore.GetScan(f.scan.ID)
	require.NoError(t, gerr)
	require.Empty(t, got.ApplyingOpID, "a refused run leaves no claim on the scan")
}

func TestAIScanApplyOp_RefusesToApplyWithoutAnOperationID(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Lucius Fox"), f.author("Lucius Fox Variant")
	book := mkRelinkOpBook(t, f.st, "ai-hold-noid", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})
	gate := &recordingStandDown{}
	f.gated(gate)

	_, err := f.runWith(&applyReporter{id: ""}, live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "without an operation id")
	require.Empty(t, gate.snapshot())
	require.Equal(t, []int{from.ID}, junctionIDs(t, f.st, book))
}

// A preview writes nothing, so it must not park the production scanner.
func TestAIScanApplyOp_APreviewDoesNotTakeTheStandDown(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Cassandra Cain"), f.author("Cassandra Cain Variant")
	mkRelinkOpBook(t, f.st, "ai-hold-preview", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})
	gate := &recordingStandDown{}
	f.gated(gate)
	dry := true

	_, err := f.run(&dry, id)

	require.NoError(t, err)
	require.Empty(t, gate.snapshot())
}

// A lost lease ends the whole run: the result being applied is left unapplied and
// the results after it are not attempted.
func TestAIScanApplyOp_AbortsTheRunOnALostStandDownLease(t *testing.T) {
	f := newApplyFixture(t)
	keepA, fromA := f.author("Stephanie Brown"), f.author("Stephanie Brown Variant")
	keepB, fromB := f.author("Duke Thomas"), f.author("Duke Thomas Variant")
	bookA := mkRelinkOpBook(t, f.st, "ai-lease-a", fromA.ID, false)
	bookB := mkRelinkOpBook(t, f.st, "ai-lease-b", fromB.ID, false)
	r1 := f.result(database.ScanSuggestion{CanonicalName: keepA.Name, KeepID: keepA.ID, AuthorIDs: []int{keepA.ID, fromA.ID}, Source: "groups_scan"})
	r2 := f.result(database.ScanSuggestion{CanonicalName: keepB.Name, KeepID: keepB.ID, AuthorIDs: []int{keepB.ID, fromB.ID}, Source: "groups_scan"})
	gate := &recordingStandDown{loseOnRenew: 2} // result 1 renews, result 2 does not
	f.gated(gate)

	rep, err := f.run(live(), r1, r2)

	require.Error(t, err)
	require.Contains(t, err.Error(), "aborted after 1 of 2 result(s)")
	require.Contains(t, err.Error(), "scan stand-down lease lost")
	require.True(t, f.applied(r1))
	require.False(t, f.applied(r2))
	require.Equal(t, []int{keepA.ID}, junctionIDs(t, f.st, bookA))
	require.Equal(t, []int{fromB.ID}, junctionIDs(t, f.st, bookB), "nothing is written past a lost lease")
	require.Len(t, rep.report(t).Results, 2)
	require.Equal(t, 1, gate.count("release"))
	got, gerr := f.scanStore.GetScan(f.scan.ID)
	require.NoError(t, gerr)
	require.Empty(t, got.ApplyingOpID, "the claim is released on the abort too")
}

// The narrator reclassify rewrites book_authors as well, so it is gated too: a
// result with nothing to merge and one narrator renews before the reclassify.
func TestAIScanApplyOp_AbortsBeforeReclassifyingOnALostStandDownLease(t *testing.T) {
	f := newApplyFixture(t)
	keep, variant, voice := f.author("Role Keep Wayne"), f.author("Role Variant Wayne"), f.author("Role Voice Wayne")
	mkRelinkOpBook(t, f.st, "ai-lease-variant", variant.ID, false)
	voiceBook := mkRelinkOpBook(t, f.st, "ai-lease-voice", voice.ID, false)
	roles, err := json.Marshal(map[string]any{"narrator": map[string]any{"name": voice.Name, "ids": []int{voice.ID}}})
	require.NoError(t, err)
	id := f.result(database.ScanSuggestion{
		CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, variant.ID, voice.ID},
		Roles: roles, Source: "full_scan",
	})
	gate := &recordingStandDown{loseOnRenew: 2} // the merge of variant renews, the reclassify does not
	f.gated(gate)

	_, err = f.run(live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "scan stand-down lease lost")
	require.False(t, f.applied(id))
	require.False(t, authorGone(t, f.st, voice.ID), "the narrator was not reclassified past a lost lease")
	require.Equal(t, []int{voice.ID}, junctionIDs(t, f.st, voiceBook))
}
