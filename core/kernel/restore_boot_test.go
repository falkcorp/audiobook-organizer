// file: core/kernel/restore_boot_test.go
// version: 1.0.0
// guid: 6e2c9b15-d478-4a30-b7f1-0a5d3e8c1f92
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// restoreScene is an install about to be restored from its own archive: the
// archive (head 0), the pre-swap store (head 1, key-spoiler revoked), one
// revocation journaled but never committed anywhere (key-redrobin), and the
// archive swapped in.
type restoreScene struct {
	*gotham
	previous string
	original string // the live store before the swap, for a swap-back
	archived journal.ServedBoot
	before   journal.Location
}

func newRestoreScene(t *testing.T, prep func(*gotham)) *restoreScene {
	t.Helper()
	g := newGotham(t)
	if prep != nil {
		prep(g)
	}
	g.restart()
	s := &restoreScene{gotham: g, previous: g.snapshot("previous"), original: g.snapshot("original")}
	g.swapIn(g.archive)
	s.archived, s.before = g.pair(), g.location()
	return s
}

func (s *restoreScene) restoreLineSeq(restoreID string) uint64 {
	s.t.Helper()
	for _, l := range s.restoreLines() {
		if info, ok := l.RestoreInfo(); ok && info.RestoreID == restoreID {
			return l.Seq
		}
	}
	s.t.Fatalf("no restore line for %s: %v", restoreID, s.kinds())
	return 0
}

// uncommitted journals a revocation of key-redrobin (line 2) that never reached
// any store: a crash between the line's fsync and its batch.
func uncommitted(g *gotham) {
	g.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-redrobin")})
}

