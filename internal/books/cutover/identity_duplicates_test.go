// file: internal/books/cutover/identity_duplicates_test.go
// version: 1.0.0
// guid: 107425a6-ce13-45ab-bcb9-bacaabd1f6d8
// last-edited: 2026-11-12

package cutover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// twinCell is one kind of row HEAD can hold twice and the importers refuse
// the second of (authstore's ImportAPIKey: a token hash that is stored;
// ImportIdentity: a provider and subject that is stored; a twin staged in the
// same chunk is a double write): the rows, the twin the preflight holds back,
// a replacement for it that no longer shares the value, and what a boot that
// completes must have imported.
type twinCell struct {
	name   string
	rows   []seedRow
	later  string // the key of the twin held back
	repair []byte // a row for that key that does not share the value
	// imported checks the store after a completed boot: the first twin is
	// always imported, the later one only when it was repaired.
	imported func(t *testing.T, s *idSnap, repaired bool)
}

func twinCells() []twinCell {
	return []twinCell{
		{
			name: "two keys of one token hash",
			rows: twinKeys(), later: "apikey:" + pTwinKeyB,
			repair: mustMarshal(map[string]any{"id": pTwinKeyB, "user_id": uBarbara, "name": "repaired", "token_hash": idDigest("mck-twin-hash-0002"),
				"scopes": []string{"library.view"}, "status": "active", "created_at": "2026-11-10T11:00:00Z", "expires_at": "2027-11-10T11:00:00Z"}),
			imported: func(t *testing.T, s *idSnap, repaired bool) {
				if _, ok := s.Keys[pTwinKeyA]; !ok {
					t.Errorf("the first twin %s was not imported", pTwinKeyA)
				}
				if _, ok := s.Keys[pTwinKeyB]; ok != repaired {
					t.Errorf("the later twin %s imported = %v, want %v", pTwinKeyB, ok, repaired)
				}
			},
		},
		{
			name: "two identities of one provider and subject",
			rows: twinIdentities(), later: "oauthid:" + pTwinIdB,
			repair: mustMarshal(map[string]any{"id": pTwinIdB, "provider": "github", "subject": pTwinSubject + "-2", "email": "", "user_id": uBarbara, "created_at": "2026-11-10T11:00:00Z"}),
			imported: func(t *testing.T, s *idSnap, repaired bool) {
				if _, ok := s.Idents["github/"+pTwinSubject]; !ok {
					t.Errorf("the first twin was not imported")
				}
				if _, ok := s.Idents["github/"+pTwinSubject+"-2"]; ok != repaired {
					t.Errorf("the repaired twin imported = %v, want %v", ok, repaired)
				}
			},
		},
	}
}

// rowValue is the JSON of the seeded row at key.
func rowValue(t testing.TB, rows []seedRow, key string) []byte {
	t.Helper()
	for _, r := range rows {
		if r.key == key {
			raw, err := json.Marshal(r.val)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
	}
	t.Fatalf("no seeded row at %s", key)
	return nil
}

// TestIdentityPreflight_DuplicatesStopBeforeTheStepRuns: HEAD can hold two
// keys of one token hash and two identities of one provider and subject
// (neither CreateAPIKey nor CreateOAuthIdentity checks for a twin, and a
// concurrent first sign-in creates one). The importers refuse the second, so a
// preflight that let it through would fail the step at every boot with
// nothing for storage cutover-blockers to list. The boot is refused in the
// preflight instead: exit 78, the later twin named and the first named by ID,
// the step never run, the store byte-identical.
func TestIdentityPreflight_DuplicatesStopBeforeTheStepRuns(t *testing.T) {
	for _, c := range twinCells() {
		t.Run(c.name, func(t *testing.T) {
			r := newIDRig(t, "stage1", idRows(c.rows...))
			mem := r.newMem(t)
			before := r.logicalState(t, mem)
			dm := r.migration()
			var steps atomic.Int64
			inner := dm.Step
			dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
				steps.Add(1)
				return inner(ctx, kv, w)
			}
			err := r.boot(context.Background(), mem, []datamigrate.DataMigration{dm}, nil)
			var cbe *storage.CutoverBlockedError
			if !errors.Is(err, storage.ErrCutoverBlocked) || !errors.As(err, &cbe) {
				t.Fatalf("the boot returned %v, want storage.ErrCutoverBlocked\n%s", err, r.logs.String())
			}
			if code := kernel.ExitCode(err); code != 78 {
				t.Errorf("the refusal exits %d, want 78", code)
			}
			if n := steps.Load(); n != 0 {
				t.Errorf("the step ran %d times: the preflight must refuse the boot first", n)
			}
			if len(cbe.Blockers) != 1 || string(cbe.Blockers[0].Key) != c.later {
				t.Fatalf("blockers %+v, want exactly the later twin %s", cbe.Blockers, c.later)
			}
			if after := r.logicalState(t, mem); !digestsEqual(before, after) {
				t.Errorf("the refused boot changed the store: %s", digestDiff(before, after))
			}
		})
	}
}

