// file: core/config/layers.go
// version: 1.0.5
// guid: 8f38706d-6653-47f3-8d73-4f397c9b3604
// last-edited: 2026-11-10

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/secrets"
)

// The operator layers (05-config §1.3.5): file, env and flags, lowest to
// highest. This row parses the flags (Options.Flags) and the boot record.
// The operator file and the environment are sources behind the seam below,
// which F3-02 fills with the canonical file form, the canonical env names,
// <NAME>_FILE, empty-is-unset, the ecosystem bindings and strict decoding of
// their values. Until then a store has no env layer, and a store whose
// Options.FilePath is set refuses to boot rather than ignore the file.

// sourced is one value an operator layer supplies.
type sourced struct {
	path Path
	raw  json.RawMessage // JSON
	by   string          // "file <name>", "env ABO_CORE_LOG_LEVEL"
}

// sourceQuery tells a source which paths the store can place.
type sourceQuery struct {
	paths       []Path          // every concrete key path of the registered struct sections
	maps        []Path          // map-typed section roots: their entries are data ("core.modules")
	passthrough func(Path) bool // Options.Passthrough; never nil
	// entrySecret reports whether p is a list entry's secret key
	// ("core.auth.oidc.<id>.client_secret" for any valid, unreserved ID): a
	// source places it like a key of paths. Never nil.
	entrySecret func(Path) bool
	// want, when non-nil, asks for these paths only (Store.Operator). A
	// source returns a wanted path even when no registered section owns it
	// yet, raw (an env value as a JSON string): the kernel's step 0 reads
	// core.paths.state_dir before core.paths registers (05-config §1.7).
	want map[Path]bool
}

// operatorSource is one operator layer parsed outside this file.
type operatorSource interface {
	// read returns every value the layer supplies for q, by canonical path.
	// A path the store cannot place is the source's to report (F3-02: warn
	// once); returning it here makes the store refuse it.
	read(q sourceQuery) ([]sourced, error)
}

// fileSource is the operator file: a layer that can also report and redact
// the values it holds (05-config §1.3.3 ConfigFileHasSecrets).
type fileSource interface {
	operatorSource
	// holds reports whether the file supplies a value at a path for which
	// drop is true.
	holds(q sourceQuery, drop func(Path) bool) (bool, error)
	// redacted renders the file without the values at paths for which drop
	// is true.
	redacted(q sourceQuery, drop func(Path) bool) ([]byte, error)
}

// unbuiltFile is the file layer until F3-02: a configured file is refused,
// never silently ignored.
type unbuiltFile struct{ path string }

func (u unbuiltFile) err() error {
	return fmt.Errorf("config: operator file %q: this build reads no operator file yet (the file grammar is F3-02's); remove Options.FilePath", u.path)
}

func (u unbuiltFile) read(sourceQuery) ([]sourced, error)                   { return nil, u.err() }
func (u unbuiltFile) holds(sourceQuery, func(Path) bool) (bool, error)      { return false, u.err() }
func (u unbuiltFile) redacted(sourceQuery, func(Path) bool) ([]byte, error) { return nil, u.err() }

// opValue is an owned key's operator value: the winner of flag > env > file.
type opValue struct {
	raw json.RawMessage // canonical
	src Source
	by  string
}

// ckey is one concrete key: a key of a struct section, or a map template
// with its entry ID.
type ckey struct {
	k  *keyInfo
	id string
	p  Path
}

// def is the key's default: for a map entry, the declared default entry's
// field when the default map has the ID, else the element zero value's.
func (c ckey) def() json.RawMessage {
	if c.k.secret {
		return secretValue(c.p)
	}
	if !c.k.sec.isMap {
		return c.k.def
	}
	raw, ok := subJSON(c.k.sec.defaults, append([]string{c.id}, c.k.rel...))
	if !ok {
		return c.k.def
	}
	v, err := c.k.canonical(c.p, raw)
	if err != nil {
		return c.k.def
	}
	return v
}

