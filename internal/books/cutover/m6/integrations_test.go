// file: internal/books/cutover/m6/integrations_test.go
// version: 1.0.1
// guid: 0b7c39d5-6a18-4e2f-9d40-c85a1f2e7b63
// last-edited: 2026-11-19

package m6_test

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/secrets"
)

func runFixture(t testing.TB, opts ...func(*rig)) (*rig, *snap) {
	t.Helper()
	r := newRig(t, append([]func(*rig){withRows(headRows(t))}, opts...)...)
	s, err := r.run(t)
	if err != nil {
		t.Fatalf("M6: %v\nlogs:\n%s", err, r.logs.String())
	}
	if s.State != "done" {
		t.Fatalf("state %q, want done", s.State)
	}
	return r, s
}

// wantInstances is every instance the fixture's HEAD config yields: the three
// metadata sources, the two ai_endpoints rows and the synthesised openai row,
// deluge, and the two webhook URLs.
var wantInstances = []string{
	"ai/local-llm", "ai/local-uv", "ai/openai", "download/deluge",
	"metadata/google-books", "metadata/hardcover", "metadata/openlibrary",
	"notify/webhook-1", "notify/webhook-2",
}

// TestIntegrationsMigration_EveryInstanceAndID (05-metadata M1.9): every HEAD
// source becomes one instance under the kind and ID the spec names, each with
// one audit record, and no instance is invented.
func TestIntegrationsMigration_EveryInstanceAndID(t *testing.T) {
	r, s := runFixture(t)
	got := instances(t, s)
	if ids := slices.Sorted(maps.Keys(got)); !slices.Equal(ids, wantInstances) {
		t.Fatalf("instances %v, want %v", ids, wantInstances)
	}
	creates := 0
	for _, k := range audits(s) {
		if strings.HasPrefix(k, "core.integration.create|") {
			creates++
		}
	}
	if creates != len(wantInstances) {
		t.Errorf("%d create audit records, want %d", creates, len(wantInstances))
	}
	reported := map[string]int{}
	for _, e := range r.report.Entries() {
		reported[e.Kind]++
	}
	if reported["instance"] != len(wantInstances) {
		t.Errorf("report names %d instances, want %d", reported["instance"], len(wantInstances))
	}
	for id, row := range got {
		if row["origin"] != "migration" {
			t.Errorf("%s: origin %v, want migration", id, row["origin"])
		}
	}
	// HEAD's enabled flags and priorities are kept.
	if got["metadata/openlibrary"]["enabled"] != false || got["metadata/hardcover"]["priority"] != float64(1) {
		t.Errorf("enabled or priority lost: %v %v", got["metadata/openlibrary"], got["metadata/hardcover"])
	}
}

// secretRefs are the refs the fixture's instances must carry, by instance and
// settings key: the names M2 stored the credentials under.
var secretRefs = map[string]map[string]string{
	"metadata/hardcover":    {"api_token": "integrations.metadata.hardcover.api_token"},
	"metadata/google-books": {"api_key": "integrations.metadata.google-books.api_key"},
	"metadata/openlibrary":  {"credentials.token": "integrations.metadata.openlibrary.credentials.token"},
	"ai/openai":             {"api_key": "integrations.ai.openai.api_key"},
	"ai/local-llm":          {"api_key": "integrations.ai.local-llm.api_key"},
	"download/deluge":       {"password": "integrations.download.deluge.password", "daemon_password": "integrations.download.deluge.daemon_password"},
	"notify/webhook-1":      {"secret": "integrations.notify.webhook-1.secret"},
	"notify/webhook-2":      {"secret": "integrations.notify.webhook-2.secret"},
}

