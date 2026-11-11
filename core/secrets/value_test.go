// file: core/secrets/value_test.go
// version: 1.0.4
// guid: adb6b331-c577-44b6-8aa4-93c55c31c6e3
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// formatted is every formatting path 05-config §2.9 names, applied to v.
func formatted(v any) map[string]string {
	out := map[string]string{}
	for _, verb := range []string{"%v", "%s", "%q", "%x", "%X", "%#v", "%+v", "%d"} {
		out[verb] = fmt.Sprintf(verb, v)
	}
	out["Sprint"] = fmt.Sprint(v)
	out["Sprintln"] = fmt.Sprintln(v)
	out["errors.New"] = errors.New(fmt.Sprint(v)).Error()
	out["%w"] = fmt.Errorf("wrapped: %v", v).Error()
	for name, h := range map[string]func(*bytes.Buffer) slog.Handler{
		"slog text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"slog json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		var b bytes.Buffer
		slog.New(h(&b)).Info("the batcomputer", "secret", v)
		out[name] = b.String()
	}
	return out
}

// TestValue_NeverFormats (05-config §2.9): every formatting path of a Value
// prints [REDACTED]; json.Marshal and MarshalText fail.
func TestValue_NeverFormats(t *testing.T) {
	v := NewValue(valueVengeance)
	for path, s := range formatted(v) {
		if strings.Contains(s, valueVengeance) || strings.Contains(s, fmt.Sprintf("%x", valueVengeance)) {
			t.Errorf("%s leaks the value", path)
		}
		if !strings.Contains(s, redacted) {
			t.Errorf("%s does not print %s", path, redacted)
		}
	}
	if _, err := json.Marshal(v); !errors.Is(err, ErrNotSerializable) {
		t.Errorf("json.Marshal(Value) = %v, want ErrNotSerializable", err)
	}
	if _, err := json.Marshal(struct{ V Value }{v}); !errors.Is(err, ErrNotSerializable) {
		t.Errorf("json.Marshal of a struct holding a Value = %v, want ErrNotSerializable", err)
	}
	if _, err := v.MarshalText(); !errors.Is(err, ErrNotSerializable) {
		t.Errorf("MarshalText = %v, want ErrNotSerializable", err)
	}
	if v.Reveal() != valueVengeance || string(v.Bytes()) != valueVengeance {
		t.Error("Reveal or Bytes does not return the value")
	}
	b := v.Bytes()
	b[0] ^= 0xff
	if v.Reveal() != valueVengeance {
		t.Error("Bytes returned the Value's own storage, not a copy")
	}
}

// TestKeyMaterial_NeverFormats (a test name coined by F3-03): key material
// prints [REDACTED] on every formatting path, so a keyring key that reaches a
// log or an error by mistake shows nothing.
func TestKeyMaterial_NeverFormats(t *testing.T) {
	m := keyMaterial(bytes.Repeat([]byte{0xb4}, keyBytes))
	hexed := fmt.Sprintf("%x", []byte(m))
	raw := string(m[:4])
	for path, s := range formatted(m) {
		if strings.Contains(s, hexed) || strings.Contains(strings.ToLower(s), "b4b4b4") || strings.Contains(s, raw) {
			t.Errorf("%s leaks key material", path)
		}
		if !strings.Contains(s, redacted) {
			t.Errorf("%s does not print %s", path, redacted)
		}
	}
	b64 := base64.StdEncoding.EncodeToString(m)[:12]
	k := keyringKey{ID: "k1", Use: useData, Status: statusActive, Material: m}
	for path, s := range formatted(k) {
		if strings.Contains(strings.ToLower(s), "b4b4b4") || strings.Contains(s, "180 180") || strings.Contains(s, b64) || strings.Contains(s, raw) {
			t.Errorf("a keyring key formatted with %s leaks its material", path)
		}
	}
}

// encodings is every form of b a formatting path could leak: the raw bytes,
// hex, fmt's decimal byte list and base64 (encoding/json's []byte form).
func encodings(b []byte) []string {
	return []string{
		string(b), hex.EncodeToString(b), strings.Trim(fmt.Sprint(b), "[]"),
		base64.StdEncoding.EncodeToString(b), base64.StdEncoding.EncodeToString(b)[:12],
	}
}

