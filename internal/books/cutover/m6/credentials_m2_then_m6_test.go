// file: internal/books/cutover/m6/credentials_m2_then_m6_test.go
// version: 0.1.0
// guid: 6a2d9f41-8c03-4e57-b1d6-5f7e0c3a8b92
// last-edited: 2026-11-28

package m6_test

import (
	"maps"
	"testing"

	"github.com/falkcorp/mediacore/secrets"
)

// The operator layer stopped being a way to supply a metadata source's
// credential (cutover.RefusedOperatorPaths), so the stored secret is the only
// place it can live. These cells prove it survives the flip for every source
// HEAD knows, for an install that runs M2 and M6 back to back: the blob holds the
// credentials, M2's own planner moves them into the store under
// integrations.metadata.<id>.credentials.<k>, and M6 binds what the adapter
// declares and keeps (quarantined and reported, never deleted) what it does not.
//
// The same six sources, started from M2's output with no blob credential at all
// (what a prod host that already ran M2 holds), are
// TestMetadataCredentials_FoldRules' cases: google-books (two rules), hardcover
// (four), openlibrary, audible, audnexus and wikipedia.

// TestMetadataCredentials_M2ThenM6_EverySourceKeepsItsCredential: per source, one
// credential in the blob, then M2, then M6.
func TestMetadataCredentials_M2ThenM6_EverySourceKeepsItsCredential(t *testing.T) {
	cases := []struct {
		id     string
		secret string
		// field is the secret field the source's adapter declares, "" for none.
		field string
	}{
		{"google-books", "fixture-gb-key-oracle", "api_key"},
		{"hardcover", "fixture-hc-token-robin", "api_token"},
		{"openlibrary", "fixture-ol-key-azrael", ""},
		{"audible", "fixture-au-key-gordon", ""},
		{"audnexus", "fixture-an-key-gordon", ""},
		{"wikipedia", "fixture-wp-key-gordon", ""},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			edit := func(tree map[string]any) {
				srcs := tree["metadata_sources"].([]any)
				have := map[string]bool{}
				for _, s := range srcs {
					have[s.(map[string]any)["id"].(string)] = true
				}
				for i, id := range extraSources {
					if !have[id] {
						srcs = append(srcs, map[string]any{"id": id, "name": id, "enabled": true, "priority": 10 + i})
					}
				}
				for _, s := range srcs {
					if m := s.(map[string]any); m["id"] == tc.id {
						m["credentials"] = map[string]any{"apiKey": tc.secret}
					}
				}
				tree["metadata_sources"] = srcs
			}
			r, s := runFixture(t, withBlob(edit), afterM2)
			stored := credName(tc.id, "api_key")
			if got, found := storedSecret(r, s, stored); !found || got != tc.secret {
				t.Fatalf("the credential is not in the store under %s after M2 then M6 (found %v)", stored, found)
			}
			row, ok := instances(t, s)["metadata/"+tc.id]
			if !ok {
				t.Fatalf("no metadata/%s instance", tc.id)
			}
			refs := secretSettings(row)
			if tc.field == "" {
				if len(refs) != 0 {
					t.Errorf("%s declares no secret field but is bound to %v", tc.id, refs)
				}
				if got := unreadCredentials(t, s, tc.id); !maps.Equal(got, map[string]string{"credentials.api_key": string(stored)}) {
					t.Errorf("the credential is neither bound nor kept quarantined: %v", got)
				}
				return
			}
			// The declared field is bound to a stored secret that holds exactly the
			// credential the blob held (the fold copies it to the declared name).
			name, bound := refs[tc.field]
			if !bound {
				t.Fatalf("%s's declared %s is not bound: %v", tc.id, tc.field, refs)
			}
			if got, found := storedSecret(r, s, secrets.Name(name)); !found || got != tc.secret {
				t.Errorf("the bound secret %s does not hold the blob's credential (found %v)", name, found)
			}
			if row["secrets_origin"] == nil || row["secrets_origin"] == "" {
				t.Error("the bound secret has no secrets_origin")
			}
			noSecretValue(t, r)
		})
	}
}

// TestMetadataCredentials_M2ThenM6_RenamedSourceKeepsItsCredential: a source whose
// HEAD id was not the instance id (google_books for google-books) got no credential
// at HEAD, and M6 does not bind it now, but the stored secret is not lost: it stays
// under its M2 name and is quarantined and reported for re-entry.
func TestMetadataCredentials_M2ThenM6_RenamedSourceKeepsItsCredential(t *testing.T) {
	const secret = "fixture-renamed-key-nightwing"
	r, s := runFixture(t, withBlob(func(tree map[string]any) {
		srcs := tree["metadata_sources"].([]any)
		m := srcs[1].(map[string]any)
		m["id"] = "google_books"
		m["credentials"] = map[string]any{"apiKey": secret}
	}), afterM2)
	old := secrets.Name("integrations.metadata.google_books.credentials.api_key")
	if got, found := storedSecret(r, s, old); !found || got != secret {
		t.Fatalf("the renamed source's credential is not in the store under %s (found %v)", old, found)
	}
	if got := secretSettings(instances(t, s)["metadata/google-books"]); len(got) != 0 {
		t.Errorf("the renamed source is bound to %v", got)
	}
	if got := unreadCredentials(t, s, "google-books"); !maps.Equal(got, map[string]string{"credentials.api_key": string(old)}) {
		t.Errorf("the renamed source's credential is not quarantined by name: %v", got)
	}
	noSecretValue(t, r)
}
