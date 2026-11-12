// file: core/config/registry.go
// version: 1.0.8
// guid: 74ce6316-8927-42d8-ac4f-38c9f6a04ebe
// last-edited: 2026-11-12

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/falkcorp/mediacore/internal/jsonschemax"
	"github.com/falkcorp/mediacore/secrets"
)

// Registry is used by the kernel to register sections listed in
// Manifest.Settings (06 §6.4). It has unexported methods, so only the kernel
// (through Store.Registry) can implement it. Registration is legal only
// before Store.Load.
type Registry interface {
	register(spec *sectionSpec) (*binding, error)
}

// Register binds a section. It is a generic free function because interface
// methods cannot have type parameters.
func Register[T any](r Registry, s Section[T]) (*Handle[T], error) {
	if r == nil {
		return nil, errors.New("config: Register with a nil Registry")
	}
	sp := s.spec()
	sp.typ = reflect.TypeFor[T]()
	b, err := r.register(sp)
	if err != nil {
		return nil, err
	}
	return &Handle[T]{b: b}, nil
}

// registrar is a Section[T] seen without its type parameter.
type registrar interface {
	registerWith(r Registry) (any, error)
}

// registerWith registers s and returns its *Handle[T] as an any.
func (s Section[T]) registerWith(r Registry) (any, error) { return Register(r, s) }

// RegisterSections registers every Section[T] value of sections, in order,
// and returns their handles (each a *Handle[T], in the same order). Name
// coined by F3-01: the kernel's core-section list (kernel.coreSections) is a
// []any of config.Define values, and Register needs T. Any element that is not
// a Section fails with ErrInvalid, before or after the ones registered.
func RegisterSections(r Registry, sections ...any) ([]any, error) {
	out := make([]any, 0, len(sections))
	for i, s := range sections {
		rs, ok := s.(registrar)
		if !ok {
			return out, fmt.Errorf("%w: RegisterSections element %d is a %T, not a config.Section", ErrInvalid, i, s)
		}
		h, err := rs.registerWith(r)
		if err != nil {
			return out, err
		}
		out = append(out, h)
	}
	return out, nil
}

// section is one registered section.
type section struct {
	path      Path
	spec      *sectionSpec
	isMap     bool                // a map-typed section root: keys are "<path>.<id>.<rel>"
	keys      []*keyInfo          // the fixed keys, or the map templates, in declaration order
	byRel     map[string]*keyInfo // relName -> key
	permRead  string
	permWrite string
	schema    *jsonschema.Schema
	defaults  json.RawMessage // the declared defaults as JSON
	elemZero  json.RawMessage // map sections: the element type's zero value as JSON
	bind      *binding

	// The last published builds, under the store mutex: eff is what Get
	// reads, pend what Pending reads; the key index is made from them.
	eff, pend *built

	// entryLists are the list keys whose entries hold secrets (keys.go,
	// "Entry secrets"): their derived keys are not in keys, because they are
	// no part of the section's JSON and exist once per entry.
	entryLists []*keyInfo
}

// core reports whether the section is a core section.
func (s *section) core() bool { return s.path == "core" || strings.HasPrefix(string(s.path), "core.") }

// claims are the paths the section owns: its keys, or for a map section its
// whole subtree (the section path).
func (s *section) claims() []Path {
	if s.isMap {
		return []Path{s.path}
	}
	out := make([]Path, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k.path(""))
	}
	return out
}

// overlaps reports whether a and b are equal or one extends the other.
func overlaps(a, b Path) bool {
	return a == b || strings.HasPrefix(string(a), string(b)+pathSep) || strings.HasPrefix(string(b), string(a)+pathSep)
}

// under reports whether p equals or extends q.
func under(p, q Path) bool { return p == q || strings.HasPrefix(string(p), string(q)+pathSep) }

// defaultPerms is 05-config §1.3.4's default section permission.
func defaultPerms(p Path) string {
	segs := strings.Split(string(p), pathSep)
	if segs[0] == "modules" && len(segs) > 1 {
		return segs[1] + ":settings.manage"
	}
	return "core:settings.manage"
}

