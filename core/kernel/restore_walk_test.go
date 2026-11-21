// file: core/kernel/restore_walk_test.go
// version: 0.1.0
// guid: d7b001ed-9b93-4fac-a039-cb92a0341fb7
// last-edited: 2026-11-21

package kernel

import (
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/restoreswap"
)

// The identity boot walks the restore journal from P4d (restoreWalks); every
// other restore boot takes swapAfter's Owed edge at P4b, byte for byte as it
// always did. F4-08 B10j.

// walkSteps is the restore journal of an identity restore boot: the spec's edges,
// the boundary record settled before step 2 moves the head, and no HooksOwed.
var walkSteps = []string{"staged", "rechecked", "previous_moved", "staged_moved", "sidecars", "swapped", "opened", "opened",
	"restore_line", "hooks_done", "marker_written", "applied"}

// TestIdentityBoot_ARestoreBootWalksTheJournalsEdges: an identity restore boot
// journals opened, the boundary, restore_line, hooks_done, marker_written and
// applied in that order, the marker owing nothing, and deletes the in-flight file.
func TestIdentityBoot_ARestoreBootWalksTheJournalsEdges(t *testing.T) {
	h, c, first := restoreHost(t)
	// A revocation, so the store's head is above the manifest's journal seq of 1.
	actor := &auth.Principal{Method: auth.MethodSession, Perms: holdsAll{}, User: &auth.User{ID: "u-alfred", Username: "alfred"}}
	u, err := first.ident.svc.Auth.CreateUser(t.Context(), actor, auth.NewUser{Username: "lucius", Password: fixturePassword})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ident.svc.Auth.SetStatus(t.Context(), actor, u.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	src := newArchiveSource()
	src.intentInstall = "live"
	src.manifest.JournalSeq = 1
	stageArchive(t, h, first, "r-walk", src)
	h.close(first)

	k, err := h.boot(c)
	if err != nil {
		t.Fatalf("an identity restore boot did not serve: %v", err)
	}
	defer h.close(k)
	recs := h.journal("r-walk")
	if got := stepsOf(recs); !slices.Equal(got, walkSteps) {
		t.Fatalf("journal %v, want %v", got, walkSteps)
	}
	for _, r := range recs {
		if r.HooksOwed {
			t.Errorf("record %q owes the hooks: the walk ran them", r.Step)
		}
	}
	if b, ok := restoreswap.BoundaryOf(recs); !ok || b != 1 {
		t.Errorf("journaled boundary = %d, %v; want the manifest's 1", b, ok)
	}
}

// TestRestoreBoot_ASwitchOffBootStaysOnTheOwedEdge: the same restore with no
// IdentityBoot walks nothing at P4d (there is no P4d): P4b journals
// marker_written{HooksOwed} and applied as before, with no boundary record, no
// restore_line and no hooks_done.
func TestRestoreBoot_ASwitchOffBootStaysOnTheOwedEdge(t *testing.T) {
	h, c, first := restoreHost(t)
	src := newArchiveSource()
	src.intentInstall = "live"
	src.manifest.JournalSeq = 1
	stageArchive(t, h, first, "r-owed", src)
	h.close(first)

	c.IdentityBoot = false
	k := h.mustBoot(c)
	defer h.close(k)
	recs := h.journal("r-owed")
	want := []string{"staged", "rechecked", "previous_moved", "staged_moved", "sidecars", "swapped", "opened", "marker_written", "applied"}
	if got := stepsOf(recs); !slices.Equal(got, want) {
		t.Fatalf("journal %v, want %v", got, want)
	}
	if !recs[7].HooksOwed {
		t.Error("marker_written does not owe the hooks")
	}
	if k.restoreWalks() {
		t.Error("a switch-off boot walks")
	}
}

// TestRestoreWalks_StandsDownForAnM8ThatRunsAfterTheRestore: the restore holds the
// intent slot until it is applied, so a pending store rewrite (M8) has the
// restore journaled applied before it (appliedBeforeRewrite) and the hooks then
// run owed; the walk must not take the edge M8 already took.
func TestRestoreWalks_StandsDownForAnM8ThatRunsAfterTheRestore(t *testing.T) {
	k := &Kernel{cfg: Config{IdentityBoot: true}, swap: &kernelSwap{out: restoreswap.Outcome{Boot: restoreswap.BootRestore}}}
	if !k.restoreWalks() {
		t.Fatal("an identity restore boot with no rewrite pending does not walk")
	}
	k.swap.appliedBeforeRewrite.Store(true)
	if k.restoreWalks() {
		t.Error("an identity restore boot walks after M8's hook journaled applied")
	}
	k.swap.appliedBeforeRewrite.Store(false)
	k.cfg.IdentityBoot = false
	if k.restoreWalks() {
		t.Error("a boot that did not declare the identity flip walks")
	}
	k.cfg.IdentityBoot = true
	k.swap.out.Boot = restoreswap.BootPlain
	if k.restoreWalks() {
		t.Error("a boot that is no restore boot walks")
	}
	k.swap = nil
	if k.restoreWalks() {
		t.Error("a kernel with no swap walks")
	}
}
