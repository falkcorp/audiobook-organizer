// file: core/health/diagnostics/paths.go
// version: 1.10.0
// guid: 5b1d7c34-9e02-4a68-b3f5-2c8e6d0a7f19
// last-edited: 2026-11-15

package diagnostics

import (
	"errors"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RedesignPendingF8_03b is true while this file's path redactor is the token
// scanner (scanPaths and tokenAt below) that the F8-03 round 7 review found
// cannot make a bundle safe to share (docs/agent-tasks/platform-core/seams.d/F8-03.md,
// "Round 8: what the redactor does not meet"; the review is the "F8-03 #4658:
// break-it and design review, round 7" report). While it is true:
//
//   - New and Build refuse, outside a test binary, with ErrRedesignPendingF8_03b
//     (redesignRefusal). That is the check every bundle passes through, from any
//     module, wrapper or constructor, so F8-04 cannot mount or run the exporter
//     by accident;
//   - core/archtest's F803bGate fails the build if non-test code in core imports
//     core/internal/healthhttp or constructs the Exporter (an early signal, with
//     the file and line), if this constant is false in any file of this package
//     (built or not, under any build tag) or declared anywhere but here, if a
//     non-test file of this package or of healthhttp carries a build constraint,
//     and if the refusal's probe is anything but testing.Testing. The constant has
//     two states, true and deleted: false is an error, not a way to open the gate,
//     and no symbol name is consulted. Deleting it is the F8-03b PR's reviewed act,
//     together with redesignRefusal and the gate. TestRedesignRefusal_RealBinary
//     builds and runs a production binary under each tag set that
//     archtest.F803bShippedTagSets derives (the TagSets() entries that run on the
//     host, plus the -tags lists in Makefile*, Dockerfile*, .github/workflows and
//     .woodpecker, make variables such as $(GO_BUILD_TAGS) resolved from the
//     Makefile and the -include'd files that exist (:= expanded where it is
//     written, = where it is used), and the release build in .goreleaser.yml
//     (tags:, env: CGO_ENABLED, overrides: and GOFLAGS=-tags=..., per goos). What
//     the reading does not model fails the test instead: make conditionals,
//     define, override, target-specific assignments, glob includes, an include
//     of a file the walker skipped, a goreleaser template or includes:, a list it
//     cannot resolve. A variable set outside the repo's files (a Makefile.local
//     that is not in the checkout included), a GOOS other than the host's (the
//     constraint rule reads those files instead) and a tag list a script
//     assembles at run time are not covered.
//
// F8-04 mounts the diagnostics routes, so F8-04 is blocked on F8-03b
// (docs/agent-tasks/platform-core/F8-03b-share-safe-bundle.md, TODO id
// TODO-F8-03B-SHARE-SAFE-BUNDLE). The row that replaces the scanner deletes this
// constant, redesignRefusal and the guard in the same change.
const RedesignPendingF8_03b = true

// ErrRedesignPendingF8_03b is what New and Build return while
// RedesignPendingF8_03b is true and the caller is not a test binary.
var ErrRedesignPendingF8_03b = errors.New("diagnostics: refusing to build a bundle: the path redactor cannot make a bundle safe to share until F8-03b replaces it (docs/agent-tasks/platform-core/F8-03b-share-safe-bundle.md)")

// redesignRefusal is the refusal New and Build apply: ErrRedesignPendingF8_03b
// when pending is true and the caller is not a test binary, else nil.
// isTestBinary is testing.Testing in production code and a stand-in in the test
// that asserts the refusal (as core/auth/password does for its test-only
// options), so the production path is exercised by a test binary without a
// package-level variable.
func redesignRefusal(pending bool, isTestBinary func() bool) error {
	if pending && !isTestBinary() {
		return ErrRedesignPendingF8_03b
	}
	return nil
}

// PathRoot is a known filesystem root, by the name a reader of the bundle
// sees. An absolute path inside Path is written "<root:NAME>/rest"; any other
// absolute path is written "<path>" (05-health 5H.3.5 layer 2, "absolute
// filesystem paths outside known roots").
type PathRoot struct {
	// Name is the root's name (05-library root names): no "<", ">" or "/".
	Name string
	// Path is the root's absolute path.
	Path string
}

// pathRoots is a bundle's roots, longest path first so the most specific root
// names a path.
type pathRoots []PathRoot

// newPathRoots cleans the roots, drops one with no name or no absolute path,
// and orders them longest first.
func newPathRoots(in []PathRoot) pathRoots {
	var out pathRoots
	for _, r := range in {
		p := filepath.Clean(r.Path)
		if r.Name == "" || strings.ContainsAny(r.Name, "<>/\\") || !filepath.IsAbs(p) || p == string(filepath.Separator) {
			continue
		}
		out = append(out, PathRoot{Name: r.Name, Path: p})
	}
	slices.SortStableFunc(out, func(a, b PathRoot) int { return len(b.Path) - len(a.Path) })
	return out
}

// maxNest bounds how deep the rule recurses into a token it has already
// matched: the tail of a path under a root, the part of a route or URL before
// its query (which is opaque, so a query never nests). Each level re-reads only the
// text inside the token above it, so the work is linear until the bound; past
// it a text that still holds a path start is written "<path>" (fail closed), so
// a line built to nest cannot make the rule quadratic.
const maxNest = 3

// redactPaths rewrites every absolute path in free text s. A path is a run of
// non-delimiter characters that starts with "/" (a Windows drive and a
// separator, or a UNC "\\\\host") at the start of the text or after a
// boundary (pathBoundary: any byte that is not part of a name), so "a/b",
// "and/or", the path part of a URL and a closing tag ("</div>") are not
// touched. A known root is matched as a whole, so a root whose own path
// contains spaces is still named; below the root, and for a path outside every
// root, a space ends the path. That cut is the documented limit of free text:
// a path with spaces outside every root leaks its tail here. Structured values
// go through redactValue, which has the whole string.
//
// The rewrite is a fixed point: the ">" that ends "<root:NAME>" is not a
// boundary (pathBoundary) and the rule never writes a path after "<path>", so a
// second pass leaves both alone.
func (r pathRoots) redactPaths(s string) string { return r.redactPathsAt(s, 0, scanText) }

// scanMode says what the string being scanned is.
type scanMode uint8

const (
	// scanText is free text, or a value judged as text.
	scanText scanMode = iota
	// scanRoutePath is the path part of a route: there a doubled slash is the
	// start of a path ("/api/v1/x//Users/bruce", which is what the access log
	// writes for "/api/v1/x/%2FUsers%2Fbruce"), and only a scheme's "//" is not.
	scanRoutePath
	// scanDecoded is a modifier, not a mode of its own: the text was produced by
	// percent-decoding a token that held an encoded separator. The two exceptions
	// of pathBoundary that exist for the redactor's own output (the "/" after a
	// configured root's placeholder, the "/" of a closing tag) do not apply to it:
	// nothing the redactor writes has an encoded separator, so a "/" that came out
	// of a "%2F" after "<root:lib>" is the start of a path, not a rewritten tail.
	scanDecoded scanMode = 4
)

func (r pathRoots) redactPathsAt(s string, depth int, mode scanMode) string {
	return r.scanPaths(s, depth, mode, true)
}

// scanPaths is redactPathsAt; skipJudged says whether a token that starts inside
// an extent already judged is skipped (the linear-time rule below). Only the
// differential test passes false, to prove the skip changes nothing.
func (r pathRoots) scanPaths(s string, depth int, mode scanMode, skipJudged bool) string {
	if !strings.ContainsAny(s, "/\\%") {
		return s
	}
	if depth > maxNest {
		if r.hasPathStart(s, mode) || mode&scanRoutePath == 0 && strings.Contains(s, "%") && r.encodedPathToken(s) {
			return "<path>"
		}
		return s
	}
	var b strings.Builder
	last := 0
	// The whole-token rules (tokenAt) can only fire in text that holds a URL's
	// "://" or a "%", and only where a token or a scheme can start; most lines
	// hold neither, and a line of 400k bytes should not pay a call per byte.
	tokens := mode&scanRoutePath == 0 && (strings.Contains(s, "://") || strings.IndexByte(s, '%') >= 0)
	// judged is the end of the last token tokenAt read and found no encoded path
	// in. A token ends at whitespace and nothing else (routeExtent), so a quote
	// inside it is a place another token may start, and the text after it is a
	// part of the same token: read again, it would find nothing the whole did not
	// (the decoding is local to each escape). Without this a line of quotes and no
	// whitespace judges its whole rest from every quote, which is quadratic.
	judged := 0
	// cut is where a URL that ended at a byte that cannot be in an authority left
	// off: the text there is a token of its own even with no whitespace or quote in
	// front of it ("https://h,%2FUsers%2Fbruce").
	cut, judgedCut := -1, 0
	// queryRun is the end of the whitespace-delimited run that starts at j when it
	// holds a "?" or "#" at or after j, and 0 when it holds none. Both searches are
	// remembered and only redone past the point they reached, so a line of cut URLs
	// costs one pass over it.
	qAt, runEnd := -1, -1
	queryRun := func(j int) int {
		if j > runEnd {
			runEnd = j + routeExtent(s[j:])
		}
		if j > qAt {
			qAt = len(s) + 1
			if k := strings.IndexAny(s[j:], "?#"); k >= 0 {
				qAt = j + k
			}
		}
		if qAt < runEnd {
			return runEnd
		}
		return 0
	}
	for i := 0; i < len(s); {
		if tokens && (i == 0 || i == cut || s[i-1] <= ' ' || s[i-1] == 0x7f || s[i-1] == '"' || s[i-1] == '`' || isAlpha(s[i])) {
			// A token that starts at a cut is a different token from the one read
			// from before it, so the read there does not stand for it; it has its
			// own mark (judgedCut), which keeps a run of cut URLs linear.
			force := i == cut && (!skipJudged || i >= judgedCut)
			end, repl, ok, read, urlCut := r.tokenAt(s, i, depth, !skipJudged || i >= judged || force, force)
			if ok {
				if urlCut {
					// The URL ended at a byte that cannot be in an authority. A "?"
					// or "#" later in the same run still opens the opaque query or
					// fragment (G8, G9): from the first one to the end of the run
					// it is one placeholder, and what lies between is scanned as
					// text. With none, the text after the cut is a token of its own.
					if e := queryRun(end); e > end {
						repl += r.afterCut(s[end:e], depth)
						end = e
					} else {
						cut = end
					}
				}
				b.WriteString(s[last:i])
				b.WriteString(repl)
				last, i = end, end
				continue
			}
			if force {
				judgedCut = max(judgedCut, read)
			} else {
				judged = max(judged, read)
			}
		}
		if !r.pathStart(s, i, mode) {
			i++
			continue
		}
		end, repl := r.pathAt(s, i, depth, mode)
		b.WriteString(s[last:i])
		b.WriteString(repl)
		last, i = end, end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// Placeholders for what the redacted bundle does not show of a request target.
const (
	queryPlaceholder    = "?<query>"
	fragmentPlaceholder = "#<fragment>"
)

// opaqueTail is the query and fragment of a route or URL as the redacted bundle
// writes them: one fixed placeholder for everything from the first "?" or "#" to
// the end of the target. It is not split on "&", "=" or ";" and not decoded: a
// query value is not diagnostic data a shared bundle needs, and splitting is
// what a raw "&", "#" or "=" inside a directory name defeats (the part after it
// would be a different value, and a key such as "b/Gordonx/Books" is as
// readable as a value). rest is "" or starts with "?" or "#".
func opaqueTail(rest string) string {
	switch {
	case rest == "":
		return ""
	case rest[0] == '?':
		return queryPlaceholder
	}
	return fragmentPlaceholder
}

// tokenAt is the whole-token rules of free text, tried where a token starts. It
// returns the end of what it replaced and the replacement, or ok=false when the
// byte at i is nothing it judges. encoded says whether the encoded-token rule may
// run here; when it ran and found nothing, read is the end of the token it read,
// so the caller does not judge a token that starts inside it again.
//
//   - A URL ("scheme://host/path?query", any scheme but file) is kept as written
//     except for its path part (routePath, as for a route) and its query and
//     fragment (opaqueTail). It is recognised wherever its scheme starts.
//   - Where a token starts (after whitespace or a control byte, past any opening
//     bracket, quote, backtick or apostrophe), a token that is not a
//     route or a URL and that carries a percent-encoded separator whose decoded
//     text holds an absolute path is "<path>" whole (encodedPathToken). Free text
//     cut a path at its delimiters; an encoded path is not cut, so its tail
//     cannot be a different token.
func (r pathRoots) tokenAt(s string, i, depth int, encoded, forced bool) (end int, repl string, ok bool, read int, urlCut bool) {
	if isAlpha(s[i]) && (i == 0 || !isSchemeByte(s[i-1])) {
		if n := urlSchemeEnd(s[i:]); n > 0 {
			ext := urlExtent(s[i:], n)
			tok := trimRouteTail(s[i : i+ext])
			if len(tok) > n+2 {
				// A URL cut at a byte that cannot be in its authority leaves the
				// rest of the token to be judged as a token of its own.
				return i + len(tok), r.urlText(tok, n, depth), true, 0, i+ext < len(s) && s[i+ext] > ' ' && s[i+ext] != 0x7f
			}
		}
	}
	if !encoded || !forced && !tokenStart(s, i) {
		return 0, "", false, 0, false
	}
	p := i
	for p < len(s) && strings.IndexByte("'([{<\"`", s[p]) >= 0 {
		p++
	}
	if p >= len(s) || routeStart(s[p:]) {
		return 0, "", false, 0, false
	}
	if n := urlSchemeEnd(s[p:]); n > 0 {
		return 0, "", false, 0, false // the URL rule above owns it
	}
	// The token is judged with the bytes skipped above in front of it, not from
	// p: a path start is judged against the byte before it (pathBoundary), and
	// from p the first byte has none, so "</b>%2F..." read from after its "<"
	// started a path at "/b>" where the same text inside a longer token is a
	// closing tag. With the prefix in, a token is judged the same wherever it
	// starts, which is what lets the caller skip a start inside an extent it has
	// read (judged).
	e := routeExtent(s[p:])
	full := trimRouteTail(s[i : p+e])
	if len(full) <= p-i || strings.IndexByte(full, '%') < 0 || !r.encodedPathToken(full) {
		return 0, "", false, p + e, false
	}
	return i + len(full), s[i:p] + "<path>", true, 0, false
}

// tokenStart reports whether a token of free text starts at i: the text start or
// the byte after whitespace, a control byte, a double quote or a backtick (a
// quote opens a quoted token; the extent of one is whitespace, routeExtent).
func tokenStart(s string, i int) bool {
	if i == 0 {
		return true
	}
	c := s[i-1]
	return c <= ' ' || c == 0x7f || c == '"' || c == '`'
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isSchemeByte(c byte) bool {
	return isAlpha(c) || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'
}

// urlSchemeEnd is the length of the scheme of s when s starts "scheme://" (a
// letter, then letters, digits, "+", "-" and "."), and 0 otherwise. file: URLs
// and URLs with an empty authority are paths, and keep the path rule.
func urlSchemeEnd(s string) int {
	if len(s) == 0 || !isAlpha(s[0]) {
		return 0
	}
	n := 1
	for n < len(s) && isSchemeByte(s[n]) {
		n++
	}
	if !strings.HasPrefix(s[n:], "://") || strings.EqualFold(s[:n], "file") {
		return 0
	}
	if len(s) > n+3 && (s[n+3] == '/' || winDrive(s, n+3)) {
		// No authority ("https:///Users/bruce"), or a drive where the authority
		// would be ("https://C:\Users\bruce"): the log's credential scrub leaves
		// these behind when it eats a host or the start of a path, and each is a
		// path, as file:/// is.
		return 0
	}
	// An authority is kept as written, so one that holds an encoded separator
	// ("https://%2FUsers%2Fbruce", "https://C%3A%5CUsers", also left by the scrub)
	// is an encoded path and not a host: the whole token is judged as one.
	auth := s[n+3:]
	end := 0
	for end < len(auth) && authorityByte(auth[end]) {
		end++
	}
	if strings.IndexByte(auth[:end], '%') >= 0 {
		if _, _, sep := lenientDecode(auth[:end], false); sep {
			return 0
		}
	}
	return n
}

// authorityByte reports a byte that can be in a URL's authority (userinfo, host
// and port): a letter, a digit, "-._~%:@[]" and a byte of a non-ASCII host. It is
// a whitelist on purpose: a byte outside it ends the authority, and what follows
// is scanned as text, so a quote, comma or backslash after a host in compact JSON
// ("https://h","p":"/Users/bruce") is never read as part of the URL. A userinfo
// with a sub-delimiter in it ("a,b@host") is cut early; the cost is that the rest
// is read as text, which still redacts every path in it. An authority that holds
// an encoded separator at any decoding layer is no authority (urlSchemeEnd).
func authorityByte(c byte) bool {
	return isAlpha(c) || c >= '0' && c <= '9' || c >= utf8.RuneSelf || strings.IndexByte("-._~%:@[]", c) >= 0
}

// urlExtent is the length of the URL token that starts at s[0] ("scheme://" is
// schemeEnd+3 bytes). The authority ends at the first byte that cannot be in one
// (authorityByte), and the token ends there; when the authority is followed by
// "/", "?" or "#" the token runs on to whitespace as a route's does (routeExtent:
// after a "?" or "#" the opaque query runs to whitespace whatever it holds, G9,
// and the path part to the same end). The scan of the authority is bounded by its
// own length, so a line of URL starts costs a pass over the authorities, and a
// token that runs to whitespace is consumed by the caller.
func urlExtent(s string, schemeEnd int) int {
	j := schemeEnd + 3
	for j < len(s) && authorityByte(s[j]) {
		j++
	}
	if j < len(s) && s[j] != '/' && s[j] != '?' && s[j] != '#' {
		return j
	}
	return j + routeExtent(s[j:])
}

// urlText is a URL token as written except for its inside: the authority stays,
// the path part is judged as a route's is (routePath), and the query and
// fragment are one placeholder (opaqueTail). schemeEnd is urlSchemeEnd of tok.
func (r pathRoots) urlText(tok string, schemeEnd, depth int) string {
	rest := tok[schemeEnd+3:]
	authEnd := 0
	for authEnd < len(rest) && authorityByte(rest[authEnd]) {
		authEnd++
	}
	if authEnd == len(rest) {
		return tok
	}
	if c := rest[authEnd]; c != '/' && c != '?' && c != '#' {
		// A byte that cannot be in an authority (a whole value that starts as a
		// URL and holds one): the URL ends there and the rest is text.
		return tok[:schemeEnd+3+authEnd] + r.afterCut(rest[authEnd:], depth)
	}
	head, rest := tok[:schemeEnd+3+authEnd], rest[authEnd:]
	pathPart, tail := rest, ""
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		pathPart, tail = rest[:i], rest[i:]
	}
	if pathPart != "" {
		head += "/" + r.routePath(pathPart[1:], depth)
	}
	return head + opaqueTail(tail)
}

// afterCut is what follows a URL that ended at a byte that cannot be in an
// authority, up to the end of its run: the text before the first "?" or "#" is
// scanned as text, and from there to the end the query or fragment is one
// placeholder, as it is for a URL that did not end early (G8, G9).
func (r pathRoots) afterCut(rest string, depth int) string {
	if q := strings.IndexAny(rest, "?#"); q >= 0 {
		return r.redactPathsAt(rest[:q], depth+1, scanText) + opaqueTail(rest[q:])
	}
	return r.redactPathsAt(rest, depth+1, scanText)
}

// urlHasTail reports whether the first whitespace-delimited chunk of s, a value
// that starts as a URL, holds a "?" or "#": the start of an opaque query or
// fragment, which then runs to the end of the value (a raw space in a query is
// part of it, as it is in a route's).
func urlHasTail(s string) bool {
	end := strings.IndexFunc(s, func(r rune) bool { return r <= ' ' })
	if end < 0 {
		end = len(s)
	}
	return strings.ContainsAny(s[:end], "?#")
}

// hasEncodedSep reports whether s holds a percent-encoded path separator.
func hasEncodedSep(s string) bool {
	return strings.Contains(s, "%2F") || strings.Contains(s, "%2f") ||
		strings.Contains(s, "%5C") || strings.Contains(s, "%5c")
}

// encodedPathToken reports whether tok carries a percent-encoded absolute path:
// at some decoding layer (lenient: a valid %XX decodes, a bad one stays, so
// nothing fails to decode) the text holds an encoded separator, and the layer
// decoded holds an absolute-path start. A token with no encoded separator at any
// layer ("50%25 done") is not judged. Bounded to maxDecode layers, each a linear
// pass; a token still decoding after that is built to hide something and counts
// (fail closed).
func (r pathRoots) encodedPathToken(tok string) bool {
	cur := tok
	// sep: some layer so far held an encoded separator, so a separator in the
	// layers after it may have been decoded (the placeholder or tag before it can
	// be encoded one layer deeper than the separator).
	sep := false
	for range maxDecode + 1 {
		next := unescapeLenient(cur, false)
		if next == cur {
			return false
		}
		sep = sep || hasEncodedSep(cur)
		if sep && r.hasPathStart(next, scanText|scanDecoded) {
			return true
		}
		cur = next
	}
	return true
}

// hasPathStart reports whether any absolute path begins in s.
func (r pathRoots) hasPathStart(s string, mode scanMode) bool {
	for i := 0; i < len(s); i++ {
		if r.pathStart(s, i, mode) {
			return true
		}
	}
	return false
}

// pathAt returns where the path starting at s[i] ends and what replaces it. A
// known root locates the extent of the path (a root with spaces in its own
// name is matched whole); then name decides what the path is called, from its
// cleaned form. A URL route shape (isRoute) is left as written except for its
// inside, which is judged (routeText); anything else is "<path>".
//
// A token that starts like a route is read to the end of its chunk (routeExtent),
// to whitespace and not to the first path delimiter: a query is one placeholder to
// the end of the token, whatever quote, bracket, comma, "&" or "=" it holds raw
// (G9), and the path part is read to the same end. If the chunk is not a route (a bad escape, a
// ".." out of the subtree, "/livez/x") the whole chunk is "<path>", so a
// route-shaped token that does not parse never leaves a tail behind.
func (r pathRoots) pathAt(s string, i, depth int, mode scanMode) (end int, repl string) {
	if mode&scanRoutePath != 0 {
		// A path that starts inside a route's path part runs to the end of the
		// part: the part is not prose, so no delimiter ends the path early.
		return len(s), r.name(s[i:], depth)
	}
	for _, root := range r {
		rest, ok := underRoot(s[i:], root.Path)
		if !ok {
			continue
		}
		tok := trimPathTail(s[i : i+len(root.Path)+pathRun(rest, isWinPath(root.Path))])
		if len(tok) < len(root.Path) {
			tok = s[i : i+len(root.Path)]
		}
		return i + len(tok), r.name(tok, depth)
	}
	if routeStart(s[i:]) {
		tok := trimRouteTail(s[i : i+routeExtent(s[i:])])
		if isRoute(tok) {
			return i + len(tok), r.routeText(tok, depth)
		}
		return i + len(tok), "<path>"
	}
	run := s[i : i+pathRun(s[i:], isWinPath(s[i:]))]
	tok := trimPathTail(run)
	return i + len(tok), r.name(tok, depth)
}

// name is what a path is called in the bundle: "<root:NAME>/rest" when the
// path, cleaned, is still under a known root, "<path>" otherwise. The raw text
// never decides: "/srv/lib/../../home/x" starts with a root and is not under
// it, and "/srv/lib/../vault/x" is under another. Only the cleaned tail is
// written, so no ".." survives into the output.
//
// A backslash is a separator only in a Windows path. Under a POSIX root it is a
// character of a name, so the text written is the POSIX-cleaned text with its
// backslashes intact. A reader may still take that backslash for a separator,
// so the path is named only when the backslash reading is under the same root
// too: "/srv/lib/x\..\..\home\bruce" is "<path>", not a name that carries a
// ".." out of the root for a reader who splits on "\".
//
// The tail below a root is scanned again for a path that starts inside it
// ("/srv/lib/a@/home/bruce/x"): a token is one run of name characters, so a
// path that begins after "@" or "=" would otherwise ride out in the tail.
func (r pathRoots) name(tok string, depth int) string {
	win := isWinPath(tok)
	clean := cleanPath(tok, win)
	alt := ""
	if !win && strings.Contains(tok, "\\") {
		alt = cleanPath(tok, true)
	}
	for _, root := range r {
		if isWinPath(root.Path) != win {
			continue
		}
		rp := root.Path
		if win {
			rp = slashes(rp)
		}
		rest, ok := cleanUnder(clean, rp)
		if !ok {
			continue
		}
		if alt != "" {
			if _, ok := cleanUnder(alt, rp); !ok {
				return "<path>"
			}
		}
		return "<root:" + root.Name + ">" + r.tailText(rest, depth)
	}
	return "<path>"
}

// tailText is what follows a root's placeholder: rest is "" or starts with "/"
// (cleanUnder), and the text after that "/" is read again for a path that
// starts inside it.
func (r pathRoots) tailText(rest string, depth int) string {
	if len(rest) <= 1 {
		return rest
	}
	return "/" + r.redactPathsAt(rest[1:], depth+1, scanText)
}

// cleanPath is the cleaned, forward-slash form of a path. A Windows path
// (drive or UNC) has its backslashes read as separators first; forcing it for
// a POSIX path gives the reading of a reader who splits on "\".
func cleanPath(tok string, backslashSeparators bool) string {
	if backslashSeparators {
		tok = slashes(tok)
	}
	return path.Clean(tok)
}

// cleanUnder reports whether the cleaned path is root itself or below it,
// returning what follows the root ("" or starting with "/"). Both are in the
// forward-slash form cleanPath writes.
func cleanUnder(clean, root string) (string, bool) {
	if len(clean) < len(root) || !pathEqual(clean[:len(root)], root) {
		return "", false
	}
	rest := clean[len(root):]
	if rest == "" || rest[0] == '/' {
		return rest, true
	}
	return "", false
}

// routeShape reports whether p, the path part of a request target, is a
// router-mounted route: the "/api/" and "/public/" subtrees and the exact
// probe endpoints. A request line or a metric label ("GET /api/v1/books/{id}",
// route="/api/core/v1/auth/status") names a route, not a file, and is the same
// text on every host. The cost is that a real directory called /api at the
// filesystem root is not redacted. An exact endpoint ends there: "/livez/x" is
// a path. cleaned says p already went through path.Clean, which turns the
// subtree's own root ("/api/") into "/api".
func routeShape(p string, cleaned bool) bool {
	switch p {
	case "/livez", "/readyz", "/metrics":
		return true
	case "/api", "/public":
		return cleaned
	}
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/public/")
}

// isRoute reports whether run (a path-shaped token) is a router-mounted route.
// Only the part before a "?" or "#" is judged. It is judged as written, and
// again percent-decoded and cleaned (a "%2e%2e" is a ".." to the router), and
// cleaned with a backslash read as a separator, so no spelling of ".." carries
// it out of the route's subtree. A percent escape that does not decode is not a
// route.
func isRoute(run string) bool {
	p := run
	if i := strings.IndexAny(run, "?#"); i >= 0 {
		p = run[:i]
	}
	if !routeShape(p, false) {
		return false
	}
	dec := p
	for range 3 {
		next, err := url.PathUnescape(dec)
		if err != nil {
			return false
		}
		if next == dec {
			break
		}
		dec = next
	}
	if next, err := url.PathUnescape(dec); err != nil || next != dec {
		return false // nested more deeply than we decode: fail closed
	}
	return routeShape(path.Clean(dec), true) && routeShape(path.Clean(slashes(dec)), true)
}

// routeStart reports whether s begins like a route: the "/api/" and "/public/"
// subtrees and the exact probe endpoints. It is the cheap test before the
// extent of a route is read; isRoute decides.
func routeStart(s string) bool {
	return strings.HasPrefix(s, "/api/") || strings.HasPrefix(s, "/public/") ||
		strings.HasPrefix(s, "/livez") || strings.HasPrefix(s, "/readyz") || strings.HasPrefix(s, "/metrics")
}

// routeExtent is the length of the chunk a route-shaped token spans in free
// text: to whitespace or a control byte, and no other byte ends it. Not a double
// quote, a backtick, a `\"` escape, an apostrophe, a bracket or a comma: a query
// may hold any of them raw (G9), and so may a directory name in the path part
// ("/api/v1/x//Users/a"b/Gordonx": the part after the quote is still the name).
// The token is then judged as a whole, so no byte of it is a way to end the
// token early and leave the rest of a name in the clear.
func routeExtent(s string) int {
	n := 0
	for n < len(s) && s[n] > ' ' && s[n] != 0x7f {
		n++
	}
	return n
}

// trimRouteTail drops the punctuation that closes a route in prose
// ("(GET /api/x)", "GET /api/x, then") so it stays outside the token. One
// backward pass. A final segment of dots ("/api/x/..") is a name, not
// punctuation, as in trimPathTail.
//
// A token with a "?" or "#" is not trimmed at all (G9): its query is opaque to
// the end of the chunk, so the bracket or quote glued to its end is part of the
// query and goes with it. Only a token with no query gives its closers back; the
// quotes among them, a double quote, a backtick and a `\"`, are not part of a
// path part either, and a quote in the middle of the token stays inside it.
func trimRouteTail(tok string) string {
	if strings.ContainsAny(tok, "?#") {
		return tok
	}
	cut := len(tok)
	for cut > 0 {
		if cut >= 2 && tok[cut-2] == '\\' && tok[cut-1] == '"' {
			cut -= 2
		} else if strings.IndexByte("'),];|}!.:\"`", tok[cut-1]) >= 0 {
			cut--
		} else {
			break
		}
	}
	if cut > 0 && (tok[cut-1] == '/' || tok[cut-1] == '\\') {
		for cut < len(tok) && tok[cut] == '.' {
			cut++
		}
	}
	return tok[:cut]
}

// routeText is a route as written, except that its path part is judged (decoded
// and read again for a path that starts inside it: routePath) and its query and
// fragment are one placeholder (opaqueTail).
func (r pathRoots) routeText(tok string, depth int) string {
	i := strings.IndexAny(tok, "?#")
	pathPart, rest := tok, ""
	if i >= 0 {
		pathPart, rest = tok[:i], tok[i:]
	}
	return "/" + r.routePath(pathPart[1:], depth) + opaqueTail(rest)
}

// routePath is a route's path part (without its first "/"): percent-decoded to a
// fixed point and read as text in scanRoutePath mode, so
// "/api/v1/x/%2FUsers%2Fbruce" and "/api/v1/x//Users/bruce" are judged alike. A
// part with no path in it is kept as written; one with a path is written
// re-encoded if it had to be decoded.
func (r pathRoots) routePath(pp string, depth int) string {
	dec, exhausted, sep := lenientDecode(pp, false)
	if exhausted {
		return "<value>"
	}
	mode := scanRoutePath
	if sep {
		mode |= scanDecoded // a "/" decoded from "%2F" is never a rewritten tail
	}
	out := r.redactPathsAt(dec, depth+1, mode)
	switch {
	case out == dec:
		return pp
	case dec == pp:
		return out
	}
	return encodeRedacted(out)
}

// wholeRoute reports whether s, a whole value that starts as an absolute path, is
// one route (and not a path under a configured root, which names the root).
func (r pathRoots) wholeRoute(s string) bool {
	if !routeStart(s) || !isRoute(s) {
		return false
	}
	for _, root := range r {
		if _, ok := underRoot(s, root.Path); ok {
			return false
		}
	}
	return true
}

// maxDecode bounds the percent-decoding rounds of one part. A browser encodes at
// most twice; a part still changing after five is built to hide something.
const maxDecode = 5

// lenientDecode percent-decodes s to a fixed point, at most maxDecode rounds,
// and reports whether it was still changing at the end (fail closed: the caller
// writes a placeholder), and whether any layer held an encoded separator (sep:
// a "/" or "\\" in the result may have been decoded). Linear: a fixed number of
// linear passes.
func lenientDecode(s string, plus bool) (dec string, exhausted, sep bool) {
	for range maxDecode {
		next := unescapeLenient(s, plus)
		if next == s {
			return s, false, sep
		}
		sep = sep || hasEncodedSep(s)
		s = next
	}
	return s, unescapeLenient(s, plus) != s, sep || hasEncodedSep(s)
}

// unescapeLenient decodes every valid %XX (and "+" as a space when plus) and
// leaves everything else, so "50%" and "%zz" stay as they are instead of
// failing the way url.QueryUnescape does.
func unescapeLenient(s string, plus bool) string {
	if !strings.Contains(s, "%") && !(plus && strings.Contains(s, "+")) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
		case c == '+' && plus:
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

// encodeRedacted percent-encodes rewritten text for a route's decoded path part
// (routePath writes it back as it was encoded): letters, digits
// "-._~" and the bytes of the rule's own placeholders ("/", ":", "<", ">") stay,
// every other byte is %XX. Nothing decoded survives as a control byte, a line
// break, a separator or a quote, and a second pass over the output decodes it
// back to the same text, so the rewrite is a fixed point.
func encodeRedacted(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) + len(s)/2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' || c == '/' || c == ':' || c == '<' || c == '>' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// redactValue rewrites a string that is a whole value (a JSON string, as
// opposed to a line of prose). When the entire value is one clean absolute path
// it is replaced as one, spaces and all, so "/Users/bruce/Library/Application
// Support/app/db" does not leak its tail. A value that is more than that (a
// second path later in the text, ": " and a message, a quoted string) is prose
// that starts with a path, and goes to redactPaths so every path in it is
// rewritten and none of its text is lost to the replacement. A newline, a value
// that is not path-shaped, and a route take the same road. A single path is
// named from its cleaned form (name), so ".." cannot carry it out of a root.
func (r pathRoots) redactValue(s string) string { return r.redactValueAt(s, 0) }

// redactValueAt: a value that starts as one route is that route whole, whatever
// follows it (a quote, ": " and a message, a later path: the query is one
// placeholder to the end of the value and the path part is judged, so none of
// that text is a way around the rule); the same holds for a value that starts as
// a URL with a "?" or "#" in its first chunk (urlHasTail); a value that is one
// clean path is that path; any other value is text.
func (r pathRoots) redactValueAt(s string, depth int) string {
	if n := urlSchemeEnd(s); n > 0 && len(s) > n+3 && !strings.ContainsAny(s, "\n\r") && urlHasTail(s) {
		return r.urlText(s, n, depth) // a URL with a query or fragment is one whole URL, as a route is
	}
	if s == "" || strings.ContainsAny(s, "\n\r") || !absolutePath(s) {
		return r.redactPathsAt(s, depth, scanText)
	}
	if r.wholeRoute(s) {
		return r.routeText(s, depth)
	}
	if !r.singlePath(s) {
		return r.redactPathsAt(s, depth, scanText)
	}
	for _, root := range r {
		if rest, ok := underRoot(s, root.Path); ok && rest != "" && !isSepFor(isWinPath(root.Path), rest[0]) {
			return r.redactPathsAt(s, depth, scanText) // punctuation after the root: prose
		}
	}
	return r.name(s, depth)
}

// singlePath reports whether s, which starts as an absolute path, is nothing
// but that path: no later path start, no ": " (a message after the path) and no
// double quote or backtick. How the path is spelled does not matter: a trailing
// or doubled separator or a "/./" segment is still one path, and a space inside
// it is part of a name, so a non-canonical value is not prose. An apostrophe is
// a character of names ("/Music/Bruce's Mix"); a quoted second path is caught
// by the later-path-start rule.
func (r pathRoots) singlePath(s string) bool {
	if strings.Contains(s, ": ") || strings.ContainsAny(s, "\"`") {
		return false
	}
	for i := 1; i < len(s); i++ {
		if r.pathStart(s, i, scanText) {
			return false
		}
	}
	return true
}

// absolutePath reports a value that starts as an absolute path: "/x", a
// Windows drive or a UNC share.
func absolutePath(s string) bool {
	return pathStartAt(s, 0)
}

// isWinPath reports a Windows path: a drive and a separator, or a UNC share.
func isWinPath(s string) bool {
	return winDrive(s, 0) || strings.HasPrefix(s, `\\`)
}

// isSepFor reports whether c separates path segments in a path of that kind: a
// Windows path takes both, a POSIX path only "/". A backslash in a POSIX path
// is a character of a name.
func isSepFor(win bool, c byte) bool { return c == '/' || win && c == '\\' }

// underRoot reports whether text starts with root and the root ends there (end
// of text, a separator or a delimiter), returning the text after the root.
func underRoot(text, root string) (string, bool) {
	if len(text) < len(root) || !pathEqual(text[:len(root)], root) {
		return "", false
	}
	win := isWinPath(root)
	rest := text[len(root):]
	if rest == "" || isSepFor(win, rest[0]) || isPathDelim(rest[0]) {
		return rest, true
	}
	// Sentence punctuation right after the root ("see /srv/lib.").
	if run := rest[:pathRun(rest, win)]; strings.Trim(run, ".:") == "" {
		return rest, true
	}
	return "", false
}

// pathEqual compares path text; a Windows path (a drive letter) ignores case.
func pathEqual(a, b string) bool {
	if winDrive(b, 0) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pathRun is the length of the run of path characters at the start of s. A
// backslash that escapes a quote ("\\"" in a quoted string) ends the run, so
// the escape stays with the text around the path. In a POSIX path (win false)
// so does a backslash escape of a control character or a code point ("\n",
// "\t", "\r", "\u2028": what a quoted log value writes), which is not a name:
// it is where the next thing starts. A Windows path keeps those, because "\n"
// there is a separator and the first letter of "new".
func pathRun(s string, win bool) int {
	n := 0
	for n < len(s) && !isPathDelim(s[n]) {
		if s[n] == '\\' && n+1 < len(s) {
			if s[n+1] == '"' || s[n+1] == '\'' {
				break
			}
			if !win && escapeAt(s, n) {
				break
			}
		}
		n++
	}
	return n
}

// escapeAt reports whether s[i:] is a backslash escape of a control character
// ("\n", "\t", "\r") or of a code point ("\u" and four hex digits).
func escapeAt(s string, i int) bool {
	if i+1 >= len(s) || s[i] != '\\' {
		return false
	}
	switch s[i+1] {
	case 'n', 't', 'r':
		return true
	case 'u':
		return i+6 <= len(s) && isHex(s[i+2]) && isHex(s[i+3]) && isHex(s[i+4]) && isHex(s[i+5])
	}
	return false
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// trimPathTail drops sentence punctuation that follows a path in prose
// ("open /x/y: no such file", "see /x/y."). A final segment made only of dots
// ("/x/.", "/x/..", "/x/...") is a name, not punctuation: stripping part of it
// would misname it or leave a ".." outside the replacement, where it undoes the
// cleaning. One pass: the last separator is found once and the run of "." and
// ":" after it is cut in one step.
func trimPathTail(tok string) string {
	seg := strings.LastIndexAny(tok, "/\\") + 1
	last := tok[seg:]
	if dots := strings.TrimRight(last, ":"); dots != "" && strings.Trim(dots, ".") == "" {
		return tok[:seg+len(dots)]
	}
	cut := seg + len(strings.TrimRight(last, ".:"))
	if cut == 0 {
		return tok
	}
	return tok[:cut]
}

// slashes writes a Windows path's backslashes as "/", one for each run of
// backslashes.
func slashes(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		for i+1 < len(s) && s[i+1] == '\\' {
			i++
		}
		b.WriteByte('/')
	}
	return b.String()
}

// isPathDelim ends a path.
func isPathDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', ',', ';', ')', ']', '}', '<', '>', '|', 0:
		return true
	}
	return false
}