// TestPostRestoreReappliesRevocations: a key revoked after the archive stays
// revoked through a restore of that archive, one journaled but never committed
// anywhere comes back revoked from the journal, and a key the journal covers
// is not asked to confirm; a journal gap, a host-loss or foreign restore (no
// coverage) puts every key still active to needs_confirmation.
func TestPostRestoreReappliesRevocations(t *testing.T) {
	t.Run("covered", func(t *testing.T) {
		s := newRestoreScene(t, uncommitted)
		steps := &rxSteps{}
		rb := archiveBoot("restore-1", "arch-1", s.previous, steps)
		res, hres := s.boot(rb, nil)
		if res.Class != classRestoreBoot || !hres.Ran || res.HeadBefore.Seq != 0 {
			t.Fatalf("class %q, hooks ran %v, head_before %+v", res.Class, hres.Ran, res.HeadBefore)
		}
		if s.keyStatus("key-spoiler") != auth.KeyRevoked || s.keyStatus("key-redrobin") != auth.KeyRevoked {
			t.Errorf("spoiler %q, redrobin %q: both must be revoked", s.keyStatus("key-spoiler"), s.keyStatus("key-redrobin"))
		}
		if got := s.keyStatus("key-batmobile"); got != auth.KeyActive {
			t.Errorf("key-batmobile = %q: the journal covers this restore, so no key is asked to confirm", got)
		}
		for _, k := range s.kinds() {
			if k == journal.Gap {
				t.Errorf("a restore with an intact journal appended a gap line: %v", s.kinds())
			}
		}
		seq := s.restoreLineSeq("restore-1")
		line := s.chain().Lines[seq-1]
		info, _ := line.RestoreInfo()
		if info.Kind != journal.RestoreArchive || info.Source != "arch-1" || info.Boundary != 0 || res.RestoreLine != seq {
			t.Errorf("restore line = %+v, result line %d", info, res.RestoreLine)
		}
		if !s.hooked(seq) {
			t.Error("restore_hooked was not recorded")
		}
	})

	t.Run("the boundary is the smaller of the source's seq and the store's head", func(t *testing.T) {
		for _, c := range []struct{ source, want uint64 }{{0, 0}, {1, 0}, {5, 0}} {
			s := newRestoreScene(t, uncommitted)
			rb := archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{})
			rb.SourceSeq = c.source
			if res, _ := s.boot(rb, nil); res.RestoreBoundary != c.want {
				t.Errorf("source seq %d, head_before 0: boundary %d, want %d", c.source, res.RestoreBoundary, c.want)
			}
		}
		// An archive whose store has seen the journal up to 1: a source seq above
		// it is capped by the head, one below it wins.
		for _, c := range []struct{ source, want uint64 }{{0, 0}, {1, 1}, {7, 1}} {
			g := newGotham(t)
			late := g.snapshot("late")
			g.restart()
			prev := g.snapshot("previous")
			g.swapIn(late)
			rb := archiveBoot("restore-1", "arch-1", prev, &rxSteps{})
			rb.SourceSeq = c.source
			if res, _ := g.boot(rb, nil); res.RestoreBoundary != c.want || res.HeadBefore.Seq != 1 {
				t.Errorf("source seq %d, head_before 1: boundary %d (head_before %d), want %d", c.source, res.RestoreBoundary, res.HeadBefore.Seq, c.want)
			}
		}
	})

	t.Run("a journal gap after the boundary", func(t *testing.T) {
		s := newRestoreScene(t, func(g *gotham) {
			uncommitted(g)
			if _, err := g.comp.AppendLine(g.ctx, journal.GapLine(g.clock.now(), journal.GapAppendFailed)); err != nil {
				t.Fatal(err)
			}
		})
		s.boot(archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), nil)
		if got := s.keyStatus("key-batmobile"); got != auth.KeyNeedsConfirmation {
			t.Errorf("key-batmobile = %q, want needs_confirmation: a gap means the journal may lack a revocation", got)
		}
		for _, id := range []string{"key-spoiler", "key-redrobin"} {
			if got := s.keyStatus(id); got != auth.KeyRevoked {
				t.Errorf("%s = %q: a journaled revocation stays revoked, gap or not", id, got)
			}
		}
	})

	t.Run("host loss and a foreign archive get no coverage", func(t *testing.T) {
		for name, mut := range map[string]func(*restoreBoot){
			"host loss": func(rb *restoreBoot) { rb.PreSwapDir = "" },
			"foreign":   func(rb *restoreBoot) { rb.Foreign = true },
		} {
			s := newRestoreScene(t, uncommitted)
			rb := archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{})
			mut(rb)
			s.boot(rb, nil)
			if got := s.keyStatus("key-batmobile"); got != auth.KeyNeedsConfirmation {
				t.Errorf("%s: key-batmobile = %q, want needs_confirmation", name, got)
			}
			if got := s.keyStatus("key-spoiler"); got != auth.KeyRevoked {
				t.Errorf("%s: key-spoiler = %q, want revoked", name, got)
			}
		}
	})

	t.Run("hook lines carry the restore line's seq", func(t *testing.T) {
		// An unjournaled revocation in the pre-swap store (its Append failed):
		// the hook derives it from the store and journals it.
		s := newRestoreScene(t, func(g *gotham) {
			uncommitted(g)
			k, err := g.auth.APIKeyByID(g.ctx, "key-batmobile")
			if err != nil {
				t.Fatal(err)
			}
			k.Status, k.Version = auth.KeyRevoked, k.Version+1
			g.commit(func(b *storage.Batch) error { return g.auth.PutAPIKey(b, k) })
		})
		res, _ := s.boot(archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), nil)
		if s.keyStatus("key-batmobile") != auth.KeyRevoked {
			t.Fatalf("key-batmobile = %q: the pre-swap store's restriction wins", s.keyStatus("key-batmobile"))
		}
		var stamped []string
		for _, l := range s.chain().Lines {
			if l.Kind == journal.APIKeyRevoked && l.RestoreSeq == res.RestoreLine && l.RestoreSeq != 0 {
				stamped = append(stamped, l.Subject.ID)
			}
		}
		if !slices.Contains(stamped, "key-batmobile") {
			t.Errorf("no hook line for key-batmobile carries RestoreSeq %d: %v", res.RestoreLine, stamped)
		}
	})
}

