// file: core/config/keys.go
// version: 1.0.5
// guid: a7f1dcb2-a279-4c3a-b299-94cbbb319e4d
// last-edited: 2026-11-10

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/falkcorp/mediacore/secrets"
)

// The key model (05-config §1.3.1, F2-09's seam "Leaf path convention"): a
// struct field with a json name is a segment; a struct recurses; every other
// type, a list and a map included, is one key whose value is the whole JSON
// value. A struct that encodes itself (time.Time, secrets.Ref) is one key too.
// The one exception is a map-typed SECTION root (core.modules): its map keys
// are data, and each entry's fields are keys under "<section>.<id>.", so an
// operator value for one module locks that module's key only.

// keyInfo is one leaf key of a section. For a map section it is a template:
// rel is relative to the entry, and the concrete path is
// "<section>.<id>.<rel>".
type keyInfo struct {
	sec       *section
	rel       []string     // JSON names below the section root, or below "<id>" in a map section
	index     []int        // field index path from the section type, or from the map's element type
	typ       reflect.Type // the key's Go type
	reload    Reload
	protect   Protection
	secret    bool // the field is a secrets.Ref
	sensitive bool
	schema    *jsonschema.Schema   // the key's sub-schema, inside sec.schema
	schemaRaw json.RawMessage      // frozen at Register
	resolved  *jsonschema.Resolved // for validating a value of this key
	def       json.RawMessage      // canonical default (struct sections); the element zero value's (map templates)
	failOrder []json.RawMessage    // FailClosed order, most restrictive first; nil when not FailClosed
}

// path is the key's path; id is the map entry's ID (ignored for a struct
// section).
func (k *keyInfo) path(id string) Path {
	p := k.sec.path
	if k.sec.isMap {
		p = p.Join(id)
	}
	for _, seg := range k.rel {
		p = p.Join(seg)
	}
	return p
}

// relName is rel joined with dots: the key's name inside its section (or
// entry).
func (k *keyInfo) relName() string { return strings.Join(k.rel, ".") }

// tagSet is the inherited field metadata of 05-config §1.3.1's tag table. A
// tag on a struct-typed field applies to every key beneath it; a key's own tag
// can only make it stricter (a later reload class, a higher protection).
type tagSet struct {
	reload    Reload
	protect   Protection
	sensitive bool
}

// refType is secrets.Ref's reflect type (a function: core holds no
// package-level variables, R5).
func refType() reflect.Type { return reflect.TypeFor[secrets.Ref]() }

// encodesItself reports whether t (or *t) implements json.Marshaler or
// json.Unmarshaler: such a struct is one key, never walked.
func encodesItself(t reflect.Type) bool {
	m := reflect.TypeFor[json.Marshaler]()
	u := reflect.TypeFor[json.Unmarshaler]()
	return t.Implements(m) || t.Implements(u) || reflect.PointerTo(t).Implements(m) || reflect.PointerTo(t).Implements(u)
}

// isLeaf reports whether a field of type t is one key.
func isLeaf(t reflect.Type) bool {
	return t.Kind() != reflect.Struct || encodesItself(t)
}

// containsRef reports whether t holds a secrets.Ref anywhere beneath it.
func containsRef(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == refType() {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return containsRef(t.Elem(), seen)
	case reflect.Map:
		return containsRef(t.Key(), seen) || containsRef(t.Elem(), seen)
	case reflect.Struct:
		for field := range t.Fields() {
			if containsRef(field.Type, seen) {
				return true
			}
		}
	}
	return false
}

// parseTags reads f's reload, protect and sensitive tags over inherited.
func parseTags(where string, f reflect.StructField, inh tagSet) (tagSet, error) {
	out := inh
	if v, ok := f.Tag.Lookup("reload"); ok {
		r := ReloadLive
		switch v {
		case "restart":
			r = ReloadRestart
		case "boot":
			r = ReloadBoot
		default:
			return out, fmt.Errorf("%w: %s: reload:%q (want restart or boot)", ErrInvalid, where, v)
		}
		out.reload = max(out.reload, r)
	}
	if v, ok := f.Tag.Lookup("protect"); ok {
		var p Protection
		switch v {
		case "interactive":
			p = ProtectInteractive
		case "admin":
			p = ProtectAdmin
		case "owner":
			p = ProtectOwner
		case "operator":
			p = ProtectOperator
		default:
			return out, fmt.Errorf("%w: %s: protect:%q (want interactive, admin, owner or operator)", ErrInvalid, where, v)
		}
		out.protect = max(out.protect, p)
	}
	if v, ok := f.Tag.Lookup("sensitive"); ok {
		switch v {
		case "true":
			out.sensitive = true
		case "false":
		default:
			return out, fmt.Errorf("%w: %s: sensitive:%q (want true or false)", ErrInvalid, where, v)
		}
	}
	return out, nil
}

// keyWalker collects a section's keys from its Go type.
type keyWalker struct {
	sec  *section
	keys []*keyInfo
}

