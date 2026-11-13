// file: core/health/diagnostics/bundle.go
// version: 1.0.4
// guid: effb9b73-b53b-433b-92a8-5f9cb806d08d
// last-edited: 2026-11-13

package diagnostics

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log/slog"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/rbac"
)

// The bundle of 05-health §5H.3.5: Section, Writer, Viewer, Contributor, and
// the writer every section serialises through. Every string a section writes
// is redacted here, not by the section, in three layers applied in this order
// and failing closed: typed (a Secret or Redacted placeholder is the only form
// a secret-typed value can take, so its value is never in the bytes), pattern
// (every string passes the log.Redactor: credentials in URLs, authorization
// headers, API keys and bearer values, cookies, invite paths), and permission
// (a section the viewer may not read is omitted, build.go and download.go).
// A redactor that panics replaces the value with "<redacted:error>".

// BundleVersion is the layout version recorded in core.manifest.
const BundleVersion = 1

// Redaction layers, the "layer" label of mediacore_diagnostics_redactions_total.
const (
	LayerTyped      = "typed"
	LayerPattern    = "pattern"
	LayerPermission = "permission"
)

// Section is one part of a bundle. Sections write only through w, which
// redacts every string it serialises.
type Section struct {
	Name  string           // "<moduleID>.<name>"; becomes the directory in the zip
	Perm  rbac.Requirement // viewer must satisfy it, else the section is omitted (listed as omitted)
	Write func(ctx context.Context, w Writer, v Viewer) error
}

// Writer is what a Section writes through.
type Writer interface {
	// JSON writes v as the JSON file name (a path inside the section's
	// directory), redacted.
	JSON(name string, v any) error
	// File writes the text from r as the file name, redacted line by line and
	// size-capped. Binary content is refused.
	File(name string, r io.Reader) error
}

// JSONLWriter is the streaming side of the bundle's Writer, which WriteJSONL
// uses so a library-scale section never holds its rows in memory.
type JSONLWriter interface {
	Writer
	// OpenJSONL starts the file name and returns the sink that appends one
	// redacted JSON value per line. The sink must be finished before the next
	// file of the section is opened.
	OpenJSONL(name string) (JSONLSink, error)
}

// JSONLSink appends lines to one file. Add returns ErrFileFull once the file
// has reached its size cap; the caller stops and treats that as success (the
// manifest lists the file as truncated).
type JSONLSink interface {
	Add(v any) error
	Close() error
}

// ErrFileFull is JSONLSink.Add's answer at the size cap.
var ErrFileFull = errors.New("diagnostics: file reached core.diagnostics.max_file_bytes")

// ErrNotText is Writer.File's refusal of content that is not text.
var ErrNotText = errors.New("diagnostics: not a text file")

// WriteJSONL writes items to the file name, one redacted JSON value per line,
// streaming. It stops quietly at the file's size cap. Over a Writer that is not
// a JSONLWriter it falls back to File over a pipe, still streaming.
func WriteJSONL[T any](w Writer, name string, items iter.Seq[T]) error {
	if jw, ok := w.(JSONLWriter); ok {
		sink, err := jw.OpenJSONL(name)
		if err != nil {
			return err
		}
		for it := range items {
			if err := sink.Add(it); err != nil {
				cerr := sink.Close()
				if errors.Is(err, ErrFileFull) {
					return cerr
				}
				return errors.Join(err, cerr)
			}
		}
		return sink.Close()
	}
	pr, pw := io.Pipe()
	go func() {
		enc := json.NewEncoder(pw)
		var encErr error
		for it := range items {
			if encErr = enc.Encode(it); encErr != nil {
				break
			}
		}
		// A nil encErr is a clean end of stream (what Close would send).
		pw.CloseWithError(encErr)
	}()
	err := w.File(name, pr)
	pr.CloseWithError(err) // unblocks the producer if File stopped reading
	return err
}

// Viewer is the requesting actor's permissions, resolved at request time and
// carried into the task (the run outlives the request: current ExportOptions rule).
type Viewer struct {
	ActorID string
	Can     func(rbac.Permission) bool
}

// Satisfies reports whether the viewer meets r. Public and any-authenticated
// requirements pass (a viewer exists, so it is authenticated); AllOf needs every
// scope and AnyOf one; a Delegated, legacy or zero requirement fails closed.
func (v Viewer) Satisfies(r rbac.Requirement) bool {
	return satisfied(specOf(r), v.Can)
}

