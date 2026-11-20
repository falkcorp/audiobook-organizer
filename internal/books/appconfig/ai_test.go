// file: internal/books/appconfig/ai_test.go
// version: 1.0.1
// guid: 5e1c8b3a-7d42-4f96-a0b8-3c9d2e647f15
// last-edited: 2026-11-19

package appconfig

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// aiList is a list as HEAD's seed builds it for a local server, a local mode for
// both uses and an OpenAI key present: a local-llm row and an openai row.
const aiList = `[{"id":"local-llm","url":"http://192.0.2.10:11434/v1","chat_model":"qwen2.5:7b-instruct","embed_model":"bge-m3","enabled":true,` +
	`"capabilities":["llm.filename_parse","llm.author_review","llm.author_discovery","llm.metadata_rerank","embed.text"]},` +
	`{"id":"openai","url":"https://api.openai.com/v1","auth_ref":"openai_api_key","priority":20,"enabled":true,"capabilities":[]}]`

func aiEngine(t *testing.T, list string) (*Engine, *fakeAccess, *fakeResidual) {
	t.Helper()
	f := newFake()
	res := newFakeResidual()
	if list != "" {
		res.vals["ai_endpoints"] = json.RawMessage(list)
	}
	e := New(f, nil, WithResidual(res))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e, f, res
}

func viewValue(e *Engine, name string) (string, bool) {
	for _, kv := range e.View().Keys {
		if kv.Name == name {
			return string(kv.Value), true
		}
	}
	return "", false
}

// TestAIBackend_RowsReadTheList: the five ai_backend.* fields are rows of the
// view and the projection, read off the ai_endpoints list.
func TestAIBackend_RowsReadTheList(t *testing.T) {
	e, _, _ := aiEngine(t, aiList)
	got := e.Snapshot().AIBackend
	if got.LocalBaseURL != "http://192.0.2.10:11434/v1" || got.LocalLLMModel != "qwen2.5:7b-instruct" || got.LocalEmbeddingModel != "bge-m3" || got.LLMMode != "local" {
		t.Fatalf("projection = %+v", got)
	}
	for name, want := range map[string]string{
		"ai_backend.local_base_url": `"http://192.0.2.10:11434/v1"`, "ai_backend.llm_mode": `"local"`,
		"ai_backend.local_llm_model": `"qwen2.5:7b-instruct"`, "ai_backend.local_embedding_model": `"bge-m3"`,
	} {
		if v, ok := viewValue(e, name); !ok || v != want {
			t.Errorf("view %s = %s (present %v), want %s", name, v, ok, want)
		}
	}
	for _, kv := range e.View().Keys {
		if strings.HasPrefix(kv.Name, "ai_backend.") && (!kv.Admin || kv.Class != config.KeyClassInput) {
			t.Errorf("%s: admin=%v class=%s; want an admin input row", kv.Name, kv.Admin, kv.Class)
		}
	}
}

// TestAIBackend_NoListKeepsTheBase: nothing stored, the base holds the fields.
func TestAIBackend_NoListKeepsTheBase(t *testing.T) {
	f := newFake()
	e := New(f, nil, WithResidual(newFakeResidual()), WithBase(func() config.Config {
		var c config.Config
		c.AIBackend.LocalLLMModel = "qwen2.5:7b-instruct"
		return c
	}))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().AIBackend.LocalLLMModel != "qwen2.5:7b-instruct" {
		t.Fatal("the base value was lost")
	}
}

// TestAIBackend_WriteFoldsIntoTheList: PUT ai_backend.* is one write of the list,
// reported under the field names, and the projection follows.
func TestAIBackend_WriteFoldsIntoTheList(t *testing.T) {
	e, _, res := aiEngine(t, aiList)
	r, err := e.Update(t.Context(), adminActor, set("ai_backend.llm_mode", `"disabled"`, "ai_backend.local_llm_model", `"batman-7b"`))
	if err != nil {
		t.Fatal(err)
	}
	if res.saves != 1 || !strings.Contains(string(res.vals["ai_endpoints"]), "batman-7b") {
		t.Fatalf("saves=%d list=%s", res.saves, res.vals["ai_endpoints"])
	}
	if got := e.Snapshot().AIBackend; got.LLMMode != "disabled" || got.LocalLLMModel != "batman-7b" {
		t.Fatalf("projection = %+v", got)
	}
	want := map[string]bool{"ai_backend.llm_mode": true, "ai_backend.local_llm_model": true}
	for _, a := range r.Applied {
		delete(want, a)
	}
	if len(want) != 0 || len(r.Applied) != 2 {
		t.Fatalf("Applied = %v; want the two field names", r.Applied)
	}
}

// TestAIBackend_UnchangedWriteIsANoOp: a form that re-sends what the list reads.
func TestAIBackend_UnchangedWriteIsANoOp(t *testing.T) {
	e, f, res := aiEngine(t, aiList)
	r, err := e.Update(t.Context(), adminActor, set("ai_backend.llm_mode", `"local"`, "ai_backend.local_base_url", `"http://192.0.2.10:11434/v1"`, "ai_backend.local_embedding_model", `"bge-m3"`))
	if err != nil || len(r.Applied) != 0 || res.saves != 0 || len(f.applied) != 0 {
		t.Fatalf("err=%v applied=%v saves=%d", err, r.Applied, res.saves)
	}
}

