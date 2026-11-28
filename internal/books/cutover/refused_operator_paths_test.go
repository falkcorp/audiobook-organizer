// file: internal/books/cutover/refused_operator_paths_test.go
// version: 1.0.0
// guid: 7c2f9e41-3a85-4d06-b1e7-5d8a0c3f6b92
// last-edited: 2026-11-28

package cutover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// refusedEnvCells reads the table shared with scripts/tests/test_deploy_env_check.py.
func refusedEnvCells(t testing.TB) []struct {
	Name    string `json:"name"`
	Refused bool   `json:"refused"`
	Why     string `json:"why"`
} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "refused_env_cells.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cells []struct {
			Name    string `json:"name"`
			Refused bool   `json:"refused"`
			Why     string `json:"why"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cells) == 0 {
		t.Fatal("refused_env_cells.json holds no cells")
	}
	return f.Cells
}

// TestRefusedOperatorPaths_EnvCells pins which env spellings the refused operator
// settings take: the canonical name and its <NAME>_FILE form (core reads both onto
// the retired path), anchored, with at least one character where a `*` stands,
// capitals only. Mutants this kills: EnvForms without the _FILE form (the F1
// finding: a retired openai URL given as a file was neither refused nor read),
// envGlob unanchored, envGlob's `.+` widened to `.*`.
func TestRefusedOperatorPaths_EnvCells(t *testing.T) {
	rules := RefusedOperatorPaths()
	for _, c := range refusedEnvCells(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := false
			for _, r := range rules {
				if direct, viaFile := r.EnvForms(c.Name); direct || viaFile {
					got = true
				}
			}
			if got != c.Refused {
				t.Errorf("EnvForms(%q) matched = %v, want %v (%s)", c.Name, got, c.Refused, c.Why)
			}
		})
	}
}

// TestRefusedOperatorPaths_EnvFormsSeparateDirectFromFile pins which reading
// matched: the books boot check treats a literal null as unset only for the
// direct reading (core tests null against a _FILE file's contents, never against
// the path in the variable).
func TestRefusedOperatorPaths_EnvFormsSeparateDirectFromFile(t *testing.T) {
	r := RefusedOperatorPaths()[0]
	for _, c := range []struct {
		name         string
		direct, file bool
	}{
		{"ABO_INTEGRATIONS_AI_OPENAI_BASE_URL", true, false},
		{"ABO_INTEGRATIONS_AI_OPENAI_BASE_URL_FILE", false, true},
		{"ABO_INTEGRATIONS_AI_OPENAI_URL_FILE", false, false},
	} {
		d, f := r.EnvForms(c.name)
		if d != c.direct || f != c.file {
			t.Errorf("EnvForms(%q) = %v, %v; want %v, %v", c.name, d, f, c.direct, c.file)
		}
	}
}

// TestRefusedOperatorPath_MatchesPath_EmptySegment pins the empty-segment guard:
// a placeholder segment stands for one NON-EMPTY segment. A file key with an
// empty segment is not a source id or a credential key. Mutant: drop the
// `segs[i] == ""` case, so a placeholder takes an empty segment.
func TestRefusedOperatorPath_MatchesPath_EmptySegment(t *testing.T) {
	var family RefusedOperatorPath
	for _, r := range RefusedOperatorPaths() {
		if r.Path == "integrations.metadata.<source id>.credentials.<k>" {
			family = r
		}
	}
	if family.Path == "" {
		t.Fatal("the credentials family is not in RefusedOperatorPaths")
	}
	for _, c := range []struct {
		path string
		want bool
	}{
		{"integrations.metadata.hardcover.credentials.api_token", true},
		{"integrations.metadata.google-books.credentials.api_key", true},
		{"integrations.metadata..credentials.api_key", false},
		{"integrations.metadata.hardcover.credentials.", false},
		{"integrations.metadata.hardcover.credentials", false},
		{"integrations.metadata.hardcover.credentials.api_token.extra", false},
		{"integrations.metadata.hardcover.api_token", false},
	} {
		if got := family.MatchesPath(c.path); got != c.want {
			t.Errorf("MatchesPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestRefusedOperatorPath_EnvInstead pins the env spelling the refusal tells a
// host to set (N4): the canonical name of the new path, and a phrase as it is.
func TestRefusedOperatorPath_EnvInstead(t *testing.T) {
	rules := RefusedOperatorPaths()
	if got, want := rules[0].EnvInstead(), "ABO_INTEGRATIONS_AI_OPENAI_URL"; got != want {
		t.Errorf("openai EnvInstead = %q, want %q", got, want)
	}
	if got := rules[0].EnvMessage(); got == rules[0].Message || !containsAll(got, "ABO_INTEGRATIONS_AI_OPENAI_URL", "integrations.ai.openai.url") {
		t.Errorf("openai EnvMessage = %q, want the path and the env spelling", got)
	}
	if got := rules[1].EnvMessage(); got != rules[1].Message {
		t.Errorf("family EnvMessage = %q, want the plain message (its replacement has no env name)", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
