// file: internal/books/cutover/m6/domains_test.go
// version: 1.0.2
// guid: 94d1a6f0-3c7e-4b58-a0e9-12f6b8c35d7a
// last-edited: 2026-11-19

package m6_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// TestAIEndpointMigration_AuthRefKeyCopy (05-ai section 10): a row with
// auth_ref openai_api_key gets its own api_key ref holding the stored OpenAI
// key and locality cloud; with the key only in the environment it gets none.
func TestAIEndpointMigration_AuthRefKeyCopy(t *testing.T) {
	_, s := runFixture(t)
	got := instances(t, s)
	st := settingsOf(got["ai/local-llm"])
	if g := refName(st, "api_key"); g != "integrations.ai.local-llm.api_key" {
		t.Errorf("local-llm api_key ref %q", g)
	}
	if st["locality"] != "cloud" {
		t.Errorf("local-llm locality %v, want cloud (HEAD calls any auth_ref endpoint cloud)", st["locality"])
	}
	if v := s.Rows[string(ownerSecrets)]["integrations.ai.local-llm.api_key"]; string(v) != secretValues["integrations.ai.openai.api_key"] {
		t.Error("the copied key is not the stored OpenAI key")
	}
	if g := refName(settingsOf(got["ai/openai"]), "api_key"); g != "integrations.ai.openai.api_key" {
		t.Errorf("the openai row's own ref %q", g)
	}
	if settingsOf(got["ai/openai"])["locality"] != "cloud" {
		t.Error("the openai row is not cloud")
	}
	if _, ok := settingsOf(got["ai/local-uv"])["locality"]; ok {
		t.Error("an endpoint with no auth_ref was given a locality: HEAD's host heuristic (auto) applies to it")
	}

	// The key only in the environment: no copy, no ref, locality still cloud.
	r, s2 := runFixture(t, func(r *rig) { delete(r.secrets.seed, "integrations.ai.openai.api_key") })
	got = instances(t, s2)
	st = settingsOf(got["ai/local-llm"])
	if g := refName(st, "api_key"); g != "" {
		t.Errorf("env-only key: local-llm references %q", g)
	}
	if st["locality"] != "cloud" {
		t.Error("env-only key: the locality was dropped with the ref")
	}
	if _, ok := s2.Rows[string(ownerSecrets)]["integrations.ai.local-llm.api_key"]; ok {
		t.Error("env-only key: a secret was written")
	}
	noted := false
	for _, e := range r.report.Entries() {
		noted = noted || e.Kind == "note" && e.ID == "ai/local-llm" && strings.Contains(e.Detail, "no stored key")
	}
	if !noted {
		t.Error("the missing key is not reported")
	}
}

// TestAIEndpointMigration_TicksRewritten: HEAD capability IDs in stored ticks
// and in per-capability models become catalog IDs. A tick HEAD does not register
// is dropped and reported, never carried: core refuses an imported endpoint that
// carries one, so keeping it would lose the whole endpoint.
func TestAIEndpointMigration_TicksRewritten(t *testing.T) {
	r, s := runFixture(t)
	st := settingsOf(instances(t, s)["ai/local-llm"])
	if g := strs(st["capabilities"]); !slices.Equal(g, []string{"books.dedup_review", "books.embed_text"}) {
		t.Errorf("capabilities %v", g)
	}
	if !strings.Contains(reportText(r), `note|ai/local-llm|dropped: capability tick "mystery.cape"`) {
		t.Errorf("the dropped tick is not reported:\n%s", reportText(r))
	}
	cm := st["capability_models"].(map[string]any)
	if len(cm) != 1 || cm["books.dedup_review"] != "alfred-13b" {
		t.Errorf("capability_models %v", cm)
	}
	if g := strs(settingsOf(instances(t, s)["ai/local-uv"])["capabilities"]); !slices.Equal(g, []string{"books.transcribe_batch"}) {
		t.Errorf("local-uv capabilities %v", g)
	}
	if st["root_paths"].(map[string]any)["batcave-lib"] != "/mnt/lib" {
		t.Errorf("host_roots was not carried to root_paths: %v", st["root_paths"])
	}
	if st["concurrency"] != float64(2) || settingsOf(instances(t, s)["ai/local-uv"])["concurrency"] != float64(1) {
		t.Error("concurrency: a 0 must become 1, a set value kept")
	}
}

type wantJob struct{ state, capability, handle string }