// TestCutoverBlockers_DuplicateRows_FixAndDelete: the held-back twin is offered
// fix and delete. A fix that keeps the shared value is refused, one that gives
// the twin its own value clears the blocker and both rows import; a delete of
// the twin leaves the first imported and the twin gone.
func TestCutoverBlockers_DuplicateRows_FixAndDelete(t *testing.T) {
	for _, c := range twinCells() {
		t.Run(c.name+"/fix", func(t *testing.T) {
			r := newIDRig(t, "stage1", idRows(c.rows...))
			mem := r.newMem(t)
			s := r.beginBlockers(t, mem)
			got := s.list()
			if len(got) != 1 || string(got[0].Key) != c.later {
				t.Fatalf("the report names %+v, want exactly %s", got, c.later)
			}
			if !offers(got[0].Fix, "fix") || !offers(got[0].Fix, "delete") {
				t.Errorf("fix text %q does not offer fix and delete", got[0].Fix)
			}
			if _, err := s.edit(storage.CutoverBlockerFix, []byte(c.later), rowValue(t, c.rows, c.later)); err == nil {
				t.Fatal("a fix that keeps the shared value was accepted")
			}
			if _, err := s.edit(storage.CutoverBlockerFix, []byte(c.later), c.repair); err != nil {
				t.Fatalf("fix: %v", err)
			}
			if left := s.list(); len(left) != 0 {
				t.Fatalf("after the fix the report still names %+v", left)
			}
			s.close()
			snap, err := r.runOn(t, mem)
			if err != nil {
				t.Fatalf("the boot after the fix: %v\n%s", err, r.logs.String())
			}
			c.imported(t, snap, true)
		})
		t.Run(c.name+"/delete", func(t *testing.T) {
			r := newIDRig(t, "stage1", idRows(c.rows...))
			mem := r.newMem(t)
			s := r.beginBlockers(t, mem)
			ids, err := s.edit(storage.CutoverBlockerDelete, []byte(c.later), nil)
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			if len(ids) != 1 || !strings.HasSuffix(c.later, ids[0]) {
				t.Errorf("the delete removed %v, want the twin %s", ids, c.later)
			}
			s.close()
			snap, err := r.runOn(t, mem)
			if err != nil {
				t.Fatalf("the boot after the delete: %v\n%s", err, r.logs.String())
			}
			c.imported(t, snap, false)
			if hasPrefix(snap.Legacy, c.later) {
				t.Errorf("the deleted twin %s is still in the store", c.later)
			}
		})
	}
}