// TestIntegrationsMigration_SecretsByRef (I3): every secret an instance uses is
// a ref to a name in the secrets store; no secret value is in any row of any
// keyspace but the secrets store's own, in the legacy range, in the report, or
// in the logs.
func TestIntegrationsMigration_SecretsByRef(t *testing.T) {
	r, s := runFixture(t)
	got := instances(t, s)
	for id, want := range secretRefs {
		st := settingsOf(got[id])
		for key, name := range want {
			if g := refName(st, key); g != name {
				t.Errorf("%s settings[%s] references %q, want %q", id, key, g, name)
			}
		}
	}
	values := slices.Collect(maps.Values(secretValues))
	for owner, rows := range s.Rows {
		if owner == string(ownerSecrets) {
			continue
		}
		for k, v := range rows {
			for _, val := range values {
				if strings.Contains(string(v), val) || strings.Contains(k, val) {
					t.Errorf("%s row %q holds a secret value", owner, k)
				}
			}
		}
	}
	for k, v := range s.Legacy {
		for _, val := range values {
			if strings.Contains(string(v), val) {
				t.Errorf("legacy row %q holds a secret value", k)
			}
		}
	}
	text := r.logs.String() + fmt.Sprint(r.report.Entries())
	for _, q := range s.Quarantine {
		if !q.Sealed {
			text += q.Value
		}
	}
	for _, val := range values {
		if strings.Contains(text, val) {
			t.Errorf("the report, logs or an unsealed quarantine record holds a secret value")
		}
	}
	// Every quarantine record of a sensitive family is sealed (never plaintext in
	// the record).
	for _, q := range s.Quarantine {
		if !q.Sealed {
			t.Errorf("quarantine record %s is not sealed", q.Key)
		}
	}
}

// TestIntegrationsMigration_TrustCarried: HEAD let any configured URL be
// reached, so an instance whose settings hold a URL is granted Local for that
// URL's origin, audited, with the secrets pinned to the same origin; an
// instance without a network URL is granted nothing.
func TestIntegrationsMigration_TrustCarried(t *testing.T) {
	_, s := runFixture(t)
	got := instances(t, s)
	wantOrigin := map[string]string{
		"metadata/hardcover":    "base_url=https://hardcover.invalid:443",
		"metadata/google-books": "base_url=https://books.invalid:443",
		"ai/local-llm":          "url=http://192.0.2.10:8080",
		"ai/openai":             "url=https://llm.invalid:443",
		"download/deluge":       "url=http://192.0.2.20:8112",
		"notify/webhook-1":      "url=http://192.0.2.30:9000",
		"notify/webhook-2":      "url=https://signal.invalid:443",
	}
	for id, row := range got {
		origin, local := wantOrigin[id]
		if g, _ := row["local"].(bool); g != local {
			t.Errorf("%s: local %v, want %v", id, g, local)
		}
		if local && row["local_origin"] != origin {
			t.Errorf("%s: local_origin %v, want %s", id, row["local_origin"], origin)
		}
		if !local && row["local_origin"] != nil {
			t.Errorf("%s: carries a local origin though it has no URL", id)
		}
		hasSecret := len(secretRefs[id]) > 0
		if local && hasSecret && row["secrets_origin"] != origin {
			t.Errorf("%s: secrets_origin %v, want %s", id, row["secrets_origin"], origin)
		}
	}
	// The two without a URL: a secret with no destination uses the adapter's.
	if got["metadata/openlibrary"]["secrets_origin"] != "adapter-default" {
		t.Errorf("openlibrary secrets_origin %v, want adapter-default", got["metadata/openlibrary"]["secrets_origin"])
	}
	for id := range wantOrigin {
		key := "core.integration.local_migrated|" + id
		if _, ok := s.Rows[string(ownerAudit)][key]; !ok {
			t.Errorf("no audit record %q", key)
		}
	}
	for _, k := range audits(s) {
		if strings.HasPrefix(k, "core.integration.local_migrated|") {
			id := strings.TrimPrefix(k, "core.integration.local_migrated|")
			if _, ok := wantOrigin[id]; !ok {
				t.Errorf("audit record %q for an instance that was not granted Local", k)
			}
		}
	}
}

