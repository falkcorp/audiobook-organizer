// file: core/health/diagnostics/sanitize.go
// version: 1.1.0
// guid: 8d41c7a2-3b60-4e95-a1f8-6c2d9e07b534
// last-edited: 2026-11-16

package diagnostics

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitizer is the share-safety rule of the diagnostics bundle (S1,
// docs/agent-tasks/platform-core/seams.d/F8-03.md): no byte of a bundle names a
// directory, a file, a user or a volume that a configured root does not name as
// "<root:NAME>". It replaces the F8-03 path scanner, which tried to find where
// a path starts and ends inside free text; that cannot be decided when a name
// may hold spaces and punctuation ("open /Users/Bruce Wayne: denied" and
// "open /Users/bruce: Wayne said no" are the same bytes), so this rule does not
// try. It wipes whole words, and reads exactly the one text whose structure is
// known, the message of a Go file error.
//
// Text works on one string at a time:
//
//  1. The string is split into words on unicode.IsSpace (so U+00A0 and U+2028
//     are separators, which only ever wipes more).
//
//  2. A word is "triggered" when it holds "/", "\", "%", "~", "&#" or any
//     named entity ("&amp;#47;" is double-encoded) anywhere, or, after leading
//     opening punctuation, starts with a letter and ":". A triggered word can be
//     a path in any spelling: raw, percent-encoded at any depth, JSON-escaped
//     ("\/", "/", "\\u002F"), an HTML entity, glued to a flag ("-I/x") or
//     to a key ("p=/x"), a "~user", "C:rel", "\rooted", a UNC path or the path of
//     a smb, nfs or sftp URL. No start is searched for and no boundary judged.
//
//  3. A line that ends a Go file error in a known errno text is wiped from its
//     first triggered word to the word before that text (templates below), so a
//     name with spaces is wiped whole and the errno text after it survives,
//     whatever operation or wrapping comes before it.
//
//  4. Bridge rule: the words between the first and the last triggered word of a
//     string are wiped with them, because they may be the middle of a name that
//     holds spaces ("/Volumes/Macintosh HD/Users/x"); a template extends the run
//     before it instead of starting a second one.
//
//  5. A wiped run becomes one placeholder, "<root:NAME>" when its text starts
//     under a configured root (nothing after it: the tail names authors and
//     titles), else "<path>". Leading opening punctuation and trailing
//     punctuation (": , ; . ) ] } \" '") stay outside it. A placeholder holds
//     no trigger byte, so Text(Text(x)) == Text(x).
//
//  6. A string that is one location and nothing else (it starts as one and holds
//     no ": ", quote, line break or second location start) is wiped whole
//     (wholeLocation), so a value like "/srv/lib/Frank Miller/The Long
//     Halloween" leaves nothing of its final segment.
//
// What survives, exactly (the residual, owner-accepted): only the last word or
// words of a space-containing final segment of a location in a string that does
// not start as a location, is not a Go file error and has no later triggered
// word ("user dir /Users/Bruce Wayne is full" keeps "Wayne is full"). What is lost: inline routes and URLs,
// MIME types and "a/b" in free text; structured route and method fields keep
// the useful part through safeKeys.
//
// The zero Sanitizer has no roots: every location is "<path>".
type Sanitizer struct {
	roots pathRoots
	errs  map[string][][]string
}

// NewSanitizer is the Sanitizer over the given known roots.
func NewSanitizer(roots []PathRoot) Sanitizer {
	return Sanitizer{roots: newPathRoots(roots), errs: errnoIndex()}
}

// Text is s with every location wiped (see Sanitizer).
func (s Sanitizer) Text(str string) string {
	out, _ := s.text(str)
	return out
}

// token is one whitespace-delimited word of the string being sanitized.
type token struct {
	s, e int
	trig bool
	line int
}

// run is a stretch of words wiped as one placeholder.
type run struct{ first, last int }

// text is Text that also reports how many steps it took (bytes inspected,
// words compared, bytes cleaned), which the linearity test bounds instead of a
// clock. The count is self-reported.
func (s Sanitizer) text(str string) (string, int) {
	ops := len(str)
	if !strings.ContainsAny(str, "/\\%~&:") {
		return str, ops
	}
	errs := s.errs
	if errs == nil {
		errs = errnoIndex()
	}
	toks := tokenize(str, &ops)
	exempt(str, toks, errs, &ops)
	found := false
	for _, t := range toks {
		if t.trig {
			found = true
			break
		}
	}
	if !found {
		return str, ops
	}
	var runs []run
	if wholeLocation(str, toks) {
		runs = []run{{first: 0, last: len(toks) - 1}}
	} else {
		runs = generic(toks, templates(str, toks, errs, &ops))
	}
	if len(runs) == 0 {
		return str, ops
	}
	return s.render(str, toks, runs, &ops), ops
}

// tokenize splits str on unicode.IsSpace and marks the triggered words.
func tokenize(str string, ops *int) []token {
	var toks []token
	line := 0
	i := 0
	for i < len(str) {
		r, w := rune(str[i]), 1
		if r >= utf8.RuneSelf {
			r, w = utf8.DecodeRuneInString(str[i:])
		}
		if unicode.IsSpace(r) {
			if r == '\n' {
				line++
			}
			i += w
			continue
		}
		start := i
		for i < len(str) {
			r, w = rune(str[i]), 1
			if r >= utf8.RuneSelf {
				r, w = utf8.DecodeRuneInString(str[i:])
			}
			if unicode.IsSpace(r) {
				break
			}
			i += w
		}
		toks = append(toks, token{s: start, e: i, trig: triggered(str[start:i]), line: line})
		*ops += i - start
	}
	return toks
}

// triggered reports a word that can be, or hold, a location.
func triggered(w string) bool {
	if w == "fork/exec" {
		return false // the os operation word of exec errors, fixed text
	}
	for i := 0; i < len(w); i++ {
		switch w[i] {
		case '/', '\\', '%', '~':
			return true
		case '&':
			if entityTrigger(w[i+1:]) {
				return true
			}
		}
	}
	return driveStart(w)
}

// entityTrigger reports the text after "&" that is an HTML entity: a numeric one
// ("#47;", "#x2F;") or a named one ("sol;", "amp;" as in a double-encoded
// "&amp;#47;"). Any entity can spell a separator once decoded, so any is a
// trigger.
func entityTrigger(rest string) bool {
	if rest != "" && rest[0] == '#' {
		return true
	}
	n := 0
	for n < len(rest) && n < 32 && (isLetter(rest[n]) || rest[n] >= '0' && rest[n] <= '9') {
		n++
	}
	return n > 0 && n < len(rest) && rest[n] == ';'
}

// driveStart reports a word that, after leading opening punctuation, starts
// with a letter and ":" (a drive, with or without a separator).
func driveStart(w string) bool {
	w = w[leadPunct(w):]
	return len(w) >= 2 && isLetter(w[0]) && w[1] == ':'
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// leadPunct is the length of the run of opening punctuation at the start of w.
func leadPunct(w string) int {
	n := 0
	for n < len(w) && strings.IndexByte("\"'([{<", w[n]) >= 0 {
		n++
	}
	return n
}

// trailPunct is the length of the run of ": , ; . ) ] } \" '" at the end of w.
func trailPunct(w string) int {
	n := 0
	for n < len(w) && strings.IndexByte(":,;.)]}\"'", w[len(w)-1-n]) >= 0 {
		n++
	}
	return n
}

// exempt clears the trigger of the words of a known errno text that follows a
// word ending in ":" ("input/output error" holds a "/"): the phrase is fixed
// text, not a location. A word of it between two triggered words is still
// bridged.
func exempt(str string, toks []token, errs map[string][][]string, ops *int) {
	for j := 1; j < len(toks); j++ {
		if str[toks[j-1].e-1] != ':' {
			continue
		}
		if n := errnoLen(str, toks, j, errs, ops); n > 0 {
			for m := j; m < j+n; m++ {
				toks[m].trig = false
			}
		}
	}
}

// errnoStart is the number of words of the errno text that begins at word j
// (0 for none), when the word before j ends in ":".
func errnoStart(str string, toks []token, j int, errs map[string][][]string, ops *int) int {
	if j < 1 || str[toks[j-1].e-1] != ':' {
		return 0
	}
	return errnoLen(str, toks, j, errs, ops)
}

// errnoLen is how many words from j make a known errno text (longest first), 0
// for none. Interior words must match exactly (ignoring case); the last may be
// followed by trailing punctuation.
func errnoLen(str string, toks []token, j int, errs map[string][][]string, ops *int) int {
	w := str[toks[j].s:toks[j].e]
	first := lowerASCII(w[:len(w)-trailPunct(w)])
	cands := errs[first]
	*ops += len(w)
	for _, p := range cands {
		if j+len(p) > len(toks) {
			continue
		}
		ok := true
		for m, want := range p {
			tw := str[toks[j+m].s:toks[j+m].e]
			if m == len(p)-1 {
				tw = tw[:len(tw)-trailPunct(tw)]
			}
			*ops += len(want)
			if lowerASCII(tw) != want {
				ok = false
				break
			}
		}
		if ok {
			return len(p)
		}
	}
	return 0
}

func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}

// templates finds, per line, the stretch that is the location of a Go file
// error. *os.PathError is "<op> <path>: <err>", *os.LinkError
// "<op> <old> <new>: <err>" and exec.Error `exec: "<name>": <err>`, and the
// error is usually wrapped ("scan <dir>: open <path>: <err>"), so the rule does
// not parse the operation. The errno text is the one fixed anchor: it is
// searched from the right, and everything from the line's first triggered word
// up to the word before it is one run, spaces, colons and all. A relative path
// has no trigger at its first word ("open My Books/a.m4b"), so the run also
// takes the words after the nearest operation word to the left of the first
// triggered word (isOpWord); an operation the list does not know leaves such a
// relative name's first words. A line with no errno text has no template and
// falls to the bridge rule.
func templates(str string, toks []token, errs map[string][][]string, ops *int) []run {
	var runs []run
	for a := 0; a < len(toks); {
		b := a
		for b < len(toks) && toks[b].line == toks[a].line {
			b++
		}
		last := -1
		for j := a + 1; j < b; j++ {
			if errnoStart(str, toks, j, errs, ops) > 0 {
				last = j
			}
		}
		if last >= 0 {
			first := -1
			for i := a; i < last; i++ {
				*ops += toks[i].e - toks[i].s
				if toks[i].trig {
					first = i
					break
				}
			}
			if first >= 0 {
				start := first
				for i := first - 1; i >= a; i-- {
					*ops += toks[i].e - toks[i].s
					if isOpWord(str[toks[i].s:toks[i].e]) {
						start = i + 1
						break
					}
				}
				runs = append(runs, run{first: start, last: last - 1})
			}
		}
		a = b
	}
	return runs
}

// isOpWord reports an os operation word ("open"), possibly after opening
// punctuation and a "key=" prefix (`err="open`).
func isOpWord(w string) bool {
	w = w[leadPunct(w):]
	if k := strings.LastIndexByte(w, '='); k >= 0 {
		w = w[k+1:]
		w = w[leadPunct(w):]
	}
	switch w {
	case "open", "openat", "stat", "lstat", "fstat", "statat", "mkdir", "mkdirat", "remove", "removeall", "rename",
		"readdir", "readdirent", "readlink", "chmod", "fchmod", "chown", "lchown", "fchown", "chtimes", "chdir",
		"fchdir", "getwd", "truncate", "ftruncate", "symlink", "link", "unlinkat", "fdopendir", "write", "read",
		"readat", "writeat", "readfrom", "writeto", "close", "seek", "sync", "fsync", "fork/exec", "exec:":
		return true
	}
	return false
}

// generic adds the bridge-rule runs: between templates, the words from the first
// to the last triggered word of the group are one run, and a template extends
// the run of the group before it when that group holds a triggered word (never
// a second run, so no word between two locations is left standing).
func generic(toks []token, tmpl []run) []run {
	var out []run
	firstTrig := func(a, b int) int {
		for i := a; i < b; i++ {
			if toks[i].trig {
				return i
			}
		}
		return -1
	}
	lastTrig := func(a, b int) int {
		for i := b - 1; i >= a; i-- {
			if toks[i].trig {
				return i
			}
		}
		return -1
	}
	gs := 0
	for _, t := range tmpl {
		if f := firstTrig(gs, t.first); f >= 0 {
			out = append(out, run{first: f, last: t.last})
		} else {
			out = append(out, t)
		}
		gs = t.last + 1
	}
	if f := firstTrig(gs, len(toks)); f >= 0 {
		out = append(out, run{first: f, last: lastTrig(gs, len(toks))})
	}
	return out
}

// render writes str with each run replaced by its placeholder.
func (s Sanitizer) render(str string, toks []token, runs []run, ops *int) string {
	var b strings.Builder
	b.Grow(len(str))
	pos := 0
	for _, r := range runs {
		ft, lt := toks[r.first], toks[r.last]
		bs := ft.s + leadPunct(str[ft.s:ft.e])
		te := lt.e - trailPunct(str[lt.s:lt.e])
		if te < bs {
			te = bs
		}
		body := str[bs:te]
		*ops += len(body) * (1 + len(s.roots))
		b.WriteString(str[pos:bs])
		if name, ok := s.roots.rootOf(body); ok && !startsAnother(str, toks, r) {
			b.WriteString("<root:" + name + ">")
		} else {
			b.WriteString("<path>")
		}
		pos = te
	}
	b.WriteString(str[pos:])
	return b.String()
}

// wholeLocation reports a string that is one location and nothing else: it starts
// as one (after opening punctuation, "/", "\", "~", a drive or "%"), and holds no
// line break, no ": " (a message after the location), no quote or backtick and
// no later word that starts a location of its own. A name with spaces is part of
// such a string ("/srv/lib/Frank Miller/The Long Halloween"), so the whole
// string is wiped and the last words of its final segment do not survive. A
// string that is more than that (an error message, prose that starts with a
// path) goes to the templates and the bridge rule instead.
func wholeLocation(str string, toks []token) bool {
	if len(toks) == 0 || strings.ContainsAny(str, "\n\r\"`") || strings.Contains(str, ": ") {
		return false
	}
	for i, t := range toks {
		w := str[t.s:t.e]
		w = w[leadPunct(w):]
		if w == "" {
			continue
		}
		starts := locationStart(w)
		if i == 0 && !starts {
			return false
		}
		if i > 0 && starts {
			return false
		}
	}
	return true
}

// locationStart reports a word (after opening punctuation) that begins as a
// location: "/", "\", "~", a drive, an encoded separator ("%2F", "%5C", "%7E")
// or an entity ("&#...", "&sol;").
func locationStart(w string) bool {
	switch w[0] {
	case '/', '\\', '~':
		return true
	case '%':
		if len(w) >= 3 {
			switch strings.ToUpper(w[1:3]) {
			case "2F", "5C", "7E":
				return true
			}
		}
	case '&':
		return entityTrigger(w[1:])
	}
	return driveStart(w)
}

// startsAnother reports a run in which a word after the first begins a location
// of its own ("/srv/lib/a to /Users/bruce/x"): a run that holds two locations is
// not "under" the root of the first, so it is "<path>". A word of a name with
// spaces ("/srv/My Library/Hush") does not begin one.
func startsAnother(str string, toks []token, r run) bool {
	for i := r.first + 1; i <= r.last; i++ {
		w := str[toks[i].s:toks[i].e]
		lo, hi := leadPunct(w), len(w)-trailPunct(w) // "y:" ends a name, it is not a drive
		if lo < hi && locationStart(w[lo:hi]) {
			return true
		}
	}
	return false
}

// errnoIndex indexes the known errno texts by their first word. Each text is a
// list of lowercase words; a list's last word has no trailing period.
func errnoIndex() map[string][][]string {
	idx := map[string][][]string{}
	for _, t := range errnoTexts() {
		ws := strings.Fields(strings.ToLower(t))
		ws[len(ws)-1] = strings.TrimRight(ws[len(ws)-1], ".")
		idx[ws[0]] = append(idx[ws[0]], ws)
	}
	for k, v := range idx {
		// Longest first, so "the system cannot find the file specified" wins
		// over "the system cannot find the file".
		for i := 1; i < len(v); i++ {
			for j := i; j > 0 && len(v[j]) > len(v[j-1]); j-- {
				v[j], v[j-1] = v[j-1], v[j]
			}
		}
		idx[k] = v
	}
	return idx
}

// errnoTexts are the texts of the syscall.Errno values the os package puts at
// the end of a file error on Linux, macOS and Windows, and of the fs and os
// sentinels. TestErrnoTexts_CoverTheHost checks the host's own table.
func errnoTexts() []string {
	return []string{
		"operation not permitted", "no such file or directory", "no such process", "interrupted system call",
		"input/output error", "no such device or address", "argument list too long", "exec format error",
		"bad file descriptor", "no child processes", "resource temporarily unavailable", "cannot allocate memory",
		"permission denied", "bad address", "block device required", "device or resource busy", "resource busy",
		"file exists", "invalid cross-device link", "cross-device link", "no such device", "not a directory",
		"is a directory", "invalid argument", "too many open files in system", "too many open files",
		"inappropriate ioctl for device", "text file busy", "file too large", "no space left on device",
		"illegal seek", "read-only file system", "too many links", "broken pipe", "numerical argument out of domain",
		"numerical result out of range", "resource deadlock avoided", "file name too long", "no locks available",
		"function not implemented", "directory not empty", "too many levels of symbolic links",
		"no message of desired type", "identifier removed", "stale file handle", "stale nfs file handle",
		"operation not supported", "operation not supported on socket", "operation not supported by device",
		"protocol not supported", "connection refused", "connection reset by peer", "connection timed out",
		"network is unreachable", "no route to host", "transport endpoint is not connected", "disk quota exceeded",
		"value too large for defined data type", "device not configured", "operation timed out",
		"no buffer space available", "bad message", "operation canceled", "operation now in progress",
		"socket is not connected", "address already in use", "host is down", "structure needs cleaning",
		"remote i/o error", "inappropriate file type or format", "authentication error", "bad cpu type in executable",
		"file does not exist", "file already exists", "file already closed", "use of closed file", "i/o timeout",
		"executable file not found in $path", "no data available", "invalid exchange", "not a typewriter",
		"the system cannot find the file specified.", "the system cannot find the file",
		"the system cannot find the path specified.", "the system cannot find the path",
		"access is denied.", "the process cannot access the file because it is being used by another process.",
		"the file exists.", "cannot create a file when that file already exists.", "the directory is not empty.",
		"the filename, directory name, or volume label syntax is incorrect.",
		"there is not enough space on the disk.", "the network path was not found.", "the device is not ready.",
		"the parameter is incorrect.", "the directory name is invalid.", "incorrect function.",
		"a required privilege is not held by the client.", "the media is write protected.",
	}
}
