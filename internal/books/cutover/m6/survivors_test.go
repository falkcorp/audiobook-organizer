// file: internal/books/cutover/m6/survivors_test.go
// version: 1.0.0
// guid: 0c6e4b18-93a7-4d5f-b2e1-7a40d8f31c96
// last-edited: 2026-11-19

package m6_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
)

// TestIntegrationsMigration_OperatorSecretOrigin: the four instances that ship an
// operator-secret pair (M1.4 step 2: Hardcover, Google Books, OpenAI, Deluge)
// carry the origin of their URL as the operator-secret origin; no other instance
// does.
func TestIntegrationsMigration_OperatorSecretOrigin(t *testing.T) {
	_, s := runFixture(t)
	want := map[string][2]string{
		"metadata/hardcover":    {"base_url", "https://hardcover.invalid:443"},
		"metadata/google-books": {"base_url", "https://books.invalid:443"},
		"ai/openai":             {"url", "https://llm.invalid:443"},
		"download/deluge":       {"url", "http://192.0.2.20:8112"},
	}
	for id, row := range instances(t, s) {
		got, has := row["operator_secret_origin"].(map[string]any)
		w, wantOne := want[id]
		switch {
		case wantOne && (!has || got[w[0]] != w[1] || len(got) != 1):
			t.Errorf("%s: operator_secret_origin %v, want %s=%s", id, row["operator_secret_origin"], w[0], w[1])
		case !wantOne && has:
			t.Errorf("%s: carries an operator-secret origin %v", id, got)
		}
	}
}

// TestQuarantine_BadEntryFirstDoesNotStopTheRest: a member M6 cannot convert is
// set aside and the entries after it in the same list, the other lists, the
// rule and the blob's own deletion all still happen.
func TestQuarantine_BadEntryFirstDoesNotStopTheRest(t *testing.T) {
	r := newRig(t, withRows(headRows(t)), withBlob(func(tree map[string]any) {
		tree["metadata_sources"] = append([]any{"not an object"}, tree["metadata_sources"].([]any)...)
		tree["ai_endpoints"] = append([]any{"not an object"}, tree["ai_endpoints"].([]any)...)
		delete(tree, "openai_base_url")
		tree["openai_base_url"] = 5 // a number where a string belongs
		wh := tree["plugins"].(map[string]any)["webhook"].(map[string]any)["settings"].(map[string]any)
		wh["urls"] = "http://bat%zz@192.0.2.9/hook, http://192.0.2.30:9000/hook, https://signal.invalid/a"
		tree["zz_unread_one"] = "burst"
		tree["zz_unread_two"] = "burst"
		tree["download_client"] = "not an object"
	}))
	s, err := r.run(t)
	if err != nil {
		t.Fatalf("M6: %v\nlogs:\n%s", err, r.logs.String())
	}
	got := instances(t, s)
	for _, id := range []string{
		"metadata/hardcover", "metadata/google-books", "metadata/openlibrary",
		"ai/local-llm", "ai/local-uv", "ai/openai", "download/deluge",
		"notify/webhook-2", "notify/webhook-3",
	} {
		if _, ok := got[id]; !ok {
			t.Errorf("%s was not migrated after a bad entry before it", id)
		}
	}
	if _, ok := got["notify/webhook-1"]; ok {
		t.Error("the unprovable webhook URL became an instance")
	}
	if rules := keysWithPrefix(s, string(ownerNotify), ""); len(rules) != 1 {
		t.Errorf("rules %q, want exactly the webhook rule", rules)
	}
	if _, ok := s.Legacy[m6.KeyBlob]; ok {
		t.Error("the blob was not consumed")
	}
	var keys []string
	for _, q := range s.Quarantine {
		keys = append(keys, strings.TrimPrefix(q.Key, memberPrefix))
	}
	slices.Sort(keys)
	for _, k := range []string{
		"ai_endpoints.0", "download_client", "metadata_sources.0", "openai_base_url",
		"plugins.webhook.urls.1", "zz_unread_one", "zz_unread_two",
	} {
		if !slices.Contains(keys, k) {
			t.Errorf("%s was not quarantined; quarantined: %v", k, keys)
		}
	}
}

// TestDelugeMigration_DaemonFieldsNote: a download_client with any daemon field set
// is reported once as not carried, and one with none set is not reported at all.
func TestDelugeMigration_DaemonFieldsNote(t *testing.T) {
	for name, c := range map[string]struct {
		torrent map[string]any
		noted   bool
	}{
		"empty":    {map[string]any{}, false},
		"type":     {map[string]any{"type": "deluge"}, true},
		"host":     {map[string]any{"deluge": map[string]any{"host": "192.0.2.20"}}, true},
		"port":     {map[string]any{"deluge": map[string]any{"port": 58846}}, true},
		"username": {map[string]any{"deluge": map[string]any{"username": "alfred"}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, withRows(headRows(t)), withBlob(func(tree map[string]any) {
				tree["download_client"] = map[string]any{"torrent": c.torrent}
			}))
			if _, err := r.run(t); err != nil {
				t.Fatalf("M6: %v", err)
			}
			noted := false
			for _, e := range r.report.Entries() {
				noted = noted || e.Kind == "note" && e.ID == "download/deluge" && strings.Contains(e.Detail, "daemon fields")
			}
			if noted != c.noted {
				t.Errorf("noted %v, want %v", noted, c.noted)
			}
		})
	}
}

// TestIntegrationsMigration_NoCapabilitiesKeepsNone: an AI endpoint that lists no
// capability gets no capabilities setting, so core's default set applies, rather
// than an empty one that would route nothing.
func TestIntegrationsMigration_NoCapabilitiesKeepsNone(t *testing.T) {
	r := newRig(t, withRows(headRows(t)), withBlob(func(tree map[string]any) {
		eps := tree["ai_endpoints"].([]any)
		delete(eps[1].(map[string]any), "capabilities")
	}))
	s, err := r.run(t)
	if err != nil {
		t.Fatalf("M6: %v", err)
	}
	if _, has := settingsOf(instances(t, s)["ai/local-uv"])["capabilities"]; has {
		t.Error("an endpoint with no capabilities was given a capabilities setting")
	}
	if _, has := settingsOf(instances(t, s)["ai/local-llm"])["capabilities"]; !has {
		t.Error("an endpoint with capabilities lost them")
	}
}