// ViewerOf is the Viewer a snapshot (PermsOf) describes. It reads the snapshot
// with the checker's own rule for core:admin: an administrator holds that one
// scope and nothing else concrete (rbac.Checker.Concrete lists what the roles
// expand to, and admin's reach over every other scope is the checker's flag,
// not a member of the set), so a held core:admin admits every scope. Reading it
// as an exact-name list instead drops every section from an administrator's
// bundle.
//
// The result is only ever the snapshot half of the run's viewer: Exporter.viewer
// intersects it with the actor's permissions as ViewerFor resolves them now,
// and that checker still refuses a Sensitive scope to a non-interactive
// principal and a scope the catalog does not declare, admin or not. Admitting
// everything here therefore grants nothing the live checker would refuse.
func ViewerOf(actorID string, perms []string) Viewer {
	held := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		held[p] = struct{}{}
	}
	_, admin := held[string(rbac.CoreAdmin)]
	return Viewer{ActorID: actorID, Can: func(p rbac.Permission) bool {
		if admin {
			return true
		}
		_, ok := held[string(p)]
		return ok
	}}
}

// PermsOf lists the concrete permissions c holds, for ExportParams.Perms. It
// never returns nil, so a run started with no permissions at all is told apart
// from a run started without a snapshot.
func PermsOf(c rbac.Checker) []string {
	out := []string{}
	if c == nil {
		return out
	}
	for _, p := range c.Concrete() {
		out = append(out, string(p))
	}
	return out
}

// RowOmitter is what the Writer a bundle hands a section also implements: it
// counts rows a section left out because the viewer may not read them, under
// the permission layer of the manifest's redaction counts.
type RowOmitter interface {
	OmitRows(n int)
}

// OmitRows counts n rows a section left out for the viewer on w, when w counts
// them (every Writer a bundle builds does).
func OmitRows(w Writer, n int) {
	if o, ok := w.(RowOmitter); ok && n > 0 {
		o.OmitRows(n)
	}
}

// Contributor is registered by a module (Registrar.Diagnostics, an addition to 06).
type Contributor interface {
	Sections() []Section
}

// PermSpec is a Requirement as the manifest records it, so the download can
// re-check each section against the downloader's current permissions.
type PermSpec struct {
	Kind  string   `json:"kind"` // public | authenticated | all_of | any_of | other
	Perms []string `json:"perms,omitempty"`
}

func specOf(r rbac.Requirement) PermSpec {
	switch r.Kind() {
	case rbac.KindPublic:
		return PermSpec{Kind: "public"}
	case rbac.KindAuthenticated:
		return PermSpec{Kind: "authenticated"}
	case rbac.KindAllOf, rbac.KindAnyOf:
		ps := r.Permissions()
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = string(p)
		}
		kind := "all_of"
		if r.Kind() == rbac.KindAnyOf {
			kind = "any_of"
		}
		return PermSpec{Kind: kind, Perms: out}
	}
	return PermSpec{Kind: "other"}
}

// satisfied evaluates a recorded requirement against can. Anything it does not
// recognise, an empty scope list included, is not satisfied.
func satisfied(p PermSpec, can func(rbac.Permission) bool) bool {
	switch p.Kind {
	case "public", "authenticated":
		return true
	case "all_of", "any_of":
		if len(p.Perms) == 0 || can == nil {
			return false
		}
		for _, s := range p.Perms {
			ok := can(rbac.Permission(s))
			if p.Kind == "all_of" && !ok {
				return false
			}
			if p.Kind == "any_of" && ok {
				return true
			}
		}
		return p.Kind == "all_of"
	}
	return false
}

// Secret is the typed layer's placeholder for a secret-typed value. It holds a
// name and no value, so the value cannot reach the bytes whatever a section
// does. It serialises as "<secret:NAME>".
type Secret struct{ Name string }

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal("<secret:" + s.Name + ">") }

// Redacted is the typed layer's placeholder for a field tagged sensitive. It
// serialises as "<redacted>".
type Redacted struct{}

// MarshalJSON implements json.Marshaler.
func (Redacted) MarshalJSON() ([]byte, error) { return json.Marshal("<redacted>") }

// counts is a bundle's redaction tally by layer.
type counts struct{ typed, pattern, permission int }

func (c counts) byLayer() map[string]int {
	m := map[string]int{}
	if c.typed > 0 {
		m[LayerTyped] = c.typed
	}
	if c.pattern > 0 {
		m[LayerPattern] = c.pattern
	}
	if c.permission > 0 {
		m[LayerPermission] = c.permission
	}
	return m
}

const (
	maxDepth     = 64
	maxLineBytes = 1 << 20
	redactedErr  = "<redacted:error>"
	redactedDeep = "<redacted:depth>"
	redactedLong = "<redacted:line_too_long>"
)

