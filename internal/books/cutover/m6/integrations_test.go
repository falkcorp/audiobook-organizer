// file: internal/books/cutover/m6/integrations_test.go
// version: 1.0.2
// guid: 0b7c39d5-6a18-4e2f-9d40-c85a1f2e7b63
// last-edited: 2026-11-19

package m6_test

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/mediacore/integrations"
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
	// Every quarantine record of the blob, the one sensitive family, is sealed
	// (never plaintext in the record).
	for _, q := range s.Quarantine {
		if strings.HasPrefix(q.Key, m6.KeyBlob) && !q.Sealed {
			t.Errorf("quarantine record %s is not sealed", q.Key)
		}
	}
}

// TestQuarantine_ImportPathRowsAreNotSealed: an import_path row holds a path, a
// name and scan counters, never a credential, so its family is not Sensitive
// and a quarantined one stays readable without the keyring.
func TestQuarantine_ImportPathRowsAreNotSealed(t *testing.T) {
	_, s := runFixture(t, withRows(map[string][]byte{"import_path:7": []byte("{not json")}))
	found := false
	for _, q := range s.Quarantine {
		if q.Key != "import_path:7" {
			continue
		}
		found = true
		if q.Sealed || q.Value != "{not json" {
			t.Errorf("the import_path record is sealed=%v value=%q, want it in the clear", q.Sealed, q.Value)
		}
	}
	if !found {
		t.Fatal("the undecodable import_path row was not quarantined")
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
		if id, ok := strings.CutPrefix(k, "core.integration.local_migrated|"); ok {
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

// renamedSource adds a metadata source whose HEAD ID core's grammar renames
// (batcave_db becomes batcave-db), with the credential M2 stored under HEAD's ID.
func renamedSource(r *rig) {
	tree := blobTree()
	tree["metadata_sources"] = append(tree["metadata_sources"].([]any),
		map[string]any{"id": "batcave_db", "name": "Batcave DB", "enabled": true, "priority": 4})
	withRows(map[string][]byte{m6.KeyBlob: blobRow(r.t, tree)})(r)
	r.secrets.seed["integrations.metadata.batcave_db.credentials.api_key"] = "fixture-batcave-db-key-lucius"
}

// TestIntegrationsMigration_RenamedSourceKeepsItsCredential: M2 stores a source's
// credentials under the ID HEAD gave it, so the instance under the renamed ID
// would otherwise lose its refs and the secret would be orphaned. The credential is
// carried to the name under the new ID, the instance refs it, and the report says
// so without the value.
func TestIntegrationsMigration_RenamedSourceKeepsItsCredential(t *testing.T) {
	r, s := runFixture(t, renamedSource)
	row, ok := instances(t, s)["metadata/batcave-db"]
	if !ok {
		t.Fatalf("the renamed source was not imported: %v", slices.Sorted(maps.Keys(instances(t, s))))
	}
	const to = "integrations.metadata.batcave-db.credentials.api_key"
	if g := refName(settingsOf(row), "credentials.api_key"); g != to {
		t.Errorf("the renamed instance refs %q, want %q", g, to)
	}
	if v := s.Rows[string(ownerSecrets)][to]; string(v) != "fixture-batcave-db-key-lucius" {
		t.Errorf("the carried secret holds %q", v)
	}
	if row["secrets_origin"] == nil || row["secrets_origin"] == "" {
		t.Error("the instance holds a secret ref with no secrets_origin")
	}
	carried := false
	for _, e := range r.report.Entries() {
		if strings.Contains(e.Detail, "fixture-batcave-db-key") {
			t.Errorf("the report holds the secret value: %v", e)
		}
		carried = carried || e.Kind == "note" && e.ID == "metadata/batcave-db" && strings.Contains(e.Detail, "carried to the renamed source")
	}
	if !carried {
		t.Error("the carry is not reported")
	}
}

// TestIntegrationsMigration_RenamedSourceUnreadableCredential: an unreadable
// credential under the old name is carried as it would stay for a source that
// kept its ID: the instance refs the new name, an unreadable marker (reason
// corrupt, "re-enter it") is staged there, and the report says to re-enter it
// and that the old name is left unreferenced.
func TestIntegrationsMigration_RenamedSourceUnreadableCredential(t *testing.T) {
	const from = "integrations.metadata.batcave_db.credentials.api_key"
	const to = "integrations.metadata.batcave-db.credentials.api_key"
	r, s := runFixture(t, renamedSource, func(r *rig) {
		delete(r.secrets.seed, from)
		r.secrets.unreadable[from] = true
	})
	row := instances(t, s)["metadata/batcave-db"]
	if g := refName(settingsOf(row), "credentials.api_key"); g != to {
		t.Errorf("the unreadable credential's ref is %q, want %q", g, to)
	}
	if v := string(s.Rows[string(ownerSecrets)][to]); v != unreadableMarker+"corrupt" {
		t.Errorf("the new name holds %q, want the corrupt unreadable marker", v)
	}
	if row["secrets_origin"] == nil || row["secrets_origin"] == "" {
		t.Error("the instance holds a secret ref with no secrets_origin")
	}
	noted, orphan := false, false
	for _, e := range r.report.Entries() {
		if e.Kind != "note" || e.ID != "metadata/batcave-db" {
			continue
		}
		noted = noted || strings.Contains(e.Detail, "carried as unreadable: re-enter it")
		orphan = orphan || strings.Contains(e.Detail, from) && strings.Contains(e.Detail, "nothing refers to it")
		if strings.Contains(e.Detail, "carried to the renamed source") {
			t.Errorf("an unreadable credential is reported carried: %v", e)
		}
	}
	if !noted {
		t.Error("the unreadable credential is not reported")
	}
	if !orphan {
		t.Error("the old name left in the store is not reported")
	}
}

// TestIntegrationsMigration_RenamedSourceOrphanAtNewName: a secret already under
// the new name belongs to no source of this pass (no entry has that HEAD ID), so
// it is not the renamed source's to take: the instance gets no ref, the old
// name's value is not copied over it, and the report says both stay unbound.
func TestIntegrationsMigration_RenamedSourceOrphanAtNewName(t *testing.T) {
	const to = "integrations.metadata.batcave-db.credentials.api_key"
	r, s := runFixture(t, renamedSource, func(r *rig) { r.secrets.seed[to] = "fixture-batcave-db-key-newer" })
	if v := s.Rows[string(ownerSecrets)][to]; v != nil {
		t.Errorf("the carry wrote %q over a secret already under the new name", v)
	}
	row := instances(t, s)["metadata/batcave-db"]
	if g := refName(settingsOf(row), "credentials.api_key"); g != "" {
		t.Errorf("the renamed source took the ref %q to a secret it does not own", g)
	}
	noted := false
	for _, e := range r.report.Entries() {
		noted = noted || e.Kind == "note" && e.ID == "metadata/batcave-db" && strings.Contains(e.Detail, "is not carried: a secret is already stored under "+to)
	}
	if !noted {
		t.Errorf("the uncarried credential is not reported:\n%s", reportText(r))
	}
}

// TestIntegrationsMigration_ExactIDWinsWhateverTheOrder: an entry whose HEAD ID
// already is the instance ID keeps it, listed before or after the entry renamed
// onto it; the renamed one is quarantined as the duplicate and never takes the
// exact entry's credential, which M2 stored under that very name.
func TestIntegrationsMigration_ExactIDWinsWhateverTheOrder(t *testing.T) {
	const name = "integrations.metadata.batcave-db.credentials.api_key"
	exact := map[string]any{"id": "batcave-db", "name": "Batcave DB (exact)", "enabled": true, "priority": 5}
	renamed := map[string]any{"id": "batcave_db", "name": "Batcave DB (renamed)", "enabled": true, "priority": 4}
	base := len(blobTree()["metadata_sources"].([]any))
	for _, c := range []struct {
		name    string
		order   []any
		renamed int // the renamed entry's index in the list
	}{{"exact first", []any{exact, renamed}, base + 1}, {"renamed first", []any{renamed, exact}, base}} {
		t.Run(c.name, func(t *testing.T) {
			r, s := runFixture(t, func(r *rig) {
				tree := blobTree()
				tree["metadata_sources"] = append(tree["metadata_sources"].([]any), c.order...)
				withRows(map[string][]byte{m6.KeyBlob: blobRow(r.t, tree)})(r)
				r.secrets.seed[name] = "fixture-batcave-db-key-exact"
				r.secrets.seed["integrations.metadata.batcave_db.credentials.api_key"] = "fixture-batcave-db-key-renamed"
			})
			row, ok := instances(t, s)["metadata/batcave-db"]
			if !ok {
				t.Fatal("no metadata/batcave-db instance")
			}
			if row["name"] != "Batcave DB (exact)" {
				t.Errorf("the instance is %v, want the exact-ID entry", row["name"])
			}
			if g := refName(settingsOf(row), "credentials.api_key"); g != name {
				t.Errorf("the exact entry refs %q, want %q", g, name)
			}
			if v := s.Rows[string(ownerSecrets)][name]; v != nil {
				t.Errorf("a carry wrote %q over the exact entry's secret", v)
			}
			want := fmt.Sprintf("%smetadata_sources.%d", memberPrefix, c.renamed)
			if !slices.ContainsFunc(s.Quarantine, func(q quarantined) bool { return q.Key == want && q.Reason == rInstanceConflict }) {
				t.Errorf("the renamed duplicate %s was not quarantined", want)
			}
			taken := false
			for _, e := range r.report.Entries() {
				taken = taken || e.Kind == "instance" && e.ID == "metadata/batcave-db" && strings.Contains(e.Detail, "whose HEAD id it is")
			}
			if !taken {
				t.Error("the conflict is not reported")
			}
		})
	}
}

// TestIntegrationsMigration_ExactIDWinsForAIEndpoints: the same rule over
// ai_endpoints, whose renamed rows also carry their credentials.
func TestIntegrationsMigration_ExactIDWinsForAIEndpoints(t *testing.T) {
	_, s := runFixture(t, withBlob(func(tree map[string]any) {
		tree["ai_endpoints"] = append([]any{
			map[string]any{"id": "oracle_node", "label": "Oracle (renamed)", "protocol": "openai_compat", "url": "http://192.0.2.40:8080", "enabled": true},
		}, append(tree["ai_endpoints"].([]any),
			map[string]any{"id": "oracle-node", "label": "Oracle (exact)", "protocol": "openai_compat", "url": "http://192.0.2.41:8080", "enabled": true})...)
	}))
	row, ok := instances(t, s)["ai/oracle-node"]
	if !ok {
		t.Fatal("no ai/oracle-node instance")
	}
	if row["name"] != "Oracle (exact)" {
		t.Errorf("the instance is %v, want the exact-ID row listed after the renamed one", row["name"])
	}
	if !slices.ContainsFunc(s.Quarantine, func(q quarantined) bool {
		return q.Key == memberPrefix+"ai_endpoints.0" && q.Reason == rInstanceConflict
	}) {
		t.Error("the renamed row listed first was not quarantined as the duplicate")
	}
}

// TestIntegrationsMigration_RenamedSourceRefusedCarriesNothing: the carry is
// staged, and reported, only once the instance import succeeds. When core
// rejects the renamed instance, as a conflict with a stored one or as invalid,
// no secret or marker is written under the new name and no carry is reported,
// readable or not.
func TestIntegrationsMigration_RenamedSourceRefusedCarriesNothing(t *testing.T) {
	invalid := func(r *rig) {
		r.refuse = func(k, i string) error {
			if k == "instance" && i == "metadata/batcave-db" {
				return fmt.Errorf("bad settings: %w", integrations.ErrInvalid)
			}
			return nil
		}
	}
	const from = "integrations.metadata.batcave_db.credentials.api_key"
	const to = "integrations.metadata.batcave-db.credentials.api_key"
	for _, c := range []struct {
		name string
		opts []func(*rig)
	}{
		{"conflict/readable", []func(*rig){conflict("instance", "metadata/batcave-db")}},
		{"conflict/unreadable", []func(*rig){conflict("instance", "metadata/batcave-db"), func(r *rig) { delete(r.secrets.seed, from); r.secrets.unreadable[from] = true }}},
		{"invalid/readable", []func(*rig){invalid}},
		{"invalid/unreadable", []func(*rig){invalid, func(r *rig) { delete(r.secrets.seed, from); r.secrets.unreadable[from] = true }}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, s := runFixture(t, append([]func(*rig){renamedSource}, c.opts...)...)
			if _, ok := instances(t, s)["metadata/batcave-db"]; ok {
				t.Fatal("the refused instance was written")
			}
			if v, ok := s.Rows[string(ownerSecrets)][to]; ok {
				t.Errorf("the refused instance's carry wrote %q under the new name", v)
			}
			for _, e := range r.report.Entries() {
				if e.ID == "metadata/batcave-db" && e.Kind == "note" && (strings.Contains(e.Detail, "carried") || strings.Contains(e.Detail, "stays in the store")) {
					t.Errorf("a carry is reported for a refused instance: %v", e)
				}
			}
		})
	}
}

// reportHas reports whether some report entry of kind on id contains detail.
func reportHas(r *rig, kind, id, detail string) bool {
	for _, e := range r.report.Entries() {
		if e.Kind == kind && e.ID == id && strings.Contains(e.Detail, detail) {
			return true
		}
	}
	return false
}

// TestIntegrationsMigration_RenamedOpenAIRowKeepsHEADsKey: HEAD sent the OpenAI
// key to any row whose auth_ref names it (poolSecret), so a hand-typed OpenAI
// row renamed to openai keeps it: the key M2 moved to the openai name is bound,
// and the binding is reported. Without auth_ref HEAD sent it none: nothing is
// bound, the row gets no operator-key pairing, and the key left unreferenced is
// reported. Either way the row suppresses the synthesised one.
func TestIntegrationsMigration_RenamedOpenAIRowKeepsHEADsKey(t *testing.T) {
	for _, c := range []struct {
		name    string
		authRef string
	}{{"auth_ref", "openai_api_key"}, {"no auth_ref", ""}} {
		t.Run(c.name, func(t *testing.T) {
			row := map[string]any{"id": "OpenAI", "label": "OpenAI (hand-typed)", "protocol": "openai_compat", "url": "https://llm.invalid/v1", "enabled": true}
			if c.authRef != "" {
				row["auth_ref"] = c.authRef
			}
			r, s := runFixture(t, withBlob(func(tree map[string]any) {
				if c.authRef == "" {
					// No other row copies the key either, so nothing uses it.
					for _, ep := range tree["ai_endpoints"].([]any) {
						delete(ep.(map[string]any), "auth_ref")
					}
				}
				tree["ai_endpoints"] = append(tree["ai_endpoints"].([]any), row)
			}))
			inst, ok := instances(t, s)["ai/openai"]
			if !ok || inst["name"] != "OpenAI (hand-typed)" {
				t.Fatalf("ai/openai is %v, want the renamed row and no synthesised one", inst["name"])
			}
			bound := refName(settingsOf(inst), "api_key")
			_, paired := inst["operator_secret_origin"]
			unref := reportHas(r, "note", "ai/openai", "is referred to by no migrated AI endpoint")
			if c.authRef != "" {
				if bound != "integrations.ai.openai.api_key" || !reportHas(r, "note", "ai/openai", "is bound to the renamed row") {
					t.Errorf("api_key refs %q (reported %v), want HEAD's key bound and reported", bound, reportHas(r, "note", "ai/openai", "is bound"))
				}
				if !paired || unref {
					t.Errorf("operator pairing %v, unreferenced note %v: want paired, no note", paired, unref)
				}
				return
			}
			if bound != "" || paired {
				t.Errorf("api_key refs %q, operator pairing %v: HEAD sent this row no key", bound, paired)
			}
			if !unref {
				t.Error("the OpenAI key left unreferenced is not reported")
			}
		})
	}
}

// TestIntegrationsMigration_ExactIDEntryNotWrittenRenamedTakesTheID: when the
// entry whose HEAD ID it is does not get written (core refuses it, or it does
// not decode), nothing holds the ID, so the renamed entry takes it in either
// order, the report says why, and it still takes no credential stored under the
// new name.
func TestIntegrationsMigration_ExactIDEntryNotWrittenRenamedTakesTheID(t *testing.T) {
	const name = "integrations.metadata.batcave-db.credentials.api_key"
	exact := map[string]any{"id": "batcave-db", "name": "Batcave DB (exact)", "enabled": true, "priority": 5}
	undecodable := map[string]any{"id": "batcave-db", "name": "Batcave DB (exact)", "priority": "high"}
	renamed := map[string]any{"id": "batcave_db", "name": "Batcave DB (renamed)", "enabled": true, "priority": 4}
	for _, c := range []struct {
		name  string
		order []any
		opts  []func(*rig)
	}{
		{"refused, exact first", []any{exact, renamed}, []func(*rig){conflict("instance", "metadata/batcave-db")}},
		{"refused, renamed first", []any{renamed, exact}, []func(*rig){conflict("instance", "metadata/batcave-db")}},
		{"undecodable, exact first", []any{undecodable, renamed}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			refusals := 0
			opts := append([]func(*rig){func(r *rig) {
				tree := blobTree()
				tree["metadata_sources"] = append(tree["metadata_sources"].([]any), c.order...)
				withRows(map[string][]byte{m6.KeyBlob: blobRow(r.t, tree)})(r)
				r.secrets.seed[name] = "fixture-batcave-db-key-exact"
			}}, c.opts...)
			// Refuse only the first import under the ID: the exact entry's,
			// which settleDeferred tries before the renamed one.
			opts = append(opts, func(r *rig) {
				inner := r.refuse
				if inner == nil {
					return
				}
				r.refuse = func(k, i string) error {
					if err := inner(k, i); err != nil && refusals == 0 {
						refusals++
						return err
					}
					return nil
				}
			})
			r, s := runFixture(t, opts...)
			row, ok := instances(t, s)["metadata/batcave-db"]
			if !ok || row["name"] != "Batcave DB (renamed)" {
				t.Fatalf("metadata/batcave-db is %v, want the renamed entry", row["name"])
			}
			if g := refName(settingsOf(row), "credentials.api_key"); g != "" {
				t.Errorf("the renamed entry refs %q, the exact entry's credential", g)
			}
			if !reportHas(r, "note", "metadata/batcave-db", "was not written, so metadata_sources.") {
				t.Error("taking the ID is not reported")
			}
			if reportHas(r, "instance", "metadata/batcave-db", "whose HEAD id it is") {
				t.Error("the report says the ID is taken by an entry that holds nothing")
			}
		})
	}
}

// TestIntegrationsMigration_TwoRenamedOnUnwrittenExactIDNamesTheRealHolder: when
// the exact entry is not written and two renamed entries land on its ID, the
// second one's conflict report names the first renamed entry, which holds the
// ID, not the exact entry that holds nothing.
func TestIntegrationsMigration_TwoRenamedOnUnwrittenExactIDNamesTheRealHolder(t *testing.T) {
	undecodable := map[string]any{"id": "batcave-db", "name": "Batcave DB (exact)", "priority": "high"}
	first := map[string]any{"id": "batcave_db", "name": "Batcave DB (first)", "enabled": true, "priority": 4}
	second := map[string]any{"id": "BATCAVE-DB", "name": "Batcave DB (second)", "enabled": true, "priority": 3}
	r, s := runFixture(t, func(r *rig) {
		tree := blobTree()
		tree["metadata_sources"] = append(tree["metadata_sources"].([]any), undecodable, first, second)
		withRows(map[string][]byte{m6.KeyBlob: blobRow(r.t, tree)})(r)
	})
	if row := instances(t, s)["metadata/batcave-db"]; row["name"] != "Batcave DB (first)" {
		t.Fatalf("metadata/batcave-db is %v, want the first renamed entry", row["name"])
	}
	if !reportHas(r, "instance", "metadata/batcave-db", "id already taken by metadata_sources.") {
		t.Error("the conflict does not name the entry that holds the ID")
	}
	if reportHas(r, "instance", "metadata/batcave-db", "whose HEAD id it is") {
		t.Error("the conflict names the exact entry, which holds nothing, as the holder")
	}
}

// TestMetadataMigration_RenamedProviderGetsNoOperatorKey: HEAD's provider switch
// was exact, so a source whose ID M6 lowers to google-books was an inert unknown
// source at HEAD. It gets no operator-key pairing: the operator key is never
// served beside a base_url HEAD never sent it to.
func TestMetadataMigration_RenamedProviderGetsNoOperatorKey(t *testing.T) {
	_, s := runFixture(t, withBlob(func(tree map[string]any) {
		srcs := tree["metadata_sources"].([]any)
		srcs[1].(map[string]any)["id"] = "Google_Books"
		srcs[1].(map[string]any)["base_url"] = "https://mirror.invalid"
	}))
	row, ok := instances(t, s)["metadata/google-books"]
	if !ok {
		t.Fatal("no metadata/google-books instance")
	}
	if got, has := row["operator_secret_origin"]; has {
		t.Errorf("the renamed source has operator_secret_origin %v", got)
	}
	if row["local_origin"] == nil {
		t.Error("the renamed source lost its Local grant, which is not what this test is about")
	}
}

// TestAIEndpointMigration_DroppedModelQuarantined: a capability_models entry for
// a capability HEAD does not register is left out of the instance, and its model
// is quarantined under its own key so the owner can recover it.
func TestAIEndpointMigration_DroppedModelQuarantined(t *testing.T) {
	r, s := runFixture(t, withBlob(func(tree map[string]any) {
		ep := tree["ai_endpoints"].([]any)[0].(map[string]any)
		ep["capability_models"] = map[string]any{"llm.dedup_review": "alfred-13b", "mystery.cape": "riddler-1b"}
	}))
	key := memberPrefix + "ai_endpoints.0.capability_models.mystery.cape"
	found := false
	for _, q := range s.Quarantine {
		if q.Key == key {
			found = true
			if q.Value != `"riddler-1b"` || q.Reason != "a value HEAD selected by nothing with, left out of the migrated instance: kept here so it can be recovered" {
				t.Errorf("%s holds %q (%s)", key, q.Value, q.Reason)
			}
		}
	}
	if !found {
		t.Errorf("the dropped model is not quarantined: %v", s.Quarantine)
	}
	if cm, _ := settingsOf(instances(t, s)["ai/local-llm"])["capability_models"].(map[string]any); len(cm) != 1 {
		t.Errorf("capability_models %v, want only the registered capability's", cm)
	}
	if len(s.Verify.Failures) != 0 {
		t.Errorf("verify failures: %v", s.Verify.Failures)
	}
	_ = r
}

// TestIntegrationsMigration_RenamedSourceSkipsStoredUserinfo: userinfo is split
// from the URL afresh on every conversion, so a userinfo secret stored under the
// old ID is neither carried nor bound.
func TestIntegrationsMigration_RenamedSourceSkipsStoredUserinfo(t *testing.T) {
	r, s := runFixture(t, renamedSource, func(r *rig) {
		r.secrets.seed["integrations.metadata.batcave_db.userinfo"] = "fixture-batcave-db-userinfo-fox"
	})
	row := instances(t, s)["metadata/batcave-db"]
	if _, has := settingsOf(row)["userinfo"]; has {
		t.Errorf("the stored userinfo is bound: %v", settingsOf(row)["userinfo"])
	}
	if v, ok := s.Rows[string(ownerSecrets)]["integrations.metadata.batcave-db.userinfo"]; ok {
		t.Errorf("the stored userinfo is carried: %q", v)
	}
	if reportHas(r, "note", "metadata/batcave-db", "credential userinfo") {
		t.Error("the stored userinfo is reported as a carried credential")
	}
}
