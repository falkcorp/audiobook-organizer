// file: core/ui/bundle.go
// version: 0.1.0
// guid: 3b8f0e52-6a1d-4c97-8e24-1f5a9d7c0b63
// last-edited: 2026-11-20

package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

// ErrInvalidBundle is the error FromFS wraps for every bundle it refuses: no
// index.html, or an index.html without the build, app or theme meta the
// shell preset writes (07 §7.6.1, FE7-7, FE8-7).
var ErrInvalidBundle = errors.New("ui: invalid shell bundle")

// Bundle is an app's built shell SPA (07 §7.6.1). The zero Bundle is valid:
// it is an API-only build, and Handler serves the placeholder page for it.
type Bundle struct {
	fsys    fs.FS  // rooted at the dist directory
	buildID string // from <meta name="mediacore-build" content="..."> in index.html (the UI build ID, §7.6.3; never the binary's identity, R5-7)
	appID   string // from <meta name="mediacore-app" content="...">, written by the app preset (§7.10, FE8-7)
	index   []byte
	scripts []string // CSP hash sources of inline <script> blocks in index.html
}

// FromFS roots fsys at dir, reads index.html, extracts the build ID, the
// app ID and the sha256 hashes of inline scripts. It fails if index.html,
// the build meta, the app meta or the theme meta is missing (the app meta
// is the preset's, like the theme meta; FE8-7): the build meta <meta name="mediacore-build">
// is written by every npm build, ABO's pre-shell vite.config.ts included
// from R1, so it cannot mark a shell bundle; the theme meta
// <meta name="mediacore-theme"> is written only by the shell preset (§7.7),
// so only shell builds are Bundles (FE7-7). An app whose SPA is not yet a
// shell build passes the zero Bundle and serves its SPA from a legacy mount
// (§7.6.3).
func FromFS(fsys fs.FS, dir string) (Bundle, error) {
	if fsys == nil {
		return Bundle{}, fmt.Errorf("%w: nil filesystem", ErrInvalidBundle)
	}
	sub := fsys
	if dir != "" && dir != "." {
		var err error
		if sub, err = fs.Sub(fsys, dir); err != nil {
			return Bundle{}, fmt.Errorf("%w: %s: %w", ErrInvalidBundle, dir, err)
		}
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: %s/index.html: %w", ErrInvalidBundle, dir, err)
	}
	metas := metaContents(index)
	buildID, ok := metas["mediacore-build"]
	if !ok || !validBuildID(buildID) {
		return Bundle{}, fmt.Errorf("%w: index.html has no valid <meta name=\"mediacore-build\"> (got %q)", ErrInvalidBundle, buildID)
	}
	appID, ok := metas["mediacore-app"]
	if !ok || !validAppID(appID) {
		return Bundle{}, fmt.Errorf("%w: index.html has no valid <meta name=\"mediacore-app\"> (got %q); only a build through the @mediacore/shell/vite preset is a shell bundle", ErrInvalidBundle, appID)
	}
	theme, ok := metas["mediacore-theme"]
	if !ok || !validThemeMeta(theme) {
		return Bundle{}, fmt.Errorf("%w: index.html has no valid <meta name=\"mediacore-theme\"> (got %q); only a build through the @mediacore/shell/vite preset is a shell bundle", ErrInvalidBundle, theme)
	}
	return Bundle{fsys: sub, buildID: buildID, appID: appID, index: index, scripts: inlineScriptHashes(index)}, nil
}

// MustFromFS panics on error. Call it only from the embed_frontend file of
// an app (ui_embed.go); the !embed_frontend file returns ui.Bundle{} instead,
// because a non-embed build's embed.FS is empty and would panic (FE2-10).
func MustFromFS(fsys fs.FS, dir string) Bundle {
	b, err := FromFS(fsys, dir)
	if err != nil {
		panic(err)
	}
	return b
}

// IsZero reports the zero Bundle: an API-only build with no embedded UI.
func (b Bundle) IsZero() bool { return b.fsys == nil }

// BuildID is the bundle's mediacore-build meta, "" for the zero Bundle.
func (b Bundle) BuildID() string { return b.buildID }

// AppID is the bundle's mediacore-app meta. In P8 the kernel refuses a
// non-zero Bundle whose AppID differs from app.Options.AppID
// (module.ProblemUIAppIDMismatch), so the server guarantees the one
// app-ID namespace and createShell's own check is defence in depth. It
// compares app IDs, never build IDs: FE4-1's "no boot-time build-ID check"
// stands (FE8-7).
func (b Bundle) AppID() string { return b.appID }

// metaContents maps each <meta name="..."> of an HTML document to its content
// attribute, attributes in either order, single or double quotes. The first
// tag of a name wins.
func metaContents(doc []byte) map[string]string {
	tagRE := regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	attrRE := regexp.MustCompile(`(?is)\b(name|content)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	out := map[string]string{}
	for _, tag := range tagRE.FindAll(doc, -1) {
		var name, content string
		var haveName, haveContent bool
		for _, m := range attrRE.FindAllSubmatch(tag, -1) {
			v := string(m[2])
			if len(m[2]) == 0 {
				v = string(m[3])
			}
			switch strings.ToLower(string(m[1])) {
			case "name":
				if !haveName {
					name, haveName = strings.ToLower(v), true
				}
			case "content":
				if !haveContent {
					content, haveContent = v, true
				}
			}
		}
		if haveName && haveContent {
			if _, dup := out[name]; !dup {
				out[name] = content
			}
		}
	}
	return out
}

// inlineScriptHashes returns a CSP hash source ('sha256-...') for every inline
// <script> element of doc: one without a src attribute and with non-blank
// text. The hash covers the exact bytes between the tags, as the browser
// hashes them.
func inlineScriptHashes(doc []byte) []string {
	scriptRE := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	srcRE := regexp.MustCompile(`(?i)(^|\s)src\s*=`)
	var out []string
	for _, m := range scriptRE.FindAllSubmatch(doc, -1) {
		if srcRE.Match(m[1]) || strings.TrimSpace(string(m[2])) == "" {
			continue
		}
		sum := sha256.Sum256(m[2])
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return out
}

// validBuildID is 07 §7.6.3 item 1's shape, ^[A-Za-z0-9._-]{1,64}$, minus
// "dev" in any case: a production bundle never carries "" or "dev"
// (TestBuildID_HeaderEqualsEmbeddedMeta), because "dev" disables the reload
// rule (FE7-6).
func validBuildID(s string) bool {
	if len(s) < 1 || len(s) > 64 || strings.EqualFold(s, "dev") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validAppID is 03 §3.6's ^[a-z][a-z0-9]{1,15}$.
func validAppID(s string) bool {
	if len(s) < 2 || len(s) > 16 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// validThemeMeta is the preset's "<key> <mode>": the key matches
// ^[A-Za-z0-9._:-]{1,128}$ and the mode is light or dark (07 §7.7, FE6-2).
func validThemeMeta(s string) bool {
	key, mode, ok := strings.Cut(s, " ")
	if !ok || (mode != "light" && mode != "dark") || len(key) < 1 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}