// TestIntegrationsMigration_UnreadableSecretReEnter (I3): a secret M2 could not
// read stays a name in the store (a marker the operator re-enters), so the
// instance that references it is kept with the same ref; a copy of an unreadable
// key is never made.
func TestIntegrationsMigration_UnreadableSecretReEnter(t *testing.T) {
	unreadable := []secrets.Name{"integrations.metadata.hardcover.api_token", "integrations.ai.openai.api_key"}
	_, s := runFixture(t, func(r *rig) {
		for _, n := range unreadable {
			delete(r.secrets.seed, n)
			r.secrets.unreadable[n] = true
		}
	})
	got := instances(t, s)
	if len(got) != len(wantInstances) {
		t.Fatalf("%d instances, want %d (an unreadable secret must not drop its instance)", len(got), len(wantInstances))
	}
	if g := refName(settingsOf(got["metadata/hardcover"]), "api_token"); g != string(unreadable[0]) {
		t.Errorf("hardcover keeps ref %q, want %q", g, unreadable[0])
	}
	if g := refName(settingsOf(got["ai/openai"]), "api_key"); g != string(unreadable[1]) {
		t.Errorf("openai keeps ref %q, want %q", g, unreadable[1])
	}
	if _, ok := s.Rows[string(ownerSecrets)]["integrations.ai.local-llm.api_key"]; ok {
		t.Error("a copy of an unreadable key was made")
	}
	if g := refName(settingsOf(got["ai/local-llm"]), "api_key"); g != "" {
		t.Errorf("local-llm references %q though there was no key to copy", g)
	}
}

// TestIntegrationsMigration_HEADRowsGone: every row of HEAD's families that M6
// retires is gone, and each family was present before.
func TestIntegrationsMigration_HEADRowsGone(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	fsys := r.seed(t)
	before := r.before(t, fsys)
	s, err := r.runOn(t, fsys)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range drained {
		seen := 0
		for k := range before {
			if k == family || (strings.HasSuffix(family, ":") || strings.HasSuffix(family, "_")) && strings.HasPrefix(k, family) {
				seen++
				if _, ok := s.Legacy[k]; ok {
					t.Errorf("%s survived", k)
				}
			}
		}
		if seen == 0 {
			t.Errorf("the fixture has no %s row to remove", family)
		}
	}
}

// TestIntegrationsMigration_DigitLeadingIDNormalized: core's instance ID starts
// with a letter, so a HEAD ID that starts with a digit is imported under an
// "id-" prefix (seams.d/F7-01.md), reported renamed, and one that cannot be made
// valid is quarantined whole instead of dropped.
func TestIntegrationsMigration_DigitLeadingIDNormalized(t *testing.T) {
	r, s := runFixture(t, withBlob(func(tree map[string]any) {
		eps := tree["ai_endpoints"].([]any)
		tree["ai_endpoints"] = append(eps,
			map[string]any{"id": "9Lives_Cat", "protocol": "openai_compat", "url": "http://192.0.2.12:8080/v1", "chat_model": "selina-7b"},
			map[string]any{"id": "Bat Cave", "protocol": "openai_compat", "url": "http://192.0.2.13:8080/v1"},
		)
	}))
	got := instances(t, s)
	row, ok := got["ai/id-9lives-cat"]
	if !ok {
		t.Fatalf("the digit-leading ID was not imported under id-9lives-cat; instances %v", slices.Sorted(maps.Keys(got)))
	}
	if settingsOf(row)["text_model"] != "selina-7b" {
		t.Errorf("row %v", row)
	}
	renamed := false
	for _, e := range r.report.Entries() {
		if e.Kind == "instance" && e.ID == "ai/id-9lives-cat" && strings.Contains(e.Detail, "9Lives_Cat") {
			renamed = true
		}
	}
	if !renamed {
		t.Error("the rename is not reported")
	}
	quarantinedBad := false
	for _, q := range s.Quarantine {
		if q.Key == memberPrefix+"ai_endpoints.3" {
			quarantinedBad = true
		}
	}
	if !quarantinedBad {
		t.Error("an ID no rename makes valid (a space) was not quarantined")
	}
}
