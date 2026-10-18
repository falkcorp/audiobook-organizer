// file: internal/operations/registry/preview.go
// version: 2.1.0
// guid: 1d72f677-0951-40e4-9b15-cb9211c3e50a
// last-edited: 2026-10-18

package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// The params keys a caller can state a run's mode with. dry_run and dryRun
// are one family (both spellings of "write nothing when true"); apply and
// live each write when true.
const (
	ModeKeyDryRun      = "dry_run"
	ModeKeyDryRunCamel = "dryRun"
	ModeKeyApply       = "apply"
	ModeKeyLive        = "live"
)

// modeKeyOrder is every mode key, in the order they are reported.
var modeKeyOrder = []string{ModeKeyDryRun, ModeKeyDryRunCamel, ModeKeyApply, ModeKeyLive}

// ModeKeyFamily is the family a mode key belongs to: dryRun is dry_run's.
func ModeKeyFamily(k string) string {
	if k == ModeKeyDryRunCamel {
		return ModeKeyDryRun
	}
	return k
}

// modeKeyWritesWhenTrue: apply and live write when true, dry_run and dryRun
// when false.
func modeKeyWritesWhenTrue(k string) bool { return k == ModeKeyApply || k == ModeKeyLive }

// Preview modes as a start request names them.
const (
	ModePreview = "preview"
	ModeLive    = "live"
)

// PreviewSpec declares how an op previews, next to the op's code.
//
// Params is a zero value of the struct the op's Run decodes its params into.
// Every mode key that struct decodes (a bool or *bool field whose JSON name is
// dry_run, dryRun, apply or live) is part of the run's mode: a preview writes
// each at its non-writing value (dry_run=true, dryRun=true, apply=false,
// live=false) and a live run each at its writing value. There is no separate
// "which key" field to get wrong: the mode keys are read off the type, so a
// declaration cannot name a key the op does not decode, and an op that gates
// on two keys (apply && !dry_run) gets both written. Whether the declared type
// really is the one Run decodes is checked by behavioural tests that run the
// op with Resolve's output (see TestPreviewBehaviour_* in the op packages);
// the type system does not tie the two.
//
// LiveOneOf names write switches of which a live run must set exactly one
// itself, for an op with several kinds of write (itunes.clone-into-library:
// apply, rollback, repair). A preview writes them all false; a live run
// refuses unless the caller set exactly one true, because no default choice
// between them is safe. They are not mode keys, and a key may not be both.
//
// StartCheck, when set, refuses params that cannot do what the mode asks
// (a live run whose params select a report-only mode, a key the preview
// value makes fail at run time). It sees the params Resolve is about to
// return; an error refuses the start.
//
// The zero value means the op has no preview a caller can ask for by params:
// it ignores every mode key, or previews only through a different op or
// handler. /api/v3/ops then requires an explicit mode "live" to start it and
// passes params through untouched.
type PreviewSpec struct {
	Params     any
	LiveOneOf  []string
	StartCheck func(params map[string]json.RawMessage, live bool) error
}

// IsZero reports whether no preview is declared.
func (p PreviewSpec) IsZero() bool {
	return p.Params == nil && len(p.LiveOneOf) == 0 && p.StartCheck == nil
}

