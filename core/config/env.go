// file: core/config/env.go
// version: 1.0.1
// guid: 42d74b09-6939-4af0-857f-5c9778bb1ca3
// last-edited: 2026-11-11

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"
)

// The env layer (05-config §1.3.5, §1.3.6): canonical names derived from the
// path, <NAME>_FILE, empty-is-unset, the ecosystem bindings and their
// conflict rule, and strict decoding of every value. Config reads only
// Options.Environ, never os.Getenv.

const (
	// fileSuffix turns any env name into "read the value from this file".
	fileSuffix = "_FILE"
	// otelMetricExportInterval is the one binding whose decoder keeps a
	// bare integer as milliseconds, the OTel convention (05-config §1.3.6).
	otelMetricExportInterval = "OTEL_METRIC_EXPORT_INTERVAL"
	// integrationsRoot is the first segment of a passthrough path
	// (05-metadata M1, C11).
	integrationsRoot = "integrations"
	// maxPassthroughSeps bounds the separators of a passthrough env name the
	// env layer tries to place: each "_" may stand for ".", "-" or "_", so
	// the candidates grow as 3^n. Instance field paths have about five.
	maxPassthroughSeps = 10
	// healthBindingConflict is raised on core.config while a canonical name
	// and an ecosystem binding of one unprotected key disagree (§1.3.5).
	healthBindingConflict = "config_binding_conflict"
)

// envSuffix is the part of p's canonical env name after "<prefix>_":
// upper(path) with "." and "-" turned into "_" and a leading "modules."
// dropped (core is a reserved module ID, so the result is unambiguous).
func envSuffix(p Path) string {
	s := string(p)
	if rest, ok := strings.CutPrefix(s, "modules."); ok {
		s = rest
	}
	s = strings.ToUpper(s)
	return strings.NewReplacer(".", "_", "-", "_").Replace(s)
}

// envName is p's canonical env name (05-config §1.3.6): core.log.level ->
// ABO_CORE_LOG_LEVEL; modules.books.scan.on_startup ->
// ABO_BOOKS_SCAN_ON_STARTUP; integrations.metadata.google-books.api_key ->
// ABO_INTEGRATIONS_METADATA_GOOGLE_BOOKS_API_KEY.
func envName(prefix string, p Path) string { return prefix + "_" + envSuffix(p) }

// parseEnviron turns KEY=value entries into a map; a later entry wins, as
// os.Getenv reads it. An entry without "=" or with an empty name is skipped.
// Empty values are kept here: the source treats them as unset.
func parseEnviron(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		out[name] = val
	}
	return out
}

// fileText is one <NAME>_FILE read, memoized.
type fileText struct {
	text string
	err  error
}

// envSource is the environment as an operator layer. The store calls read
// under its mutex, so the memo maps need no lock of their own.
type envSource struct {
	prefix   string
	vars     map[string]string
	retired  map[string]bool // retired env names: never read (retired.go)
	log      *slog.Logger
	readFile func(string) ([]byte, error)

	files  map[string]fileText // <NAME>_FILE contents by file path, read once
	warned map[string]bool     // unknown prefixed names already warned about
	pt     map[string][]Path   // passthrough candidates by env name
}

func newEnvSource(prefix string, vars map[string]string, retired map[string]bool, log *slog.Logger, readFile func(string) ([]byte, error)) *envSource {
	return &envSource{
		prefix: prefix, vars: vars, retired: retired, log: log, readFile: readFile,
		files: map[string]fileText{}, warned: map[string]bool{}, pt: map[string][]Path{},
	}
}

// envResolver maps env names to the paths of one query.
type envResolver struct {
	e     *envSource
	q     sourceQuery
	canon map[string][]Path // canonical names of the registered struct keys
}

func (e *envSource) resolver(q sourceQuery) envResolver {
	r := envResolver{e: e, q: q, canon: make(map[string][]Path, len(q.paths))}
	for _, p := range q.paths {
		n := envName(e.prefix, p)
		r.canon[n] = append(r.canon[n], p)
	}
	return r
}