// TestAIBackend_NeedsAdminAndCannotBeCleared.
func TestAIBackend_NeedsAdminAndCannotBeCleared(t *testing.T) {
	e, _, res := aiEngine(t, aiList)
	user := config.Actor{Subject: "user:robin", Interactive: true, Can: func(string) bool { return false }}
	if _, err := e.Update(t.Context(), user, set("ai_backend.llm_mode", `"disabled"`)); !errors.Is(err, coreconfig.ErrForbidden) || !contains(err, "ai_backend.llm_mode") {
		t.Fatalf("non-admin: %v", err)
	}
	if _, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"ai_backend.llm_mode"}}); !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, "ai_backend.llm_mode") {
		t.Fatalf("unset: %v", err)
	}
	if res.saves != 0 {
		t.Fatalf("a refused write saved %d times", res.saves)
	}
}

// TestAIBackend_RefusalsNameTheField: a value the list cannot hold is an
// ErrInvalid naming the field, and nothing is written.
func TestAIBackend_RefusalsNameTheField(t *testing.T) {
	e, _, res := aiEngine(t, `[{"id":"openai","url":"https://api.openai.com/v1","auth_ref":"openai_api_key","priority":20,"enabled":true,"capabilities":[]}]`)
	for _, c := range []struct {
		name string
		u    config.Update
		want string
	}{
		{"local mode with no local row", set("ai_backend.llm_mode", `"local"`), "ai_backend.llm_mode"},
		{"unknown mode", set("ai_backend.llm_mode", `"warp"`), "ai_backend.llm_mode"},
		{"not a string", set("ai_backend.local_llm_model", `5`), "ai_backend.local_llm_model"},
		{"url with credentials", set("ai_backend.local_base_url", `"http://bruce:pw@192.0.2.10/v1"`), "ai_backend.local_base_url"},
	} {
		if _, err := e.Update(t.Context(), adminActor, c.u); !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, c.want) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	if res.saves != 0 {
		t.Fatalf("saves = %d", res.saves)
	}
}

// TestAIBackend_WithTheListInTheSameUpdate: the fold starts from the list the
// same update sets, not the stored one.
func TestAIBackend_WithTheListInTheSameUpdate(t *testing.T) {
	e, _, res := aiEngine(t, `[]`)
	if _, err := e.Update(t.Context(), adminActor, set("ai_endpoints", aiList, "ai_backend.local_llm_model", `"batman-7b"`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.vals["ai_endpoints"]), "batman-7b") || !strings.Contains(string(res.vals["ai_endpoints"]), "embed.text") {
		t.Fatalf("list = %s", res.vals["ai_endpoints"])
	}
}

// TestAIBackend_FoldedURLIsEgressChecked: the local row an operator gave an
// auth_ref sends that key to its URL, so moving the URL through ai_backend is
// held to the same rule as writing the list.
func TestAIBackend_FoldedURLIsEgressChecked(t *testing.T) {
	list := `[{"id":"local-llm","url":"http://192.0.2.10:11434/v1","auth_ref":"openai_api_key","chat_model":"a","embed_model":"b","enabled":true,"capabilities":[]}]`
	e, f, res := aiEngine(t, list)
	f.secrets[openAIKeyName] = "harley"
	if err := e.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err := e.Update(t.Context(), adminActor, set("ai_backend.local_base_url", `"http://192.0.2.99:11434/v1"`))
	if !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, "ai_endpoints") || res.saves != 0 {
		t.Fatalf("err = %v saves=%d", err, res.saves)
	}
	if _, err := e.Update(t.Context(), adminActor, set("ai_backend.local_base_url", `"http://192.0.2.99:11434/v1"`, "openai_api_key", `"oswald"`)); err != nil {
		t.Fatalf("with a new key: %v", err)
	}
}

// TestAIBackend_MutateRoundTrip: Mutate changing an ai_backend field reaches the list.
func TestAIBackend_MutateRoundTrip(t *testing.T) {
	e, _, res := aiEngine(t, aiList)
	if err := e.Mutate(func(c *config.Config) { c.AIBackend.LocalEmbeddingModel = "bge-large" }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.vals["ai_endpoints"]), "bge-large") || e.Snapshot().AIBackend.LocalEmbeddingModel != "bge-large" {
		t.Fatalf("list = %s", res.vals["ai_endpoints"])
	}
}

// TestAIBackend_MalformedListIsUnrepresentable (two local rows, which the five
// fields cannot say): the base values stay, the rest of
// the projection publishes, and the fault is named.
func TestAIBackend_MalformedListIsUnrepresentable(t *testing.T) {
	f := newFake()
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	res := newFakeResidual()
	res.vals["ai_endpoints"] = json.RawMessage(`[{"id":"local-llm"},{"id":"local-llm"}]`)
	e := New(f, nil, WithResidual(res))
	if err := e.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if e.Snapshot().LogLevel != "warn" || !contains(errors.New(strings.Join(e.Unrepresentable(), ",")), "ai_backend") {
		t.Fatalf("log level %q, unrepresentable %v", e.Snapshot().LogLevel, e.Unrepresentable())
	}
}
