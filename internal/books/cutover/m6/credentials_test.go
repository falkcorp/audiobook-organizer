// file: internal/books/cutover/m6/credentials_test.go
// version: 1.0.0
// guid: 0e8f4a62-b7d1-4c35-9a06-5d2c18e7f349

package m6_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/mediacore/secrets"
)

// rUnreadCredential mirrors m6's reasonUnreadCredential: the quarantine reason of
// a secret setting M6 leaves unbound.
const rUnreadCredential = "a secret setting of a metadata source that HEAD never read for it, or that another of its secrets overrode, and its adapter declares no field for: left unbound, its secret stays in the store"

// The credentials of a metadata source. M2 names them
// integrations.metadata.<id>.credentials.<snake k>; core's settings schema is
// closed and a books adapter declares only api_key (google-books) and api_token
// (hardcover), so a dotted setting makes the instance HealthMisconfigured and the
// whole source goes dark. M6 folds the credential HEAD actually sent into the
// declared field and drops, quarantines and reports the rest.

func credName(id, k string) secrets.Name {
	return secrets.Name("integrations.metadata." + id + ".credentials." + k)
}

func fieldName(id, field string) secrets.Name {
	return secrets.Name("integrations.metadata." + id + "." + field)
}

// extraSources appended to the fixture's three (hardcover 0, google-books 1,
// openlibrary 2), by blob position.
var extraSources = []string{"audible", "audnexus", "wikipedia"}

func withExtraSources(r *rig) {
	tree := blobTree()
	srcs := tree["metadata_sources"].([]any)
	for i, id := range extraSources {
		srcs = append(srcs, map[string]any{"id": id, "name": id, "enabled": true, "priority": 10 + i})
	}
	tree["metadata_sources"] = srcs
	withRows(map[string][]byte{m6.KeyBlob: blobRow(r.t, tree)})(r)
}

// memberIndex is a source's position in the blob's metadata_sources.
func memberIndex(id string) int {
	return slices.Index(append([]string{"hardcover", "google-books", "openlibrary"}, extraSources...), id)
}

// withSecrets drops the named seeded secrets and adds the others.
func withSecrets(add map[secrets.Name]string, drop ...secrets.Name) func(*rig) {
	return func(r *rig) {
		for _, n := range drop {
			delete(r.secrets.seed, n)
		}
		maps.Copy(r.secrets.seed, add)
	}
}

// unreadCredentials is the quarantine records of one source's secret settings, by
// the field they were bound as, each holding the secret's NAME and never its value.
func unreadCredentials(t *testing.T, s *snap, id string) map[string]string {
	t.Helper()
	prefix := m6.KeyBlob + "#metadata_sources." + strconv.Itoa(memberIndex(id)) + ".secrets."
	out := map[string]string{}
	for _, q := range s.Quarantine {
		field, ok := strings.CutPrefix(q.Key, prefix)
		if !ok {
			continue
		}
		if q.Reason != rUnreadCredential {
			t.Errorf("%s: quarantined as %q, want the unread-credential reason", q.Key, q.Reason)
		}
		if !q.Sealed {
			t.Errorf("%s: a record of the config blob is not sealed", q.Key)
		}
		var name string
		if err := json.Unmarshal([]byte(q.Value), &name); err != nil {
			t.Errorf("%s: holds %q, not a JSON string: %v", q.Key, q.Value, err)
		}
		out[field] = name
	}
	return out
}

// secretSettings are the settings of an instance that reference a secret, by
// field.
func secretSettings(row map[string]any) map[string]string {
	out := map[string]string{}
	for k := range settingsOf(row) {
		if n := refName(settingsOf(row), k); n != "" {
			out[k] = n
		}
	}
	return out
}

