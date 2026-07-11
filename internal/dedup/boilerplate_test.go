// file: internal/dedup/boilerplate_test.go
// version: 1.0.0
// guid: 50999f4c-4d92-482f-8f6d-a637c6497352
// last-edited: 2026-07-11

// Tests for the boilerplate title blocklist moved out of engine.go into
// boilerplate.go (INIT-4 T5): default-parity (every compiled-in pattern
// still flags, exactly like the pre-move hardcoded lists), config-extension
// (extras appended via config.DedupBoilerplateConfig get flagged, and never
// replace the compiled-in defaults — Decision 8), and anti-over-suppression
// (real titles that merely start with a boilerplate-adjacent word, e.g.
// "Introduction to Algorithms", must never be flagged).
package dedup

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// resetBoilerplatePatternsForTestOnly clears the cached effective pattern
// set (populated once per process by boilerplateInit) so the next
// isBoilerplateTitle call re-derives it from the compiled-in defaults plus
// whatever config.AppConfig.DedupBoilerplate extras are set at that moment.
// Test-only: production code never resets this cache.
func resetBoilerplatePatternsForTestOnly() {
	boilerplateInit = sync.Once{}
	effectiveTitlePatterns = nil
	effectivePrefixPatterns = nil
}

// withBoilerplateExtras sets config.AppConfig.DedupBoilerplate to the given
// extras for the duration of the test, forces isBoilerplateTitle to
// re-derive its pattern cache on next use, and restores/reset everything on
// cleanup so no state leaks into other tests in this package.
func withBoilerplateExtras(t *testing.T, titles, prefixes []string) {
	t.Helper()
	prev := config.Snapshot().DedupBoilerplate
	config.Mutate(func(c *config.Config) {
		c.DedupBoilerplate = config.DedupBoilerplateConfig{
			ExtraTitlePatterns:  titles,
			ExtraPrefixPatterns: prefixes,
		}
	})
	resetBoilerplatePatternsForTestOnly()
	t.Cleanup(func() {
		config.Mutate(func(c *config.Config) {
			c.DedupBoilerplate = prev
		})
		resetBoilerplatePatternsForTestOnly()
	})
}

// TestIsBoilerplateTitle_DefaultPatternsAllFlagged proves every compiled-in
// pattern (both the exact-title list and the anchored-prefix list) is still
// flagged after the move to boilerplate.go — default-parity with the
// pre-move hardcoded engine.go behavior.
func TestIsBoilerplateTitle_DefaultPatternsAllFlagged(t *testing.T) {
	resetBoilerplatePatternsForTestOnly()
	t.Cleanup(resetBoilerplatePatternsForTestOnly)

	for _, p := range boilerplateTitlePatterns {
		if !isBoilerplateTitle(p) {
			t.Errorf("exact pattern %q: expected isBoilerplateTitle=true, got false", p)
		}
	}
	for _, p := range boilerplateTitlePrefixPatterns {
		// Prefix patterns match when followed by a space and trailing text
		// (strings.HasPrefix(normalized, pattern+" ")), so exercise the
		// anchored form rather than the bare pattern.
		title := p + " and some trailing publisher copy"
		if !isBoilerplateTitle(title) {
			t.Errorf("prefix pattern %q (as %q): expected isBoilerplateTitle=true, got false", p, title)
		}
	}
}

// TestIsBoilerplateTitle_AntiOverSuppression proves real book titles that
// merely share a boilerplate-adjacent leading word survive (are NOT
// flagged), with defaults alone and with a config extras list active.
func TestIsBoilerplateTitle_AntiOverSuppression(t *testing.T) {
	realTitles := []string{
		"Introduction to Algorithms",
		"The End Credits of a Life",
	}

	t.Run("defaults_only", func(t *testing.T) {
		resetBoilerplatePatternsForTestOnly()
		t.Cleanup(resetBoilerplatePatternsForTestOnly)

		for _, title := range realTitles {
			if isBoilerplateTitle(title) {
				t.Errorf("real title %q: expected isBoilerplateTitle=false, got true", title)
			}
		}
	})

	t.Run("with_extras_active", func(t *testing.T) {
		withBoilerplateExtras(t,
			[]string{"a completely different exact boilerplate title"},
			[]string{"totally unrelated prefix"},
		)

		for _, title := range realTitles {
			if isBoilerplateTitle(title) {
				t.Errorf("real title %q: expected isBoilerplateTitle=false even with extras active, got true", title)
			}
		}
	})
}

// TestIsBoilerplateTitle_ConfigExtraPattern proves a title matching only a
// config-supplied extra (not any compiled-in default) gets flagged once the
// extras are active — the config-extension path works end to end.
func TestIsBoilerplateTitle_ConfigExtraPattern(t *testing.T) {
	withBoilerplateExtras(t,
		[]string{"Ce Livre Est Termine"}, // exact extra (normalization-cased input)
		[]string{"Bienvenue Chez"},       // prefix extra
	)

	if !isBoilerplateTitle("Ce Livre Est Termine") {
		t.Error("exact config extra: expected isBoilerplateTitle=true, got false")
	}
	if !isBoilerplateTitle("Bienvenue Chez Audible France") {
		t.Error("prefix config extra: expected isBoilerplateTitle=true, got false")
	}
	if isBoilerplateTitle("An Unrelated Real Book Title") {
		t.Error("unrelated real title: expected isBoilerplateTitle=false, got true")
	}

	// Empty/whitespace-only extras must never become match-everything
	// patterns.
	withBoilerplateExtras(t, []string{"  ", ""}, []string{" "})
	if isBoilerplateTitle("Any Title At All") {
		t.Error("blank config extras: expected no match-everything behavior, got true")
	}
	if isBoilerplateTitle("") {
		t.Error("empty title: expected isBoilerplateTitle=false, got true")
	}
}

// TestBoilerplateConfigExtension proves that with config extras active, the
// compiled-in defaults are ALWAYS retained (Decision 8: extension-only, no
// replace escape hatch) — a compiled-in pattern still hits alongside the
// extras.
func TestBoilerplateConfigExtension(t *testing.T) {
	withBoilerplateExtras(t,
		[]string{"a brand new publisher boilerplate line"},
		nil,
	)

	// Compiled-in default still flags.
	if !isBoilerplateTitle("this is audible") {
		t.Error("compiled-in default pattern must still flag with extras active, but did not")
	}
	// The new extra also flags.
	if !isBoilerplateTitle("a brand new publisher boilerplate line") {
		t.Error("config extra pattern must flag once active, but did not")
	}
}

// TestIsBoilerplateTitle_EmptyConfigByteIdenticalToDefaults proves that with
// an empty (zero-value) DedupBoilerplateConfig — the out-of-the-box state —
// isBoilerplateTitle behaves exactly like the pre-move hardcoded lists: every
// default pattern flags, no extra patterns exist to flag.
func TestIsBoilerplateTitle_EmptyConfigByteIdenticalToDefaults(t *testing.T) {
	withBoilerplateExtras(t, nil, nil)

	for _, p := range boilerplateTitlePatterns {
		if !isBoilerplateTitle(p) {
			t.Errorf("empty-config default pattern %q: expected true, got false", p)
		}
	}
	if isBoilerplateTitle("Introduction to Algorithms") {
		t.Error("empty-config real title: expected false, got true")
	}
}
