// file: core/integrations/ai/structured_test.go
// version: 1.0.0
// guid: c5e8a3b1-7d49-4f26-91a0-4b6e2d8f3c57
// last-edited: 2026-11-12

package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// verdict is the answer type of the structured tests.
type verdict struct {
	Villain    string   `json:"villain"`
	Confidence float64  `json:"confidence"`
	Mood       string   `json:"mood" enum:"smug,manic"`
	Hideout    *string  `json:"hideout"`
	Aliases    []string `json:"aliases,omitempty"`
}

const goodVerdict = `{"villain":"The Riddler","confidence":0.9,"mood":"smug","hideout":null}`

func structEndpoint(h *harness, features ...ai.Feature) *coretest.FakeAI {
	f := fake()
	set := textEndpoint("joker-7b", "gotham.riddle")
	set.Features = features
	h.addEndpoint("arkham", 1, set, f)
	return f
}

// The schema comes from the Go type: every property required, additional
// properties refused, an optional field nullable, an enum from its tag, and
// the request goes out as a strict schema where the endpoint has it.
func TestGenerateJSON_SchemaFromType(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")
	f := structEndpoint(h, ai.FeatureJSONSchema)
	f.ScriptText(coretest.FakeAIStep{Text: goodVerdict, Usage: ai.Usage{Requests: 1, InputTokens: 7, OutputTokens: 3}})

	got, resp, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, text("who?"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Villain != "The Riddler" || got.Confidence != 0.9 || got.Mood != "smug" || got.Hideout != nil {
		t.Errorf("decoded = %+v", got)
	}
	if resp.Served.Endpoint != aiRef("arkham") || resp.Served.Model != "joker-7b" || resp.Served.Attempts != 1 {
		t.Errorf("Served = %+v", resp.Served)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls, want 1", len(calls))
	}
	out := calls[0].Text.Output
	if out.Kind != ai.OutputJSONSchema || !out.Strict || out.Name != "gotham_riddle" {
		t.Errorf("Output = %+v, want a strict json_schema named after the capability", out)
	}
	var schema struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Additional bool                       `json:"additionalProperties"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(out.Schema, &schema); err != nil {
		t.Fatalf("schema %s: %v", out.Schema, err)
	}
	if schema.Type != "object" || schema.Additional {
		t.Errorf("schema = %s; want an object that refuses additional properties", out.Schema)
	}
	if len(schema.Required) != 5 {
		t.Errorf("required = %v; every property is listed so the schema is strict-output shaped", schema.Required)
	}
	if !strings.Contains(string(schema.Properties["mood"]), `"enum":["smug","manic"]`) {
		t.Errorf("mood = %s, want the enum from its tag", schema.Properties["mood"])
	}
	if !strings.Contains(string(schema.Properties["hideout"]), `"null"`) {
		t.Errorf("hideout = %s, want a nullable string for a pointer field", schema.Properties["hideout"])
	}
	if !strings.Contains(string(schema.Properties["aliases"]), `"null"`) {
		t.Errorf("aliases = %s, want nullable for an omitempty field", schema.Properties["aliases"])
	}
	if resp.Usage.InputTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	// A request's own schema wins over the derived one, and is not enforced
	// beyond the strict decode of T.
	f.ScriptText(coretest.FakeAIStep{Text: goodVerdict})
	own := text("who?")
	own.Output = ai.OutputFormat{Kind: ai.OutputJSONSchema, Name: "custom", Schema: json.RawMessage(`{"type":"object"}`), Strict: false}
	if _, _, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, own); err != nil {
		t.Fatal(err)
	}
	if o := f.Calls()[1].Text.Output; o.Name != "custom" || o.Strict || string(o.Schema) != `{"type":"object"}` {
		t.Errorf("own schema Output = %+v", o)
	}

	// A type with no schema is refused before any call: not at the first answer.
	before := len(f.Calls())
	for name, fn := range map[string]func() error{
		"a channel field": func() error {
			_, _, err := ai.GenerateJSON[struct{ C chan int }](context.Background(), h.r, c, text("x"))
			return err
		},
		"an interface field": func() error {
			_, _, err := ai.GenerateJSON[struct{ V any }](context.Background(), h.r, c, text("x"))
			return err
		},
		"a non-object root": func() error {
			_, _, err := ai.GenerateJSON[[]string](context.Background(), h.r, c, text("x"))
			return err
		},
		"a map with int keys": func() error {
			_, _, err := ai.GenerateJSON[map[int]string](context.Background(), h.r, c, text("x"))
			return err
		},
	} {
		if err := fn(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(f.Calls()) != before {
		t.Error("an unschemable type reached the endpoint")
	}
	// A non-text capability is refused.
	emb := h.capability("embed_notes", func(s *ai.CapabilitySpec) { s.Kind = ai.KindEmbed })
	if _, _, err := ai.GenerateJSON[verdict](context.Background(), h.r, emb, text("x")); err == nil {
		t.Error("GenerateJSON on an embedding capability did not fail")
	}
}

// The features change delivery, never eligibility: json mode with the schema in
// the system prompt where the endpoint has only json_object, plain text with the
// same prompt where it has neither, and the answer is validated the same way.
func TestGenerateJSON_JSONModeFallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		features []ai.Feature
		kind     ai.OutputKind
	}{
		{"json mode only", []ai.Feature{ai.FeatureJSONMode}, ai.OutputJSONObject},
		{"neither", nil, ai.OutputText},
		{"both prefers the strict schema", []ai.Feature{ai.FeatureJSONMode, ai.FeatureJSONSchema}, ai.OutputJSONSchema},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			c := h.capability("riddle")
			f := structEndpoint(h, tc.features...)
			f.ScriptText(coretest.FakeAIStep{Text: "```json\n" + goodVerdict + "\n```"})
			req := text("who?")
			req.System = "You are a detective."
			got, _, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, req)
			if err != nil || got.Villain != "The Riddler" {
				t.Fatalf("GenerateJSON = %+v, %v", got, err)
			}
			sent := f.Calls()[0].Text
			if sent.Output.Kind != tc.kind {
				t.Errorf("Output.Kind = %v, want %v", sent.Output.Kind, tc.kind)
			}
			inPrompt := strings.Contains(sent.System, `"required"`)
			if inPrompt != (tc.kind != ai.OutputJSONSchema) {
				t.Errorf("schema in the system prompt = %v for %v", inPrompt, tc.kind)
			}
			if !strings.HasPrefix(sent.System, "You are a detective.") {
				t.Errorf("the caller's system prompt was lost: %q", sent.System)
			}
		})
	}
}

// One repair turn goes to the SAME endpoint with the wrong answer and what was
// wrong; a second failure is a QualityError that does not fail over and does
// not bench the endpoint.
func TestGenerateJSON_RepairThenQualityError(t *testing.T) {
	t.Parallel()
	t.Run("repaired", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		c := h.capability("riddle")
		f := structEndpoint(h, ai.FeatureJSONSchema)
		f.ScriptText(
			coretest.FakeAIStep{Text: `not json at all`, Usage: ai.Usage{Requests: 1, InputTokens: 10, OutputTokens: 2}},
			coretest.FakeAIStep{Text: goodVerdict, Usage: ai.Usage{Requests: 1, InputTokens: 20, OutputTokens: 4}},
		)
		got, resp, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, text("who?"))
		if err != nil || got.Villain != "The Riddler" {
			t.Fatalf("GenerateJSON = %+v, %v", got, err)
		}
		calls := f.Calls()
		if len(calls) != 2 {
			t.Fatalf("%d calls, want the call and one repair", len(calls))
		}
		msgs := calls[1].Text.Messages
		if len(msgs) != 3 || msgs[1].Role != ai.RoleAssistant || msgs[1].Parts[0].Text != "not json at all" ||
			msgs[2].Role != ai.RoleUser || !strings.Contains(msgs[2].Parts[0].Text, "not accepted") {
			t.Errorf("the repair turn = %+v; want the bad answer and what was wrong with it", msgs)
		}
		if resp.Usage.InputTokens != 30 || resp.Usage.OutputTokens != 6 || resp.Usage.Requests != 2 {
			t.Errorf("usage = %+v, want both turns summed", resp.Usage)
		}
	})
	t.Run("quality error", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		c := h.capability("riddle")
		first := structEndpoint(h, ai.FeatureJSONSchema)
		first.ScriptText(coretest.FakeAIStep{Text: `{"villain": 7}`})
		second := fake()
		second.ScriptText(coretest.FakeAIStep{Text: goodVerdict})
		set2 := textEndpoint("joker-7b", "gotham.riddle")
		h.addEndpoint("blackgate", 2, set2, second)

		_, _, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, text("who?"))
		if _, ok := errors.AsType[*ai.QualityError](err); !ok {
			t.Fatalf("err = %v, want a QualityError", err)
		}
		if got := integrations.ClassOf(context.Background(), err); got != integrations.ClassBadOutput {
			t.Errorf("class = %s, want bad_output", got)
		}
		if n := len(first.Calls()); n != 2 {
			t.Errorf("the endpoint got %d calls, want the call plus one repair", n)
		}
		if n := len(second.Calls()); n != 0 {
			t.Errorf("a quality error failed over (%d call(s) to the second endpoint)", n)
		}
		if _, benched := h.r.BenchedUntil(aiRef("arkham")); benched {
			t.Error("a bad answer benched a healthy endpoint")
		}
	})
	t.Run("repair attempts are config", func(t *testing.T) {
		t.Parallel()
		for repairs, wantCalls := range map[int]int{-1: 1, 2: 3} {
			h := newHarness(t)
			h.setConfig(ai.Config{RepairAttempts: repairs})
			c := h.capability("riddle")
			f := structEndpoint(h, ai.FeatureJSONSchema)
			f.ScriptText(coretest.FakeAIStep{Text: `nope`})
			_, _, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, text("who?"))
			if _, ok := errors.AsType[*ai.QualityError](err); !ok {
				t.Fatalf("repairs=%d: err = %v", repairs, err)
			}
			if n := len(f.Calls()); n != wantCalls {
				t.Errorf("repair_attempts=%d: %d calls, want %d", repairs, n, wantCalls)
			}
		}
	})
}

// The decode is strict: an unknown field, a missing required field, a null for
// a field that is not optional, an enum value the tag does not list, text after
// the JSON and an empty answer are all refused (and repaired once, then a
// QualityError).
func TestGenerateJSON_UnknownFieldRejected(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"an unknown field":           `{"villain":"The Riddler","confidence":0.9,"mood":"smug","hideout":null,"secret_lair":"x"}`,
		"a missing required field":   `{"confidence":0.9,"mood":"smug","hideout":null}`,
		"null for a required field":  `{"villain":null,"confidence":0.9,"mood":"smug","hideout":null}`,
		"an enum value not listed":   `{"villain":"The Riddler","confidence":0.9,"mood":"furious","hideout":null}`,
		"a wrong type":               `{"villain":"The Riddler","confidence":"high","mood":"smug","hideout":null}`,
		"text after the JSON":        goodVerdict + ` and that is my answer`,
		"two JSON values":            goodVerdict + goodVerdict,
		"an empty answer":            ``,
		"only a fence":               "```json\n```",
		"an array instead of object": `[1,2,3]`,
		"an unknown nested field":    `{"villain":"x","confidence":1,"mood":"smug","hideout":null,"aliases":[]}x`,
		"a number for the pointer":   `{"villain":"x","confidence":1,"mood":"smug","hideout":4}`,
	}
	for name, answer := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			c := h.capability("riddle")
			f := structEndpoint(h, ai.FeatureJSONSchema)
			f.ScriptText(coretest.FakeAIStep{Text: answer})
			_, _, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, text("who?"))
			if _, ok := errors.AsType[*ai.QualityError](err); !ok {
				t.Fatalf("answer %q: err = %v, want a QualityError", answer, err)
			}
		})
	}
	good := map[string]string{
		"a plain answer":          goodVerdict,
		"a fenced answer":         "```json\n" + goodVerdict + "\n```",
		"a bare fence":            "```\n" + goodVerdict + "\n```",
		"a hideout":               `{"villain":"x","confidence":1,"mood":"manic","hideout":"Crime Alley"}`,
		"aliases present":         `{"villain":"x","confidence":1,"mood":"manic","hideout":null,"aliases":["Edward"]}`,
		"aliases null":            `{"villain":"x","confidence":1,"mood":"manic","hideout":null,"aliases":null}`,
		"an absent optional":      `{"villain":"x","confidence":1,"mood":"smug"}`,
		"surrounding whitespace":  "\n\n  " + goodVerdict + "  \n",
		"an integer for a number": `{"villain":"x","confidence":1,"mood":"smug","hideout":null}`,
	}
	for name, answer := range good {
		t.Run("accepts "+name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			c := h.capability("riddle")
			f := structEndpoint(h, ai.FeatureJSONSchema)
			f.ScriptText(coretest.FakeAIStep{Text: answer})
			if _, _, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, text("who?")); err != nil {
				t.Fatalf("answer %q refused: %v", answer, err)
			}
			if n := len(f.Calls()); n != 1 {
				t.Errorf("%d calls for an acceptable answer", n)
			}
		})
	}

	// A request that brings its own schema is still decoded strictly into T:
	// the unknown field is refused even though the model was never told T's shape.
	h := newHarness(t)
	c := h.capability("riddle")
	f := structEndpoint(h, ai.FeatureJSONSchema)
	f.ScriptText(coretest.FakeAIStep{Text: `{"villain":"x","confidence":1,"mood":"smug","hideout":null,"extra":1}`})
	req := text("who?")
	req.Output = ai.OutputFormat{Kind: ai.OutputJSONSchema, Name: "own", Schema: json.RawMessage(`{"type":"object"}`)}
	if _, _, err := ai.GenerateJSON[verdict](context.Background(), h.r, c, req); err == nil {
		t.Error("an unknown field passed under the request's own schema")
	}
}

// A QualityError classifies as bad_output for whoever reads it with
// integrations.ClassOf, beats the class of an error it wraps, and still
// unwraps to that error.
func TestQualityError_ClassWinsAndUnwraps(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("the answer was a riddle")
	inner := &integrations.Error{Op: "generate", Class: integrations.ClassTransient, Err: sentinel}
	q := ai.Quality(inner)

	if got := integrations.ClassOf(context.Background(), q); got != integrations.ClassBadOutput {
		t.Errorf("ClassOf(Quality(transient)) = %s, want bad_output", got)
	}
	if got := integrations.ClassOf(context.Background(), ai.Quality(sentinel)); got != integrations.ClassBadOutput {
		t.Errorf("ClassOf(Quality(plain)) = %s, want bad_output", got)
	}
	if !errors.Is(q, sentinel) {
		t.Error("Quality hides the error it wraps from errors.Is")
	}
	var qe *ai.QualityError
	if !errors.As(q, &qe) || qe.Err != inner {
		t.Error("errors.As does not find the QualityError")
	}
	if q.Error() != inner.Error() {
		t.Errorf("Error() = %q, want the wrapped text %q", q.Error(), inner.Error())
	}
	if ai.Quality(nil) != nil {
		t.Error("Quality(nil) is not nil")
	}
	// A done parent still wins over every class.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := integrations.ClassOf(ctx, q); got != integrations.ClassCanceled {
		t.Errorf("a done parent = %s, want canceled", got)
	}
}