// direct lists the paths name names as it is spelled: a registered key's
// canonical name, a binding, a map section entry's key, a list entry's
// secret, or a passthrough path.
func (r envResolver) direct(name string) []Path {
	out := slices.Clone(r.canon[name])
	if p, ok := r.q.bindings[name]; ok {
		out = append(out, p)
	}
	if rest, ok := strings.CutPrefix(name, r.e.prefix+"_"); ok {
		out = append(out, r.templated(rest, r.q.maps, r.q.mapRels, nil)...)
		out = append(out, r.templated(rest, slices.Sorted(maps.Keys(r.q.entryRels)), r.q.entryRels, r.q.entrySecret)...)
		out = append(out, r.passthrough(rest)...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// templated places rest (an env name without "<prefix>_") under one of
// roots: "<root>_<ID>_<REL>" where ID is a valid segment spelled in upper
// case and REL one of rels[root]. ok, when non-nil, must accept the path.
func (r envResolver) templated(rest string, roots []Path, rels map[Path][]string, ok func(Path) bool) []Path {
	var out []Path
	for _, root := range roots {
		tail, found := strings.CutPrefix(rest, envSuffix(root)+"_")
		if !found {
			continue
		}
		for _, rel := range rels[root] {
			idUp, found := strings.CutSuffix(tail, "_"+envSuffix(Path(rel)))
			if !found || idUp == "" {
				continue
			}
			id := strings.ToLower(idUp)
			// Exact case: the ID's env spelling is its upper case.
			if !validSegment(id) || strings.ToUpper(id) != idUp {
				continue
			}
			p := root.Join(id).Join(rel)
			if ok == nil || ok(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// passthrough places an INTEGRATIONS_* name: every reading of its "_" as
// ".", "-" or "_" that Options.Passthrough accepts. Config cannot list the
// passthrough paths, only test one, so it tries each reading (bounded by
// maxPassthroughSeps).
func (r envResolver) passthrough(rest string) []Path {
	tail, ok := strings.CutPrefix(rest, strings.ToUpper(integrationsRoot)+"_")
	if !ok || tail == "" {
		return nil
	}
	name := r.e.prefix + "_" + rest
	if got, seen := r.e.pt[name]; seen {
		return got
	}
	var out []Path
	if exactUpperName(tail) && strings.Count(tail, "_") <= maxPassthroughSeps {
		lower := strings.ToLower(tail)
		var walk func(i int, cur []byte)
		walk = func(i int, cur []byte) {
			if i == len(lower) {
				p := Path(integrationsRoot + "." + string(cur))
				if !strings.Contains(string(p), "..") && !strings.HasSuffix(string(p), ".") && r.q.passthrough(p) {
					out = append(out, p)
				}
				return
			}
			if lower[i] != '_' {
				walk(i+1, append(cur, lower[i]))
				return
			}
			for _, sep := range []byte{'.', '-', '_'} {
				walk(i+1, append(slices.Clone(cur), sep))
			}
		}
		walk(0, make([]byte, 0, len(lower)))
	}
	r.e.pt[name] = out
	return out
}

// exactUpperName reports whether s holds only [A-Z0-9_], starting with a
// letter or digit: the spelling of a canonical env name's tail.
func exactUpperName(s string) bool {
	if s == "" || s[0] == '_' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// resolve places name: directly, or as <NAME>_FILE of a name that places.
// Both readings placing is an ambiguity the operator cannot see, so it is
// refused (ErrDuplicate), as is a name that places twice.
func (r envResolver) resolve(name string) (paths []Path, viaFile bool, err error) {
	paths = r.direct(name)
	if base, ok := strings.CutSuffix(name, fileSuffix); ok && base != "" {
		if fp := r.direct(base); len(fp) > 0 {
			if len(paths) > 0 {
				return nil, false, fmt.Errorf("%w: env %s is the env name of %s and the %s form of %s (%s)", ErrDuplicate, name, joinPaths(paths), fileSuffix, base, joinPaths(fp))
			}
			paths, viaFile = fp, true
		}
	}
	if len(paths) > 1 {
		return nil, false, fmt.Errorf("%w: env %s names %s", ErrDuplicate, name, joinPaths(paths))
	}
	return paths, viaFile, nil
}

// joinPaths lists paths for an error.
func joinPaths(ps []Path) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = string(p)
	}
	return strings.Join(s, " and ")
}

// read returns every env value q can place (05-config §1.3.6). An empty value
// is unset: it supplies nothing and locks nothing (§1.3.5). A retired name is
// never read. An unknown <prefix>_* name warns once, at Load only: at Boot the
// module sections are not registered yet.
func (e *envSource) read(q sourceQuery) ([]sourced, error) {
	if q.want != nil {
		return e.readWanted(q)
	}
	r := e.resolver(q)
	var out []sourced
	for _, name := range slices.Sorted(maps.Keys(e.vars)) {
		val := e.vars[name]
		if val == "" {
			continue
		}
		paths, viaFile, err := r.resolve(name)
		if err != nil {
			return nil, err
		}
		if e.retired[name] {
			if len(paths) > 0 {
				return nil, fmt.Errorf("%w: env %s is a retired name and the env name of %s", ErrDuplicate, name, paths[0])
			}
			continue
		}
		if len(paths) == 0 {
			if q.strict && strings.HasPrefix(name, e.prefix+"_") && !e.warned[name] {
				e.warned[name] = true
				e.log.Warn("config: an env variable with the app's prefix names no key; it is ignored", "env", name)
			}
			continue
		}
		v, err := e.value(name, val, viaFile)
		if err != nil {
			return nil, err
		}
		if envUnset(v) {
			continue
		}
		base := name
		if viaFile {
			base = strings.TrimSuffix(name, fileSuffix)
		}
		_, binding := q.bindings[base]
		out = append(out, sourced{path: paths[0], raw: json.RawMessage(v), by: "env " + name, text: true, binding: binding})
	}
	return out, nil
}

// readWanted is read for Store.Operator: only the wanted paths, by their
// canonical name, its <NAME>_FILE form and their bindings, whether or not a
// section owns them yet. It never warns.
func (e *envSource) readWanted(q sourceQuery) ([]sourced, error) {
	var out []sourced
	for _, w := range slices.Sorted(maps.Keys(q.want)) {
		names := []string{envName(e.prefix, w)}
		for n, p := range q.bindings {
			if p == w {
				names = append(names, n)
			}
		}
		slices.Sort(names[1:])
		for i, n := range names {
			for _, viaFile := range []bool{false, true} {
				full := n
				if viaFile {
					full += fileSuffix
				}
				val := e.vars[full]
				if val == "" || e.retired[full] {
					continue
				}
				v, err := e.value(full, val, viaFile)
				if err != nil {
					return nil, err
				}
				if envUnset(v) {
					continue
				}
				out = append(out, sourced{path: w, raw: json.RawMessage(v), by: "env " + full, text: true, binding: i > 0})
			}
		}
	}
	return out, nil
}

// value is one variable's text: the value itself, or the contents of the
// file a <NAME>_FILE names (read once, one trailing newline trimmed; an
// unreadable file fails boot). OTEL_METRIC_EXPORT_INTERVAL's bare integer is
// milliseconds.
func (e *envSource) value(name, val string, viaFile bool) (string, error) {
	text := val
	base := name
	if viaFile {
		base = strings.TrimSuffix(name, fileSuffix)
		ft, ok := e.files[val]
		if !ok {
			b, err := e.readFile(val)
			ft = fileText{text: string(b), err: err}
			e.files[val] = ft
		}
		if ft.err != nil {
			return "", fmt.Errorf("config: env %s: read %q: %w", name, val, ft.err)
		}
		if len(ft.text) > maxEnvFileBytes {
			return "", fmt.Errorf("%w: config: env %s: the file %q is larger than %d bytes; a <NAME>_FILE file holds one value",
				ErrInvalid, name, val, maxEnvFileBytes)
		}
		text = trimOneNewline(ft.text)
	}
	if base == otelMetricExportInterval && allDigits(text) {
		text += "ms"
	}
	return text, nil
}

// envUnset reports whether an env value supplies nothing (05-config §1.3.5):
// empty, or the literal null, for every key type, as a YAML null in the file
// is unset. The same holds for the contents of a <NAME>_FILE file. An env
// variable cannot set the string "null" (the file can, quoted).
func envUnset(text string) bool { return text == "" || text == "null" }

// maxEnvFileBytes caps a <NAME>_FILE file: a credential or a setting, never
// a bulk file. A larger one fails boot, naming the variable and the file and
// never its contents.
const maxEnvFileBytes = 1 << 20

// readEnvFile is the default <NAME>_FILE reader: it reads at most one byte
// past maxEnvFileBytes, so an oversized file is refused without being loaded.
func readEnvFile(path string) (b []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return io.ReadAll(io.LimitReader(f, maxEnvFileBytes+1))
}

// trimOneNewline drops one trailing line break ("\n" or "\r\n").
func trimOneNewline(s string) string {
	if t, ok := strings.CutSuffix(s, "\n"); ok {
		return strings.TrimSuffix(t, "\r")
	}
	return s
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// textJSON converts an operator's text (an env value, a <NAME>_FILE's
// contents) into JSON for key k by strict decoding (05-config §1.3.6). A
// sensitive key's error never quotes the text.
func textJSON(k *keyInfo, p Path, text string) (json.RawMessage, error) {
	out, err := textJSONOf(k, p, text)
	if err != nil && k != nil && k.redacts() {
		return nil, &FieldError{Path: p, Err: errSensitiveRedacted()}
	}
	return out, err
}

// textJSONOf is textJSON without the redaction: true/false for a bool, a Go
// duration, a number, JSON for an object or array, the text itself for a
// string, a secret and a passthrough path (config does not own its type).
// HEAD's lenient spellings (yes, a bare count of nanoseconds, a
// space-separated list) do not decode.
func textJSONOf(k *keyInfo, p Path, text string) (json.RawMessage, error) {
	if k == nil || k.secret {
		return json.Marshal(text)
	}
	t := k.typ
	switch {
	case t == reflect.TypeFor[Duration]():
		if _, err := time.ParseDuration(text); err != nil {
			return nil, &FieldError{Path: p, Err: fmt.Errorf("not a Go duration: %w", err)}
		}
		return json.Marshal(text)
	case t.Kind() == reflect.String:
		return json.Marshal(text)
	case t.Kind() == reflect.Bool:
		if text != "true" && text != "false" {
			return nil, &FieldError{Path: p, Err: fmt.Errorf("want true or false, got %q", text)}
		}
		return json.RawMessage(text), nil
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Float64:
		// A number is a JSON number literal of the key's own type, as the
		// file gives it: strconv's extras ("+5", "007", "0x1f", "1_000",
		// "Inf") are not JSON, so they do not decode, and the value is never
		// handed on in a form the snapshot cannot read.
		if !json.Valid([]byte(text)) {
			return nil, &FieldError{Path: p, Err: fmt.Errorf("not a %s", t.Kind())}
		}
		if err := json.Unmarshal([]byte(text), reflect.New(t).Interface()); err != nil {
			return nil, &FieldError{Path: p, Err: fmt.Errorf("not a %s: %w", t.Kind(), err)}
		}
		return json.RawMessage(text), nil
	default:
		if !json.Valid([]byte(text)) {
			return nil, &FieldError{Path: p, Err: errors.New("an object or array key takes JSON")}
		}
		return json.RawMessage(text), nil
	}
}

// operatorJSON is one source value as JSON: a text value decoded strictly
// for its key, a JSON value as given.
func operatorJSON(k *keyInfo, v sourced) (json.RawMessage, error) {
	if !v.text {
		return v.raw, nil
	}
	return textJSON(k, v.path, string(v.raw))
}

// comparable is v's value in the form two env values of one key compare in:
// canonical for a registered key, the JSON as given for a passthrough path.
func (s *Store) comparable(v sourced) (json.RawMessage, error) {
	ck, ok := s.keyFor(v.path)
	var k *keyInfo
	if ok {
		k = ck.k
	}
	raw, err := operatorJSON(k, v)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", v.by, invalid(err))
	}
	if !ok {
		return raw, nil
	}
	c, err := ck.k.canonical(v.path, raw)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", v.by, invalid(err))
	}
	if ck.k.secret {
		return raw, nil // the secret's text, never its reference
	}
	return c, nil
}

// protectedForBinding reports whether a canonical-versus-binding conflict
// on p fails boot: a key at ProtectAdmin or higher (§1.3.5), and, failing
// closed, a passthrough path or a path no section owns (config cannot see
// its class).
func (s *Store) protectedForBinding(p Path) bool {
	ck, ok := s.keyFor(p)
	if !ok {
		return true
	}
	return ck.k.protect >= ProtectAdmin
}

// mergeEnv applies the env layer's in-layer rules to vals (05-config
// §1.3.5): per key, the canonical name beats an ecosystem binding; the two
// set to different values fail boot on a ProtectAdmin+ key naming both and
// otherwise keep the canonical value with a warning (conflicts names those
// keys for health config_binding_conflict). A name and its <NAME>_FILE form
// both set, or two bindings disagreeing, fail boot: the operator's intent is
// unknown.
func (s *Store) mergeEnv(vals []sourced) (kept []sourced, conflicts map[Path]string, err error) {
	byPath := map[Path][]sourced{}
	var order []Path
	for _, v := range vals {
		if _, seen := byPath[v.path]; !seen {
			order = append(order, v.path)
		}
		byPath[v.path] = append(byPath[v.path], v)
	}
	for _, p := range order {
		group := byPath[p]
		if len(group) == 1 {
			kept = append(kept, group[0])
			continue
		}
		var canon, binds []sourced
		for _, v := range group {
			if v.binding {
				binds = append(binds, v)
			} else {
				canon = append(canon, v)
			}
		}
		if len(canon) > 1 {
			return nil, nil, fmt.Errorf("%w: config: %s and %s both set %s; set one", ErrDuplicate, canon[0].by, canon[1].by, p)
		}
		for i := 1; i < len(binds); i++ {
			same, err := s.sameValue(binds[0], binds[i])
			if err != nil {
				return nil, nil, err
			}
			if !same {
				return nil, nil, fmt.Errorf("config: %s and %s set %s to different values; set one", binds[0].by, binds[i].by, p)
			}
		}
		if len(canon) == 0 {
			kept = append(kept, binds[0])
			continue
		}
		same, err := s.sameValue(canon[0], binds[0])
		if err != nil {
			return nil, nil, err
		}
		if !same {
			if s.protectedForBinding(p) {
				return nil, nil, fmt.Errorf("config: %s and %s set %s to different values; it is a protected key, so boot stops: set one, or set both to the same value", canon[0].by, binds[0].by, p)
			}
			if conflicts == nil {
				conflicts = map[Path]string{}
			}
			conflicts[p] = fmt.Sprintf("%s wins over %s", canon[0].by, binds[0].by)
		}
		kept = append(kept, canon[0])
	}
	return kept, conflicts, nil
}

// sameValue compares two env values of one key.
func (s *Store) sameValue(a, b sourced) (bool, error) {
	ca, err := s.comparable(a)
	if err != nil {
		return false, err
	}
	cb, err := s.comparable(b)
	if err != nil {
		return false, err
	}
	return string(ca) == string(cb), nil
}

// noteBindingConflicts raises or lowers config_binding_conflict and warns
// once per key per boot (05-config §1.3.5, §1.5).
func (s *Store) noteBindingConflicts(conflicts map[Path]string) {
	if len(conflicts) == 0 {
		delete(s.health, healthBindingConflict)
		return
	}
	s.health[healthBindingConflict] = fmt.Sprintf("%d keys set by a canonical env name and a binding that disagree", len(conflicts))
	for _, p := range slices.Sorted(maps.Keys(conflicts)) {
		if s.conflictLogged[p] {
			continue
		}
		s.conflictLogged[p] = true
		s.log.Warn("config: a canonical env name and an ecosystem binding set one key to different values; the canonical name wins",
			"key", p, "detail", conflicts[p])
	}
}

// checkEnvNames refuses a section whose keys' canonical env names collide
// with each other, with a registered key's, or with a binding, and a binding
// that equals a canonical name (05-config §1.3.1, §1.3.6: ErrDuplicate).
// names are the section's bindings.
func (s *Store) checkEnvNames(sec *section, names map[string]Path) error {
	mine := map[string]Path{}
	if !sec.isMap {
		for _, k := range sec.keys {
			p := k.path("")
			n := envName(s.prefix, p)
			if o, dup := mine[n]; dup {
				return fmt.Errorf("%w: %s and %s share the env name %s", ErrDuplicate, o, p, n)
			}
			if o, dup := s.canonNames[n]; dup {
				return fmt.Errorf("%w: %s and %s share the env name %s", ErrDuplicate, o, p, n)
			}
			if o, dup := s.envNames[n]; dup {
				return fmt.Errorf("%w: the env name %s of %s is bound to %s", ErrDuplicate, n, p, o)
			}
			if o, dup := s.appBindings[n]; dup {
				return fmt.Errorf("%w: the env name %s of %s is the app's binding of %s", ErrDuplicate, n, p, o)
			}
			mine[n] = p
		}
	}
	for n, p := range names {
		if o, dup := mine[n]; dup {
			return fmt.Errorf("%w: env name %s is bound to %s and is the canonical name of %s", ErrDuplicate, n, p, o)
		}
		if o, dup := s.canonNames[n]; dup {
			return fmt.Errorf("%w: env name %s is bound to %s and is the canonical name of %s", ErrDuplicate, n, p, o)
		}
		if o, dup := s.appBindings[n]; dup {
			return fmt.Errorf("%w: env name %s is bound to %s and is the app's binding of %s", ErrDuplicate, n, p, o)
		}
	}
	if err := s.checkFileFormClash(mine, names); err != nil {
		return err
	}
	maps.Copy(s.canonNames, mine)
	return nil
}

// checkFileFormClash refuses a name that is another name's <NAME>_FILE form:
// key A's canonical name equal to key B's canonical name plus _FILE would
// make one variable both A's value and the path of B's value. Every
// registered name counts: canonical names, section bindings and the app's
// bindings, the new section's included.
func (s *Store) checkFileFormClash(mine, names map[string]Path) error {
	all := map[string]Path{}
	for _, m := range []map[string]Path{s.canonNames, s.envNames, s.appBindings, mine, names} {
		maps.Copy(all, m)
	}
	for _, m := range []map[string]Path{mine, names} {
		for _, n := range slices.Sorted(maps.Keys(m)) {
			if o, clash := all[n+fileSuffix]; clash {
				return fmt.Errorf("%w: env name %s of %s is the %s form of %s's env name %s", ErrDuplicate, n+fileSuffix, o, fileSuffix, m[n], n)
			}
			if base, ok := strings.CutSuffix(n, fileSuffix); ok {
				if o, clash := all[base]; clash {
					return fmt.Errorf("%w: env name %s of %s is the %s form of %s's env name %s", ErrDuplicate, n, m[n], fileSuffix, o, base)
				}
			}
		}
	}
	return nil
}