// TestMetadataCredentials_FoldRules: every rule of the credential fold, each
// through a full M6 run. HEAD (internal/metafetch/service_search.go) read
// google-books' operator key then credentials["apiKey"], and hardcover's
// operator token, then credentials["api_token"], then credentials["apiKey"], and
// no other source read credentials at all.
func TestMetadataCredentials_FoldRules(t *testing.T) {
	const gbKey, hcTok = "fixture-google-key-oracle", "fixture-hardcover-token-robin"
	cases := []struct {
		name string
		id   string
		opts []func(*rig)
		// refs is every secret setting the instance must hold afterwards.
		refs map[string]secrets.Name
		// stored is a secret row that must hold exactly this value afterwards.
		stored map[secrets.Name]string
		// quarantined is the secret settings left unbound, by field, each with the
		// secret it named.
		quarantined map[string]secrets.Name
		folded      secrets.Name // the credential reported folded, or ""
	}{
		{
			name: "google-books: the operator key wins over credentials.apiKey",
			id:   "google-books",
			opts: []func(*rig){withSecrets(map[secrets.Name]string{credName("google-books", "api_key"): "fixture-shadowed-key-penguin"})},
			refs: map[string]secrets.Name{"api_key": fieldName("google-books", "api_key")},
			stored: map[secrets.Name]string{
				fieldName("google-books", "api_key"): gbKey,
				credName("google-books", "api_key"):  "fixture-shadowed-key-penguin",
			},
			quarantined: map[string]secrets.Name{"credentials.api_key": credName("google-books", "api_key")},
		},
		{
			name: "google-books: credentials.apiKey is the key when there is no operator key",
			id:   "google-books",
			opts: []func(*rig){withSecrets(map[secrets.Name]string{credName("google-books", "api_key"): "fixture-credential-key-riddler"}, fieldName("google-books", "api_key"))},
			refs: map[string]secrets.Name{"api_key": fieldName("google-books", "api_key")},
			stored: map[secrets.Name]string{
				fieldName("google-books", "api_key"): "fixture-credential-key-riddler",
				credName("google-books", "api_key"):  "fixture-credential-key-riddler",
			},
			folded: credName("google-books", "api_key"),
		},
		{
			name: "hardcover: the operator token wins over both credentials",
			id:   "hardcover",
			opts: []func(*rig){withSecrets(map[secrets.Name]string{
				credName("hardcover", "api_token"): "fixture-cred-token-joker",
				credName("hardcover", "api_key"):   "fixture-cred-key-bane",
			})},
			refs: map[string]secrets.Name{"api_token": fieldName("hardcover", "api_token")},
			stored: map[secrets.Name]string{
				fieldName("hardcover", "api_token"): hcTok,
				credName("hardcover", "api_token"):  "fixture-cred-token-joker",
				credName("hardcover", "api_key"):    "fixture-cred-key-bane",
			},
			quarantined: map[string]secrets.Name{
				"credentials.api_token": credName("hardcover", "api_token"),
				"credentials.api_key":   credName("hardcover", "api_key"),
			},
		},
		{
			name: "hardcover: credentials.api_token beats credentials.apiKey when there is no operator token",
			id:   "hardcover",
			opts: []func(*rig){withSecrets(map[secrets.Name]string{
				credName("hardcover", "api_token"): "fixture-cred-token-joker",
				credName("hardcover", "api_key"):   "fixture-cred-key-bane",
			}, fieldName("hardcover", "api_token"))},
			refs: map[string]secrets.Name{"api_token": fieldName("hardcover", "api_token")},
			stored: map[secrets.Name]string{
				fieldName("hardcover", "api_token"): "fixture-cred-token-joker",
				credName("hardcover", "api_key"):    "fixture-cred-key-bane",
			},
			quarantined: map[string]secrets.Name{"credentials.api_key": credName("hardcover", "api_key")},
			folded:      credName("hardcover", "api_token"),
		},
		{
			name: "hardcover: credentials.apiKey alone is the token",
			id:   "hardcover",
			opts: []func(*rig){withSecrets(map[secrets.Name]string{credName("hardcover", "api_key"): "fixture-cred-key-bane"}, fieldName("hardcover", "api_token"))},
			refs: map[string]secrets.Name{"api_token": fieldName("hardcover", "api_token")},
			stored: map[secrets.Name]string{
				fieldName("hardcover", "api_token"): "fixture-cred-key-bane",
			},
			folded: credName("hardcover", "api_key"),
		},
		{
			name: "hardcover: credentials.api_token alone is the token",
			id:   "hardcover",
			opts: []func(*rig){withSecrets(map[secrets.Name]string{credName("hardcover", "api_token"): "fixture-cred-token-joker"}, fieldName("hardcover", "api_token"))},
			refs: map[string]secrets.Name{"api_token": fieldName("hardcover", "api_token")},
			stored: map[secrets.Name]string{
				fieldName("hardcover", "api_token"): "fixture-cred-token-joker",
			},
			folded: credName("hardcover", "api_token"),
		},
		{
			// The fixture's own credentials.token: HEAD never sent a credential to
			// Open Library, and its adapter declares no secret field.
			name:        "openlibrary: credentials.token is dropped, its secret stays",
			id:          "openlibrary",
			refs:        map[string]secrets.Name{},
			stored:      map[secrets.Name]string{credName("openlibrary", "token"): "fixture-openlibrary-token-azrael"},
			quarantined: map[string]secrets.Name{"credentials.token": credName("openlibrary", "token")},
		},
		{
			name:        "audible: credentials.api_key is dropped",
			id:          "audible",
			opts:        []func(*rig){withExtraSources, withSecrets(map[secrets.Name]string{credName("audible", "api_key"): "fixture-audible-key-gordon"})},
			refs:        map[string]secrets.Name{},
			stored:      map[secrets.Name]string{credName("audible", "api_key"): "fixture-audible-key-gordon"},
			quarantined: map[string]secrets.Name{"credentials.api_key": credName("audible", "api_key")},
		},
		{
			name:        "audnexus: credentials.api_key is dropped",
			id:          "audnexus",
			opts:        []func(*rig){withExtraSources, withSecrets(map[secrets.Name]string{credName("audnexus", "api_key"): "fixture-audnexus-key-gordon"})},
			refs:        map[string]secrets.Name{},
			stored:      map[secrets.Name]string{credName("audnexus", "api_key"): "fixture-audnexus-key-gordon"},
			quarantined: map[string]secrets.Name{"credentials.api_key": credName("audnexus", "api_key")},
		},
		{
			name:        "wikipedia: credentials.api_key is dropped",
			id:          "wikipedia",
			opts:        []func(*rig){withExtraSources, withSecrets(map[secrets.Name]string{credName("wikipedia", "api_key"): "fixture-wikipedia-key-gordon"})},
			refs:        map[string]secrets.Name{},
			stored:      map[secrets.Name]string{credName("wikipedia", "api_key"): "fixture-wikipedia-key-gordon"},
			quarantined: map[string]secrets.Name{"credentials.api_key": credName("wikipedia", "api_key")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, s := runFixture(t, tc.opts...)
			row, ok := instances(t, s)["metadata/"+tc.id]
			if !ok {
				t.Fatalf("no metadata/%s instance", tc.id)
			}
			if got := secretSettings(row); !maps.Equal(got, func() map[string]string {
				w := map[string]string{}
				for k, n := range tc.refs {
					w[k] = string(n)
				}
				return w
			}()) {
				t.Errorf("secret settings %v, want %v", got, tc.refs)
			}
			for k := range settingsOf(row) {
				if strings.Contains(k, ".") {
					t.Errorf("the instance holds the dotted setting %q, which its adapter cannot declare", k)
				}
			}
			for n, want := range tc.stored {
				if got, found := storedSecret(r, s, n); !found || got != want {
					t.Errorf("secret %s holds %q (found %v), want %q", n, got, found, want)
				}
			}
			gotQ := unreadCredentials(t, s, tc.id)
			wantQ := map[string]string{}
			for f, n := range tc.quarantined {
				wantQ[f] = string(n)
			}
			if !maps.Equal(gotQ, wantQ) {
				t.Errorf("quarantined secret settings %v, want %v", gotQ, wantQ)
			}
			text := reportText(r)
			noSecretValue(t, r)
			if tc.folded != "" {
				want := "the credential " + string(tc.folded) + " is folded into "
				if !strings.Contains(text, want) {
					t.Errorf("the fold is not reported (want %q):\n%s", want, text)
				}
				if row["secrets_origin"] == nil || row["secrets_origin"] == "" {
					t.Error("the folded secret is bound with no secrets_origin")
				}
			} else if strings.Contains(text, "is folded into") {
				t.Errorf("a fold is reported where none happened:\n%s", text)
			}
			for f, n := range tc.quarantined {
				want := "dropped: secret setting " + f + " is not bound"
				if !strings.Contains(text, want) || !strings.Contains(text, string(n)) {
					t.Errorf("the dropped setting %s is not reported:\n%s", f, text)
				}
			}
		})
	}
}