// buildSection reflects sp into a section: keys, schema, defaults, and the
// checks of 05-config §1.3.1 and §1.5 that need only the section itself.
func buildSection(sp *sectionSpec) (*section, error) {
	if err := sp.path.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if sp.typ == nil {
		return nil, fmt.Errorf("%w: section %s was not built by Register", ErrInvalid, sp.path)
	}
	sec := &section{path: sp.path, spec: sp, byRel: map[string]*keyInfo{}}
	schema, err := jsonschemax.ForType(sp.typ, storeOverrides())
	if err != nil {
		return nil, fmt.Errorf("%w: section %s: %w", ErrInvalid, sp.path, err)
	}
	sec.schema = schema.CloneSchemas()
	w := &keyWalker{sec: sec}
	t := sp.typ
	switch {
	case t.Kind() == reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: section %s: a map-typed section is keyed by string IDs, not %s", ErrInvalid, sp.path, t.Key())
		}
		sec.isMap = true
		elem := t.Elem()
		zero, err := json.Marshal(reflect.Zero(elem).Interface())
		if err != nil {
			return nil, fmt.Errorf("%w: section %s: %w", ErrInvalid, sp.path, err)
		}
		sec.elemZero = zero
		if isLeaf(elem) {
			err = w.add(sp.path.String()+" entry", elem, nil, nil, tagSet{})
		} else {
			err = w.walk(elem, nil, nil, tagSet{}, map[string]string{})
		}
		if err != nil {
			return nil, err
		}
	case isLeaf(t):
		if err := w.add(sp.path.String(), t, nil, nil, tagSet{}); err != nil {
			return nil, err
		}
	default:
		if err := w.walk(t, nil, nil, tagSet{}, map[string]string{}); err != nil {
			return nil, err
		}
	}
	sec.keys = w.keys
	for _, k := range sec.keys {
		sec.byRel[k.relName()] = k
	}
	if err := sec.reserveEntryIDs(); err != nil {
		return nil, err
	}
	if sec.defaults, err = json.Marshal(sp.defaults); err != nil {
		return nil, fmt.Errorf("%w: section %s defaults: %w", ErrInvalid, sp.path, err)
	}
	if sec.isMap && string(sec.defaults) == "null" {
		sec.defaults = json.RawMessage("{}")
	}
	sec.permRead, sec.permWrite = sp.permRead, sp.permWrite
	if sec.permRead == "" {
		sec.permRead = defaultPerms(sp.path)
	}
	if sec.permWrite == "" {
		sec.permWrite = defaultPerms(sp.path)
	}
	if err := sec.decorate(); err != nil {
		return nil, err
	}
	if err := sec.checkOptions(); err != nil {
		return nil, err
	}
	return sec, nil
}

// reserveEntryIDs collects the entry lists and, for each, the IDs its entries
// may not take: every json name of the list's parent object, the list's own
// included, since "<parent>.<id>.<ref>" must never extend a declared key. Two
// entry lists under one parent would share one ID namespace, so a section
// declaring them is refused.
func (s *section) reserveEntryIDs() error {
	parents := map[Path]Path{}
	for _, k := range s.keys {
		if len(k.entrySecrets) == 0 {
			continue
		}
		parent := k.entryParent()
		if prev, dup := parents[parent]; dup {
			return fmt.Errorf("%w: section %s: the lists %s and %s both hold entry secrets under %s, so their entries' secret names would collide", ErrInvalid, s.path, prev, k.path(""), parent)
		}
		parents[parent] = k.path("")
		k.reservedIDs = map[string]bool{}
		for _, o := range s.keys {
			if rest, ok := strings.CutPrefix(string(o.path("")), string(parent)+pathSep); ok {
				seg, _, _ := strings.Cut(rest, pathSep)
				k.reservedIDs[seg] = true
			}
		}
		s.entryLists = append(s.entryLists, k)
	}
	return nil
}

