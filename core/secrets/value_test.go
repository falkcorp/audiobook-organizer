// file: core/secrets/value_test.go
// version: 1.0.2
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