// TestCutoverBlockers_UserDeleteTakesItsAllowListGrants: core's invite
// allow-list is by e-mail address alone, so the grant HEAD recorded for a user
// (the row's user_id names the user whose accept put the address there) goes
// with the user a delete removes; another user's grant is imported as before.
func TestCutoverBlockers_UserDeleteTakesItsAllowListGrants(t *testing.T) {
	const (
		blocked  = "01M7ZZZGRANTUSER0000000001"
		goneMail = "two-face@gotham.invalid"
		keptMail = "harvey.bullock@gotham.invalid"
	)
	r := newIDRig(t, "stage1", idRows(
		// A user the importer rejects (a blank username), with the grant its accept recorded.
		seedRow{"u:" + blocked, map[string]any{"id": blocked, "username": " ", "email": goneMail, "password_hash_algo": "argon2id", "password_hash": idArgon,
			"roles": []string{"viewer"}, "status": "active", "created_at": "2026-11-10T11:00:00Z", "updated_at": "2026-11-10T11:00:00Z", "version": 1}},
		seedRow{"inviteallow:" + goneMail, map[string]any{"user_id": blocked, "invite_token": "mck-grant-token-1", "granted_at": "2026-11-10T11:00:00Z"}},
		seedRow{"inviteallow:" + keptMail, map[string]any{"user_id": uBarbara, "granted_at": "2026-11-10T11:00:00Z"}},
	))
	mem := r.newMem(t)
	s := r.beginBlockers(t, mem)
	if got := s.list(); len(got) != 1 || string(got[0].Key) != "u:"+blocked {
		t.Fatalf("the report names %+v, want exactly the blank-username user", got)
	}
	if _, err := s.edit(storage.CutoverBlockerDelete, []byte("u:"+blocked), nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	s.close()
	snap, err := r.runOn(t, mem)
	if err != nil {
		t.Fatalf("the boot after the delete: %v\n%s", err, r.logs.String())
	}
	for _, a := range snap.Allow {
		if a == goneMail {
			t.Errorf("the deleted user's address %s was imported onto the allow-list", goneMail)
		}
	}
	var kept bool
	for _, a := range snap.Allow {
		kept = kept || a == keptMail
	}
	if !kept {
		t.Errorf("another user's grant %s was not imported: %v", keptMail, snap.Allow)
	}
}

// dropFamilies deletes every row of the given prefixes from the seeded store.
func dropFamilies(prefixes ...string) func(*idRig) {
	return func(r *idRig) {
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(t, lr)
			}
			ctx := context.Background()
			var doomed [][]byte
			for kv, err := range lr.Scan(ctx, storage.ScanOptions{}) {
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range prefixes {
					if strings.HasPrefix(string(kv.Key), p) {
						doomed = append(doomed, bytes.Clone(kv.Key))
						break
					}
				}
			}
			for _, k := range doomed {
				if err := lr.Delete(ctx, k); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// TestIdentityCutover_FirstItemIsARoleOrUserRow: the first item of a chunk
// pulls the chunk's roles and users forward and deletes their rows, so a store
// in which no row sorts before the first role (or the first user) hands that
// very row to Step as the chunk's first item. begin has just handled it; the
// step must accept it, not fail with "was not pulled forward".
func TestIdentityCutover_FirstItemIsARoleOrUserRow(t *testing.T) {
	// Source order is apikey:, the idx: children, invite:, inviteallow:, oauthid:, role:,
	// sess:, the two setting: rows, u:.
	for _, c := range []struct {
		name string
		drop []string
	}{
		{"a role row", []string{"apikey:", "idx:user:", "idx:role:", "idx:apikey:", "idx:invite:", "idx:oauth:", "idx:sess:", "invite:", "inviteallow:", "oauthid:"}},
		{"a user row", []string{"apikey:", "idx:user:", "idx:role:", "idx:apikey:", "idx:invite:", "idx:oauth:", "idx:sess:", "invite:", "inviteallow:", "oauthid:", "role:", "sess:", "setting:bootstrap_token"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newIDRig(t, "stage1", dropFamilies(c.drop...))
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("the boot: %v\n%s", err, r.logs.String())
			}
			if _, ok := snap.Users[uBarbara]; !ok {
				t.Errorf("user %s was not imported: %v", uBarbara, mapKeys(snap.Users))
			}
			if left := legacyIDs(snap.Legacy, "u:"); len(left) != 0 {
				t.Errorf("user rows survive the step: %v", left)
			}
			if snap.State != "done" {
				t.Errorf("state %q, want done", snap.State)
			}
		})
	}
}
