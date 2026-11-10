// file: internal/books/store/booksclaims_test.go
// version: 1.0.1
// guid: 2f8c4d61-9e0a-4b37-a6d5-3c1e7b9f0a24
// last-edited: 2026-11-09

package store

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// TestABOClaimsMatchOwnershipTable checks books' claims against 05-storage
// §5S.9.1, counting by group rather than against a literal total (DATA-28):
// every registered family except the reserved c/ and m/ is exactly one books
// claim, every claim is ClaimReadWrite at R1, rows 1–7 and only they are
// Serialized, bare-key families are claimed by Key, and the only claims that
// are not families are the table's nested Serialized sub-prefixes and keys,
// each inside a registered family. Core accepts the set (no conflict, every
// prefix in claim grammar).
func TestABOClaimsMatchOwnershipTable(t *testing.T) {
	claims := BooksClaims()
	byName := map[string]storage.Claim{}
	for _, c := range claims {
		name := c.Prefix + c.Key
		if c.Prefix != "" && c.Key != "" {
			t.Errorf("claim %q has both Prefix and Key", name)
		}
		if _, dup := byName[name]; dup {
			t.Errorf("claim %q appears twice", name)
		}
		byName[name] = c
		if c.Mode != storage.ClaimReadWrite {
			t.Errorf("claim %q is %s; every books claim is read-write at R1", name, c.Mode)
		}
	}

	fams := KeyFamilies()
	families := map[string]bool{}
	for _, f := range fams {
		families[f.Prefix] = true
		c, claimed := byName[f.Prefix]
		switch {
		case reservedFamily(f.Prefix):
			if claimed {
				t.Errorf("reserved core family %q is claimed by books", f.Prefix)
			}
		case !claimed:
			t.Errorf("family %q is registered but not claimed: books could not write it", f.Prefix)
		case exactKeyFamily(f.Prefix) != (c.Key != ""):
			t.Errorf("family %q: Key claim = %v, want %v (a bare key is claimed by Key)", f.Prefix, c.Key != "", exactKeyFamily(f.Prefix))
		}
	}
	for _, k := range []string{"narrator_counter", "sync_alias_use_seed_cutoff"} {
		if c := byName[k]; c.Key != k {
			t.Errorf("bare key %q is not an exact-key claim: %+v", k, c)
		}
	}

	// The nested claims that are not families: each inside a registered
	// books family, and Serialized.
	var nested []string
	for name, c := range byName {
		if families[name] {
			continue
		}
		nested = append(nested, name)
		parent := ""
		for f := range families {
			if !reservedFamily(f) && f != name && strings.HasPrefix(name, f) && len(f) > len(parent) {
				parent = f
			}
		}
		if parent == "" {
			t.Errorf("claim %q is neither a registered family nor nested in one", name)
		}
		if !c.Serialized {
			t.Errorf("nested claim %q is not Serialized; only the table's Serialized sub-claims are added", name)
		}
	}

	// Rows 1–7: exactly these are Serialized.
	wantSerialized := []string{
		"u:", "idx:user:", // row 1
		"role:", "idx:role:name:", // row 2
		"apikey:", "idx:apikey:", // row 3
		"invite:", "idx:invite:username:", // row 4a
		"inviteallow:",           // row 4b
		"oauthid:", "idx:oauth:", // row 5
		"sess:", "idx:sess:user:", // row 6
		"setting:config_blob", "setting:openai_api_key", "setting:google_books_api_key", // row 7
		"setting:hardcover_api_token", "setting:basic_auth_password",
	}
	var gotSerialized []string
	for name, c := range byName {
		if c.Serialized {
			gotSerialized = append(gotSerialized, name)
		}
	}
	slices.Sort(gotSerialized)
	slices.Sort(wantSerialized)
	if !slices.Equal(gotSerialized, wantSerialized) {
		t.Errorf("Serialized claims = %q, want rows 1–7 = %q", gotSerialized, wantSerialized)
	}
	slices.Sort(nested)
	var wantNested []string
	for _, n := range wantSerialized {
		if !families[n] {
			wantNested = append(wantNested, n)
		}
	}
	slices.Sort(wantNested)
	if !slices.Equal(nested, wantNested) {
		t.Errorf("nested non-family claims = %q, want %q", nested, wantNested)
	}

	// The table's groups, counted at this commit (§5S.9.1 counted at
	// 748fd787a; tests count by group at their base commit, ADV-11). Row 19,
	// "everything else", is the remainder and is not asserted.
	count := func(pred func(string) bool) int {
		n := 0
		for f := range families {
			if !reservedFamily(f) && pred(f) {
				n++
			}
		}
		return n
	}
	has := func(p string) func(string) bool { return func(f string) bool { return strings.HasPrefix(f, p) } }
	in := func(set ...string) func(string) bool { return func(f string) bool { return slices.Contains(set, f) } }
	for _, g := range []struct {
		row  string
		got  int
		want int
	}{
		{"rows 1–6 families", count(in("u:", "role:", "apikey:", "invite:", "inviteallow:", "oauthid:", "sess:")), 7},
		{"row 11 ops run families", count(in("opv2:act:", "opv2:op:", "opv2:state:", "opv3:run:", "opv3:prog:", "opv3:ckpt:", "op:batch:", "opv3:sched:")), 8},
		{"row 12 v1 ops", count(in("opstate:", "op_result:", "opsummary:", "operation:")), 4},
		{"row 13a Requires stamps", count(in("op:completion:", "op:deprev:")), 2},
		{"rows 11–13 ops group (v1 5, op: 4, opv2: 14, opv3: 20)", count(func(f string) bool {
			return in("operation:", "operationlog:", "op_result:", "opstate:", "opsummary:")(f) ||
				has("op:")(f) || has("opv2:")(f) || has("opv3:")(f)
		}), 43},
		{"row 14 syslog", count(in("syslog:")), 1},
		{"row 15 act: and children", count(has("act:")), 13},
		{"row 16 setting:, preference:, pref: (12 at 748fd787a; setting:repairs_last_completed_plan_op: since)", count(func(f string) bool {
			return has("setting:")(f) || has("preference:")(f) || has("pref:")(f)
		}), 13},
		{"row 17 guestcftoken", count(in("guestcftoken:")), 1},
		{"row 18 idx:", count(in("idx:")), 1},
	} {
		if g.got != g.want {
			t.Errorf("%s: %d families, want %d (update the table's count and this test together)", g.row, g.got, g.want)
		}
	}

	// Core accepts the set: no conflict, every prefix in claim grammar.
	op, err := storage.Open(context.Background(), storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "claims.pebble"), FS: storage.MemFS(), Build: "batcomputer-1",
		Owners: []storage.OwnerSpec{{Owner: BooksOwner, Enabled: true, Spec: storage.Spec{Claims: claims}}},
	})
	if err != nil {
		t.Fatalf("core refuses books' claims: %v", err)
	}
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}
}
