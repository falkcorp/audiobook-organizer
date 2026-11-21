// file: core/kernel/reset_freshrows_test.go
// version: 0.1.0
// guid: 444d4337-48fb-43c3-91af-b4786bd633e9
// last-edited: 2026-11-20

package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The SEC8-5 fix (05-storage §5S.3.15 "Next boot"): a reset boot stores every
// FailClosed key at its default, so a stale operator value left in a unit
// cannot loosen it on the fresh store, while an ordinary fresh install stores
// none and an owner can still remove the row.

const (
	authMode  config.Path = "core.auth.mode"
	basicGate config.Path = "core.http.basic_gate.enabled"
	// staleAuthOff is the stale operator value: auth turned off in the unit
	// long ago, as R6-6 anticipates.
	staleAuthOff = "GOTHAM_CORE_AUTH_MODE=disabled"
)

// effective is p's effective value as a JSON string.
func effective(t *testing.T, k *Kernel, p config.Path) string {
	t.Helper()
	ks, ok := k.cfgBoot.store.Lookup(p)
	if !ok {
		t.Fatalf("%s is not declared", p)
	}
	return string(ks.Effective)
}

// storedRows is every c/config/ row of the open store, by path.
func storedRows(t *testing.T, k *Kernel) map[config.Path]config.Override {
	t.Helper()
	out := map[config.Path]config.Override{}
	for ov, err := range k.cfgBoot.native.Load(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		out[ov.Path] = ov
	}
	return out
}

// stageReset stages a reset of the host's store with nothing to delete.
func (h *swapHost) stageReset(id string, kind restorejournal.ResetKind) {
	h.t.Helper()
	if err := h.resetStager().Stage(h.ctx, h.resetIntent(id, kind)); err != nil {
		h.t.Fatal(err)
	}
}

// labelOrder records the crash-point labels a boot enters, in order.
type labelOrder struct {
	mu     sync.Mutex
	labels []string
}

func (l *labelOrder) Enter(label string) func() {
	l.mu.Lock()
	l.labels = append(l.labels, label)
	l.mu.Unlock()
	return func() {}
}

func (l *labelOrder) index(label string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Index(l.labels, label)
}

// TestResetFreshRows_StaleOperatorValueLoses: after a reset of either kind,
// a stale GOTHAM_CORE_AUTH_MODE=disabled no longer turns auth off, because the
// reset boot stored core.auth.mode at its default "required" and the stricter
// stored value wins (05-config §1.3.5), reported as config_fail_closed_override.
// The Basic gate's row holds its default too, off, so the reset tightens
// nothing that HEAD's factory defaults left open.
func TestResetFreshRows_StaleOperatorValueLoses(t *testing.T) {
	for _, kind := range []restorejournal.ResetKind{restorejournal.ResetStore, restorejournal.ResetFactory} {
		t.Run(string(kind), func(t *testing.T) {
			h := newSwapHost(t)
			k := h.mustBoot(h.envConfig(nil, staleAuthOff))
			if got := effective(t, k, authMode); got != `"disabled"` {
				t.Fatalf("before the reset core.auth.mode = %s, want the operator's \"disabled\"", got)
			}
			h.close(k)
			const id = "z-reset"
			h.stageReset(id, kind)
			k = h.mustBoot(h.envConfig(nil, staleAuthOff))
			if got := effective(t, k, authMode); got != `"required"` {
				t.Fatalf("after the reset core.auth.mode = %s, want the stored \"required\" over the stale operator value", got)
			}
			if got := effective(t, k, basicGate); got != "false" {
				t.Fatalf("after the reset core.http.basic_gate.enabled = %s, want its default false", got)
			}
			if !slices.Contains(k.cfgBoot.HealthTokens(), healthFailClosedOverride) {
				t.Fatalf("health tokens %v, want %s for the stored value beating the operator's", k.cfgBoot.HealthTokens(), healthFailClosedOverride)
			}
			rows := storedRows(t, k)
			want := map[config.Path]string{authMode: `"required"`, basicGate: "false"}
			if len(rows) != len(want) {
				t.Fatalf("stored rows %v, want exactly the FailClosed keys %v", rows, want)
			}
			for p, v := range want {
				ov, ok := rows[p]
				if !ok || string(ov.Value) != v || ov.By != resetFreshRowsBy+id {
					t.Errorf("row %s = %+v (present %v), want %s by %q", p, ov, ok, v, resetFreshRowsBy+id)
				}
			}
			if got := stepsOf(h.journal(id)); got[len(got)-1] != "hook_done" {
				t.Fatalf("journal %v, want the reset completed", got)
			}
		})
	}
}