// TestMetadataCredentials_RenamedSourceFoldsNothing: a source whose HEAD ID was
// not the instance ID was an unknown source at HEAD (its provider switch is exact
// and case-sensitive), so HEAD sent it no credential: its credentials.<k> is
// dropped, never folded into the declared field, and its credential is not
// carried to the new name either.
func TestMetadataCredentials_RenamedSourceFoldsNothing(t *testing.T) {
	const old = "integrations.metadata.google_books.credentials.api_key"
	r, s := runFixture(t, withBlob(func(tree map[string]any) {
		srcs := tree["metadata_sources"].([]any)
		srcs[1].(map[string]any)["id"] = "google_books"
	}), withSecrets(map[secrets.Name]string{old: "fixture-renamed-key-nightwing"}, fieldName("google-books", "api_key")))
	row, ok := instances(t, s)["metadata/google-books"]
	if !ok {
		t.Fatal("no metadata/google-books instance")
	}
	if got := secretSettings(row); len(got) != 0 {
		t.Errorf("the renamed source holds secret settings %v", got)
	}
	if v := s.Rows[string(ownerSecrets)][string(fieldName("google-books", "api_key"))]; v != nil {
		t.Errorf("a credential was copied to the declared name for a source HEAD sent none: %q", v)
	}
	if got := unreadCredentials(t, s, "google-books"); !maps.Equal(got, map[string]string{"credentials.api_key": old}) {
		t.Errorf("quarantined secret settings %v, want credentials.api_key naming %s", got, old)
	}
	if strings.Contains(reportText(r), "is folded into") {
		t.Errorf("a fold is reported for a renamed source:\n%s", reportText(r))
	}
}