// pathStart reports whether an absolute path begins at s[i].
func (r pathRoots) pathStart(s string, i int, mode scanMode) bool {
	if i > 0 && !r.pathBoundary(s, i, mode) {
		return false
	}
	return pathStartAt(s, i)
}

// pathStartAt reports whether s[i:] begins like an absolute path: "/x" (or a
// doubled "//x", a join that did not collapse its slash), a drive ("C:\") or a
// UNC share ("\\\\host\\share"). A "/" with a single segment after it is a path
// only if that segment is a name (shapedSegment), so "*/15", "*/*", "+/-",
// "50%/50%" and "(read)/(write)" are not.
func pathStartAt(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	switch c := s[i]; {
	case c == '/':
		j := i
		for j < len(s) && s[j] == '/' {
			j++
		}
		return j < len(s) && !isPathDelim(s[j]) && shapedSegment(s, j)
	case c == '\\':
		return i+2 < len(s) && s[i+1] == '\\' && s[i+2] != '\\' && !isPathDelim(s[i+2])
	default:
		return winDrive(s, i)
	}
}

// shapedSegment reports whether the path whose first segment starts at s[j]
// looks like a path: it has a second segment, or its one segment starts with a
// letter, a digit, "_", "." or "~" and holds a letter. The scan stops at the
// next "/" or delimiter, so the cost is the segment, and segments do not
// overlap.
func shapedSegment(s string, j int) bool {
	k := j
	for k < len(s) && s[k] != '/' && !isPathDelim(s[k]) {
		k++
	}
	if k < len(s) && s[k] == '/' {
		return true
	}
	seg := s[j:k]
	first, _ := utf8.DecodeRuneInString(seg)
	if !(unicode.IsLetter(first) || unicode.IsDigit(first) || first == '_' || first == '.' || first == '~') {
		return false
	}
	for _, c := range seg {
		if unicode.IsLetter(c) {
			return true
		}
	}
	return false
}