// Check returns why the declaration is unusable, nil when it is usable or
// when nothing is declared. ValidateOpDef refuses to register an op whose
// declaration does not check.
func (p PreviewSpec) Check() error {
	if p.IsZero() {
		return nil
	}
	if p.Params == nil {
		return fmt.Errorf("preview declared without the params type the op decodes")
	}
	fields, err := paramsFields(p.Params)
	if err != nil {
		return err
	}
	switches := append(slices.Clone(modeKeyOrder), p.LiveOneOf...)
	for name := range fields {
		for _, k := range switches {
			if name != k && strings.EqualFold(name, k) {
				return fmt.Errorf("preview params %s field %q differs from mode key %q only in case; tag it exactly %q",
					paramsTypeName(p.Params), name, k, k)
			}
		}
	}
	seen := map[string]bool{}
	for _, k := range p.LiveOneOf {
		if seen[k] {
			return fmt.Errorf("preview LiveOneOf lists %q twice", k)
		}
		seen[k] = true
		if ModeKeyFamily(k) == ModeKeyDryRun {
			return fmt.Errorf("preview LiveOneOf lists %q, which writes when false; only write switches belong there", k)
		}
		if err := boolField(fields, k, p.Params); err != nil {
			return fmt.Errorf("preview LiveOneOf: %w", err)
		}
	}
	keys := p.ModeKeys()
	for _, k := range keys {
		if err := boolField(fields, k, p.Params); err != nil {
			return err
		}
	}
	if len(keys) == 0 && len(p.LiveOneOf) == 0 {
		return fmt.Errorf("preview params %s decodes none of %v and declares no LiveOneOf", paramsTypeName(p.Params), modeKeyOrder)
	}
	return nil
}

// ModeKeys returns the mode keys the declared params type decodes, in report
// order, leaving out LiveOneOf switches. Nil when nothing is declared.
func (p PreviewSpec) ModeKeys() []string {
	if p.Params == nil {
		return nil
	}
	fields, err := paramsFields(p.Params)
	if err != nil {
		return nil
	}
	var out []string
	for _, k := range modeKeyOrder {
		if _, ok := fields[k]; ok && !slices.Contains(p.LiveOneOf, k) {
			out = append(out, k)
		}
	}
	return out
}