// TestAIJobMigration_EveryJobAndState (05-ai section 10): every aijob: row is
// one core job row with HEAD's ID, payload, attempts and handle and the state
// the table gives; open jobs get an open-index row; the payload and batch-index
// rows are folded in.
func TestAIJobMigration_EveryJobAndState(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	fsys := r.seed(t)
	before := r.before(t, fsys)
	s, err := r.runOn(t, fsys)
	if err != nil || s.State != "done" {
		t.Fatalf("M6: %v (%s)", err, s.State)
	}
	want := map[string]wantJob{
		"01JKNIGHTFALL":    {"completed", "books.dedup_review", "batch-knightfall"},
		"01JNOMANSLAND":    {"submitted", "books.author_dedup_batch", "batch-nomansland"},
		"01JBATSIGNAL":     {"failed", "books.bat_signal", "batch-batsignal"},
		"01JHUSH":          {"applying", "books.dedup_review", "batch-hush"},
		"01JLONGHALLOWEEN": {"failed", "books.dedup_review", ""},
		"01JYEARONE":       {"failed", "books.author_dedup_batch", "batch-yearone"},
		"01JKILLINGJOKE":   {"completed", "books.dedup_review", "batch-killingjoke"},
	}
	got := jobs(t, s)
	if len(got) != len(want) {
		t.Fatalf("%d jobs, want %d", len(got), len(want))
	}
	for id, w := range want {
		j, ok := got[id]
		if !ok {
			t.Errorf("job %s missing", id)
			continue
		}
		if j["state"] != w.state || j["capability"] != w.capability {
			t.Errorf("job %s: state %v capability %v, want %s %s", id, j["state"], j["capability"], w.state, w.capability)
		}
		if h, _ := j["handle"].(string); h != w.handle {
			t.Errorf("job %s: handle %q, want %q", id, h, w.handle)
		}
		var head struct {
			ApplyAttempts int `json:"apply_attempts"`
		}
		if err := json.Unmarshal(before["aijob:"+id], &head); err != nil {
			t.Fatal(err)
		}
		if int(j["apply_attempts"].(float64)) != head.ApplyAttempts {
			t.Errorf("job %s: attempts %v, want %d", id, j["apply_attempts"], head.ApplyAttempts)
		}
		if string(s.Rows[string(ownerAI)]["payloads\x00\x01"+id+"\x00\x01"]) != string(before["aijob_payload:"+id]) {
			t.Errorf("job %s: payload not carried byte for byte", id)
		}
	}
	// Open jobs (submitted, applying) have an open-index row, and nothing else does.
	open := keysWithPrefix(s, string(ownerAI), "ix\x00\x01open\x00\x01")
	if len(open) != 2 || !strings.Contains(open[0], "01JHUSH") || !strings.Contains(open[1], "01JNOMANSLAND") {
		t.Errorf("open index %q, want exactly 01JHUSH and 01JNOMANSLAND", open)
	}
	for k := range s.Legacy {
		if strings.HasPrefix(k, "aijob") {
			t.Errorf("HEAD row %s survived", k)
		}
	}
}

// TestAIJobMigration_UnknownTypeImportedFailed (05-ai section 10): a job whose
// type has no catalog capability is imported as failed whatever HEAD's status
// said, with a note naming the capability, so it is history and never polled.
func TestAIJobMigration_UnknownTypeImportedFailed(t *testing.T) {
	r, s := runFixture(t)
	j := jobs(t, s)["01JBATSIGNAL"]
	if j["state"] != "failed" {
		t.Fatalf("state %v, want failed (HEAD: pending)", j["state"])
	}
	for _, k := range keysWithPrefix(s, string(ownerAI), "ix\x00\x01open\x00\x01") {
		if strings.Contains(k, "01JBATSIGNAL") {
			t.Error("a failed job is in the open index")
		}
	}
	note := string(s.Rows[string(ownerAI)]["notes\x00\x0101JBATSIGNAL\x00\x01"])
	if !strings.Contains(note, "books.bat_signal") {
		t.Errorf("note %q does not name the capability", note)
	}
	got := ""
	for _, e := range r.report.Entries() {
		if e.Kind == "job" && e.ID == "01JBATSIGNAL" {
			got = e.Detail
		}
	}
	if got != "failed" {
		t.Errorf("report says %q", got)
	}
}