// pathBoundary reports whether a path may start after s[i-1]. Any byte that is
// not part of a name is a boundary (the set of bytes that cannot be one is
// closed, the set of separators is not: "@", "+", "#", "!", "&", "*", "=",
// "(", a quote, U+2028 and the rest all begin a path), so a path is found after
// whatever precedes it. A name byte is not: letters, digits and "._~-", and
// any letter, digit or mark of another script. The exceptions keep the rewrite
// a fixed point and leave markup and URLs alone:
//
//   - the ">" that ends the placeholder of a configured root ("<root:NAME>"),
//     whose tail is already rewritten;
//   - the "/" of a closing tag ("</div>");
//   - a "/" after a "/" (the path is the first of the run), except the third
//     slash of "file:///x";
//   - the "//" after a scheme ("https://host"), though the "/" after the host
//     of a "file://host/x" does start a path;
//   - a backslash escape that is itself a boundary ("\n", "\t", "\r",
//     "\uXXXX": the quoted text "l1\n/home/bruce/x" has a path in it).
func (r pathRoots) pathBoundary(s string, i int, mode scanMode) bool {
	c := s[i-1]
	switch {
	case c == '>' && mode&scanDecoded == 0 && r.endsRootPlaceholder(s, i):
		return false
	case c == '<' && mode&scanDecoded == 0 && closingTag(s, i):
		return false
	case c == '/':
		if i >= 3 && s[i-2] == '/' && s[i-3] == ':' {
			return true // the third slash of "file:///x"
		}
		// In a route's path part the second slash of a run starts a path, unless
		// the run follows a scheme's colon or is longer (the first slash of the
		// run is judged once, so a run of slashes is not rescanned).
		return mode&scanRoutePath != 0 && (i < 2 || s[i-2] != '/' && s[i-2] != ':')
	case c == ':' && s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
		return false
	case isNameByte(c):
		return afterEscape(s, i) || fileURLHost(s, i)
	case c >= utf8.RuneSelf:
		rn, _ := utf8.DecodeLastRuneInString(s[:i])
		return !(unicode.IsLetter(rn) || unicode.IsDigit(rn) || unicode.IsMark(rn))
	}
	return true
}

