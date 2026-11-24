// file: internal/books/cutover/identity_system_user_test.go
// version: 0.1.0
// guid: 7e21b5c9-3a48-4d06-8f1b-c5a29d0e6b73
// last-edited: 2026-11-22

package cutover

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// HEAD's _system user (internal/auth/seed.go) has a ULID id, and every HEAD writer
// attributes to the literal "_system", which is core's own system user ID: M3
// imports the row under that ID (m3/systemuser.go) and every reference to the
// ULID follows. Without it the P4d boot write that seeds core's system user fails
// on the imported row's username and the build refuses to serve.

const (
	// sysKey and sysIdent sort after every corpus key and identity, so with one
	// item per chunk they are imported long after the chunk that pulls the users.
	sysKey   = "01M7ZZZSYSOWNEDKEY00000001"
	sysIdent = "01M7ZZZSYSOWNEDIDN00000001"
	// sysMerged is a disabled user merged into the system row.
	sysMerged = "01M7ZZZSYSMERGED000000001"
)

func systemOwnedRows() func(*idRig) {
	return idRows(rowsOf(
		headKeyRows(sysKey, uSystem, "mck-r2-synthetic-system-owned-key", []string{"library.view"}, "active"),
		headUserRows(sysMerged, "victor.zsasz", nil, "inactive"),
		[]seedRow{
			{"oauthid:" + sysIdent, map[string]any{"id": sysIdent, "provider": "github", "subject": "ras-al-ghul", "user_id": uSystem, "created_at": "2026-11-10T11:00:00Z"}},
			{"idx:oauth:user:" + uSystem + ":" + sysIdent, sysIdent},
			// Overwrites the row above with one that is merged into the system user.
			{"u:" + sysMerged, map[string]any{"id": sysMerged, "username": "victor.zsasz", "email": "", "password_hash_algo": "argon2id", "password_hash": idArgon,
				"roles": []string{}, "status": "inactive", "created_at": "2026-11-10T11:00:00Z", "updated_at": "2026-11-10T11:00:00Z", "version": 1,
				"merged_into_user_id": uSystem, "merged_at": "2026-11-10T12:00:00Z"}},
		},
	)...)
}

func checkSystemRemap(t *testing.T, snap *idSnap) {
	t.Helper()
	sys := string(auth.SystemUserID)
	if _, kept := snap.Users[uSystem]; kept {
		t.Errorf("the system user is stored under its HEAD ULID")
	}
	su, ok := snap.Users[sys]
	if !ok {
		t.Fatalf("no user %q after M3: users %v", sys, mapKeys(snap.Users))
	}
	if su.User.Username != "_system" || su.User.Status != auth.StatusSystem || su.User.HasPassword || su.Hash != "" {
		t.Errorf("the system user is %+v hash %q, want _system, status system, no password", su.User, su.Hash)
	}
	if k, ok := snap.Keys[sysKey]; !ok || k.Key.Owner != auth.SystemUserID {
		t.Errorf("the key the system user owned: present %v, owner %q, want _system", ok, k.Key.Owner)
	}
	found := false
	for _, id := range snap.Idents {
		if id.Subject == "ras-al-ghul" {
			found = true
			if id.UserID != auth.SystemUserID {
				t.Errorf("the identity the system user owned is linked to %q, want _system", id.UserID)
			}
		}
	}
	if !found {
		t.Errorf("the identity the system user owned was not imported (identities %v)", mapKeys(snap.Idents))
	}
	if mu := snap.Users[sysMerged].User; mu.MergedInto != auth.SystemUserID {
		t.Errorf("the user merged into the system row is merged into %q, want _system", mu.MergedInto)
	}
	for k := range snap.Legacy {
		if strings.Contains(k, uSystem) {
			t.Errorf("a legacy row still names the system ULID: %s", k)
		}
	}
	if len(snap.Verify.Failures) > 0 {
		t.Errorf("Verify failed: %v", snap.Verify.Failures)
	}
}

// TestIdentityCutover_SystemUserImportsUnderCoreID: the system row, the key and
// identity it owns and the user merged into it all import under "_system", and
// no row is left naming the ULID.
func TestIdentityCutover_SystemUserImportsUnderCoreID(t *testing.T) {
	r := newIDRig(t, "overlay", systemOwnedRows())
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	checkSystemRemap(t, snap)
}

// TestIdentityCutover_SystemUserSurvivesAKill: killed at each of the first
// calls (the chunk that pulls the users and the chunks after it), M3 ends in
// the store an uninterrupted run leaves. The first chunk is the one that knows the
// ULID: once it commits the HEAD row is gone, so the key and identity that name
// it must have been imported in it.
func TestIdentityCutover_SystemUserSurvivesAKill(t *testing.T) {
	want, err := newIDRig(t, "overlay", systemOwnedRows()).run(t)
	if err != nil {
		t.Fatalf("uninterrupted M3: %v", err)
	}
	checkSystemRemap(t, want)
	for _, n := range []int64{1, 2, 3, 6, 12} {
		r := newIDRig(t, "overlay", systemOwnedRows())
		mem := r.newMem(t)
		err := r.boot(context.Background(), mem, []datamigrate.DataMigration{killAt(r.migration(), n)}, nil)
		if !errors.Is(err, errKilled) {
			t.Fatalf("kill at call %d: the boot returned %v", n, err)
		}
		got, err := r.runOn(t, mem)
		if err != nil {
			t.Fatalf("kill at call %d: the boot after it failed: %v\n%s", n, err, r.logs.String())
		}
		checkSystemRemap(t, got)
		sameIdentityOutcome(t, got, want)
		if t.Failed() {
			t.Fatalf("the cell killed at call %d differs", n)
		}
	}
}

// TestCutoverStopsOnAmbiguousSystemUser: each row that makes "the system user"
// ambiguous stops the cutover with a blocker on that row, and nothing is imported.
func TestCutoverStopsOnAmbiguousSystemUser(t *testing.T) {
	const impostor, second = "01M7ZZZSYSIMPOSTOR0000001", "01M7ZZZSYSSECOND00000001"
	cases := []struct {
		name string
		rows []seedRow
		key  string
	}{
		{"named _system without the status", headUserRows(impostor, "_SYSTEM", nil, "active"), "u:" + impostor},
		{"status system under another name", headUserRows(second, "selina.kyle", nil, "system"), "u:" + second},
		{"a second status system row", headUserRows(second, "_system2", nil, "system"), "u:" + second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newIDRig(t, "overlay", idRows(c.rows...))
			var named []string
			for _, b := range r.refusal(t, r.newMem(t)) {
				named = append(named, string(b.Key))
			}
			if !slices.Contains(named, c.key) {
				t.Errorf("the refusal names %v, want it to name %s", named, c.key)
			}
		})
	}
}