// TestWebhookMigration_InstancesAndRule (05-notify section 9): each URL of the
// webhook plugin becomes a core.webhook instance (blank entries skipped,
// numbered in order), and one rule routes the mapped topics to all of them.
func TestWebhookMigration_InstancesAndRule(t *testing.T) {
	r, s := runFixture(t)
	got := instances(t, s)
	for n, url := range map[string]string{"notify/webhook-1": "http://192.0.2.30:9000/hook", "notify/webhook-2": "https://signal.invalid/a"} {
		row := got[n]
		st := settingsOf(row)
		if row["adapter"] != "core.webhook" || st["url"] != url || st["method"] != "POST" || row["enabled"] != true {
			t.Errorf("%s: %v", n, row)
		}
	}
	rules := keysWithPrefix(s, string(ownerNotify), "")
	if len(rules) != 1 {
		t.Fatalf("rules %q, want exactly one", rules)
	}
	rule := decodeRow(t, s.Rows[string(ownerNotify)][rules[0]])
	if rule["id"] != "webhooks" || rule["enabled"] != true {
		t.Errorf("rule %v", rule)
	}
	if g := strs(rule["topics"]); !slices.Equal(g, []string{"books.book.imported.v1", "books.scan.completed.v1"}) {
		t.Errorf("topics %v (the unknown event bat.signal must be dropped)", g)
	}
	if g := strs(rule["instances"]); !slices.Equal(g, []string{"notify/webhook-1", "notify/webhook-2"}) {
		t.Errorf("instances %v", g)
	}
	dropped := false
	for _, e := range r.report.Entries() {
		dropped = dropped || e.Kind == "note" && strings.Contains(e.ID, "bat.signal")
	}
	if !dropped {
		t.Error("the dropped event is not reported")
	}
}

// TestWebhookMigration_SecretByValue: the webhook's signing secret is a ref to
// the name M2 stored it under, never a value, and M6 stages no secret of its
// own for it.
func TestWebhookMigration_SecretByValue(t *testing.T) {
	r, s := runFixture(t)
	got := instances(t, s)
	for _, n := range []string{"notify/webhook-1", "notify/webhook-2"} {
		want := "integrations." + strings.Replace(n, "/", ".", 1) + ".secret"
		if g := refName(settingsOf(got[n]), "secret"); g != want {
			t.Errorf("%s secret ref %q, want %q", n, g, want)
		}
		if raw, _ := json.Marshal(got[n]); strings.Contains(string(raw), secretValues["integrations.notify.webhook-1.secret"]) {
			t.Errorf("%s holds the signing secret's value", n)
		}
	}
	for _, name := range r.secrets.imports {
		if strings.Contains(string(name), "webhook") {
			t.Errorf("M6 staged secret %s; M2 already holds it", name)
		}
	}
}

// TestDelugeMigration_InstanceAndPassword (05-download section 10): deluge_web_url
// becomes download/deluge with both passwords by ref; the daemon fields have no
// native home and are reported as dropped (never quarantined, never with a value).
func TestDelugeMigration_InstanceAndPassword(t *testing.T) {
	r, s := runFixture(t)
	row := instances(t, s)["download/deluge"]
	st := settingsOf(row)
	if row["adapter"] != "core.deluge" || st["url"] != "http://192.0.2.20:8112" {
		t.Errorf("deluge row %v", row)
	}
	if refName(st, "password") != "integrations.download.deluge.password" || refName(st, "daemon_password") != "integrations.download.deluge.daemon_password" {
		t.Errorf("deluge refs %v", st)
	}
	for _, q := range s.Quarantine {
		if strings.Contains(q.Key, "download_client") {
			t.Errorf("the daemon fields were quarantined: %s", q.Key)
		}
	}
	noted := false
	for _, e := range r.report.Entries() {
		if e.Kind == "note" && e.ID == "download/deluge" {
			noted = true
			if strings.Contains(e.Detail, "alfred") || strings.Contains(e.Detail, secretValues["integrations.download.deluge.daemon_password"]) {
				t.Errorf("the note holds a username or password: %s", e.Detail)
			}
		}
	}
	if !noted {
		t.Error("the dropped daemon fields were not reported")
	}
}