// walk adds the keys of struct type t found below rel. names holds the JSON
// names already declared in the same JSON object, mapped to the field that
// declared them: an embedded struct's fields are promoted into its parent's
// object, so they share the parent's names.
//
// A JSON name declared twice in one object is refused with ErrDuplicate.
// encoding/json resolves such a clash silently (the shallower field wins, or
// at equal depth neither is decoded), while byRel would keep one keyInfo for
// both. A non-admin could then write an owner-tagged field through its
// untagged twin.
func (w *keyWalker) walk(t reflect.Type, rel []string, idx []int, inh tagSet, names map[string]string) error {
	for i := range t.NumField() {
		f := t.Field(i)
		where := fmt.Sprintf("%s field %s.%s", w.sec.path, t.Name(), f.Name)
		jsonTag, _ := f.Tag.Lookup("json")
		name, _, hasOpts := strings.Cut(jsonTag, ",")
		if name == "-" && !hasOpts {
			continue
		}
		ix := append(slices.Clone(idx), i)
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct {
			// encoding/json promotes the fields of an embedded *struct when
			// it is set and cannot allocate one of an unexported type, so
			// its keys would depend on a pointer config never sees.
			return fmt.Errorf("%w: %s embeds a pointer to a struct: embed the struct by value or give the field a json name (05-config §1.3.1)", ErrInvalid, where)
		}
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct && !encodesItself(f.Type) {
			// encoding/json promotes an embedded struct's fields into the
			// parent; the keys follow it, in the parent's JSON object.
			tags, err := parseTags(where, f, inh)
			if err != nil {
				return err
			}
			if err := w.walk(f.Type, rel, ix, tags, names); err != nil {
				return err
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			return fmt.Errorf("%w: %s has no json name: every key of a section is named by its json tag (05-config §1.3.1)", ErrInvalid, where)
		}
		if !validSegment(name) {
			return fmt.Errorf("%w: %s: json name %q does not match [a-z][a-z0-9_]{0,47}", ErrInvalid, where, name)
		}
		if prev, dup := names[name]; dup {
			return fmt.Errorf("%w: %s and %s both have the json name %q in one JSON object: encoding/json would decode at most one of them, so one key would stand for both (05-config §1.3.1)",
				ErrDuplicate, prev, where, name)
		}
		names[name] = where
		tags, err := parseTags(where, f, inh)
		if err != nil {
			return err
		}
		r := append(slices.Clone(rel), name)
		if !isLeaf(f.Type) {
			if err := w.walk(f.Type, r, ix, tags, map[string]string{}); err != nil {
				return err
			}
			continue
		}
		if err := w.add(where, f.Type, r, ix, tags); err != nil {
			return err
		}
	}
	return nil
}

// add records one leaf key.
func (w *keyWalker) add(where string, t reflect.Type, rel []string, idx []int, tags tagSet) error {
	k := &keyInfo{sec: w.sec, rel: rel, index: idx, typ: t, reload: tags.reload, protect: tags.protect, sensitive: tags.sensitive}
	if k.reload == 0 {
		k.reload = ReloadLive
	}
	if k.protect == 0 {
		k.protect = ProtectNone
	}
	k.secret = t == refType()
	switch {
	case !k.secret && containsRef(t, map[reflect.Type]bool{}):
		return fmt.Errorf("%w: %s holds a secrets.Ref inside a list, map or pointer: a secret must be a field of its own (F3-03 owns secret lists)", ErrInvalid, where)
	case k.secret && k.reload == ReloadBoot:
		return fmt.Errorf("%w: %s: a secrets.Ref is never reload:\"boot\" (the boot record holds no secret)", ErrInvalid, where)
	case k.secret && w.sec.isMap:
		return fmt.Errorf("%w: %s: a secrets.Ref inside a map-typed section is not supported", ErrInvalid, where)
	case k.reload == ReloadBoot && !w.sec.core():
		return fmt.Errorf("%w: %s: reload:\"boot\" is for core sections only: Boot never sees a module section (05-config §1.5)", ErrInvalid, where)
	}
	shown := k.path("")
	if w.sec.isMap {
		shown = k.path("<id>")
	}
	if err := checkNested(where, shown, t, tagSet{reload: k.reload, protect: k.protect, sensitive: k.sensitive}, map[reflect.Type]bool{}); err != nil {
		return err
	}
	w.keys = append(w.keys, k)
	return nil
}

// checkNested refuses a tag inside one key's value that is stricter than the
// key. The walker makes one key of a list, a map, an array, a pointer and a
// struct that encodes itself, and config authorizes, reloads and redacts per
// key, so a protect, reload or sensitive tag on a field beneath such a key
// would be silently ignored: an element field tagged protect:"owner" inside a
// list tagged protect:"admin" would be writable by any interactive admin.
// 05-config §1.3.1 lets a nested tag only make a key stricter and says
// nothing of tags inside a key's value, so Register fails closed with
// ErrInvalid and the declarer tags the key itself (F3-01's choice, recorded
// in seams.d/F3-01.md). It walks element types, map values and every field of
// a struct, the self-encoding ones included, once per type. A malformed tag
// there fails as it would on a key.
func checkNested(where string, key Path, t reflect.Type, own tagSet, seen map[reflect.Type]bool) error {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
			continue
		}
		break
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return nil
	}
	seen[t] = true
	for f := range t.Fields() {
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		if name, _, hasOpts := strings.Cut(f.Tag.Get("json"), ","); name == "-" && !hasOpts {
			continue
		}
		fw := fmt.Sprintf("%s (inside key %s) field %s.%s", where, key, t.Name(), f.Name)
		tags, err := parseTags(fw, f, tagSet{})
		if err != nil {
			return err
		}
		switch {
		case tags.protect > own.protect:
			return fmt.Errorf("%w: %s carries protect:%q, stricter than its key's %s: config protects whole keys, so the key itself must carry it (05-config §1.3.1)",
				ErrInvalid, fw, tags.protect, own.protect)
		case tags.reload > own.reload:
			return fmt.Errorf("%w: %s carries reload:%q, later than its key's %s: config reloads whole keys, so the key itself must carry it (05-config §1.3.1)",
				ErrInvalid, fw, tags.reload, own.reload)
		case tags.sensitive && !own.sensitive:
			return fmt.Errorf("%w: %s carries sensitive:\"true\" and its key does not: config redacts whole keys, so the key itself must carry it (05-config §1.3.1)",
				ErrInvalid, fw)
		}
		if err := checkNested(where, key, f.Type, own, seen); err != nil {
			return err
		}
	}
	return nil
}