// ModeFamilies returns the distinct families of ModeKeys: an op whose type
// decodes both dry_run and apply has two, and a run writes only when both say
// so.
func (p PreviewSpec) ModeFamilies() []string {
	var out []string
	for _, k := range p.ModeKeys() {
		if f := ModeKeyFamily(k); !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// Decodes reports whether the declared params type has a field whose JSON
// name is exactly name.
func (p PreviewSpec) Decodes(name string) bool {
	if p.Params == nil {
		return false
	}
	fields, err := paramsFields(p.Params)
	if err != nil {
		return false
	}
	_, ok := fields[name]
	return ok
}

// ModeError is a start request whose params and mode cannot be reconciled.
// Every one is the caller's to fix (an HTTP 400).
type ModeError struct{ Msg string }

func (e *ModeError) Error() string { return e.Msg }

func modeErrorf(format string, args ...any) error {
	return &ModeError{Msg: fmt.Sprintf(format, args...)}
}

// Resolve applies mode ("", "preview" or "live") to params, a JSON object,
// and returns the params to queue.
//
//   - A mode key or LiveOneOf switch spelled in a different case is refused:
//     encoding/json would decode it into the same field, so {"APPLY":true}
//     would reach an op whose mode was resolved without it.
//   - A mode key the declared type does not decode is refused: the op would
//     never see it.
//   - Each stated mode key and true LiveOneOf switch implies a mode; keys
//     that imply different modes are refused.
//   - An empty mode takes the stated keys' mode, else preview. A mode that
//     contradicts a stated key is refused.
//   - Every mode key is then written at the mode's value, LiveOneOf switches
//     are written false for a preview, and a live run with LiveOneOf must
//     have exactly one switch true.
//   - StartCheck, when declared, sees the result last.
//
// Every error is a *ModeError.
func (p PreviewSpec) Resolve(params json.RawMessage, mode string) (json.RawMessage, error) {
	return p.resolve(params, mode, true)
}

// SameMode reports whether two params bodies of the op run in the same mode:
// both are normalized as Resolve with an empty mode would (StartCheck aside:
// it judges whether a start may be queued, not which mode a body is), and
// the values of every mode key and LiveOneOf switch are compared. A body that
// does not normalize (malformed, or keys that disagree) is in no mode, so it
// is never the same as another. EnqueueOp's dedupe uses it so a live request
// is never answered with a queued preview's run id, or the reverse.
func (p PreviewSpec) SameMode(a, b json.RawMessage) bool {
	sa, err := p.modeSignature(a)
	if err != nil {
		return false
	}
	sb, err := p.modeSignature(b)
	if err != nil {
		return false
	}
	return sa == sb
}

// modeSignature is the normalized value of every mode key and LiveOneOf
// switch, in declaration order; an absent switch is false.
func (p PreviewSpec) modeSignature(params json.RawMessage) (string, error) {
	out, err := p.resolve(params, "", false)
	if err != nil {
		return "", err
	}
	m, err := paramsObject(out)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, k := range append(p.ModeKeys(), p.LiveOneOf...) {
		v := false
		if raw, ok := m[k]; ok {
			if v, err = paramsBool(raw, k); err != nil {
				return "", err
			}
		}
		fmt.Fprintf(&b, "%s=%t;", k, v)
	}
	return b.String(), nil
}

func (p PreviewSpec) resolve(params json.RawMessage, mode string, startCheck bool) (json.RawMessage, error) {
	if p.IsZero() {
		return nil, modeErrorf("no preview declared")
	}
	if err := p.Check(); err != nil {
		return nil, modeErrorf("preview declaration is unusable: %v", err)
	}
	switch mode {
	case "", ModePreview, ModeLive:
	default:
		return nil, modeErrorf(`mode must be %q or %q`, ModePreview, ModeLive)
	}
	m, err := paramsObject(params)
	if err != nil {
		return nil, err
	}
	if err := refuseCaseVariants(m, p.LiveOneOf); err != nil {
		return nil, err
	}
	keys := p.ModeKeys()
	for _, k := range modeKeyOrder {
		if _, ok := m[k]; ok && !slices.Contains(keys, k) {
			if slices.Contains(p.LiveOneOf, k) {
				continue
			}
			return nil, modeErrorf("this op does not read %s; its mode keys are %v", k, keys)
		}
	}

	stated, statedBy := "", ""
	imply := func(k, md string) error {
		if stated != "" && stated != md {
			return modeErrorf("%s and %s in params disagree about the mode", statedBy, k)
		}
		stated, statedBy = md, k
		return nil
	}
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		v, err := paramsBool(raw, k)
		if err != nil {
			return nil, err
		}
		md := ModePreview
		if v == modeKeyWritesWhenTrue(k) {
			md = ModeLive
		}
		if err := imply(k, md); err != nil {
			return nil, err
		}
	}
	var on []string
	for _, k := range p.LiveOneOf {
		raw, ok := m[k]
		if !ok {
			continue
		}
		v, err := paramsBool(raw, k)
		if err != nil {
			return nil, err
		}
		if v {
			on = append(on, k)
			if err := imply(k, ModeLive); err != nil {
				return nil, err
			}
		}
	}

	if mode == "" {
		mode = ModePreview
		if stated != "" {
			mode = stated
		}
	} else if stated != "" && stated != mode {
		return nil, modeErrorf("mode %s contradicts %s in params", mode, statedBy)
	}
	live := mode == ModeLive
	for _, k := range keys {
		m[k] = jsonBool(live == modeKeyWritesWhenTrue(k))
	}
	if len(p.LiveOneOf) > 0 {
		if live {
			if len(on) != 1 {
				return nil, modeErrorf("a live run of this op must set exactly one of %v to true in params; got %v", p.LiveOneOf, on)
			}
		} else {
			for _, k := range p.LiveOneOf {
				m[k] = jsonBool(false)
			}
		}
	}
	if startCheck && p.StartCheck != nil {
		if err := p.StartCheck(m, live); err != nil {
			return nil, &ModeError{Msg: err.Error()}
		}
	}
	out, merr := json.Marshal(m)
	if merr != nil {
		return nil, modeErrorf("params: %v", merr)
	}
	return out, nil
}

// CheckModeKeySpelling refuses a params key that differs from a mode key only
// in case. For an op without a preview declaration, which never gets its mode
// written, it is the only spelling guard.
func CheckModeKeySpelling(params json.RawMessage) error {
	m, err := paramsObject(params)
	if err != nil {
		return err
	}
	return refuseCaseVariants(m, nil)
}

// CheckStatedMode is for an op whose mode /api/v3/ops does not write: it
// refuses params whose mode keys disagree with each other, or with mode when
// mode is not empty. A key whose value is not a bool is left to the op (a
// read-only op may use the name for something else).
func CheckStatedMode(params json.RawMessage, mode string) error {
	m, err := paramsObject(params)
	if err != nil {
		return err
	}
	stated, statedBy := "", ""
	for _, k := range modeKeyOrder {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var v bool
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &v) != nil {
			continue
		}
		md := ModePreview
		if v == modeKeyWritesWhenTrue(k) {
			md = ModeLive
		}
		if stated != "" && stated != md {
			return modeErrorf("%s and %s in params disagree about the mode", statedBy, k)
		}
		stated, statedBy = md, k
	}
	if mode != "" && stated != "" && stated != mode {
		return modeErrorf("mode %s contradicts %s in params", mode, statedBy)
	}
	return nil
}

