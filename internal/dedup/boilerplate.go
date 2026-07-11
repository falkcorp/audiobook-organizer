// file: internal/dedup/boilerplate.go
// version: 1.0.0
// guid: 2349d60e-e5e8-4c03-b3a7-0123a0b0bf36
// last-edited: 2026-07-11

package dedup

import (
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// boilerplateTitlePatterns are exact publisher intro/outro "titles" that are
// not real books and must not seed dedup matches. Seeded from
// docs/agent-tasks/dedup-intro-falsepositive/FINDINGS.md and safe to extend.
//
// This list is the compiled-in DEFAULT set and is ALWAYS active — config
// extras (config.DedupBoilerplateConfig, INIT-4 T5) only APPEND to it, never
// replace it (spec docs/specs/2026-07-10-filtering-search-design.md Decision
// 8: a replace escape hatch would let a misconfigured deployment silently
// drop all Audible/publisher suppression and re-open the dedup false-positive
// bug this list exists to prevent).
var boilerplateTitlePatterns = []string{
	"this is audible",
	"audible hopes you have enjoyed this program",
	"audible hopes you have enjoyed this book",
	"audible studios presents",
	"audible presents",
	"this is an audible original",
	"end credits",
	"credits",
	"opening credits",
	"closing credits",
	"intro",
	"introduction",
	"outro",
	"epilogue music",
	"publisher introduction",
	"publisher's note",
	"produced by audible studios",
	"recorded books presents",
	"graphic audio presents",
	"brilliance audio presents",
}

// boilerplateTitlePrefixPatterns are anchored boilerplate phrases that may carry
// trailing publisher copy. Keep this list narrower than the exact-title list so
// real books like "Introduction to Algorithms" still match normally.
var boilerplateTitlePrefixPatterns = []string{
	"this is audible",
	"audible hopes you have enjoyed this program",
	"audible hopes you have enjoyed this book",
	"audible studios presents",
	"audible presents",
	"this is an audible original",
	"produced by audible studios",
	"recorded books presents",
	"graphic audio presents",
	"brilliance audio presents",
}

// boilerplateInit guards the one-time build of effectiveTitlePatterns /
// effectivePrefixPatterns from the compiled-in defaults above plus any
// config.AppConfig.DedupBoilerplate extras. Loading once (rather than on
// every isBoilerplateTitle call) keeps the hot per-book scan path lock-free
// after startup — isBoilerplateTitle is called from the AcoustIDScan
// per-pair gates, which must stay a fast, deterministic, non-blocking
// function (see the emitShards/bookCacheMu design notes in engine.go).
var boilerplateInit sync.Once

// effectiveTitlePatterns / effectivePrefixPatterns are the normalized
// pattern sets isBoilerplateTitle actually matches against: the compiled-in
// defaults ALWAYS present, plus normalized config extras appended (never
// replacing). Populated once by loadBoilerplatePatterns.
var effectiveTitlePatterns []string
var effectivePrefixPatterns []string

// loadBoilerplatePatterns builds effectiveTitlePatterns/effectivePrefixPatterns
// from the compiled-in defaults plus normalized config extras (read from
// config.Snapshot().DedupBoilerplate). Always starts from the full
// compiled-in slices — config can only APPEND, never replace or remove a
// default pattern (Decision 8). Empty/whitespace-only extras are dropped so
// an empty config entry can never become a match-everything pattern. With no
// configured extras (the default, empty DedupBoilerplateConfig), the
// effective sets are byte-identical to the compiled-in defaults.
func loadBoilerplatePatterns() {
	effectiveTitlePatterns = append([]string(nil), boilerplateTitlePatterns...)
	effectivePrefixPatterns = append([]string(nil), boilerplateTitlePrefixPatterns...)

	cfg := config.Snapshot()
	for _, p := range cfg.DedupBoilerplate.ExtraTitlePatterns {
		normalized := util.NormalizeTitle(util.CollapseSpaces(p))
		if normalized == "" {
			continue
		}
		effectiveTitlePatterns = append(effectiveTitlePatterns, normalized)
	}
	for _, p := range cfg.DedupBoilerplate.ExtraPrefixPatterns {
		normalized := util.NormalizeTitle(util.CollapseSpaces(p))
		if normalized == "" {
			continue
		}
		effectivePrefixPatterns = append(effectivePrefixPatterns, normalized)
	}
}

func isBoilerplateTitle(title string) bool {
	boilerplateInit.Do(loadBoilerplatePatterns)

	normalized := util.NormalizeTitle(util.CollapseSpaces(title))
	if normalized == "" {
		return false
	}
	for _, pattern := range effectiveTitlePatterns {
		if normalized == pattern {
			return true
		}
	}
	for _, pattern := range effectivePrefixPatterns {
		if strings.HasPrefix(normalized, pattern+" ") {
			return true
		}
	}
	return false
}