// TestStore_NeverFormatsSecrets (a test name coined by F3-03): an open
// *Store holding an operator value, its keyring and the keyring's keys
// print no value and no key material on any formatting path (every verb,
// Sprint, errors, slog's text and JSON handlers). fmt prints unexported
// fields raw, bypassing Value's and keyMaterial's own redaction, so each of
// these types redacts itself.
func TestStore_NeverFormatsSecrets(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameModule, valueSerious)
	s.Ephemeral(nameGate, NewValue(valueVengeance), SourceEnv)
	var leaks []string
	for _, v := range []string{valueVengeance, valueSerious} {
		leaks = append(leaks, encodings([]byte(v))...)
	}
	for _, m := range [][]byte{s.ring.data.Material, s.ring.derive.Material, s.ring.fp} {
		leaks = append(leaks, encodings(m[:8])...)
	}
	for name, v := range map[string]any{
		"*Store": s, "keyring": s.ring, "data key": s.ring.data, "derive key": s.ring.derive,
	} {
		for path, out := range formatted(v) {
			for _, l := range leaks {
				if strings.Contains(out, l) {
					t.Errorf("%s formatted with %s leaks a value or key material", name, path)
					break
				}
			}
			if !strings.Contains(out, redacted) && !strings.Contains(out, "app_id") && !strings.Contains(out, "id=") && !strings.Contains(out, `"id"`) {
				t.Errorf("%s formatted with %s = %q, want a redacted rendering", name, path, out)
			}
		}
	}
}

// TestStore_NeverFormatsInsideAWrapper (a test name coined by the F3-03
// follow-up): a struct holding the *Store in an unexported field (the shape
// of kernel.kernelSecrets) prints no value and no key material under any
// verb or slog handler. fmt cannot call the Store's own Format through an
// unexported field; its bad-verb path (%s, %q on a pointer) reprints the
// pointer with %v at depth 0, which walks the Store's fields one level, so
// every field that holds secret bytes must sit behind a pointer. A bound Ref
// reaches the Store the same way, through its resolver.
func TestStore_NeverFormatsInsideAWrapper(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameModule, valueSerious)
	s.Ephemeral(nameGate, NewValue(valueVengeance), SourceEnv)
	s.Ephemeral(nameOracle, NewValue(valueClock), SourceFile)
	var leaks []string
	for _, v := range []string{valueVengeance, valueSerious, valueClock} {
		leaks = append(leaks, encodings([]byte(v))...)
		leaks = append(leaks, strings.TrimPrefix(fmt.Sprintf("%#v", []byte(v)), "[]byte{")[:20])
	}
	for _, m := range [][]byte{s.ring.data.Material, s.ring.derive.Material, s.ring.fp} {
		leaks = append(leaks, encodings(m[:8])...)
	}
	type wrapper struct {
		name  string
		store *Store
	}
	type nested struct{ inner wrapper }
	type refHolder struct{ ref Ref }
	w := wrapper{name: "batcomputer", store: s}
	ref := RefTo(nameGate).Bind(s)
	for name, v := range map[string]any{
		"wrapper": w, "*wrapper": &w, "nested wrapper": nested{inner: w}, "[]wrapper": []wrapper{w},
		"bound Ref": ref, "Ref field": refHolder{ref: ref}, "*Ref field": &refHolder{ref: ref},
	} {
		for path, out := range formatted(v) {
			for _, l := range leaks {
				if strings.Contains(out, l) {
					t.Errorf("%s formatted with %s leaks a value or key material: %s", name, path, out)
					break
				}
			}
		}
	}
}

