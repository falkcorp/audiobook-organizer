// file: internal/books/cutover/m4/answers_test.go
// version: 1.0.0
// guid: b3a9e610-4d27-48c5-96f1-0a7e5c2d8b43
// last-edited: 2026-11-18

package m4_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// answersRel is M4's expected answers over the R2 corpus (F5-16): what each
// corpus store must become, as counts and ID lists. The corpus tests compute
// the same numbers from the booted store and compare them with this file, one
// section per question, so a change to the step that moves a count shows up as
// a diff here for review. Regenerate with UPDATE_M4_ANSWERS=1.
const answersRel = "internal/books/testdata/r2corpus/m4/answers.json"

// checkAnswers compares got (marshalled) with the named section of the answers
// file, or writes the section when UPDATE_M4_ANSWERS=1.
func checkAnswers(t *testing.T, section string, got any) {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(answersRel))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", answersRel, err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_M4_ANSWERS") == "1" {
		doc[section] = gotJSON
		out, err := marshalSorted(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, ok := doc[section]
	if !ok {
		t.Fatalf("%s has no section %q (regenerate with UPDATE_M4_ANSWERS=1)", answersRel, section)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(gotJSON, &gotAny); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantAny); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		pretty, _ := json.MarshalIndent(gotAny, "", "  ")
		wantPretty, _ := json.MarshalIndent(wantAny, "", "  ")
		t.Errorf("section %q no longer matches %s\n--- got\n%s\n--- file\n%s", section, answersRel, pretty, wantPretty)
	}
}

// marshalSorted writes the document with its keys sorted and the "_header"
// first, indented two spaces.
func marshalSorted(doc map[string]json.RawMessage) ([]byte, error) {
	generic := map[string]any{}
	for k, v := range doc {
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			return nil, err
		}
		generic[k] = x
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil { // map keys are sorted; "_header" sorts first
		return nil, err
	}
	return buf.Bytes(), nil
}
