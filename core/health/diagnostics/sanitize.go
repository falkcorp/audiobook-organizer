// file: core/health/diagnostics/sanitize.go
// version: 1.2.0
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
// directory, a file or a user's folder that a configured root does not name as
// "<root:NAME>". It replaces the F8-03 path scanner, which tried to find where
// a path starts and ends inside free text; that cannot be decided when a name
// may hold spaces and punctuation ("open /Users/Bruce Wayne: denied" and
// "open /Users/bruce: Wayne said no" are the same bytes), so this rule does not
// try. It decides where a location STARTS and wipes from there to the end of the
// line, except where the line ends in a text it knows.
//
// Text works on one string at a time:
//
//  1. The string is split into lines and the lines into words on
//     unicode.IsSpace (so U+00A0 and U+2028 are separators, which only ever
//     wipes more).
//
//  2. A word is "triggered" when it holds "/", "\", "%", "~", "&#" or any
//     named entity ("&amp;#47;" is double-encoded) anywhere, or, after leading
//     opening punctuation, starts with a letter and ":". A triggered word can be
//     a path in any spelling: raw, percent-encoded at any depth, JSON-escaped
//     ("\/", "/", "\\u002F"), an HTML entity, glued to a flag ("-I/x") or
//     to a key ("p=/x"), a "~user", "C:rel", "\rooted", a UNC path or the path of
//     a smb, nfs or sftp URL.
//
//  3. On a line with a triggered word, everything from the first triggered
//     word to the end of the line is one run. Where the location is known to
//     start (a word that begins with "/", "\", "~", a drive, "scheme://", an
//     encoded or entity separator, after opening punctuation and an optional
//     "key=") the run starts at that word; when the first triggered word has
//     name characters before its first separator ("Gordon/The", "-I/x",
//     "text/plain"), the start of the name is not known and the run starts at
//     the beginning of the line.
//
//  4. The one exception is an error that ends in a text the sanitizer knows
//     (errnoTexts: the syscall errors, the fs and os sentinels, the Windows API
//     texts, "exit status N", "context canceled" and the like), preceded by a
//     word that ends in ":". The run then ends at the word before that text,
//     and the text stays: "open /Volumes/My Books/x: permission denied" is
//     "open <path>: permission denied". The known text must be the end of the
//     line, so a name cannot hide words behind it.
//
//  5. A wiped run becomes one placeholder, "<root:NAME>" when its text starts
//     under a configured root and holds no second location start, else
//     "<path>". Leading opening punctuation stays outside it; so does the ":"
//     that ends a template's last word. A placeholder holds no trigger byte, so
//     Text(Text(x)) == Text(x).
//
// What survives: the words before the first triggered word of a line, when that
// word starts a location ("open /x" keeps "open"). What is lost with a location
// is the rest of its line, whatever it says: a message after a path ("copied
// /a/b to the shelf"), a "key=value" after a path, a stack frame's "+0x1d", an
// inline route, MIME type or URL ("GET /api/v1/books/{id} 200" is "GET <path>",
// "text/plain" is "<path>"), the tail of a wrapped error that is not a known text
// ("write tags /a/b: unsupported format" is "write tags <path>"), and a relative
// path from the start of its line ("planned Alfred Pennyworth/Batman Hush/Part
// 1.m4b" is "<path>"). A quote that opened before the location stays, since the
// run does not end where the quote closes ("open \"<path>").
//
// Not locations, so not touched: a bare user or host name, a name with no
// separator at all, a time of day, and a classic Mac path
// ("Macintosh HD:Users:bruce"), which has no trigger byte. Two shapes are not
// told apart from prose by any rule: a relative path whose first name ends in
// white space or in an opening quote ("HD /x" reads as the word "HD" before an
// absolute path, so "HD" stays), and a name that holds a line break, which is
// wiped one line at a time because the next line has no trigger.
//
// The zero Sanitizer has no roots and knows no error text: every location is
// "<path>" and wipes to the end of its line.
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

// run is a stretch of words wiped as one placeholder. tmpl marks a run that
// ends at a known errno text, whose last word then ends in the ":" of the wrap.
type run struct {
	first, last int
	tmpl        bool
}

// maxTextBytes is the longest string that is tokenized. A longer one that has a
// trigger byte is replaced whole: a token slice is about 32 bytes per word.
const maxTextBytes = 8 << 20

// tooLong replaces a string over maxTextBytes. It holds no trigger.
const tooLong = "<redacted:line_too_long>"

