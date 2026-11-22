// file: internal/books/cutover/m6/openai_row_test.go
// version: 0.1.0
// guid: 7d2f4a90-3b6e-4c18-a5e7-91c0b8d3f264
// last-edited: 2026-11-22

package m6_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
)

// openAIRowBlob appends row to the fixture's ai_endpoints. With stripOthers it
// first removes the auth_ref of every other row, so no row copies the stored
// OpenAI key and the group pass has the key to say nothing refers to.
func openAIRowBlob(row map[string]any, stripOthers bool) func(*rig) {
	return withBlob(func(tree map[string]any) {
		if stripOthers {
			for _, ep := range tree["ai_endpoints"].([]any) {
				delete(ep.(map[string]any), "auth_ref")
			}
		}
		tree["ai_endpoints"] = append(tree["ai_endpoints"].([]any), row)
	})
}

// TestOpenAIRow_ExactRowBindsTheKeyOnlyWhenHEADSentIt (r2 m-f): HEAD sent the
// OpenAI key to a row whose auth_ref is "openai_api_key" and to no other
// (poolSecret); a row with no auth_ref got the dummy local key, and a row whose
// auth_ref names nothing was uncallable. The exact openai row binds the key M2
// moved to its name, and pairs with the operator's key, only in the first case.
// In the others the key is left unreferenced and the report says so, where binding
// it would serve a credential HEAD withheld.
func TestOpenAIRow_ExactRowBindsTheKeyOnlyWhenHEADSentIt(t *testing.T) {
	for _, c := range []struct {
		name, authRef string
		bound         bool
	}{
		{"auth_ref openai_api_key", "openai_api_key", true},
		{"no auth_ref", "", false},
		{"auth_ref no secret answers to", "batcave_key", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := map[string]any{"id": "openai", "label": "OpenAI (exact)", "protocol": "openai_compat", "url": "https://llm.invalid/v1", "enabled": true}
			if c.authRef != "" {
				row["auth_ref"] = c.authRef
			}
			r, s := runFixture(t, openAIRowBlob(row, true))
			inst, ok := instances(t, s)["ai/openai"]
			if !ok || inst["name"] != "OpenAI (exact)" {
				t.Fatalf("ai/openai is %v, want the exact row and no synthesised one", inst["name"])
			}
			bound := refName(settingsOf(inst), "api_key")
			_, paired := inst["operator_secret_origin"]
			unref := reportHas(r, "note", "ai/openai", "is referred to by no migrated AI endpoint")
			if c.bound {
				if bound != "integrations.ai.openai.api_key" || !paired || unref {
					t.Errorf("api_key %q, operator pairing %v, unreferenced note %v: want HEAD's key bound and paired", bound, paired, unref)
				}
				return
			}
			if bound != "" || paired {
				t.Errorf("api_key %q, operator pairing %v: HEAD sent this row no key", bound, paired)
			}
			if !unref {
				t.Error("the stored key no row refers to is not reported")
			}
		})
	}
}

// TestOpenAIRow_RenamedRowWithoutAStoredKeyBindsNothing (r2 m-d): a row renamed to
// openai with auth_ref openai_api_key, when no OpenAI key is stored (HEAD's
// lived in the environment, or none was set). There is no key to bind, so no ref
// is written, no binding is reported, and the row is written all the same.
func TestOpenAIRow_RenamedRowWithoutAStoredKeyBindsNothing(t *testing.T) {
	row := map[string]any{"id": "OpenAI", "label": "OpenAI (hand-typed)", "protocol": "openai_compat", "url": "https://llm.invalid/v1", "enabled": true, "auth_ref": "openai_api_key"}
	r, s := runFixture(t, openAIRowBlob(row, false), func(r *rig) { delete(r.secrets.seed, "integrations.ai.openai.api_key") })
	inst, ok := instances(t, s)["ai/openai"]
	if !ok || inst["name"] != "OpenAI (hand-typed)" {
		t.Fatalf("ai/openai is %v, want the renamed row", inst["name"])
	}
	if g := refName(settingsOf(inst), "api_key"); g != "" {
		t.Errorf("api_key refs %q with no stored key", g)
	}
	if reportHas(r, "note", "ai/openai", "is bound to the renamed row") {
		t.Error("a binding is reported although none was made")
	}
	if reportHas(r, "note", "ai/openai", "is referred to by no migrated AI endpoint") {
		t.Error("the unreferenced-key note fires with no key stored")
	}
}