// TestStore_SecretFieldsSitBehindPointers (a test name coined by the F3-03
// follow-up) is TestStore_NeverFormatsInsideAWrapper's rule checked on the
// Store's type, so a field added later fails here even when no test fills
// it: no type that holds key material is reachable from Store's fields
// without passing a pointer. fmt walks structs, arrays, slices and maps by
// value after its bad-verb dereference, and stops at a pointer. Value is not
// listed: its own bytes sit where fmt never follows
// (TestValue_NeverFormatsInsideAnyShape).
func TestStore_SecretFieldsSitBehindPointers(t *testing.T) {
	secretTypes := map[reflect.Type]bool{
		reflect.TypeFor[keyMaterial](): true,
		reflect.TypeFor[keyring]():     true,
		reflect.TypeFor[keyringFile](): true,
		reflect.TypeFor[keyringKey]():  true,
		reflect.TypeFor[ringKey]():     true,
	}
	var walk func(path string, typ reflect.Type, seen map[reflect.Type]bool)
	walk = func(path string, typ reflect.Type, seen map[reflect.Type]bool) {
		if secretTypes[typ] {
			t.Errorf("Store%s holds %s by value: put it behind a pointer", path, typ)
			return
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Struct:
			for f := range typ.Fields() {
				walk(path+"."+f.Name, f.Type, seen)
			}
		case reflect.Array, reflect.Slice:
			walk(path+"[]", typ.Elem(), seen)
		case reflect.Map:
			walk(path+"[key]", typ.Key(), seen)
			walk(path+"[value]", typ.Elem(), seen)
		}
	}
	walk("", reflect.TypeFor[Store](), map[reflect.Type]bool{})
}

// TestValue_NeverFormatsInsideAnyShape (a test name coined by the F3-03
// follow-up): a Value held where fmt cannot call its methods (an unexported
// field directly, or in a slice, a map value, an array or a nested struct,
// each also behind a pointer) prints none of its bytes under any verb or
// slog's text and JSON handlers.
func TestValue_NeverFormatsInsideAnyShape(t *testing.T) {
	v := NewValue(valueVengeance)
	leaks := append(encodings([]byte(valueVengeance)),
		strings.TrimPrefix(fmt.Sprintf("%#v", []byte(valueVengeance)), "[]byte{")[:20])
	type direct struct{ v Value }
	type inSlice struct{ vs []Value }
	type inMap struct{ m map[string]Value }
	type inArray struct{ a [2]Value }
	type nested struct {
		name  string
		inner struct{ d direct }
	}
	n := nested{name: "batcomputer"}
	n.inner.d = direct{v: v}
	shapes := map[string]any{
		"direct field": direct{v: v}, "slice": inSlice{vs: []Value{v}}, "map value": inMap{m: map[string]Value{"gate": v}},
		"array": inArray{a: [2]Value{v, v}}, "nested struct": n,
		"*direct field": &direct{v: v}, "*slice": &inSlice{vs: []Value{v}}, "*map value": &inMap{m: map[string]Value{"gate": v}},
		"*array": &inArray{a: [2]Value{v, v}}, "*nested struct": &n, "*Value field": struct{ p *Value }{&v},
	}
	for name, x := range shapes {
		outs := map[string]string{}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
			outs[verb] = fmt.Sprintf(verb, x)
		}
		var text, js bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("the batcomputer", "holder", x)
		slog.New(slog.NewJSONHandler(&js, nil)).Info("the batcomputer", "holder", x)
		outs["slog text"], outs["slog json"] = text.String(), js.String()
		for path, out := range outs {
			for _, l := range leaks {
				if strings.Contains(out, l) {
					t.Errorf("a Value in a %s formatted with %s leaks its bytes: %s", name, path, out)
					break
				}
			}
		}
	}
	if v.Reveal() != valueVengeance {
		t.Fatal("formatting changed the Value")
	}
}

// TestValue_ZeroAndOwnership (a test name coined by the F3-03 follow-up):
// the zero Value is empty and safe to read; valueOf takes ownership of its
// bytes and raw returns them in place, which the store relies on to wipe.
func TestValue_ZeroAndOwnership(t *testing.T) {
	var z Value
	if !z.IsZero() || z.Reveal() != "" || len(z.Bytes()) != 0 || z.raw() != nil {
		t.Fatal("the zero Value is not empty")
	}
	if !NewValue("").IsZero() {
		t.Fatal("NewValue(\"\") is not zero")
	}
	b := []byte(valueClock)
	v := valueOf(b)
	clear(v.raw())
	if b[0] != 0 || !bytes.Equal(v.raw(), make([]byte, len(valueClock))) {
		t.Fatal("raw is not the Value's own bytes")
	}
}