// TestRootsCutover_FromHEADFixture (05-library section 5L.9): root_dir becomes
// the books-library root, each import_path an import-<n> root in ID order, the
// path aliases attach to their root, iTunes path mappings resolve to a library
// path, protected_paths carry over, and the per-root scan state moves to
// rootscan: rows; a mapping no root contains is quarantined, never guessed.
func TestRootsCutover_FromHEADFixture(t *testing.T) {
	r, s := runFixture(t)
	var roots []map[string]any
	if err := json.Unmarshal(s.Rows[string(ownerConfig)]["core.library.roots"], &roots); err != nil {
		t.Fatal(err)
	}
	var ids, paths []string
	for _, ro := range roots {
		ids = append(ids, ro["id"].(string))
		paths = append(paths, ro["path"].(string))
	}
	if !slices.Equal(ids, []string{"books-library", "import-1", "import-2", "import-3"}) ||
		!slices.Equal(paths, []string{rootDir, importOne, importTwo, importThree}) {
		t.Fatalf("roots %v %v", ids, paths)
	}
	if roots[0]["role"] != "library" || roots[1]["role"] != "import" {
		t.Errorf("roles %v %v", roots[0]["role"], roots[1]["role"])
	}
	if roots[3]["watch"] != "off" || roots[1]["watch"] != "auto" {
		t.Errorf("an enabled import root watches, a disabled one does not: %v %v", roots[1]["watch"], roots[3]["watch"])
	}
	if al := roots[0]["aliases"].([]any); len(al) != 1 || al[0].(map[string]any)["windows"] != "W:" {
		t.Errorf("aliases %v", roots[0]["aliases"])
	}
	// import-2 is below books-library and has no alias of its own: HEAD rendered
	// its paths through books-library's (the longest alias root over them), so it
	// carries that alias with its relative path appended, in each form.
	if al, _ := roots[2]["aliases"].([]any); len(al) != 1 ||
		al[0].(map[string]any)["windows"] != `W:\incoming` || al[0].(map[string]any)["unc"] != `\\gotham\books\incoming` ||
		al[0].(map[string]any)["smb_url"] != "smb://gotham/books/incoming" {
		t.Errorf("import-2 aliases %v, want books-library's carried to it", roots[2]["aliases"])
	}
	if _, has := roots[1]["aliases"]; has {
		t.Errorf("import-1, under no alias root, has aliases %v", roots[1]["aliases"])
	}
	var maps []map[string]any
	if err := json.Unmarshal(s.Rows[string(ownerConfig)]["core.library.mappings"], &maps); err != nil {
		t.Fatal(err)
	}
	if len(maps) != 1 || maps[0]["source"] != "itunes" || maps[0]["local"].(map[string]any)["root"] != "books-library" || maps[0]["local"].(map[string]any)["rel"] != "itunes" {
		t.Errorf("mappings %v", maps)
	}
	if g := string(s.Rows[string(ownerConfig)]["core.library.protected_paths"]); g != `["/batcave/seeding"]` {
		t.Errorf("protected paths %s", g)
	}
	// The scan state of each import root moved, byte for byte what HEAD kept.
	for n, want := range map[string]string{
		"import-1": `"book_count":17`, "import-2": `"book_count":0`, "import-3": `"book_count":5`,
	} {
		if v := string(s.Legacy["rootscan:"+n]); !strings.Contains(v, want) {
			t.Errorf("rootscan:%s = %q, want it to hold %s", n, v, want)
		}
	}
	if v := string(s.Legacy["rootscan:import-3"]); !strings.Contains(v, `"enabled":false`) {
		t.Errorf("the disabled flag was lost: %s", v)
	}
	nested, unmapped := false, false
	for _, e := range r.report.Entries() {
		nested = nested || e.Kind == "root" && e.ID == "import-2" && strings.Contains(e.Detail, "nested")
		unmapped = unmapped || e.Kind == "quarantine" && strings.Contains(e.ID, "itunes.path_mappings.1")
	}
	if !nested {
		t.Error("the root nested inside books-library is not reported")
	}
	if !unmapped {
		t.Error("the unresolvable iTunes mapping is not reported")
	}
}

// TestWebhookMigration_RuleNamesOnlyWhatWasImported: a webhook instance core
// conflicts on or refuses is quarantined and left out of the rule, which names
// only the instances that exist; with none left there is no rule at all.
func TestWebhookMigration_RuleNamesOnlyWhatWasImported(t *testing.T) {
	_, s := runFixture(t, conflict("instance", "notify/webhook-1"))
	got := instances(t, s)
	if _, ok := got["notify/webhook-1"]; ok {
		t.Error("the conflicting webhook instance was written")
	}
	rules := keysWithPrefix(s, string(ownerNotify), "")
	if len(rules) != 1 {
		t.Fatalf("rules %q, want exactly one", rules)
	}
	rule := decodeRow(t, s.Rows[string(ownerNotify)][rules[0]])
	if g := strs(rule["instances"]); !slices.Equal(g, []string{"notify/webhook-2"}) {
		t.Errorf("the rule's instances %v, want only notify/webhook-2", g)
	}
	quarantinedFirst := false
	for _, q := range s.Quarantine {
		quarantinedFirst = quarantinedFirst || q.Reason == rInstanceConflict && strings.HasPrefix(q.Key, memberPrefix+"plugins.webhook.urls.")
	}
	if !quarantinedFirst {
		t.Errorf("the conflicting webhook URL was not quarantined: %v", s.Quarantine)
	}

	_, none := runFixture(t, func(r *rig) {
		r.refuse = func(kind, id string) error {
			if kind == "instance" && strings.HasPrefix(id, "notify/") {
				return fmt.Errorf("a different one is stored: %w", storage.ErrImportConflict)
			}
			return nil
		}
	})
	if rules := keysWithPrefix(none, string(ownerNotify), ""); len(rules) != 0 {
		t.Errorf("a rule %q was written though no webhook instance was", rules)
	}
}
