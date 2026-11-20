// file: internal/config/protected_fields_test.go
// version: 1.2.5
// guid: 4f2a8c61-d93e-4b07-a5c8-1e6b3d9f7a20
// last-edited: 2026-11-09

package config

import (
	"encoding"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The field walk below stops at a type that decodes itself, as encoding/json
// does.
var (
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// joinKeyPath is the dotted config key path of key under path.
func joinKeyPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// sensitiveFieldName matches a config field whose name says it is a path, a
// program, a database, an endpoint or a sign-in setting. Every such field must
// be classified in configFieldRules, protected or not, so adding one is a
// decision someone makes rather than a default nobody saw.
var sensitiveFieldName = regexp.MustCompile(
	`(path|dir|root|url|endpoint|host|command|cmd|binary|exec|tool|file|folder|pattern|mapping|alias|settings|location|mount|socket|plugin|_mode$|database|_db|(^|[._])auth($|[._])|oauth|cf_access|password|secret|token|_key$|login|session|owner|email|role|abs_|rate_limit)`)

// configLeafPaths lists every decodable leaf of Config as a JSON path in the
// rules' notation ("[]" for each slice element or map value).
func configLeafPaths() []string {
	var out []string
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			if pt := reflect.PointerTo(t); pt.Implements(jsonUnmarshalerType) || pt.Implements(textUnmarshalerType) || t.PkgPath() == "time" {
				out = append(out, prefix)
				return
			}
			for f := range t.Fields() {
				if !f.IsExported() {
					continue
				}
				tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if tag == "-" {
					continue
				}
				if tag == "" {
					tag = f.Name
				}
				walk(f.Type, joinKeyPath(prefix, tag))
			}
		case reflect.Slice, reflect.Map:
			et := t.Elem()
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct || et.Kind() == reflect.Map {
				walk(et, prefix+"[]")
				return
			}
			out = append(out, prefix)
		default:
			out = append(out, prefix)
		}
	}
	walk(reflect.TypeFor[Config](), "")
	return out
}

// parentFieldPath is the nearest ancestor of a rule path: "a.b[]" -> "a.b",
// "a.b" -> "a", "a" -> "". Test-only: production resolves classes through
// configFieldRules directly (it had a production twin, ConfigFieldClass, with
// no caller outside these tests until 2026-10-18).
func parentFieldPath(p string) string {
	if before, ok := strings.CutSuffix(p, "[]"); ok {
		return before
	}
	i := strings.LastIndex(p, ".")
	if i < 0 {
		return ""
	}
	return p[:i]
}

// fieldClassAt returns the class of the field at a JSON path (as the rules
// spell it), using the nearest classified ancestor.
func fieldClassAt(path string) FieldClass {
	for p := path; p != ""; p = parentFieldPath(p) {
		if r, ok := configFieldRules[p]; ok {
			return r.Class
		}
	}
	return FieldUnprotected
}

func classifiedAt(path string) bool {
	for p := path; p != ""; p = parentFieldPath(p) {
		if _, ok := configFieldRules[p]; ok {
			return true
		}
	}
	return false
}

// TestConfigFieldRules_EveryPathLikeFieldIsClassified fails when someone adds a
// path-, program-, endpoint- or sign-in-shaped field to Config without saying
// whether an API key may change it.
func TestConfigFieldRules_EveryPathLikeFieldIsClassified(t *testing.T) {
	leaves := configLeafPaths()
	if len(leaves) < 200 {
		t.Fatalf("walked only %d config leaves; the walker is broken", len(leaves))
	}
	var missing []string
	for _, p := range leaves {
		// Match on the field's own name segments, not the whole path, so a
		// harmless leaf below a classified parent is not double-counted and a
		// sensitive leaf below an unclassified parent is still caught.
		if sensitiveFieldName.MatchString(p) && !classifiedAt(p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		t.Errorf("config fields look like a path, program, endpoint or sign-in setting but have no entry in configFieldRules (protected_fields.go); classify each one:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// TestConfigFieldRules_EveryRuleResolves checks the list in the other
// direction: a rule naming a field that was renamed or removed would protect
// nothing (ChangedProtectedFields fails closed on it, but say why here).
func TestConfigFieldRules_EveryRuleResolves(t *testing.T) {
	cfg := Config{Plugins: map[string]PluginConfig{"p": {}}, MetadataSources: []MetadataSource{{}}}
	for path, rule := range configFieldRules {
		if rule.Why == "" {
			t.Errorf("rule %q has no reason", path)
		}
		if _, err := projectField(reflect.ValueOf(cfg), strings.Split(path, ".")); err != nil {
			t.Errorf("rule %q does not resolve: %v", path, err)
		}
	}
}

// Every owner trust-root field resolves and is also a sign-in rule, so an
// API key is refused on it before the owner check is even reached.
func TestOwnerTrustRootFields_AreSignInRules(t *testing.T) {
	for path, why := range ownerTrustRootFields {
		if why == "" {
			t.Errorf("trust-root field %q has no reason", path)
		}
		if fieldClassAt(path) != FieldSignIn {
			t.Errorf("trust-root field %q has field class %d, want FieldSignIn (%d)", path, fieldClassAt(path), FieldSignIn)
		}
		if _, err := projectField(reflect.ValueOf(Config{}), strings.Split(path, ".")); err != nil {
			t.Errorf("trust-root field %q does not resolve: %v", path, err)
		}
	}
}
