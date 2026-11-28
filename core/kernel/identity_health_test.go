// file: core/kernel/identity_health_test.go
// version: 0.1.0
// guid: 2d7a3e91-5c04-4b68-a1f3-9e8b60c4d725
// last-edited: 2026-11-21

package kernel

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/health"
)

// Batman names only. F4-08 B9a: the identity boot's health tokens. They are
// posture tokens: the booted kernel raises them on its registry, which is the
// source of the authenticated degraded field, and the unauthenticated surface
// (health.PublicTokens, what /readyz reads) never carries one.

// componentState is the registry's state of component id after a boot.
func componentState(t *testing.T, k *Kernel, id health.ComponentID) (health.ComponentState, bool) {
	t.Helper()
	for _, cs := range k.healthReg.Snapshot().Components {
		if cs.ID == id {
			return cs, true
		}
	}
	return health.ComponentState{}, false
}

// requirePosture fails unless component id is Degraded with reason, and then
// that the unauthenticated surface hides it: the token is not in /readyz's body
// (a Degraded component that is not a readiness failure is not in NotReady at
// all) and health.PublicTokens would generalise it to "<id>:down".
func requirePosture(t *testing.T, k *Kernel, id health.ComponentID, reason string) {
	t.Helper()
	cs, ok := componentState(t, k, id)
	if !ok {
		t.Fatalf("%s is not registered", id)
	}
	if cs.Result.Status != health.StatusDegraded || cs.Result.Reason != reason {
		t.Fatalf("%s = %v %q (%s), want degraded %q", id, cs.Result.Status, cs.Result.Reason, cs.Result.Detail, reason)
	}
	for _, tok := range k.healthReg.Status().NotReady {
		if tok == string(id)+":"+reason {
			t.Errorf("/readyz carries %q", tok)
		}
	}
	if got := health.PublicTokens([]string{string(id) + ":" + reason}); !slices.Equal(got, []string{string(id) + ":down"}) {
		t.Errorf("PublicTokens(%s:%s) = %v: a posture token reaches an unauthenticated caller by name", id, reason, got)
	}
}

// journalPath is the revocation journal's file of a booted identity host.
func journalPath(k *Kernel) string { return k.ident.journal.Path() }

// TestHealth_RevocationJournalBrokenRaised: a journal whose chain is broken
// before boot raises revocation_journal_broken (degraded) on core.backup, the
// source of the authenticated degraded field of GET /api/v1/system/status, and
// the boot still serves (05-backup 5B.5: a broken journal never stops it); the
// unauthenticated surface never carries it.
func TestHealth_RevocationJournalBrokenRaised(t *testing.T) {
	h, c := identityHost(t)
	first := h.mustBoot(c)
	path := journalPath(first)
	h.close(first)

	// A complete line that is no line of the chain: the journal does not verify.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"seq\":2,\"kind\":\"user_disabled\",\"mac\":\"not-the-batcomputer\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	k := h.mustBoot(c)
	requirePosture(t, k, componentBackup, journal.HealthBroken)
}

// TestHealth_RevocationJournalUnwritableRaised: a journal that stops taking
// lines after the boot (here: the journal is held, as it is until a replaced
// store's restore hooks have run) is revocation_journal_unwritable on core.backup
// by itself, degraded, and the unauthenticated surface never names it. The probe
// is the one the boot registered (identityBoot.backupProbe).
func TestHealth_RevocationJournalUnwritableRaised(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	// A clean boot is Up (the colocation token of a test directory is info).
	if cs, _ := componentState(t, k, componentBackup); cs.Result.Status != health.StatusUp {
		t.Fatalf("a clean boot is %v (%q) on core.backup", cs.Result.Status, cs.Result.Reason)
	}
	k.ident.journal.Hold(errors.New("the batcomputer is syncing"))
	res := k.ident.backupProbe()(t.Context())
	if res.Status != health.StatusDegraded || res.Reason != journal.HealthUnwritable {
		t.Fatalf("core.backup probe = %v %q (%s), want degraded %q", res.Status, res.Reason, res.Detail, journal.HealthUnwritable)
	}
	id := string(componentBackup) + ":" + res.Reason
	if got := health.PublicTokens([]string{id}); !slices.Equal(got, []string{string(componentBackup) + ":down"}) {
		t.Errorf("PublicTokens(%s) = %v: the token reaches an unauthenticated caller by name", id, got)
	}
}

// TestHealth_AuthDisabledRemote: while core.auth.mode is disabled on an install
// a remote caller can reach, core.auth is degraded with auth_disabled_remote,
// and never on the unauthenticated surface. The same mode on a loopback-only
// install, and the required mode, raise nothing.
func TestHealth_AuthDisabledRemote(t *testing.T) {
	cases := []struct {
		name   string
		kv     []string
		raised bool
	}{
		{"disabled and remote-reachable", []string{"core.auth.mode", "disabled", "core.auth.disabled_allow_remote", "true", "core.http.listen", "0.0.0.0:0"}, true},
		{"disabled behind a trusted proxy", []string{"core.auth.mode", "disabled", "core.auth.disabled_allow_remote", "true", "core.http.listen", "localhost:0", "core.http.trusted_proxies", `["192.0.2.1"]`}, true},
		{"disabled on loopback only", []string{"core.auth.mode", "disabled", "core.http.listen", "localhost:0"}, false},
		{"required and remote-reachable", []string{"core.http.listen", "0.0.0.0:0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, c := identityHost(t)
			withCore(&c, tc.kv...)
			k := h.mustBoot(c)
			if tc.raised {
				requirePosture(t, k, componentAuth, reasonAuthDisabledRemote)
				return
			}
			cs, ok := componentState(t, k, componentAuth)
			if !ok {
				t.Fatal("core.auth is not registered")
			}
			if cs.Result.Status != health.StatusUp || cs.Result.Reason != "" {
				t.Errorf("core.auth = %v %q, want up with no reason", cs.Result.Status, cs.Result.Reason)
			}
		})
	}
}

// TestHealth_SwitchOffRegistersNoIdentityComponents: the ABO-style boot has no
// core.auth component and none of the identity tokens.
func TestHealth_SwitchOffRegistersNoIdentityComponents(t *testing.T) {
	h, c := identityHost(t)
	c.IdentityBoot = false
	withCore(&c, "core.http.listen", "0.0.0.0:0")
	k := h.mustBoot(c)
	for _, id := range []health.ComponentID{componentAuth, componentBackup} {
		if _, ok := componentState(t, k, id); ok {
			t.Errorf("a switch-off boot registered %s", id)
		}
	}
}