// derivedKey resolves p to a derived key of one of the section's entry lists,
// for any valid, unreserved entry ID: whether an entry holds that ID is the
// caller's question (Apply refuses a key no entry names).
func (s *section) derivedKey(p Path) (ckey, bool) {
	for _, lk := range s.entryLists {
		rest, ok := strings.CutPrefix(string(p), string(lk.entryParent())+pathSep)
		if !ok {
			continue
		}
		id, name, _ := strings.Cut(rest, pathSep)
		if !validSegment(id) || lk.reservedIDs[id] {
			continue
		}
		for _, d := range lk.entrySecrets {
			if d.rel[0] == name {
				return ckey{k: d, id: id, p: p}, true
			}
		}
	}
	return ckey{}, false
}

// String is the path as a string (for messages).
func (p Path) String() string { return string(p) }

// entrySchema is the schema keys are found under: the section's, or a map
// entry's.
func (s *section) entrySchema() *jsonschema.Schema {
	if s.isMap {
		return s.schema.AdditionalProperties
	}
	return s.schema
}

// decorate finds each key's sub-schema, writes the vendor keywords, its
// default and its description onto it, freezes it and resolves it for
// validation; it also fills and checks each key's default.
func (s *section) decorate() error {
	root := s.entrySchema()
	for _, k := range s.keys {
		sub := root
		if len(k.rel) > 0 {
			sub = subSchema(root, k.rel)
		}
		if sub == nil {
			return fmt.Errorf("%w: section %s: no schema for key %q", ErrInvalid, s.path, k.relName())
		}
		k.schema = sub
		setExtra(sub, "x-mediacore-reload", k.reload.String())
		if k.protect != ProtectNone {
			setExtra(sub, "x-mediacore-protect", k.protect.String())
		}
		if k.sensitive {
			setExtra(sub, "x-mediacore-sensitive", true)
		}
		if k.secret {
			sub.WriteOnly = true
			setExtra(sub, "x-mediacore-secret", true)
		}
		p := k.path("")
		if s.isMap {
			p = s.path.Join("<id>")
			for _, seg := range k.rel {
				p = p.Join(seg)
			}
		}
		if d, ok := s.spec.docs[p]; ok {
			sub.Description = d
		}
	}
	for _, fc := range s.spec.failClosed {
		if k := s.keyOf(fc.path); k != nil {
			setExtra(k.schema, "x-mediacore-fail-closed", true)
		}
	}
	for _, lk := range s.entryLists {
		if err := lk.decorateEntrySecrets(); err != nil {
			return err
		}
	}
	for _, k := range s.keys {
		resolved, err := k.schema.CloneSchemas().Resolve(nil)
		if err != nil {
			return fmt.Errorf("%w: section %s key %q: schema: %w", ErrInvalid, s.path, k.relName(), err)
		}
		k.resolved = resolved
		if err := s.fillDefault(k); err != nil {
			return err
		}
		if !k.secret && !s.isMap {
			k.schema.Default = k.def
		}
	}
	for _, k := range s.keys {
		raw, err := json.Marshal(k.schema)
		if err != nil {
			return fmt.Errorf("%w: section %s key %q: schema: %w", ErrInvalid, s.path, k.relName(), err)
		}
		k.schemaRaw = raw
	}
	return nil
}

