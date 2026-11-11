// file: core/secrets/types.go
// version: 1.0.2
// guid: a67c98df-8fe3-435f-9a52-46603e1ad0cb
// last-edited: 2026-11-10

// Package secrets is core's secret store (05-config Part 2). This file is its
// types-only leaf (F3-01): the names config uses, Ref, Resolver, Write and
// Meta, with the types they are built from (Name, Value, Source), copied from
// 05-config §2.3. There is no store, no keyring and no storage import here:
// F3-03 grows the package (Open, Store, Scope, Importer, the keyring) around
// this file without changing it.
//
// Layer L2 (03 R4): this file imports the standard library only, and the
// package holds no package-level mutable state (R5).
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Sentinel errors this file's methods return. F3-03 adds the store's own
// (ErrNotSet, ErrKeyUnavailable, ErrCorrupt, ...) in its own file.
var (
	// ErrUnbound: a Ref was resolved before config bound it to a resolver
	// (the store is attached at 06 P4d step 1).
	ErrUnbound = errors.New("secrets: Ref used before the store was attached")
	// ErrNotSerializable: a Value was marshalled. Accidental API exposure
	// fails loudly instead of leaking the secret.
	ErrNotSerializable = errors.New("secrets: Value must not be serialized")
)

// Name is a secret's name. Grammar: seg = [a-z][a-z0-9_-]{0,63};
// name = seg ("." seg)*; the first segment is "core", "modules" or
// "integrations". A config secret's name defaults to its config path; an
// integration instance secret is "integrations.<kind>.<instance>.<field>".
type Name string

// maxNameSegment is the longest name segment: one leading letter plus 63.
const maxNameSegment = 64

// Validate reports whether n follows the grammar above, naming the first
// offending segment.
func (n Name) Validate() error {
	if n == "" {
		return errors.New("secrets: name is empty")
	}
	segs := strings.Split(string(n), ".")
	switch segs[0] {
	case "core", "modules", "integrations":
	default:
		return fmt.Errorf("secrets: name %q: first segment %q must be core, modules or integrations", string(n), segs[0])
	}
	for _, seg := range segs {
		if !validNameSegment(seg) {
			return fmt.Errorf("secrets: name %q: segment %q does not match [a-z][a-z0-9_-]{0,63}", string(n), seg)
		}
	}
	return nil
}

