// file: internal/books/cutover/passthrough_test.go
// version: 1.0.0
// guid: 0d5f7a28-91c4-4e3b-8a16-5f2c7b9e4d10
// last-edited: 2026-11-19

package cutover

import (
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
)

// TestConfigPassthrough pins the predicate: every passthrough or passthrough
// secret target of the rename table, and both ecosystem bindings, are accepted;
// a family placeholder matches one segment; nothing outside the integrations
// subtree, and no integrations path the table does not name, is.
func TestConfigPassthrough(t *testing.T) {
	for _, r := range Table().Rows {
		for _, tg := range r.Targets {
			if tg.Kind != TargetPassthrough && !(tg.Kind == TargetSecret && tg.Passthrough) {
				continue
			}
			p := tg.Path
			if p == "" || p[0] == '<' {
				continue
			}
			// Instantiate a family placeholder with a Batman-flavoured name.
			concrete := p
			for {
				i := strings.IndexByte(concrete, '<')
				if i < 0 {
					break
				}
				j := strings.IndexByte(concrete[i:], '>')
				concrete = concrete[:i] + "batman" + concrete[i+j+1:]
			}
			if !ConfigPassthrough(config.Path(concrete)) {
				t.Errorf("table target %q (as %q) is not accepted", p, concrete)
			}
		}
	}
	for _, b := range ConfigEnvBindings() {
		if !ConfigPassthrough(b.Path) {
			t.Errorf("binding %s -> %s is not accepted", b.Name, b.Path)
		}
	}
	for _, p := range []config.Path{
		"integrations.ai.openai.model",        // under a bound instance, not itself a table target
		"integrations.notify.webhook-.secret", // a family hole needs a non-empty segment tail
		"integrations.notify.webhook-bane.secret.extra",
		// The refused operator settings (RefusedOperatorPaths): the operator
		// layer no longer supplies either, so core must not place them. The
		// books boot check refuses a host that still sets them.
		"integrations.metadata.penguin.credentials.token",
		"integrations.metadata.google-books.credentials.api_key",
		"integrations.ai.openai.base_url",
		"core.log.level",
		"modules.books.scan.root",
		"integrations",
		"",
	} {
		if ConfigPassthrough(p) {
			t.Errorf("%q is accepted", p)
		}
	}
	if !ConfigPassthrough("integrations.notify.webhook-joker.secret") {
		t.Error("a family path with a concrete segment is refused")
	}
	// The 4-segment metadata secrets stay passthrough: they are the adapters'
	// declared fields, not the credentials.<k> family.
	for _, p := range []config.Path{"integrations.metadata.hardcover.api_token", "integrations.metadata.google-books.api_key"} {
		if !ConfigPassthrough(p) {
			t.Errorf("%s is refused though it is a declared passthrough secret", p)
		}
	}
}