// decorateEntrySecrets rewrites the list key's entry schema for each derived
// key: the reflected Ref property is the write-only string a secret key
// takes, and inside the list it is the reference {"secret": "<name>"} the
// canonical value carries, never a value; the vendor keyword
// x-mediacore-entry-secret (coined, seams.d/F3-03.md) names the derived key's
// template path. It also gives each derived key its own schema: a write-only
// string marked x-mediacore-secret with its reload and protection.
func (k *keyInfo) decorateEntrySecrets() error {
	items := k.schema.Items
	if items == nil || items.Properties == nil {
		return fmt.Errorf("%w: section %s key %q: the list's schema has no entry properties", ErrInvalid, k.sec.path, k.relName())
	}
	for _, d := range k.entrySecrets {
		name := d.rel[0]
		if items.Properties[name] == nil {
			return fmt.Errorf("%w: section %s key %q: no schema for the entry secret %q", ErrInvalid, k.sec.path, k.relName(), name)
		}
		items.Properties[name] = &jsonschema.Schema{
			Type:                 "object",
			Properties:           map[string]*jsonschema.Schema{"secret": {Type: "string"}},
			Required:             []string{"secret"},
			AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			Extra:                map[string]any{"x-mediacore-entry-secret": string(d.templatePath())},
		}
		// An entry may omit its reference: the canonical value adds it.
		items.Required = slices.DeleteFunc(items.Required, func(r string) bool { return r == name })
		ds := &jsonschema.Schema{Type: "string", WriteOnly: true}
		setExtra(ds, "x-mediacore-secret", true)
		setExtra(ds, "x-mediacore-reload", d.reload.String())
		if d.protect != ProtectNone {
			setExtra(ds, "x-mediacore-protect", d.protect.String())
		}
		raw, err := json.Marshal(ds)
		if err != nil {
			return fmt.Errorf("%w: section %s entry secret %s: schema: %w", ErrInvalid, k.sec.path, d.templatePath(), err)
		}
		d.schema, d.schemaRaw = ds, raw
	}
	return nil
}

// fillDefault sets k.def from the declared defaults (struct sections) or the
// element's zero value (map templates), checked like any value.
func (s *section) fillDefault(k *keyInfo) error {
	if k.secret {
		k.def = secretValue(k.path(""))
		return nil
	}
	src, rel := s.defaults, k.rel
	if s.isMap {
		src = s.elemZero
	}
	raw, ok := json.RawMessage(src), true
	if len(rel) > 0 {
		raw, ok = subJSON(src, rel)
	}
	if !ok {
		return fmt.Errorf("%w: section %s: no default for key %q", ErrInvalid, s.path, k.relName())
	}
	p := k.path("")
	if s.isMap {
		p = k.path("default")
	}
	c, err := k.canonical(p, raw)
	if err != nil {
		return fmt.Errorf("%w: section %s: the declared default of %q is invalid: %w", ErrInvalid, s.path, k.relName(), err)
	}
	k.def = c
	return nil
}

// keyOf returns the key of a struct section at path p, or nil.
func (s *section) keyOf(p Path) *keyInfo {
	if s.isMap {
		return nil
	}
	if p == s.path {
		return s.byRel[""]
	}
	rel, ok := strings.CutPrefix(string(p), string(s.path)+pathSep)
	if !ok {
		return nil
	}
	return s.byRel[rel]
}