// sectionOf returns the registered section that owns p: the longest section
// path p equals or extends.
func (s *Store) sectionOf(p Path) *section {
	var best *section
	for _, sec := range s.sections {
		if under(p, sec.path) && (best == nil || len(sec.path) > len(best.path)) {
			best = sec
		}
	}
	return best
}

// keyFor resolves p to a concrete registered key.
func (s *Store) keyFor(p Path) (ckey, bool) {
	sec := s.sectionOf(p)
	if sec == nil {
		return ckey{}, false
	}
	if !sec.isMap {
		k := sec.keyOf(p)
		if k == nil {
			return sec.derivedKey(p)
		}
		return ckey{k: k, p: p}, true
	}
	rest, ok := strings.CutPrefix(string(p), string(sec.path)+pathSep)
	if !ok {
		return ckey{}, false
	}
	id, rel, _ := strings.Cut(rest, pathSep)
	if !validSegment(id) {
		return ckey{}, false
	}
	k := sec.byRel[rel]
	if k == nil {
		return ckey{}, false
	}
	return ckey{k: k, id: id, p: p}, true
}

// passthrough reports whether p is a passthrough path.
func (s *Store) passthrough(p Path) bool {
	return s.o.Passthrough != nil && s.o.Passthrough(p)
}

// query describes the registered sections to a source.
func (s *Store) query() sourceQuery {
	q := sourceQuery{passthrough: s.passthrough, entrySecret: func(p Path) bool {
		c, ok := s.keyFor(p)
		return ok && c.k.list != nil
	}}
	for _, sec := range s.sections {
		if sec.isMap {
			q.maps = append(q.maps, sec.path)
			continue
		}
		for _, k := range sec.keys {
			q.paths = append(q.paths, k.path(""))
		}
	}
	return q
}

// flagJSON converts an explicitly set flag's text into JSON for key k:
// strict, as 05-config §1.3.6 decodes every operator value (true/false for a
// bool, a Go duration, a number, JSON for an object or array, the text itself
// for a string). A sensitive key's error never quotes the text: strconv and
// time echo their input.
func flagJSON(k *keyInfo, p Path, text string) (json.RawMessage, error) {
	out, err := flagJSONOf(k, p, text)
	if err != nil && k != nil && k.redacts() {
		return nil, &FieldError{Path: p, Err: errSensitiveRedacted()}
	}
	return out, err
}

// flagJSONOf is flagJSON without the redaction.
func flagJSONOf(k *keyInfo, p Path, text string) (json.RawMessage, error) {
	if k == nil {
		return json.Marshal(text) // a passthrough path: config does not own its type
	}
	if k.secret {
		return nil, &FieldError{Path: p, Err: errors.New("a secret is never set by a flag (it would show in the process list); use the environment or the file")}
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
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return nil, &FieldError{Path: p, Err: fmt.Errorf("not an integer: %w", err)}
		}
		return json.RawMessage(text), nil
	case t.Kind() >= reflect.Uint && t.Kind() <= reflect.Uint64:
		if _, err := strconv.ParseUint(text, 10, 64); err != nil {
			return nil, &FieldError{Path: p, Err: fmt.Errorf("not an unsigned integer: %w", err)}
		}
		return json.RawMessage(text), nil
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return nil, &FieldError{Path: p, Err: fmt.Errorf("not a number: %w", err)}
		}
		return json.RawMessage(text), nil
	default:
		if !json.Valid([]byte(text)) {
			return nil, &FieldError{Path: p, Err: errors.New("an object or array key takes JSON")}
		}
		return json.RawMessage(text), nil
	}
}

// flagBy is a flag's lock reason.
func flagBy(p Path) string { return "flag " + string(p) }

// layerRead is one read of the operator layers.
type layerRead struct {
	op          map[Path]opValue
	passthrough map[Path]OperatorValue
	fileSet     map[Path]bool // every path the file supplied, before env and flags override it
}