// text is Text that also reports how many steps it took (bytes inspected,
// words compared, bytes cleaned), which the linearity test bounds instead of a
// clock. The count is self-reported.
func (s Sanitizer) text(str string) (string, int) {
	ops := len(str)
	if !strings.ContainsAny(str, "/\\%~&:") {
		return str, ops
	}
	if len(str) > maxTextBytes {
		return tooLong, ops
	}
	// errs is nil for the zero Sanitizer, whose lookups find nothing: it knows no
	// error text, so a location wipes to the end of its line, and no index is
	// built per call.
	errs := s.errs
	toks := tokenize(str, &ops)
	exempt(str, toks, errs, &ops)
	runs := lineRuns(str, toks, errs, &ops)
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
// text, not a location.
func exempt(str string, toks []token, errs map[string][][]string, ops *int) {
	for j := 1; j < len(toks); j++ {
		if str[toks[j-1].e-1] != ':' {
			continue
		}
		if n := errnoLen(str, toks, j, len(toks), 0, errs, ops); n > 0 {
			for m := j; m < j+n; m++ {
				toks[m].trig = false
			}
		}
	}
}

// maxErrnoWord is longer than any word of a known text: a longer word cannot
// start one, so it is never lowered or compared.
const maxErrnoWord = 64

// errnoLen is how many words from j (before end) make a known errno text, 0 for
// none: the longest one when exactly is 0, else only a text of exactly that many
// words. Interior words must match exactly (ignoring ASCII case); the last may be
// followed by trailing punctuation; the word "#" of a text matches a number.
func errnoLen(str string, toks []token, j, end, exactly int, errs map[string][][]string, ops *int) int {
	w := str[toks[j].s:toks[j].e]
	if len(w) > maxErrnoWord {
		return 0
	}
	w = w[:len(w)-trailPunct(w)]
	*ops += len(w)
	for _, p := range errs[lowerASCII(w)] {
		if j+len(p) > end || exactly != 0 && len(p) != exactly {
			continue
		}
		ok := true
		for m, want := range p {
			tw := str[toks[j+m].s:toks[j+m].e]
			if len(tw) > maxErrnoWord {
				ok = false // too long to be a word of any text: not trimmed, lowered or compared
				break
			}
			*ops += len(tw)
			if m == len(p)-1 {
				tw = tw[:len(tw)-trailPunct(tw)]
			}
			if !wordIs(tw, want) {
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

// wordIs reports that w is want ignoring ASCII case; want "#" is a number of one
// to six digits. The lengths are compared first, so a long word costs nothing.
func wordIs(w, want string) bool {
	if want == "#" {
		if len(w) == 0 || len(w) > 6 {
			return false
		}
		for i := 0; i < len(w); i++ {
			if w[i] < '0' || w[i] > '9' {
				return false
			}
		}
		return true
	}
	if len(w) != len(want) {
		return false
	}
	for i := 0; i < len(w); i++ {
		c := w[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != want[i] {
			return false
		}
	}
	return true
}

func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}

// lineRuns finds, per line, the one run to wipe: it starts at the line's first
// triggered word (at the start of the line when that word does not start a
// location, see startsAsLocation) and ends at the end of the line, or at the word
// before a known errno text that is the end of the line and follows a word ending
// in ":". The errno text is the one thing that may stay after a location: a Go
// file error is "<op> <path>: <err>", usually wrapped ("scan <dir>: open <path>:
// <err>"), and it is searched from the left so a text inside a name cannot end the
// run early (the text has to reach the end of the line).
func lineRuns(str string, toks []token, errs map[string][][]string, ops *int) []run {
	var runs []run
	for a := 0; a < len(toks); {
		b := a
		for b < len(toks) && toks[b].line == toks[a].line {
			b++
		}
		f := -1
		for i := a; i < b; i++ {
			*ops += toks[i].e - toks[i].s
			if toks[i].trig {
				f = i
				break
			}
		}
		if f >= 0 {
			r := run{first: f, last: b - 1}
			if !startsAsLocation(str[toks[f].s:toks[f].e]) {
				r.first = a
			}
			for j := f + 1; j < b; j++ {
				if str[toks[j-1].e-1] == ':' && errnoLen(str, toks, j, b, b-j, errs, ops) == b-j {
					r.last, r.tmpl = j-1, true
					break
				}
			}
			runs = append(runs, r)
		}
		a = b
	}
	return runs
}

// render writes str with each run replaced by its placeholder.
func (s Sanitizer) render(str string, toks []token, runs []run, ops *int) string {
	var b strings.Builder
	b.Grow(len(str))
	pos := 0
	for _, r := range runs {
		ft, lt := toks[r.first], toks[r.last]
		bs := ft.s + leadPunct(str[ft.s:ft.e])
		te := lt.e
		if r.tmpl && str[te-1] == ':' {
			te-- // the wrap's colon stays outside; nothing else of the run does
		}
		if te < bs {
			te = bs
		}
		body := str[bs:te]
		*ops += len(body) * (1 + len(s.roots))
		b.WriteString(str[pos:bs])
		if name, ok := s.roots.rootOf(labelBody(body)); ok && !startsAnother(str, toks, r) {
			b.WriteString("<root:" + name + ">")
		} else {
			b.WriteString("<path>")
		}
		pos = te
	}
	b.WriteString(str[pos:])
	return b.String()
}

// labelBody is the text of a run that the root of its location is read from: the
// run without the "key=" prefix and the opening punctuation of its first word
// (the whole run is wiped either way).
func labelBody(body string) string {
	if k := strings.IndexByte(body, '='); k > 0 && !strings.ContainsAny(body[:k], "/\\%~& \t\n") {
		body = body[k+1:]
	}
	return body[leadPunct(body):]
}

// startsAsLocation reports a word that begins a location, after opening
// punctuation and an optional "key=" prefix: see locationStart. A word that does
// not ("Gordon/The", "-I/x", "text/plain") has name characters before its first
// separator, and where they begin is not known.
func startsAsLocation(w string) bool {
	if k := strings.IndexByte(w, '='); k > 0 && !strings.ContainsAny(w[:k], "/\\%~&") {
		w = w[k+1:]
	}
	w = w[leadPunct(w):]
	return w != "" && locationStart(w)
}

// locationStart reports a word (after opening punctuation) that begins as a
// location: "/", "\", "~", a drive, "scheme://", an encoded separator ("%2F",
// "%5C", "%7E") or an entity ("&#...", "&sol;").
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
	return driveStart(w) || schemeStart(w)
}

// schemeStart reports "scheme://": a letter, then letters, digits, "+", "-" or
// ".", then "://".
func schemeStart(w string) bool {
	if !isLetter(w[0]) {
		return false
	}
	for i := 1; i < len(w); i++ {
		c := w[i]
		if c == ':' {
			return strings.HasPrefix(w[i:], "://")
		}
		if !(isLetter(c) || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			return false
		}
	}
	return false
}

// startsAnother reports a run in which a word after the first begins a location
// of its own ("/srv/lib/a to /Users/bruce/x", "copy /srv/lib/a key=/Users/x"): a
// run that holds two locations is not "under" the root of the first, so it is
// "<path>". A word of a name with spaces ("/srv/My Library/Hush") does not begin
// one.
func startsAnother(str string, toks []token, r run) bool {
	for i := r.first + 1; i <= r.last; i++ {
		w := str[toks[i].s:toks[i].e]
		w = w[:len(w)-trailPunct(w)] // "y:" ends a name, it is not a drive
		if w != "" && startsAsLocation(w) {
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
		ws[len(ws)-1] = ws[len(ws)-1][:len(ws[len(ws)-1])-trailPunct(ws[len(ws)-1])]
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
// the end of a file error on Linux, macOS and Windows, of the fs and os
// sentinels, and of the few other texts that end an error in this program
// (the "#" word of "exit status #" is a number). TestSanitize_ErrnoTextsCoverTheHost
// checks the host's own table. A text that is missing is not a leak: the line is
// wiped to its end instead.
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
		// More syscall.Errno texts of Linux and macOS.
		"no medium found", "wrong medium type", "software caused connection abort", "required key not available",
		"key has expired", "key has been revoked", "key was rejected by service", "link has been severed",
		"too many users", "attribute not found",
		// The rest of the macOS table, then Linux texts that differ from it.
		"result too large", "operation already in progress", "socket operation on non-socket",
		"destination address required", "message too long", "protocol wrong type for socket", "protocol not available",
		"socket type not supported", "protocol family not supported", "address family not supported by protocol family",
		"can't assign requested address", "network is down", "network dropped connection on reset",
		"socket is already connected", "can't send after socket shutdown", "too many references: can't splice",
		"too many processes", "disc quota exceeded", "too many levels of remote in path", "rpc struct is bad",
		"rpc version wrong", "rpc prog. not avail", "program version wrong", "bad procedure for program",
		"need authenticator", "device power is off", "device error", "value too large to be stored in data type",
		"bad executable (or shared library)", "shared library version mismatch", "malformed mach-o file",
		"illegal byte sequence", "emultihop (reserved)", "no message available on stream", "enolink (reserved)",
		"no stream resources", "not a stream", "protocol error", "stream ioctl timeout", "policy not found",
		"state not recoverable", "previous owner died", "interface output queue is full",
		"channel number out of range", "level 2 not synchronized", "level 3 halted", "level 3 reset",
		"link number out of range", "protocol driver not attached", "no csi structure available", "level 2 halted",
		"invalid request descriptor", "exchange full", "no anode", "invalid request code", "invalid slot",
		"bad font file format", "device not a stream", "timer expired", "out of streams resources",
		"machine is not on the network", "package not installed", "object is remote", "advertise error", "srmount error",
		"communication error on send", "multihop attempted", "rfs specific error", "name not unique on network",
		"file descriptor in bad state", "remote address changed", "can not access a needed shared library",
		"accessing a corrupted shared library", ".lib section in a.out corrupted",
		"attempting to link in too many shared libraries", "cannot exec a shared library directly",
		"invalid or incomplete multibyte or wide character", "interrupted system call should be restarted",
		"streams pipe error", "address family not supported by protocol", "cannot assign requested address",
		"transport endpoint is already connected", "cannot send after transport endpoint shutdown",
		"too many references: cannot splice", "not a xenix named type file", "no xenix semaphores available",
		"is a named type file", "owner died", "operation not possible due to rf-kill", "memory page has hardware error",
		// Texts that end an error without being an errno: the os.Root escape error, the
		// context errors, an exec exit and the one ffprobe ends with.
		"path escapes from parent", "context canceled", "context deadline exceeded", "unexpected eof", "eof",
		"exit status #", "invalid data found when processing input", "file type does not support deadline",
	}
}
