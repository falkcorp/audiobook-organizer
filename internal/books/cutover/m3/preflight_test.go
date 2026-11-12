// file: internal/books/cutover/m3/preflight_test.go
// version: 1.0.0
// guid: ae479a7b-22a6-46d3-b19b-a3a77d3dcf0c
// last-edited: 2026-11-12

package m3

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// A password hash no real credential has (the cutover package's fixture).
const unitArgon = "$argon2id$v=19$m=65536,t=3,p=4$YmF0Y2F2ZS1zYWx0$YmF0bW9iaWxlLXRlc3QtaGFzaA"

func unitKeyRow(id, owner, hash string) string {
	return `{"id":"` + id + `","user_id":"` + owner + `","name":"n","token_hash":"` + hash + `","scopes":["library.view"],"status":"active",` +
		`"created_at":"2026-11-10T11:00:00Z","expires_at":"2027-11-10T11:00:00Z"}`
}

func unitIdentityRow(id, provider, subject, owner string) string {
	return `{"id":"` + id + `","provider":"` + provider + `","subject":"` + subject + `","user_id":"` + owner + `","created_at":"2026-11-10T11:00:00Z"}`
}

// TestPreflight_DuplicateRows: HEAD can hold two keys of one token hash and
// two identities of one (provider, subject), and the importers refuse the
// second of each. The preflight holds back the later row in key order and
// names the first by ID, never by the value they share. A row that is blocked
// for another reason does not hold the value, and the same subject under
// another provider is not a twin.
func TestPreflight_DuplicateRows(t *testing.T) {
	const (
		owner   = "01M7ZZZPREUNITOWNER0000001"
		keyA    = "01M7ZZZPREUNITKEYA00000001"
		keyB    = "01M7ZZZPREUNITKEYB00000002"
		keyC    = "01M7ZZZPREUNITKEYC00000003"
		keyX    = "01M7ZZZPREUNITKEYX00000004"
		keyY    = "01M7ZZZPREUNITKEYY00000005"
		idA     = "01M7ZZZPREUNITIDNA00000001"
		idB     = "01M7ZZZPREUNITIDNB00000002"
		idC     = "01M7ZZZPREUNITIDNC00000003"
		ghost   = "01M7ZZZPREUNITGHOST0000001"
		subject = "mck-unit-subject"
	)
	shared := strings.Repeat("ab", 32)
	distinct := strings.Repeat("cd", 32)
	heldBack := strings.Repeat("ef", 32)
	rows := memReader{
		"u:" + owner:     `{"id":"` + owner + `","username":"harvey.bullock","password_hash_algo":"argon2id","password_hash":"` + unitArgon + `","roles":["viewer"],"status":"active"}`,
		"apikey:" + keyA: unitKeyRow(keyA, owner, shared),
		"apikey:" + keyB: unitKeyRow(keyB, owner, shared), // the twin of keyA
		"apikey:" + keyC: unitKeyRow(keyC, owner, distinct),
		// keyX has no owner row, so it is blocked and does not hold heldBack: keyY,
		// which carries the same hash and a stored owner, is the one the importer takes.
		"apikey:" + keyX: unitKeyRow(keyX, ghost, heldBack),
		"apikey:" + keyY: unitKeyRow(keyY, owner, heldBack),
		"oauthid:" + idA: unitIdentityRow(idA, "github", subject, owner),
		"oauthid:" + idB: unitIdentityRow(idB, "github", subject, owner), // the twin of idA
		"oauthid:" + idC: unitIdentityRow(idC, "gitlab", subject, owner), // another provider: not a twin
	}
	m := testMigrationOver(t, noNative{}, func() storage.Reader { return rows })
	got, err := m.Preflight(context.Background(), rows)
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ key, family, reason string }
	wants := []want{
		{"apikey:" + keyB, "apikey:", "the token hash is held by key " + keyA},
		{"apikey:" + keyX, "apikey:", "owner is not stored"},
		{"oauthid:" + idB, "oauthid:", "provider and subject are held by identity " + idA},
	}
	if len(got) != len(wants) {
		t.Fatalf("blockers %d, want %d: %+v", len(got), len(wants), got)
	}
	for i, w := range wants {
		b := got[i]
		if string(b.Key) != w.key || b.Family != w.family || !strings.Contains(b.Reason, w.reason) {
			t.Errorf("blocker %d = {%s %s %q}, want {%s %s ...%q...}", i, b.Key, b.Family, b.Reason, w.key, w.family, w.reason)
		}
		if b.Fix != fixHint {
			t.Errorf("blocker %d fix %q, want %q: fix and delete are offered", i, b.Fix, fixHint)
		}
		for _, v := range []string{shared, subject, heldBack} {
			if strings.Contains(b.Reason, v) {
				t.Errorf("blocker %d reason carries the value %q", i, v)
			}
		}
	}
}