// isNameByte reports a byte that belongs to a name: letters, digits and "._~-".
func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-'
}

// afterEscape reports whether s[i] follows a backslash escape ("\n", "\t",
// "\r" or "\uXXXX") that ends at i.
func afterEscape(s string, i int) bool {
	return i >= 2 && escapeAt(s, i-2) && s[i-1] != 'u' || i >= 6 && escapeAt(s, i-6)
}

// fileURLHost reports whether the "/" at s[i] follows "file://HOST": the path
// of a file URL with a host ("file://localhost/home/bruce/x").
func fileURLHost(s string, i int) bool {
	if s[i] != '/' {
		return false
	}
	j := i
	for j > 0 && i-j <= 253 && (isNameByte(s[j-1]) && s[j-1] != '~') {
		j--
	}
	return j < i && j >= 7 && strings.EqualFold(s[j-7:j], "file://")
}

// endsRootPlaceholder reports whether the ">" at s[i-1] closes "<root:NAME>"
// for the name of a configured root: the redactor's own output, whose tail is
// already rewritten. "<path>" is not skipped: the redactor never writes a
// path right after it, so "<path>/home/bruce/x" is a path to redact. A name no
// root has is text someone wrote, not output of this rule.
func (r pathRoots) endsRootPlaceholder(s string, i int) bool {
	// Compare the text before the ">" with each root's "<root:NAME" in place:
	// no search back through the line, so a line of ">" costs O(roots) per
	// character, not O(line).
	const open = "<root:"
	for _, root := range r {
		start := i - (len(open) + len(root.Name) + 1)
		if start >= 0 && s[start:start+len(open)] == open && s[start+len(open):i-1] == root.Name {
			return true
		}
	}
	return false
}

// closingTag reports whether s[i] is the "/" of "</name>": tag-name characters
// follow it and then ">".
func closingTag(s string, i int) bool {
	if s[i] != '/' {
		return false
	}
	j := i + 1
	for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
		j++
	}
	return j > i+1 && j < len(s) && s[j] == '>'
}

// winDrive reports "X:\" or "X:/" at s[i].
func winDrive(s string, i int) bool {
	if i+2 >= len(s) {
		return false
	}
	c := s[i]
	return (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') && s[i+1] == ':' && (s[i+2] == '\\' || s[i+2] == '/')
}