// readOperator reads file, env and flags for the registered sections.
// strict (Load) refuses a flag whose path nothing registered owns; Boot skips
// it (a module key, registered at P3).
func (s *Store) readOperator(strict bool) (layerRead, error) {
	lr := layerRead{op: map[Path]opValue{}, passthrough: map[Path]OperatorValue{}, fileSet: map[Path]bool{}}
	q := s.query()
	layers := []struct {
		src Source
		os  operatorSource
	}{{SourceFile, s.file}, {SourceEnv, s.env}}
	for _, l := range layers {
		if l.os == nil {
			continue
		}
		vals, err := l.os.read(q)
		if err != nil {
			return lr, err
		}
		for _, v := range vals {
			if err := s.placeOperator(lr, l.src, v); err != nil {
				return lr, err
			}
		}
	}
	for _, p := range slices.Sorted(maps.Keys(s.o.Flags)) {
		ck, ok := s.keyFor(p)
		var k *keyInfo
		switch {
		case ok:
			k = ck.k
		case s.passthrough(p):
		case !strict:
			continue
		default:
			return lr, fmt.Errorf("%w: %s: %s names no registered key", ErrUnknownKey, flagBy(p), p)
		}
		raw, err := flagJSON(k, p, s.o.Flags[p])
		if err != nil {
			return lr, fmt.Errorf("config: %s: %w", flagBy(p), invalid(err))
		}
		if err := s.placeOperator(lr, SourceFlag, sourced{path: p, raw: raw, by: flagBy(p)}); err != nil {
			return lr, err
		}
	}
	return lr, nil
}

// placeOperator records one operator value: a passthrough value raw, an owned
// key's canonical; a later (higher) layer overwrites an earlier one.
func (s *Store) placeOperator(lr layerRead, src Source, v sourced) error {
	if src == SourceFile {
		lr.fileSet[v.path] = true
	}
	if ck, ok := s.keyFor(v.path); ok {
		c, err := ck.k.canonical(v.path, v.raw)
		if err != nil {
			return fmt.Errorf("config: %s: %w", v.by, invalid(err))
		}
		lr.op[v.path] = opValue{raw: c, src: src, by: v.by}
		return nil
	}
	if s.passthrough(v.path) {
		lr.passthrough[v.path] = OperatorValue{Value: append(json.RawMessage(nil), v.raw...), Src: src, By: v.by}
		return nil
	}
	if src == SourceFlag {
		return nil // skipped at Boot; Load refused it in readOperator
	}
	return fmt.Errorf("%w: %s supplies %s, which no registered section owns", ErrUnknownKey, v.by, v.path)
}

// view is the stored layers one build reads (05-config §1.4): the boot
// record for boot keys, DB values for live and for restart keys. The
// effective view reads restart keys as of Attach and boot keys as of Load;
// the pending view reads the current stored values for both.
type view struct {
	boot    map[Path]json.RawMessage
	live    map[Path]json.RawMessage
	restart map[Path]json.RawMessage
}

// keyVal is one key's resolved value in a build.
type keyVal struct {
	raw json.RawMessage
	src Source
	by  string
}

// resolve merges one key: operator layers (flag > env > file) lock it; then
// the boot record (boot keys) or the DB layer (other keys) over the default.
// ProtectOperator keys read neither stored layer (05-config §1.3.5).
func (s *Store) resolve(c ckey, v view) keyVal {
	if o, ok := s.op[c.p]; ok {
		return keyVal(o)
	}
	k := c.k
	switch {
	case k.secret:
		return keyVal{raw: secretValue(c.p), src: s.secretSource(c.p)}
	case k.protect == ProtectOperator:
	case k.reload == ReloadBoot:
		if r, ok := v.boot[c.p]; ok {
			return keyVal{raw: r, src: SourceBootRecord}
		}
	case k.reload == ReloadRestart:
		if r, ok := v.restart[c.p]; ok {
			return keyVal{raw: r, src: SourceOverride}
		}
	default:
		if r, ok := v.live[c.p]; ok {
			return keyVal{raw: r, src: SourceOverride}
		}
	}
	return keyVal{raw: c.def(), src: SourceDefault}
}

// secretSource is a secret key's Source when no operator layer supplies it:
// SourceOverride when the secrets store holds it, else SourceDefault.
func (s *Store) secretSource(p Path) Source {
	m := s.secretMeta[p]
	switch {
	case m == nil || !m.Set:
		return SourceDefault
	case m.Source == secrets.SourceEnv:
		return SourceEnv
	}
	return SourceOverride
}