// TestRestoreBootWritesMarkersAfterHooks: a staged restore with an intact
// journal appends no gap line and needs no key confirmation; served_boot and
// backup.location.json are written only after step 3's hooks commit, in the
// order restore_line, hooks_done, marker_written, applied; a failing hook that
// swaps back leaves the original store "same store" at the next boot with the
// anchor equal to its head; a boot failing after applied is read as the same
// store; and a crash after the hooks resumes without running them again.
func TestRestoreBootWritesMarkersAfterHooks(t *testing.T) {
	t.Run("order", func(t *testing.T) {
		s := newRestoreScene(t, uncommitted)
		steps := &rxSteps{}
		var seen []string
		steps.on = func(e string) {
			switch e {
			case "hooks_done":
				seq := s.restoreLineSeq("restore-1")
				seen = append(seen, "hooked:"+boolStr(s.hooked(seq)), "pair_moved:"+boolStr(s.pair() != s.archived), "file_moved:"+boolStr(s.location() != s.before))
			case "marker_written", "applied":
				seen = append(seen, e+":pair_in_file:"+boolStr(s.location().ServedBoot == s.pair() && s.pair() != s.archived))
			}
		}
		res, _ := s.boot(archiveBoot("restore-1", "arch-1", s.previous, steps), nil)
		want := "restore_line:" + u64(res.RestoreLine) + ",hooks_done,marker_written,applied"
		if got := steps.log(); got != want {
			t.Errorf("restore journal edges = %q, want %q", got, want)
		}
		wantSeen := []string{"hooked:true", "pair_moved:false", "file_moved:false", "marker_written:pair_in_file:true", "applied:pair_in_file:true"}
		if !slices.Equal(seen, wantSeen) {
			t.Errorf("state at each edge = %v, want %v: the markers are written after the hooks and before marker_written", seen, wantSeen)
		}
		if got := s.keyStatus("key-batmobile"); got != auth.KeyActive {
			t.Errorf("key-batmobile = %q: an intact journal needs no key confirmation", got)
		}
		if loc := s.location(); loc.Head != s.head() || loc.Store != s.db.Admin().StoreIdentity() {
			t.Errorf("location %+v, store head %+v", loc, s.head())
		}
		// A boot failing at P6 after applied reads as the same store.
		s.restart()
		res2, hres := s.boot(nil, nil)
		if res2.Class != classSame || hres.Ran || len(s.restoreLines()) != 1 {
			t.Errorf("next boot: class %q, hooks ran %v, %d restore line(s)", res2.Class, hres.Ran, len(s.restoreLines()))
		}
	})

	t.Run("a failing hook that swaps back", func(t *testing.T) {
		s := newRestoreScene(t, nil)
		steps := &rxSteps{}
		boom := errors.New("the audit hook failed")
		failing := func(env hookEnv) ([]journal.RestoreHook, error) {
			real, err := restoreHookList(s.stores())(env)
			return append(real, func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }), err
		}
		res, _, err := s.tryBoot(archiveBoot("restore-1", "arch-1", s.previous, steps), nil, failing)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the failing hook's", err)
		}
		if s.pair() != s.archived || s.location() != s.before {
			t.Errorf("the markers moved before the hooks committed: pair %+v (was %+v), file %+v", s.pair(), s.archived, s.location())
		}
		if got := steps.log(); got != "restore_line:"+u64(res.RestoreLine) {
			t.Errorf("edges = %q: hooks_done must not be journaled for a failed hook", got)
		}

		// Step x: a restore_aborted line, the hook lines already written, the
		// original store and the saved location file back.
		seq := res.RestoreLine
		if _, err := s.comp.AppendLine(s.ctx, journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-batmobile"), RestoreSeq: seq, At: s.clock.now()}); err != nil {
			t.Fatal(err)
		}
		if err := restoreAborted(s.ctx, s.comp, s.keys, "restore-1", s.clock.now()); err != nil {
			t.Fatal(err)
		}
		s.swapIn(s.original)
		if loc := s.location(); loc.Head != s.head() {
			t.Errorf("anchor %+v, original store head %+v: they must be equal after the swap-back", loc.Head, s.head())
		}
		// The store's identity differs now, which would read replaced if the
		// aborted restore line counted as evidence.
		s.setLocation(func(l *journal.Location) { l.Store.Epoch += 1 })

		next, hres := s.boot(nil, nil)
		if next.Class != classRelocated || hres.Ran || len(next.Owed) != 0 {
			t.Fatalf("after the swap-back: class %q (%s), hooks ran %v, owed %d: an aborted restore line is neither evidence nor resumed", next.Class, next.Why, hres.Ran, len(next.Owed))
		}
		if got := s.keyStatus("key-batmobile"); got != auth.KeyActive {
			t.Errorf("key-batmobile = %q: a hook line of an aborted restore is verified but never re-applied", got)
		}
		if got, end := s.head().Seq, s.chain().IntactEnd(); got < end-1 {
			t.Errorf("head %d, journal end %d: the head must advance past the skipped lines", got, end)
		}

		// A second restore, with coverage, must not take the aborted restore's
		// hook line for a revocation either: it is verified, in the journal after
		// the boundary, and still never applied.
		prev2 := s.snapshot("previous2")
		s.restart()
		s.swapIn(s.archive2)
		s.boot(archiveBoot("restore-2", "arch-1", prev2, &rxSteps{}), nil)
		if got := s.keyStatus("key-batmobile"); got != auth.KeyActive {
			t.Errorf("after a second, covered restore key-batmobile = %q: the aborted restore's hook line was applied", got)
		}
	})

	t.Run("a crash after the hooks resumes without running them", func(t *testing.T) {
		s := newRestoreScene(t, uncommitted)
		boom := errors.New("the host died")
		steps := &rxSteps{fail: map[string]error{"hooks_done": boom}}
		res, _, err := s.tryBoot(archiveBoot("restore-1", "arch-1", s.previous, steps), nil, nil)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if !s.hooked(res.RestoreLine) || s.pair() != s.archived {
			t.Fatalf("hooked %v, pair moved %v: the crash is after the hooks and before the markers", s.hooked(res.RestoreLine), s.pair() != s.archived)
		}
		s.restart()
		steps2 := &rxSteps{}
		res2, hres, err := s.tryBoot(archiveBoot("restore-1", "arch-1", s.previous, steps2), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if hres.Ran || res2.RestoreLine != res.RestoreLine || len(s.restoreLines()) != 1 {
			t.Errorf("resume: hooks ran %v, line %d (want %d), %d restore line(s)", hres.Ran, res2.RestoreLine, res.RestoreLine, len(s.restoreLines()))
		}
		if got := steps2.log(); got != "restore_line:"+u64(res.RestoreLine)+",hooks_done,marker_written,applied" {
			t.Errorf("resumed edges = %q", got)
		}
		if s.pair() == s.archived || s.location().ServedBoot != s.pair() {
			t.Errorf("the markers were not written: pair %+v, file %+v", s.pair(), s.location().ServedBoot)
		}
	})

	t.Run("a restore journal that already read applied before the hooks ran", func(t *testing.T) {
		// A build without P4d steps 2-3 applied the restore (HooksOwed); the
		// next journaling boot runs the hooks it owed, with the source's
		// boundary, and writes the markers, with no journal edge to walk.
		s := newRestoreScene(t, nil)
		rb := archiveBoot("restore-1", "arch-1", s.previous, nil)
		rb.Owed = true
		res, hres := s.boot(rb, nil)
		if !hres.Ran || !s.hooked(res.RestoreLine) || s.pair() == s.archived {
			t.Errorf("hooks ran %v, hooked %v, pair moved %v", hres.Ran, s.hooked(res.RestoreLine), s.pair() != s.archived)
		}
	})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func u64(n uint64) string { return strconv.FormatUint(n, 10) }