// TestMetadataCredentials_UnreadableCredentialFoldsAsUnreadable: a credential M2
// could not read is still the key HEAD would have read, so it is folded as an
// unreadable marker at the declared name (never a copy of bytes there are none of)
// and the report says to re-enter it.
func TestMetadataCredentials_UnreadableCredentialFoldsAsUnreadable(t *testing.T) {
	cred := credName("hardcover", "api_key")
	r, s := runFixture(t, withSecrets(nil, fieldName("hardcover", "api_token")), func(r *rig) { r.secrets.unreadable[cred] = true })
	row := instances(t, s)["metadata/hardcover"]
	want := fieldName("hardcover", "api_token")
	if g := refName(settingsOf(row), "api_token"); g != string(want) {
		t.Fatalf("api_token refs %q, want %q", g, want)
	}
	if v := string(s.Rows[string(ownerSecrets)][string(want)]); v != unreadableMarker+"corrupt" {
		t.Errorf("the declared name holds %q, want the corrupt unreadable marker", v)
	}
	if !strings.Contains(reportText(r), "is unreadable and is folded into api_token as unreadable: re-enter it") {
		t.Errorf("the unreadable fold is not reported:\n%s", reportText(r))
	}
}

// TestMetadataCredentials_NoCredentialsIsUntouched: a source with only the
// operator's secret (the fixture's hardcover and google-books) keeps exactly the
// refs it had and quarantines nothing, so the fold changes no source that never
// had a credentials map.
func TestMetadataCredentials_NoCredentialsIsUntouched(t *testing.T) {
	_, s := runFixture(t)
	got := instances(t, s)
	for id, want := range map[string]map[string]string{
		"hardcover":    {"api_token": string(fieldName("hardcover", "api_token"))},
		"google-books": {"api_key": string(fieldName("google-books", "api_key"))},
	} {
		if g := secretSettings(got["metadata/"+id]); !maps.Equal(g, want) {
			t.Errorf("%s: secret settings %v, want %v", id, g, want)
		}
		if g := unreadCredentials(t, s, id); len(g) != 0 {
			t.Errorf("%s: quarantined %v though it had no credentials", id, g)
		}
	}
}

// TestMetadataCredentials_ReportNamesNoValue: a fold and a drop are reported by
// secret NAME; no value of any secret the run held is in the report.
func TestMetadataCredentials_ReportNamesNoValue(t *testing.T) {
	r, _ := runFixture(t, withSecrets(map[secrets.Name]string{
		credName("hardcover", "api_key"):    "fixture-cred-key-bane",
		credName("google-books", "api_key"): "fixture-shadowed-key-penguin",
	}))
	noSecretValue(t, r)
	if !strings.Contains(reportText(r), "dropped: secret setting credentials.api_key is not bound") {
		t.Errorf("the drops are not reported:\n%s", reportText(r))
	}
}

// storedSecret is the value a secret holds after the run: a row the run wrote, or
// else the seed M2 left.
func storedSecret(r *rig, s *snap, n secrets.Name) (string, bool) {
	if raw, ok := s.Rows[string(ownerSecrets)][string(n)]; ok {
		return string(raw), true
	}
	v, ok := r.secrets.seed[n]
	return v, ok
}

// noSecretValue fails when the report holds any value the rig's store held.
func noSecretValue(t *testing.T, r *rig) {
	t.Helper()
	for _, e := range r.report.Entries() {
		for _, v := range r.secrets.seed {
			if strings.Contains(e.Detail, v) {
				t.Errorf("the report holds a secret value: %v", e)
			}
		}
	}
}