// checkOptions validates the recorded options against the keys: Docs,
// ProtectionWaivers, EnvBinding and FailClosed name keys of this section, and
// FailClosed's order covers exactly a bool or enum key's values (05-config
// §1.3.1).
func (s *section) checkOptions() error {
	if !s.isMap {
		for p := range s.spec.docs {
			if s.keyOf(p) == nil {
				return fmt.Errorf("%w: section %s: Docs names %s, which is not a key of the section", ErrInvalid, s.path, p)
			}
		}
		for p := range s.spec.waivers {
			if s.keyOf(p) == nil {
				return fmt.Errorf("%w: section %s: ProtectionWaivers names %s, which is not a key of the section", ErrInvalid, s.path, p)
			}
		}
	}
	for _, eb := range s.spec.envBindings {
		if s.keyOf(eb.path) == nil {
			return fmt.Errorf("%w: section %s: EnvBinding names %s, which is not a key of the section", ErrInvalid, s.path, eb.path)
		}
		if eb.name == "" {
			return fmt.Errorf("%w: section %s: EnvBinding for %s has an empty name", ErrInvalid, s.path, eb.path)
		}
	}
	seen := map[Path]bool{}
	for _, fc := range s.spec.failClosed {
		k := s.keyOf(fc.path)
		switch {
		case k == nil:
			return fmt.Errorf("%w: section %s: FailClosed names %s, which is not a key of the section", ErrInvalid, s.path, fc.path)
		case seen[fc.path]:
			return fmt.Errorf("%w: section %s: FailClosed declared twice for %s", ErrInvalid, s.path, fc.path)
		case k.secret:
			return fmt.Errorf("%w: section %s: FailClosed on the secret key %s", ErrInvalid, s.path, fc.path)
		}
		seen[fc.path] = true
		allowed, ok := k.schemaAllows()
		if !ok {
			return fmt.Errorf("%w: section %s: FailClosed on %s, which is not a bool or enum key", ErrInvalid, s.path, fc.path)
		}
		order := make([]json.RawMessage, 0, len(fc.order))
		for _, o := range fc.order {
			c, err := k.canonical(fc.path, o)
			if err != nil {
				return fmt.Errorf("%w: section %s: FailClosed order for %s: %w", ErrInvalid, s.path, fc.path, err)
			}
			if slices.ContainsFunc(order, func(x json.RawMessage) bool { return string(x) == string(c) }) {
				return fmt.Errorf("%w: section %s: FailClosed order for %s lists %s twice", ErrInvalid, s.path, fc.path, c)
			}
			order = append(order, c)
		}
		for _, a := range allowed {
			if !slices.ContainsFunc(order, func(x json.RawMessage) bool { return string(x) == string(a) }) {
				return fmt.Errorf("%w: section %s: FailClosed order for %s is missing %s", ErrInvalid, s.path, fc.path, a)
			}
		}
		k.failOrder = order
	}
	return nil
}

// setExtra writes one vendor keyword.
func setExtra(s *jsonschema.Schema, key string, v any) {
	if s.Extra == nil {
		s.Extra = map[string]any{}
	}
	s.Extra[key] = v
}

// assemble builds the section's JSON from per-key canonical values: vals maps
// each concrete key path to its value; ids are a map section's entries.
func (s *section) assemble(vals map[Path]json.RawMessage, ids []string) (json.RawMessage, error) {
	if !s.isMap && len(s.keys) == 1 && len(s.keys[0].rel) == 0 {
		return vals[s.keys[0].path("")], nil
	}
	tree := map[string]any{}
	if s.isMap {
		for _, id := range ids {
			entry := map[string]any{}
			var whole json.RawMessage
			for _, k := range s.keys {
				v := vals[k.path(id)]
				if len(k.rel) == 0 {
					whole = v
					continue
				}
				setTree(entry, k.rel, v)
			}
			if whole != nil {
				tree[id] = whole
			} else {
				tree[id] = entry
			}
		}
	} else {
		for _, k := range s.keys {
			setTree(tree, k.rel, vals[k.path("")])
		}
	}
	return json.Marshal(tree)
}

// decode strictly decodes the section's JSON into a fresh *T, binds every
// secrets.Ref to res and runs the type's Validator. The entries of a list in
// opLists (one an operator layer supplies) bind through operatorOnly, so they
// read only an operator-layer secret. Errors are *FieldError.
func (s *section) decode(raw json.RawMessage, res secrets.Resolver, opLists map[*keyInfo]bool) (any, error) {
	v := reflect.New(s.spec.typ)
	if err := strictDecode(raw, v.Interface()); err != nil {
		return nil, s.redact(&FieldError{Path: s.path, Err: err}, nil)
	}
	for _, k := range s.keys {
		if !k.secret {
			continue
		}
		f := v.Elem().FieldByIndex(k.index)
		if !f.CanSet() {
			return nil, &FieldError{Path: k.path(""), Err: errors.New("a secrets.Ref field config cannot set")}
		}
		f.Set(reflect.ValueOf(secrets.RefTo(secrets.Name(k.path(""))).Bind(res)))
	}
	for _, lk := range s.entryLists {
		// The list's value is canonical, so its entries pass; binding them
		// again here ties every Ref to its entry's ID whatever the bytes say.
		r := res
		if opLists[lk] && r != nil {
			r = operatorOnly{r: r}
		}
		if err := lk.bindEntries(lk.path(""), v.Elem().FieldByIndex(lk.index), r); err != nil {
			return nil, s.redact(err, nil)
		}
	}
	if val, ok := v.Interface().(Validator); ok {
		ids := s.entryIDs(v) // before Validate, which could add an entry
		if err := val.Validate(); err != nil {
			return nil, s.redact(asFieldError(s.path, err), ids)
		}
	}
	return v.Interface(), nil
}