// validNameSegment reports whether seg matches [a-z][a-z0-9_-]{0,63}. It is
// hand-written: core holds no package-level regexp (R5).
func validNameSegment(seg string) bool {
	if seg == "" || len(seg) > maxNameSegment {
		return false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// redacted is what every formatting path of a Value prints.
const redacted = "[REDACTED]"

// Value holds secret bytes. Every formatting path is redacted; only Reveal
// and Bytes expose it.
//
// Value's own methods redact it where fmt or slog can call them: a Value
// passed as an argument or held in an exported field. Held in an unexported
// field, alone or in a slice, array, map or nested struct, the methods
// cannot be called and fmt walks the fields instead. So the bytes sit behind
// a pointer to a pointer: fmt dereferences a pointer only at the top level
// (which its bad-verb path, %s or %q on a pointer, resets to) and only when
// it points at an array, slice, struct or map, so a **[]byte prints as an
// address on every path, whatever wraps the Value
// (TestValue_NeverFormatsInsideAnyShape).
type Value struct {
	p **[]byte  // nil for the zero Value; see valueOf and raw
	_ [0]func() // not comparable, as it was when it held a []byte
}

// valueOf returns a Value that owns b (no copy).
func valueOf(b []byte) Value {
	bp := &b
	return Value{p: &bp}
}

// raw is v's own bytes, not a copy: the store reads, seals and wipes them
// in place. nil for the zero Value.
func (v Value) raw() []byte {
	if v.p == nil || *v.p == nil {
		return nil
	}
	return **v.p
}

// NewValue returns a Value holding a copy of s's bytes.
func NewValue(s string) Value { return valueOf([]byte(s)) }

// Reveal returns the secret as a string.
func (v Value) Reveal() string { return string(v.raw()) }

// Bytes returns a copy of the secret's bytes.
func (v Value) Bytes() []byte { return append([]byte(nil), v.raw()...) }

// IsZero reports whether v holds no bytes.
func (v Value) IsZero() bool { return len(v.raw()) == 0 }

// String prints [REDACTED].
func (Value) String() string { return redacted }

// GoString prints secrets.Value([REDACTED]).
func (Value) GoString() string { return "secrets.Value(" + redacted + ")" }

// Format prints [REDACTED] for every verb.
func (Value) Format(f fmt.State, _ rune) {
	// A short write to a fmt.State has nowhere to be reported: fmt's own
	// Formatter contract returns nothing, and the bytes are a constant.
	if _, err := f.Write([]byte(redacted)); err != nil {
		return
	}
}

// LogValue renders as [REDACTED] in slog.
func (Value) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON refuses with ErrNotSerializable.
func (Value) MarshalJSON() ([]byte, error) { return nil, ErrNotSerializable }

// MarshalText refuses with ErrNotSerializable.
func (Value) MarshalText() ([]byte, error) { return nil, ErrNotSerializable }

// Ref is a config field type. config's decoder fills its name (the field's
// path) and binds the resolver (Bind).
type Ref struct {
	name Name
	r    Resolver
}

// RefTo returns an unbound Ref to n.
func RefTo(n Name) Ref { return Ref{name: n} }

// Bind returns a copy of r that resolves through res (name coined by F3-01:
// config binds every Ref of a published snapshot to the store's resolver,
// 05-config §1.4 step 6, and Ref's fields are not reachable from config).
func (r Ref) Bind(res Resolver) Ref { return Ref{name: r.name, r: res} }

// Name is the secret's name.
func (r Ref) Name() Name { return r.name }

// Resolve reads the secret through the bound resolver: ErrUnbound before
// config bound one, else the resolver's error (ErrNotSet, ErrKeyUnavailable).
func (r Ref) Resolve(ctx context.Context) (Value, error) {
	if r.r == nil {
		return Value{}, ErrUnbound
	}
	v, _, err := r.r.Get(ctx, r.name)
	if err != nil {
		return Value{}, err
	}
	return v, nil
}

// refJSON is a Ref's JSON form: the name, never the value.
type refJSON struct {
	Secret Name `json:"secret"`
}

// MarshalJSON writes {"secret": "<name>"}, never the value.
func (r Ref) MarshalJSON() ([]byte, error) { return json.Marshal(refJSON{Secret: r.name}) }

// UnmarshalJSON reads {"secret": "<name>"}; any other shape is refused, so a
// secret value can never be decoded into a Ref by mistake. The result is
// unbound.
func (r *Ref) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var j refJSON
	if err := dec.Decode(&j); err != nil {
		return fmt.Errorf("secrets: a Ref is {\"secret\": \"<name>\"}: %w", err)
	}
	if dec.More() {
		return errors.New("secrets: a Ref is one JSON object")
	}
	*r = Ref{name: j.Secret}
	return nil
}

// Resolver reads secrets; config binds Refs to one (Store.SetSecrets).
type Resolver interface {
	Get(ctx context.Context, n Name) (Value, Meta, error)
	Meta(ctx context.Context, n Name) (Meta, error)
}

// Source says where a secret's value comes from.
type Source uint8

const (
	// SourceStored is encrypted in storage.
	SourceStored Source = iota + 1
	// SourceEnv is operator env (incl. <NAME>_FILE and an ecosystem binding);
	// in memory only, never persisted.
	SourceEnv
	// SourceFile is the operator config file; in memory only.
	SourceFile
)

// Meta describes a secret without its value.
type Meta struct {
	Name      Name
	Set       bool
	Source    Source
	KeyID     string // stored only
	UpdatedAt time.Time
	UpdatedBy string
	// Fingerprint is the first 8 hex of HMAC-SHA256(fingerprint subkey,
	// value): stable per value, not reversible.
	Fingerprint string
	// Err is ErrKeyUnavailable or ErrCorrupt when the record cannot be
	// decrypted, or carries an unreadable marker from the cutover (05-config
	// §1.12.2).
	Err error
}

// Write is one secret change, staged by config.Store.Apply into
// Committer.Commit. A zero Value with Delete=true deletes.
type Write struct {
	Name   Name
	Value  Value
	Delete bool
	By     string
}