// strictDecode decodes raw into ptr with unknown fields refused and nothing
// after the one value.
func strictDecode(raw []byte, ptr any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(ptr); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the value")
	}
	return nil
}

// secretValue is a secret key's value inside its section's JSON: the
// reference, never the secret.
// It is spelled out, not marshalled: a valid Path holds only [a-z0-9_.], so
// the bytes are exactly what secrets.Ref.MarshalJSON writes.
func secretValue(p Path) json.RawMessage {
	return json.RawMessage(`{"secret":"` + string(p) + `"}`)
}

// canonical checks raw against k's schema and type and returns its canonical
// JSON (decoded into the key's Go type and encoded again), so two spellings of
// one value compare equal.
//
// A sensitive key's error never carries the value: the schema validator echoes
// the rejected value, and these errors reach logs and the quarantine report
// (05-config §1.3.1: sensitive values are redacted in logs).
func (k *keyInfo) canonical(p Path, raw json.RawMessage) (json.RawMessage, error) {
	out, err := k.canonicalOf(p, raw)
	if err != nil && k.sensitive {
		return nil, &FieldError{Path: p, Err: errSensitiveRedacted()}
	}
	return out, err
}

// errSensitiveRedacted is the message of a sensitive key's error: the
// validator's own text may quote the rejected value.
func errSensitiveRedacted() error {
	return errors.New("the value fails the key's schema, type or Validator (detail redacted: sensitive key)")
}

// canonicalOf is canonical without the redaction.
func (k *keyInfo) canonicalOf(p Path, raw json.RawMessage) (json.RawMessage, error) {
	if k.secret {
		return secretValue(p), nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, &FieldError{Path: p, Err: errors.New("empty value")}
	}
	var inst any
	if err := json.Unmarshal(raw, &inst); err != nil {
		return nil, &FieldError{Path: p, Err: fmt.Errorf("not JSON: %w", err)}
	}
	if err := k.resolved.Validate(inst); err != nil {
		return nil, &FieldError{Path: p, Err: err}
	}
	v := reflect.New(k.typ)
	if err := strictDecode(raw, v.Interface()); err != nil {
		return nil, &FieldError{Path: p, Err: err}
	}
	out, err := json.Marshal(v.Interface())
	if err != nil {
		return nil, &FieldError{Path: p, Err: err}
	}
	return out, nil
}

// subJSON returns the value at rel inside the JSON object raw.
func subJSON(raw json.RawMessage, rel []string) (json.RawMessage, bool) {
	cur := raw
	for _, seg := range rel {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(cur, &m); err != nil {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// setTree sets value at rel inside tree, creating objects on the way.
func setTree(tree map[string]any, rel []string, value json.RawMessage) {
	for _, seg := range rel[:len(rel)-1] {
		next, ok := tree[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			tree[seg] = next
		}
		tree = next
	}
	tree[rel[len(rel)-1]] = value
}

// subSchema navigates s to the property at rel.
func subSchema(s *jsonschema.Schema, rel []string) *jsonschema.Schema {
	for _, seg := range rel {
		if s == nil {
			return nil
		}
		s = s.Properties[seg]
	}
	return s
}

// schemaAllows lists the values a bool or enum key allows, as canonical JSON;
// ok is false for any other key.
func (k *keyInfo) schemaAllows() ([]json.RawMessage, bool) {
	if k.typ.Kind() == reflect.Bool {
		return []json.RawMessage{json.RawMessage("true"), json.RawMessage("false")}, true
	}
	if k.schema == nil || len(k.schema.Enum) == 0 {
		return nil, false
	}
	out := make([]json.RawMessage, 0, len(k.schema.Enum))
	for _, e := range k.schema.Enum {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, false
		}
		out = append(out, b)
	}
	return out, true
}
