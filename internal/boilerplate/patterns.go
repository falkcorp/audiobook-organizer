// file: internal/boilerplate/patterns.go
// version: 1.0.0
// guid: e8d1c3a7-4b62-4f90-9a15-2c7e6b0d4f38
// last-edited: 2026-07-12

// Package boilerplate holds the compiled-in list of publisher intro/outro
// "titles" that are not real books, plus a pure matcher over that list. It is a
// leaf package (imports only internal/util) so it can be shared by both the
// live dedup engine (internal/dedup) and the offline mining rules
// (internal/dedup/dataset) — which cannot import internal/dedup because
// internal/dedup already imports internal/dedup/dataset (an import cycle).
//
// This package is the SINGLE SOURCE OF TRUTH for the default patterns. The
// engine's config-extra append path (internal/dedup/boilerplate.go) layers
// deployment-specific extras ON TOP of these defaults; the matcher here is
// deliberately defaults-only so deterministic mining does not depend on
// per-deployment config.
package boilerplate

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// DefaultTitlePatterns are exact publisher intro/outro "titles" that are not
// real books and must not seed dedup matches. Seeded from
// docs/agent-tasks/dedup-intro-falsepositive/FINDINGS.md and safe to extend.
var DefaultTitlePatterns = []string{
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
	// "big finish ident" is the recurring Big Finish audio-drama publisher jingle
	// extracted as a standalone track across hundreds of releases. It is
	// embedding-identical (cosine ~1.0) to every other copy of itself but is not
	// a book, so pairs of it are legitimately not_dup — this entry keeps the
	// same-title/high-similarity mining guard from wrongly downgrading those
	// correct not_dup labels to unsure (298 such pairs on prod, 2026-07-12).
	"big finish ident",
}

// DefaultPrefixPatterns are anchored boilerplate phrases that may carry trailing
// publisher copy. Kept narrower than DefaultTitlePatterns so real books like
// "Introduction to Algorithms" still match normally.
var DefaultPrefixPatterns = []string{
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

// IsBoilerplateTitle reports whether title matches one of the compiled-in
// default patterns after normalization (lowercase, collapsed whitespace). It
// mirrors the normalization used by internal/dedup's isBoilerplateTitle so the
// two agree on the default set. Config extras are intentionally NOT consulted
// here (see package doc).
func IsBoilerplateTitle(title string) bool {
	normalized := util.NormalizeTitle(util.CollapseSpaces(title))
	if normalized == "" {
		return false
	}
	for _, pattern := range DefaultTitlePatterns {
		if normalized == pattern {
			return true
		}
	}
	for _, pattern := range DefaultPrefixPatterns {
		if strings.HasPrefix(normalized, pattern+" ") {
			return true
		}
	}
	return false
}