// healthFailClosedOverride is config's health token for a FailClosed key
// whose stored value wins (05-config §1.3.5).
const healthFailClosedOverride = "config_fail_closed_override"

// TestResetFreshRows_FreshInstallStoresNone: an ordinary fresh install, no
// reset, stores no row, so an operator who installs with auth disabled gets
// it ("with only one of them set, that one", 05-config §1.3.5).
func TestResetFreshRows_FreshInstallStoresNone(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.envConfig(nil, staleAuthOff))
	if got := effective(t, k, authMode); got != `"disabled"` {
		t.Fatalf("core.auth.mode = %s, want the operator's \"disabled\" on a fresh install", got)
	}
	if rows := storedRows(t, k); len(rows) != 0 {
		t.Fatalf("a fresh install stored %v, want no row", rows)
	}
	h.close(k)
	k = h.mustBoot(h.envConfig(nil, staleAuthOff))
	if rows := storedRows(t, k); len(rows) != 0 {
		t.Fatalf("a plain reboot stored %v, want no row", rows)
	}
}

// TestResetFreshRows_OwnerUnsetSticks: after the reset an owner who means
// auth to be off removes the stored row through Apply (the FailClosed rule
// accepts an unset); the operator value is effective from the next boot, and
// no later boot writes the row again.
func TestResetFreshRows_OwnerUnsetSticks(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.envConfig(nil, staleAuthOff)))
	h.stageReset("z-reset", restorejournal.ResetStore)
	k := h.mustBoot(h.envConfig(nil, staleAuthOff))
	owner := config.Actor{Subject: "batman", Interactive: true, Owner: true, Can: func(string) bool { return true }}
	if _, err := k.cfgBoot.store.Apply(t.Context(), config.Patch{Unset: []config.Path{authMode}}, owner); err != nil {
		t.Fatalf("the owner's unset of %s: %v", authMode, err)
	}
	h.close(k)
	for boot := range 2 {
		k = h.mustBoot(h.envConfig(nil, staleAuthOff))
		if got := effective(t, k, authMode); got != `"disabled"` {
			t.Fatalf("boot %d after the unset: core.auth.mode = %s, want the operator's \"disabled\"", boot+1, got)
		}
		if _, ok := storedRows(t, k)[authMode]; ok {
			t.Fatalf("boot %d after the unset stored %s again", boot+1, authMode)
		}
		h.close(k)
	}
}

// TestResetFreshRows_DurableBeforeApplied: the fresh rows are committed before
// the journal reads applied, so a crash between them repeats the reset boot,
// which writes them; journalled the other way round, the crash would resume
// as an applied reset that never writes them. A reset boot that fails before
// the rows (secrets.Open refuses) leaves set_aside, and its retry stores them
// once, with the same values.
func TestResetFreshRows_DurableBeforeApplied(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.envConfig(nil, staleAuthOff)))
	const id = "z-reset"
	h.stageReset(id, restorejournal.ResetStore)
	if err := os.Mkdir(filepath.Join(h.state, "not-a-keyring"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.boot(h.envConfig(nil, staleAuthOff, "GOTHAM_CORE_SECRETS_KEY_FILE=not-a-keyring")); err == nil {
		t.Fatal("a reset boot whose secrets.Open fails succeeded")
	}
	if got := stepsOf(h.journal(id)); got[len(got)-1] != "set_aside" {
		t.Fatalf("journal %v after the failed reset boot, want set_aside", got)
	}
	order := &labelOrder{}
	h.ctx = bootseam.With(h.t.Context(), &bootseam.Seams{BuildID: swapBuild, Labels: order})
	k := h.mustBoot(h.envConfig(nil, staleAuthOff))
	rows, applied := order.index(labelResetFreshRows), order.index("swap.journal.applied")
	if rows < 0 || applied < 0 || rows > applied {
		t.Fatalf("labels %v: the fresh rows at %d, applied at %d; want the rows committed first", order.labels, rows, applied)
	}
	if got := effective(t, k, authMode); got != `"required"` {
		t.Fatalf("after the retried reset core.auth.mode = %s, want \"required\"", got)
	}
	var v string
	if err := json.Unmarshal(storedRows(t, k)[authMode].Value, &v); err != nil || v != "required" {
		t.Fatalf("stored %s = %q (%v), want required", authMode, v, err)
	}
}
