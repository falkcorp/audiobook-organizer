// file: internal/books/cutover/m3/systemuser_test.go
// version: 0.1.0
// guid: 0c47d9a2-8e35-4b1f-a6d0-71e2f5b3c968
// last-edited: 2026-11-22

package m3

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage"
)

const (
	sysULID  = "01M7ZZZSYSUNITSYSTEM000001"
	sysOwner = "01M7ZZZSYSUNITOWNER0000001"
)

func sysRow(id, username, status string) string {
	return `{"id":"` + id + `","username":"` + username + `","email":"` + username + `@local","status":"` + status + `","roles":[]}`
}

func entriesOf(t *testing.T, rows map[string]string) []userEntry {
	t.Helper()
	var out []userEntry
	for k, v := range rows {
		h, err := decodeUser(k, []byte(v))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, userEntry{key: k, h: h})
	}
	return out
}

// TestClassifySystem: exactly one status-system row named _system is the system
// user; every row that makes it ambiguous is refused by key with a fixed class
// and names no value.
func TestClassifySystem(t *testing.T) {
	const other = "01M7ZZZSYSUNITOTHER0000001"
	cases := []struct {
		name    string
		rows    map[string]string
		from    string
		refused map[string]string
	}{
		{"none", map[string]string{"u:" + sysOwner: sysRow(sysOwner, "harvey.bullock", "active")}, "", nil},
		{"one", map[string]string{
			"u:" + sysULID:  sysRow(sysULID, "_system", "system"),
			"u:" + sysOwner: sysRow(sysOwner, "harvey.bullock", "active"),
		}, sysULID, nil},
		{"status and name are folded", map[string]string{"u:" + sysULID: sysRow(sysULID, "_System", " SYSTEM ")}, sysULID, nil},
		{"already core's ID", map[string]string{"u:_system": sysRow("_system", "_system", "system")}, "_system", nil},
		{"two status-system rows", map[string]string{
			"u:" + sysULID: sysRow(sysULID, "_system", "system"),
			"u:" + other:   sysRow(other, "_system2", "system"),
		}, "", map[string]string{"u:" + sysULID: reasonSeveralSystem, "u:" + other: reasonSeveralSystem}},
		{"status system, another name", map[string]string{"u:" + sysULID: sysRow(sysULID, "selina.kyle", "system")},
			"", map[string]string{"u:" + sysULID: reasonSystemRename}},
		{"named _system, another status", map[string]string{"u:" + sysOwner: sysRow(sysOwner, "_system", "active")},
			"", map[string]string{"u:" + sysOwner: reasonSystemName}},
		{"the system row and an impostor", map[string]string{
			"u:" + sysULID: sysRow(sysULID, "_system", "system"),
			"u:" + other:   sysRow(other, "_SYSTEM", "active"),
		}, "", map[string]string{"u:" + other: reasonSystemName}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifySystem(entriesOf(t, c.rows))
			if got.from != c.from {
				t.Errorf("from = %q, want %q", got.from, c.from)
			}
			if len(got.refusals) != len(c.refused) {
				t.Fatalf("refusals %v, want %v", got.refusals, c.refused)
			}
			for k, want := range c.refused {
				if got.refusals[k] != want {
					t.Errorf("refusal of %s = %q, want %q", k, got.refusals[k], want)
				}
			}
		})
	}
}

// TestSystemUsers_Remap: the system row maps to core's ID with status system and
// no password, a reference to its HEAD ID names core's, and every other ID and
// every other user's status is as mapUser has it.
func TestSystemUsers_Remap(t *testing.T) {
	su := classifySystem(entriesOf(t, map[string]string{"u:" + sysULID: sysRow(sysULID, "_system", "system")}))
	h, err := decodeUser("u:"+sysULID, []byte(sysRow(sysULID, "_system", "system")))
	if err != nil {
		t.Fatal(err)
	}
	u, hash, reported := su.mapUser(h, nil)
	if u.ID != auth.SystemUserID || u.Username != "_system" || u.Status != auth.StatusSystem {
		t.Errorf("system user = {%q %q %q}, want {_system _system system}", u.ID, u.Username, u.Status)
	}
	if u.HasPassword || hash != "" || u.PasswordResetRequired {
		t.Errorf("system user has password %v, hash %q, reset required %v", u.HasPassword, hash, u.PasswordResetRequired)
	}
	if reported != "" {
		t.Errorf("the system status was reported as odd (%q): it is the status core uses", reported)
	}
	if got := su.ref(sysULID); got != auth.SystemUserID {
		t.Errorf("ref(system ULID) = %q, want _system", got)
	}
	if got := su.ref(sysOwner); got != auth.UserID(sysOwner) {
		t.Errorf("ref(other) = %q, want it unchanged", got)
	}
	// Without a system row nothing is remapped.
	if got := (systemUsers{}).ref(sysULID); got != auth.UserID(sysULID) {
		t.Errorf("ref with no system row = %q, want it unchanged", got)
	}
	// A user merged into the system row follows it like any reference.
	mh, err := decodeUser("u:"+sysOwner, []byte(`{"id":"`+sysOwner+`","username":"harvey.bullock","status":"inactive","merged_into_user_id":"`+sysULID+`","merged_at":"2026-11-10T11:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	if mu, _, _ := su.mapUser(mh, nil); mu.MergedInto != auth.SystemUserID {
		t.Errorf("merged-into = %q, want _system", mu.MergedInto)
	}
}

// TestPreflight_SystemUser: the row that HEAD's seed wrote is not a blocker, the
// keys and identities it owns find their owner under core's ID, and each row
// that makes the system user ambiguous is held back by key with a fixed class.
func TestPreflight_SystemUser(t *testing.T) {
	const (
		keyS = "01M7ZZZSYSUNITKEYS000000001"
		idS  = "01M7ZZZSYSUNITIDNS000000001"
	)
	hash := strings.Repeat("ab", 32)
	good := memReader{
		"u:" + sysULID:   sysRow(sysULID, "_system", "system"),
		"u:" + sysOwner:  `{"id":"` + sysOwner + `","username":"harvey.bullock","password_hash_algo":"argon2id","password_hash":"` + unitArgon + `","roles":["viewer"],"status":"active"}`,
		"apikey:" + keyS: unitKeyRow(keyS, sysULID, hash),
		"oauthid:" + idS: unitIdentityRow(idS, "github", "mck-unit-subject", sysULID),
	}
	m := testMigrationOver(t, noNative{}, func() storage.Reader { return good })
	got, err := m.Preflight(context.Background(), good)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the system user and what it owns block the cutover: %+v", got)
	}

	bad := memReader{
		"u:" + sysULID:  sysRow(sysULID, "_system", "system"),
		"u:" + sysOwner: sysRow(sysOwner, "_system", "active"),
	}
	m = testMigrationOver(t, noNative{}, func() storage.Reader { return bad })
	got, err = m.Preflight(context.Background(), bad)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0].Key) != "u:"+sysOwner || got[0].Reason != reasonSystemName || got[0].Fix != fixHint {
		t.Fatalf("blockers = %+v, want one on u:%s with %q", got, sysOwner, reasonSystemName)
	}
}