// redactString applies the pattern layer to one string under key, counting what
// changed. A redactor that panics fails closed.
func (st *build) redactString(key, s string) (out string) {
	defer func() {
		if recover() != nil {
			out = redactedErr
			st.counts.pattern++
		}
	}()
	if isTypedPlaceholder(s) {
		st.counts.typed++
		return s
	}
	out, changed := st.scrub(key, s)
	if changed {
		st.counts.pattern++
	}
	return out
}

// redactKey applies the pattern layer to an object member's name. A key is text
// like any other (a map keyed by URL, by token or by path puts that text in the
// bytes), so it passes the same redactor and path rule as a value. A key is
// never a typed placeholder, so there is no typed shortcut here.
func (st *build) redactKey(k string) (out string) {
	defer func() {
		if recover() != nil {
			out = redactedErr
			st.counts.pattern++
		}
	}()
	out, changed := st.scrub("", k)
	if changed {
		st.counts.pattern++
	}
	return out
}

// scrub is the pattern layer proper: the log.Redactor, then the absolute-path
// rule. It reports whether either changed s.
func (st *build) scrub(key, s string) (string, bool) {
	out, kind := st.red.StringKinds(key, s)
	changed := kind != 0 || out != s
	if p := st.roots.redactPaths(out); p != out {
		out, changed = p, true
	}
	return out, changed
}

// isTypedPlaceholder reports a string the typed layer produced.
// It is exact: a string that merely starts with "<secret:" must not skip the
// pattern layer.
func isTypedPlaceholder(s string) bool {
	return s == "<redacted>" || secretPlaceholder(s)
}

// secretPlaceholder is ^<secret:[A-Za-z0-9_.-]{1,128}>$.
func secretPlaceholder(s string) bool {
	id, ok := strings.CutPrefix(s, "<secret:")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, ">")
	if !ok || len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// walk redacts a decoded JSON tree under key. A deny-listed key replaces its
// whole value, whatever its kind.
func (st *build) walk(key string, v any, depth int) any {
	if depth > maxDepth {
		st.counts.pattern++
		return redactedDeep
	}
	switch x := v.(type) {
	case string:
		return st.redactString(key, x)
	case map[string]any:
		return st.walkObject(x, depth)
	case []any:
		for i, val := range x {
			x[i] = st.walk(key, val, depth+1)
		}
		return x
	case json.Number:
		if st.red.KeyDenied(key, slog.KindFloat64) {
			st.counts.pattern++
			return "[REDACTED]"
		}
	case bool:
		if st.red.KeyDenied(key, slog.KindBool) {
			st.counts.pattern++
			return "[REDACTED]"
		}
	}
	return v
}