func refuseCaseVariants(m map[string]json.RawMessage, extra []string) error {
	switches := append(slices.Clone(modeKeyOrder), extra...)
	for name := range m {
		for _, k := range switches {
			if name != k && strings.EqualFold(name, k) {
				return modeErrorf("params key %q differs from %q only in case; send %q", name, k, k)
			}
		}
	}
	return nil
}

func paramsObject(params json.RawMessage) (map[string]json.RawMessage, error) {
	t := bytes.TrimSpace(params)
	if len(t) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(t, &m); err != nil || m == nil {
		return nil, modeErrorf("params must be a JSON object")
	}
	return m, nil
}

func paramsBool(raw json.RawMessage, k string) (bool, error) {
	var v bool
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &v) != nil {
		return false, modeErrorf("%s must be true or false", k)
	}
	return v, nil
}

func jsonBool(v bool) json.RawMessage {
	if v {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

func paramsTypeName(params any) string {
	t := reflect.TypeOf(params)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.String()
}

// paramsFields maps every JSON name the params struct decodes (exact tag
// names, embedded structs flattened the way encoding/json flattens them) to
// the field's type.
func paramsFields(params any) (map[string]reflect.Type, error) {
	t := reflect.TypeOf(params)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("preview params %s is not a struct", t)
	}
	out := map[string]reflect.Type{}
	collectJSONFields(t, out, 0)
	return out, nil
}

func collectJSONFields(t reflect.Type, out map[string]reflect.Type, depth int) {
	if depth > 8 {
		return
	}
	var embedded []reflect.Type
	for i := range t.NumField() {
		f := t.Field(i)
		tag, hasTag := f.Tag.Lookup("json")
		tagName, _, _ := strings.Cut(tag, ",")
		if tagName == "-" {
			continue
		}
		if f.Anonymous && tagName == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				embedded = append(embedded, et)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		key := tagName
		if !hasTag || key == "" {
			key = f.Name
		}
		if _, dup := out[key]; !dup {
			out[key] = f.Type
		}
	}
	// A field of the outer struct shadows an embedded one of the same name,
	// as in encoding/json.
	for _, et := range embedded {
		collectJSONFields(et, out, depth+1)
	}
}

func boolField(fields map[string]reflect.Type, k string, params any) error {
	ft, ok := fields[k]
	if !ok {
		return fmt.Errorf("preview params %s has no field tagged %q", paramsTypeName(params), k)
	}
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	if ft.Kind() != reflect.Bool {
		return fmt.Errorf("preview params %s field %q is %s, not bool", paramsTypeName(params), k, ft)
	}
	return nil
}