// operatorOnly is the resolver of a list entry an operator layer supplies: it
// serves the entry's secret only from an operator layer (env, file). A stored
// secret may predate the entry: an operator who adds an entry at boot would
// otherwise hand it whatever an earlier entry of that ID left in the store,
// and config writes nothing at boot to clear it (C13; seams.d/F3-03.md). A
// stored value reads as not set, with Meta.Set false.
type operatorOnly struct{ r secrets.Resolver }

// fromOperator reports whether m's value comes from an operator layer.
func fromOperator(m secrets.Meta) bool {
	return m.Source == secrets.SourceEnv || m.Source == secrets.SourceFile
}

func (o operatorOnly) Get(ctx context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	v, m, err := o.r.Get(ctx, n)
	if fromOperator(m) || errors.Is(err, secrets.ErrNotSet) {
		return v, m, err
	}
	return secrets.Value{}, secrets.Meta{Name: n}, fmt.Errorf("%w: %s belongs to a list entry an operator layer supplies, so only an operator-layer value is used", secrets.ErrNotSet, n)
}

func (o operatorOnly) Meta(ctx context.Context, n secrets.Name) (secrets.Meta, error) {
	m, err := o.r.Meta(ctx, n)
	if err != nil || fromOperator(m) {
		return m, err
	}
	return secrets.Meta{Name: n}, nil
}

// redact keeps a sensitive value out of a decode or Validator error of this
// section (05-config §1.3.1: a sensitive value is redacted in logs). A list
// whose entries hold secrets counts as a sensitive key here (fail closed:
// keys.go redacts). These
// errors reach AttachReport.Quarantined, KeyState.Quarantined, the logs and
// Apply's error, and a Validator's message may quote any field it checks: a
// cross-field check on a plain key can quote the sensitive key beside it. So
// in a section that holds a sensitive key, no Validator-authored message is
// kept (fail closed). Each part of the error (errors.Join parts are taken one
// by one) that is a *FieldError on one of the section's keys, sensitive or
// plain, keeps its path and gets a fixed message in place of its own; anything
// else (a bare error, a wrapped one, a *FieldError on the section itself or
// on a path that is not a key) is replaced whole by a fixed message on the
// section. A section without a sensitive key is returned as is, messages
// included. In a map section the kept path is Validator text too, so it is
// kept only when its entry ID is one of ids, the entries of the candidate the
// Validator checked; a path naming any other ID (one invented from a
// sensitive value, say) is replaced like any other part.
func (s *section) redact(err error, ids map[string]bool) error {
	if err == nil || !slices.ContainsFunc(s.keys, (*keyInfo).redacts) {
		return err
	}
	return s.redactPart(err, ids)
}

// entryIDs is the entry IDs of a map section's decoded candidate v (a *T);
// nil for a struct section.
func (s *section) entryIDs(v reflect.Value) map[string]bool {
	if !s.isMap {
		return nil
	}
	m := v.Elem()
	ids := make(map[string]bool, m.Len())
	for _, k := range m.MapKeys() {
		ids[k.String()] = true
	}
	return ids
}

// redactPart is redact for one part of the error tree.
func (s *section) redactPart(err error, ids map[string]bool) error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		parts := j.Unwrap()
		out := make([]error, 0, len(parts))
		for _, e := range parts {
			out = append(out, s.redactPart(e, ids))
		}
		return errors.Join(out...)
	}
	if fe, ok := err.(*FieldError); ok {
		if s.concreteKey(fe.Path, ids) != nil {
			return &FieldError{Path: fe.Path, Err: errors.New("the value fails the section's Validator (detail redacted: the section holds a sensitive key)")}
		}
	}
	return &FieldError{Path: s.path, Err: errors.New("the section fails validation (detail redacted: the section holds a sensitive key)")}
}