// entryIDs lists a map section's entries: the declared default's IDs and
// every ID any layer of v (and the operator layers) holds.
func (s *Store) entryIDs(sec *section, v view) []string {
	ids := map[string]bool{}
	var def map[string]json.RawMessage
	if err := json.Unmarshal(sec.defaults, &def); err == nil {
		for id := range def {
			ids[id] = true
		}
	}
	add := func(p Path) {
		if rest, ok := strings.CutPrefix(string(p), string(sec.path)+pathSep); ok {
			id, _, _ := strings.Cut(rest, pathSep)
			if validSegment(id) {
				ids[id] = true
			}
		}
	}
	for p := range s.op {
		add(p)
	}
	for _, m := range []map[Path]json.RawMessage{v.boot, v.live, v.restart} {
		for p := range m {
			add(p)
		}
	}
	return slices.Sorted(maps.Keys(ids))
}

// concrete lists a section's concrete keys under v.
func (s *Store) concrete(sec *section, v view) []ckey {
	if !sec.isMap {
		out := make([]ckey, 0, len(sec.keys))
		for _, k := range sec.keys {
			out = append(out, ckey{k: k, p: k.path("")})
		}
		return out
	}
	ids := s.entryIDs(sec, v)
	out := make([]ckey, 0, len(ids)*len(sec.keys))
	for _, id := range ids {
		for _, k := range sec.keys {
			out = append(out, ckey{k: k, id: id, p: k.path(id)})
		}
	}
	return out
}

// built is one section built under a view.
type built struct {
	val  any
	raw  json.RawMessage
	ids  []string
	keys []ckey
	vals map[Path]keyVal
}

// holds reports whether the build has the concrete key p.
func (b *built) holds(p Path) bool {
	_, ok := b.vals[p]
	return ok
}

// build resolves every key of sec under v, assembles the section and
// decodes it (schema, strict decode, Validator). Errors are *FieldError.
func (s *Store) build(sec *section, v view) (*built, error) {
	b := &built{keys: s.concrete(sec, v), vals: map[Path]keyVal{}}
	raws := make(map[Path]json.RawMessage, len(b.keys))
	for _, c := range b.keys {
		kv := s.resolve(c, v)
		b.vals[c.p] = kv
		raws[c.p] = kv.raw
	}
	if sec.isMap {
		b.ids = s.entryIDs(sec, v)
	}
	raw, err := sec.assemble(raws, b.ids)
	if err != nil {
		return nil, &FieldError{Path: sec.path, Err: err}
	}
	val, err := sec.decode(raw, s.resolver)
	if err != nil {
		return nil, err
	}
	b.raw, b.val = raw, val
	// One derived key per entry secret of every entry the build holds: they
	// are listed (Lookup, Keys) and writable only while an entry names them.
	for _, lk := range sec.entryLists {
		for _, id := range lk.entryIDsOf(raws[lk.path("")]) {
			for _, d := range lk.entrySecrets {
				c := ckey{k: d, id: id, p: d.path(id)}
				b.keys = append(b.keys, c)
				b.vals[c.p] = s.resolve(c, v)
			}
		}
	}
	return b, nil
}

// operatorSources names the operator layers that supply keys of sec, for a
// boot error (05-config §1.5: the message names the variable or file key).
func (s *Store) operatorSources(sec *section) string {
	var bys []string
	for p, o := range s.op {
		if s.sectionOf(p) == sec {
			bys = append(bys, o.by)
		}
	}
	if len(bys) == 0 {
		return "no operator value: the declared defaults"
	}
	slices.Sort(bys)
	return "operator values: " + strings.Join(bys, ", ")
}

// withoutSection returns a copy of m without sec's paths.
func withoutSection(m map[Path]json.RawMessage, sec *section, s *Store) map[Path]json.RawMessage {
	out := make(map[Path]json.RawMessage, len(m))
	for p, v := range m {
		if s.sectionOf(p) != sec {
			out[p] = v
		}
	}
	return out
}