// TestOpenAIRow_RefusedRenamedRowReportsNoBinding (r2 n1): the note that HEAD's
// key is bound to a renamed row is said once the importer accepted the instance.
// When core refuses it the row is quarantined, nothing is bound, and the report
// must not say otherwise.
func TestOpenAIRow_RefusedRenamedRowReportsNoBinding(t *testing.T) {
	row := map[string]any{"id": "OpenAI", "label": "OpenAI (hand-typed)", "protocol": "openai_compat", "url": "https://llm.invalid/v1", "enabled": true, "auth_ref": "openai_api_key"}
	r, s := runFixture(t, openAIRowBlob(row, false), conflict("instance", "ai/openai"))
	if _, ok := instances(t, s)["ai/openai"]; ok {
		t.Fatal("ai/openai was written although core refused it")
	}
	if reportHas(r, "note", "ai/openai", "is bound to the renamed row") {
		t.Error("the report says the key is bound to a row that was never written")
	}
	quarantined := false
	for _, q := range s.Quarantine {
		quarantined = quarantined || q.Key == m6.KeyBlob+"#ai_endpoints.2"
	}
	if !quarantined {
		t.Error("the refused row is not quarantined")
	}
}

// TestSynthesisedOpenAI_FollowsEnableAIParsing (r2 m-a): the ai/openai row M6
// synthesises for HEAD's legacy OpenAI path is ticked with the capabilities the
// legacy sites served and is enabled exactly when enable_ai_parsing is on. Off,
// every one of those sites refused to run, so the row must not be enabled (an
// enabled row with the key is health-probed, and used); it keeps its ticks, and
// either way the report says which it is.
func TestSynthesisedOpenAI_FollowsEnableAIParsing(t *testing.T) {
	wantTicks := []string{"books.author_review", "books.author_discovery", "books.author_dedup_batch", "books.diagnostics"}
	for _, c := range []struct {
		name string
		off  bool
		note string
	}{
		{"on", false, "the capabilities HEAD's legacy OpenAI path served with enable_ai_parsing on"},
		{"off", true, "the synthesised row is written disabled: enable_ai_parsing is off"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, s := runFixture(t, func(r *rig) { r.aiParsingOff = c.off })
			inst, ok := instances(t, s)["ai/openai"]
			if !ok {
				t.Fatal("the fixture's synthesised ai/openai is missing")
			}
			if enabled, _ := inst["enabled"].(bool); enabled == c.off {
				t.Errorf("enabled = %v with enable_ai_parsing off = %v", enabled, c.off)
			}
			if got := strs(settingsOf(inst)["capabilities"]); !slices.Equal(got, wantTicks) {
				t.Errorf("ticks %v, want %v", got, wantTicks)
			}
			if !reportHas(r, "note", "ai/openai", c.note) {
				t.Errorf("no note %q", c.note)
			}
		})
	}
}

// TestSynthesisedOpenAI_UnreadableEnableAIParsingFailsTheBoot: the switch decides
// whether HEAD's legacy path ran, and a guess either way is wrong (enabled, it
// serves what HEAD refused; disabled, it drops what HEAD served), so a failed
// read stops the step, and a store with no key to synthesise a row for never
// asks.
func TestSynthesisedOpenAI_UnreadableEnableAIParsingFailsTheBoot(t *testing.T) {
	r := newRig(t, withRows(headRows(t)), func(r *rig) { r.aiParsingErr = errors.New("the config store is down") })
	if _, err := r.run(t); err == nil || !strings.Contains(err.Error(), "enable_ai_parsing") {
		t.Fatalf("run = %v, want the read failure", err)
	}
	// No stored key: nothing is synthesised, so the switch is never read.
	runFixture(t, func(r *rig) {
		r.aiParsingErr = errors.New("never read")
		delete(r.secrets.seed, "integrations.ai.openai.api_key")
	})
}