// concreteKey is the key p names in this section: a struct section's key, or
// a map entry's "<section>.<id>.<rel>" whose id is one of ids, the entries of
// the candidate. nil when p is not such a key of s.
func (s *section) concreteKey(p Path, ids map[string]bool) *keyInfo {
	if !s.isMap {
		if p == s.path {
			return nil // the section itself: a section-level error
		}
		return s.keyOf(p)
	}
	rest, ok := strings.CutPrefix(string(p), string(s.path)+pathSep)
	if !ok {
		return nil
	}
	id, rel, _ := strings.Cut(rest, pathSep)
	if !ids[id] {
		return nil
	}
	return s.byRel[rel]
}

// asFieldError keeps a Validator's *FieldErrors and wraps anything else as
// one FieldError on the section.
func asFieldError(p Path, err error) error {
	if _, ok := errors.AsType[*FieldError](err); ok {
		return err
	}
	return &FieldError{Path: p, Err: err}
}

// snapshot is one published value of a section: an immutable, deep-decoded
// *T, its JSON and the generation that produced it.
type snapshot struct {
	val any
	raw json.RawMessage
	gen uint64
}

// binding is a section's live state: its snapshots and subscribers.
type binding struct {
	sec   *section
	store *Store
	cur   atomic.Pointer[snapshot] // effective (Get)
	pend  atomic.Pointer[snapshot] // stored, incl. restart-pending changes (Pending)
	mu    sync.Mutex
	subs  []*subscription
	hooks []*hook
}

// Handle is the live, typed view of one section. Safe for concurrent use.
// Never nil after Register.
type Handle[T any] struct{ b *binding }

// Path is the section's path.
func (h *Handle[T]) Path() Path { return h.b.sec.path }

// Get returns the current effective snapshot: READ-ONLY, one atomic load.
// No two snapshots share a slice or map, and mutating one is a bug.
func (h *Handle[T]) Get() *T { return h.b.cur.Load().val.(*T) }

// Pending returns the stored value including restart-required changes; it is
// Get() when none is pending.
func (h *Handle[T]) Pending() *T { return h.b.pend.Load().val.(*T) }

// Generation is the store generation that last changed the effective value.
func (h *Handle[T]) Generation() uint64 { return h.b.cur.Load().gen }

// OnChange runs fn on a dedicated goroutine after each commit that changed
// this section's effective value. Deliveries coalesce: a slow fn sees only
// the latest snapshot, with prev the value it last saw. ctx is cancelled at
// Store.Close. The goroutine starts at Load (or at once after it); stop is
// idempotent, waits for an in-flight call, and must not be called from fn.
func (h *Handle[T]) OnChange(fn func(ctx context.Context, prev, next *T)) (stop func()) {
	return h.b.subscribe(func(ctx context.Context, prev, next any) { fn(ctx, prev.(*T), next.(*T)) })
}

// AfterCommit runs fn synchronously inside Apply, after the durable commit,
// in registration order, with the stored (Pending) values before and after.
// It cannot veto; a returned error (or a panic) is reported to the caller as
// *PostCommitError while the change stays committed.
func (h *Handle[T]) AfterCommit(fn func(ctx context.Context, prev, next *T) error) (stop func()) {
	return h.b.addHook(func(ctx context.Context, prev, next any) error { return fn(ctx, prev.(*T), next.(*T)) })
}

// hook is one AfterCommit registration.
type hook struct {
	fn      func(ctx context.Context, prev, next any) error
	removed atomic.Bool
}

// addHook registers fn and returns its idempotent stop.
func (b *binding) addHook(fn func(ctx context.Context, prev, next any) error) func() {
	h := &hook{fn: fn}
	b.mu.Lock()
	b.hooks = append(b.hooks, h)
	b.mu.Unlock()
	return func() {
		if h.removed.Swap(true) {
			return
		}
		b.mu.Lock()
		b.hooks = slices.DeleteFunc(b.hooks, func(x *hook) bool { return x == h })
		b.mu.Unlock()
	}
}

