// file: core/secrets/value_test.go
// version: 1.0.0
// guid: adb6b331-c577-44b6-8aa4-93c55c31c6e3
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"encoding/base64"
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
	for path, s := range formatted(m) {
		if strings.Contains(s, hexed) || strings.Contains(strings.ToLower(s), "b4b4b4") {
			t.Errorf("%s leaks key material", path)
		}
	}
	b64 := base64.StdEncoding.EncodeToString(m)[:12]
	k := keyringKey{ID: "k1", Use: useData, Status: statusActive, Material: m}
	for path, s := range formatted(k) {
		if strings.Contains(strings.ToLower(s), "b4b4b4") || strings.Contains(s, "180 180") || strings.Contains(s, b64) {
			t.Errorf("a keyring key formatted with %s leaks its material", path)
		}
	}
}
