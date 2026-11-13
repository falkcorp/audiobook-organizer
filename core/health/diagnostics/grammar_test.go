// file: core/health/diagnostics/grammar_test.go
// version: 1.0.0
// guid: 4c9e2a17-0d5b-4f38-b6a1-7e8d3f2c5091
// last-edited: 2026-11-13

package diagnostics

import (
	"regexp"
	"strings"
	"testing"
)

// TestGrammars_MatchTheirRegexpSpecs pins the hand-written name predicates to
// the regular expressions that specify them (core forbids package-level
// regexps, 03 R5): every case below must agree with the spec form.
func TestGrammars_MatchTheirRegexpSpecs(t *testing.T) {
	name := regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	placeholder := regexp.MustCompile(`^<secret:[A-Za-z0-9_.-]{1,128}>$`)
	section := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)+$`)
	bundle := regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}\.zip(\.tmp)?$`)

	long := strings.Repeat("a", 128)
	inputs := []string{
		"", ".", "a", "A", "0", "-", "_", "a-b_c", "a.b", "a..b", ".a", "a.", "a.b.c", "core.system", "Core.system",
		"core._x", "core.-x", "core.x_", "core.x-y.z0", "core system", "a/b", "../a", "a\x00", "<secret:bruce>", "<secret:>",
		"<secret:a.b-c_d>", "<secret:bruce>x", "x<secret:bruce>", "<secret:bruce", "<secret:br uce>", "<redacted>",
		long, long + "a", "<secret:" + long + ">", "<secret:" + long + "a>", long + ".zip", long + "a.zip",
		"run.zip", "run.zip.tmp", "run.zip.tmp.tmp", "run.tmp", ".zip", ".zip.tmp", "run.zip ", "run.ZIP", "a.b.zip",
	}
	for _, in := range inputs {
		if got, want := plainName(in), name.MatchString(in); got != want {
			t.Errorf("plainName(%q) = %v, spec says %v", in, got, want)
		}
		if got, want := secretPlaceholder(in), placeholder.MatchString(in); got != want {
			t.Errorf("secretPlaceholder(%q) = %v, spec says %v", in, got, want)
		}
		if got, want := validSectionName(in), section.MatchString(in); got != want {
			t.Errorf("validSectionName(%q) = %v, spec says %v", in, got, want)
		}
		if got, want := isBundleName(in), bundle.MatchString(in); got != want {
			t.Errorf("isBundleName(%q) = %v, spec says %v", in, got, want)
		}
	}
}