// runHooks runs the AfterCommit hooks in registration order and returns the
// first error of each failing hook, a panic included.
func (b *binding) runHooks(ctx context.Context, prev, next any) []error {
	b.mu.Lock()
	hs := slices.Clone(b.hooks)
	b.mu.Unlock()
	var errs []error
	for _, h := range hs {
		if h.removed.Load() {
			continue
		}
		if err := callHook(ctx, h, prev, next); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// callHook runs one hook, turning a panic into an error.
func callHook(ctx context.Context, h *hook, prev, next any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("after-commit hook panicked: %v", r)
		}
	}()
	return h.fn(ctx, prev, next)
}

// subscription is one OnChange registration: a goroutine with a one-slot
// coalescing mailbox (05-config §1.6).
type subscription struct {
	b    *binding
	fn   func(ctx context.Context, prev, next any)
	wake chan struct{} // capacity 1: a pending delivery
	quit chan struct{} // closed by stop
	done chan struct{} // closed when the goroutine returns

	stopOnce sync.Once
	mu       sync.Mutex
	started  bool
	pending  bool
	seen     uint64 // the highest generation posted: an older post is stale
	prev     *snapshot
	next     *snapshot
}

// subscribe registers fn and starts its goroutine when the store is loaded.
func (b *binding) subscribe(fn func(ctx context.Context, prev, next any)) func() {
	sub := &subscription{b: b, fn: fn, wake: make(chan struct{}, 1), quit: make(chan struct{}), done: make(chan struct{})}
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()
	b.store.startSub(sub)
	return sub.stop
}

// stop ends the subscription and waits for an in-flight call.
func (sub *subscription) stop() {
	sub.stopOnce.Do(func() {
		close(sub.quit)
		sub.b.mu.Lock()
		sub.b.subs = slices.DeleteFunc(sub.b.subs, func(x *subscription) bool { return x == sub })
		sub.b.mu.Unlock()
	})
	sub.mu.Lock()
	started := sub.started
	sub.mu.Unlock()
	if started {
		<-sub.done
	}
}

// stopped reports whether stop has been called.
func (sub *subscription) stopped() bool {
	select {
	case <-sub.quit:
		return true
	default:
		return false
	}
}

// notifyAll posts a change of the effective snapshot to every subscriber.
func (b *binding) notifyAll(prev, next *snapshot) {
	b.mu.Lock()
	subs := slices.Clone(b.subs)
	b.mu.Unlock()
	for _, sub := range subs {
		sub.notify(prev, next)
	}
}

// notify posts a change: the mailbox keeps the oldest undelivered prev and
// the latest next. Apply posts after it releases the store mutex, so two
// Applies may post out of order; a post whose next is not newer than one
// already posted is stale and dropped.
func (sub *subscription) notify(prev, next *snapshot) {
	sub.mu.Lock()
	if next.gen <= sub.seen {
		sub.mu.Unlock()
		return
	}
	sub.seen = next.gen
	if !sub.pending {
		sub.prev = prev
		sub.pending = true
	}
	sub.next = next
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// run is the subscription's goroutine.
func (sub *subscription) run(ctx context.Context, done func()) {
	defer done()
	defer close(sub.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.quit:
			return
		case <-sub.wake:
		}
		sub.mu.Lock()
		prev, next, ok := sub.prev, sub.next, sub.pending
		sub.prev, sub.next, sub.pending = nil, nil, false
		sub.mu.Unlock()
		if ok {
			sub.deliver(ctx, prev.val, next.val)
		}
	}
}

// deliver calls fn, recovering a panic: one subscriber must not break config
// (05-config §1.5).
func (sub *subscription) deliver(ctx context.Context, prev, next any) {
	defer func() {
		if r := recover(); r != nil {
			sub.b.store.subscriberPanicked(sub.b.sec.path, r)
		}
	}()
	sub.fn(ctx, prev, next)
}
