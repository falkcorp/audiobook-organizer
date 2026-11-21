// file: internal/books/appconfig/secrets_test.go
// version: 1.0.0
// guid: 9b4e2d70-1a58-4c36-8f0d-5e7a3c1b9d84
// last-edited: 2026-11-19

package appconfig

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/secrets"
)

// TestSecretAdmit_IsTheTableAndNothingElse: every secret target the rename
// table lists is admitted (a placeholder filled with one segment), and names
// the table does not list are not: another module's, a sibling field, one
// segment too many or too few, a placeholder filled with nothing.
func TestSecretAdmit_IsTheTableAndNothingElse(t *testing.T) {
	admit := SecretAdmit()
	var n int
	for _, r := range cutover.Table().Rows {
		for _, tg := range r.Targets {
			if tg.Kind != cutover.TargetSecret {
				continue
			}
			n++
			var segs []string
			for s := range strings.SplitSeq(tg.Path, ".") {
				if strings.HasPrefix(s, "<") {
					s = "robin"
				}
				segs = append(segs, s)
			}
			name := strings.Join(segs, ".")
			if !admit(secrets.Name(name)) {
				t.Errorf("table secret target %q (as %q) is not admitted", tg.Path, name)
			}
			if admit(secrets.Name(name + ".extra")) {
				t.Errorf("%q admitted with one segment too many", name)
			}
			if admit(secrets.Name(name[:strings.LastIndex(name, ".")])) {
				t.Errorf("%q admitted with its last segment cut", name)
			}
		}
	}
	if n < 15 {
		t.Fatalf("only %d secret targets in the table; the golden lists 15", n)
	}
	for _, out := range []secrets.Name{
		"modules.bane.venom", "core.http.basic_gate.user", "integrations.ai.openai.base_url",
		"integrations.metadata..credentials.key", "integrations.notify.webhook-3", "", "core",
	} {
		if admit(out) {
			t.Errorf("%q admitted though no table row maps it", out)
		}
	}
	for _, in := range []secrets.Name{
		"integrations.metadata.audible.credentials.token", "integrations.notify.webhook-2.secret", "modules.books.plugins.alfred.settings",
	} {
		if !admit(in) {
			t.Errorf("%q not admitted though a placeholder row maps it", in)
		}
	}
}
