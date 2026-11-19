// file: internal/books/appconfig/secrets.go
// version: 1.0.0
// guid: 3f9a1c5e-6b27-4d84-9e0a-c2d7b8f45a13
// last-edited: 2026-11-19

package appconfig

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/secrets"
)

// SecretAdmit is module.AppConfigSpec.Secrets for books: it admits exactly the
// secret names the rename table maps (a TargetSecret target), derived from the
// table so a row added there is admitted without a line here. A target whose
// path has placeholders (integrations.metadata.<source id>.credentials.<k>,
// integrations.notify.webhook-<n>.secret, modules.books.plugins.<id>.settings)
// admits any one name segment in each placeholder's place and no more.
//
// The OIDC client secrets the fold writes are named by the fold's providers,
// not by a TargetSecret row of their own other than the two it lists, which the
// table carries as concrete targets.
func SecretAdmit() func(secrets.Name) bool {
	var patterns [][]string
	for _, r := range cutover.Table().Rows {
		for _, t := range r.Targets {
			if t.Kind == cutover.TargetSecret && t.Path != "" {
				patterns = append(patterns, strings.Split(t.Path, "."))
			}
		}
	}
	return func(n secrets.Name) bool {
		segs := strings.Split(string(n), ".")
		for _, pat := range patterns {
			if matches(pat, segs) {
				return true
			}
		}
		return false
	}
}

// matches reports whether segs fits pat. A segment holding a placeholder
// ("<id>", "webhook-<n>") fits any segment with the same text around it and at
// least one character in its place.
func matches(pat, segs []string) bool {
	if len(pat) != len(segs) {
		return false
	}
	for i, p := range pat {
		open, end := strings.Index(p, "<"), strings.LastIndex(p, ">")
		if open < 0 || end < open {
			if p != segs[i] {
				return false
			}
			continue
		}
		pre, post := p[:open], p[end+1:]
		if len(segs[i]) <= len(pre)+len(post) || !strings.HasPrefix(segs[i], pre) || !strings.HasSuffix(segs[i], post) {
			return false
		}
	}
	return true
}