// walkObject rebuilds an object with its keys redacted. The value is judged by
// the original key (a deny-listed name still replaces its value), the output
// carries the redacted key text. Keys that collapse to the same text are kept
// apart with a "#2", "#3" suffix, in the order of the original keys, so no
// member is lost and the result does not depend on map order.
func (st *build) walkObject(x map[string]any, depth int) map[string]any {
	keys := make([]string, 0, len(x))
	for k := range x {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make(map[string]any, len(x))
	for _, k := range keys {
		base := st.redactKey(k)
		nk := base
		for n := 2; ; n++ {
			if _, taken := out[nk]; !taken {
				break
			}
			nk = fmt.Sprintf("%s#%d", base, n)
		}
		out[nk] = st.walkMember(k, x[k], depth+1)
	}
	return out
}

// walkMember redacts the value of object member k. A deny-listed key replaces
// an object or array whole, so nothing nested under it survives.
func (st *build) walkMember(k string, v any, depth int) any {
	switch v.(type) {
	case map[string]any, []any:
		if st.red.KeyDenied(k, slog.KindAny) {
			st.counts.pattern++
			return "[REDACTED]"
		}
	}
	return st.walk(k, v, depth)
}

// redactJSON marshals v, redacts the decoded tree and returns the redacted
// JSON (indented when pretty).
func (st *build) redactJSON(v any, pretty bool) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: marshal: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("diagnostics: re-read: %w", err)
	}
	tree = st.walk("", tree, 0)
	// HTML escaping off: the placeholders are "<secret:NAME>" and "<redacted>",
	// and a reader should see them as written.
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(tree); err != nil {
		return nil, fmt.Errorf("diagnostics: encode: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// validSectionName is the grammar of Section.Name, "<moduleID>.<name>": two or
// more dot-separated segments of [a-z0-9_-], each starting with [a-z0-9]
// (^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)+$).
func validSectionName(n string) bool {
	segs := strings.Split(n, ".")
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs {
		if seg == "" {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			digitOrLower := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
			if !digitOrLower && (i == 0 || (c != '_' && c != '-')) {
				return false
			}
		}
	}
	return true
}

// validEntryName reports whether name is a relative path with no "..", no
// backslash, no NUL and no empty segment: a zip entry that cannot climb out of
// its section directory (zip slip).
func validEntryName(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, "\\\x00")
}

// sectionWriter is the Writer of one section.
type sectionWriter struct {
	st      *build
	section string
	open    bool // a JSONL sink is open
}

// entry starts the zip entry <section>/<name>.
func (w *sectionWriter) entry(name string) (*countingEntry, error) {
	if w.open {
		return nil, errors.New("diagnostics: finish the open JSONL file before starting another")
	}
	if !validEntryName(name) {
		return nil, fmt.Errorf("diagnostics: file name %q is not a plain relative path", name)
	}
	path := w.section + "/" + name
	if w.st.written[path] {
		return nil, fmt.Errorf("diagnostics: file %q written twice", path)
	}
	w.st.written[path] = true
	zw, err := w.st.zw.CreateHeader(&zip.FileHeader{Name: path, Method: zip.Deflate, Modified: w.st.now()})
	if err != nil {
		return nil, fmt.Errorf("diagnostics: create %s: %w", path, err)
	}
	return &countingEntry{w: zw, limit: w.st.limit, path: path, st: w.st}, nil
}

// countingEntry caps an entry's bytes: past the limit writes are discarded and
// the entry is listed as truncated.
type countingEntry struct {
	w     io.Writer
	limit int64
	n     int64
	over  bool
	path  string
	st    *build
}

func (c *countingEntry) Write(p []byte) (int, error) {
	if c.over {
		return len(p), nil
	}
	room := c.limit - c.n
	if int64(len(p)) > room {
		if room > 0 {
			if _, err := c.w.Write(p[:room]); err != nil {
				return 0, err
			}
			c.n += room
		}
		c.over = true
		c.st.truncated = append(c.st.truncated, c.path)
		return len(p), nil
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// JSON implements Writer.
func (w *sectionWriter) JSON(name string, v any) error {
	b, err := w.st.redactJSON(v, true)
	if err != nil {
		return err
	}
	e, err := w.entry(name)
	if err != nil {
		return err
	}
	if int64(len(b)) > w.st.limit {
		w.st.truncated = append(w.st.truncated, e.path)
		b = []byte(fmt.Sprintf(`{"error":"too_large","bytes":%d}`, len(b)))
	}
	_, err = e.Write(append(b, '\n'))
	return err
}

// File implements Writer.
func (w *sectionWriter) File(name string, r io.Reader) error {
	e, err := w.entry(name)
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, tooLong, rerr := readLine(br)
		if len(line) > 0 || tooLong {
			if bytes.IndexByte(line, 0) >= 0 {
				return fmt.Errorf("%w: %s/%s has a NUL byte", ErrNotText, w.section, name)
			}
			text := redactedLong
			if !tooLong {
				text = w.st.redactString("", string(line))
			} else {
				w.st.counts.pattern++
			}
			if _, err := io.WriteString(e, text+"\n"); err != nil {
				return err
			}
			if e.over {
				// The producer finishes; nothing more is kept. A read error
				// while draining is still an error of the section.
				_, err := io.Copy(io.Discard, br)
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// readLine reads one line without its newline. A line longer than
// maxLineBytes is consumed to its end and reported as tooLong with no content.
func readLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, e := br.ReadLine()
		if e != nil {
			return line, tooLong, e
		}
		if !tooLong {
			if len(line)+len(chunk) > maxLineBytes {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if !isPrefix {
			return line, tooLong, nil
		}
	}
}

// OpenJSONL implements JSONLWriter.
func (w *sectionWriter) OpenJSONL(name string) (JSONLSink, error) {
	e, err := w.entry(name)
	if err != nil {
		return nil, err
	}
	w.open = true
	return &jsonlSink{w: w, e: e}, nil
}

type jsonlSink struct {
	w      *sectionWriter
	e      *countingEntry
	closed bool
}

func (s *jsonlSink) Add(v any) error {
	if s.e.over {
		return ErrFileFull
	}
	b, err := s.w.st.redactJSON(v, false)
	if err != nil {
		return err
	}
	if _, err := s.e.Write(append(b, '\n')); err != nil {
		return err
	}
	if s.e.over {
		return ErrFileFull
	}
	return nil
}

func (s *jsonlSink) Close() error {
	if !s.closed {
		s.closed = true
		s.w.open = false
	}
	return nil
}

// OmitRows implements RowOmitter.
func (w *sectionWriter) OmitRows(n int) { w.st.counts.permission += n }

// ValidSectionName reports whether n is a section name, "<moduleID>.<name>".
// A start request is checked against it before a run is enqueued.
func ValidSectionName(n string) bool { return validSectionName(n) }
