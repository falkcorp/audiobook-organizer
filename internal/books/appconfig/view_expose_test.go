// file: internal/books/appconfig/view_expose_test.go
// version: 1.0.0
// guid: 5e1a7c93-2b64-4d08-9f3a-c6d8b0e41a72
// last-edited: 2026-11-19

package appconfig

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// fill sets every field reachable from v to a non-zero value, so a field that
// json.Marshal drops for omitempty is still emitted and one the projection
// shows carries something to leak.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("sentinel-value")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k)
		fill(e)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fill(p.Elem())
		v.Set(p)
	}
}

func populated() config.Config {
	var c config.Config
	fill(reflect.ValueOf(&c).Elem())
	return c
}

// hasPath reports whether the JSON tree has a value at segs.
func hasPath(tree map[string]any, segs []string) bool {
	var cur any = tree
	for _, s := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[s]; !ok {
			return false
		}
	}
	return true
}

func viewOverPopulated(t *testing.T) (config.View, config.Config) {
	t.Helper()
	base := populated()
	e := New(newFake(), nil, WithBase(func() config.Config { return base }))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e.View(), base
}

// TestView_NeverShowsAJSONDashField: a Config field tagged json:"-" is
// environment-only; HEAD's GET /config never emitted it. Over a Config in which
// every field holds a value, no such field's key carries one in View. The set
// is found by walking the type's tags, so the test names no field.
func TestView_NeverShowsAJSONDashField(t *testing.T) {
	v, _ := viewOverPopulated(t)
	hidden := map[string]bool{}
	var walk func(rt reflect.Type, path []string)
	walk = func(rt reflect.Type, path []string) {
		for i := range rt.NumField() {
			f := rt.Field(i)
			name := fieldName(f)
			if name == "" {
				continue
			}
			p := append(slices.Clone(path), name)
			if j, _, _ := strings.Cut(f.Tag.Get("json"), ","); j == "-" {
				hidden[strings.Join(p, ".")] = true
			} else if f.Type.Kind() == reflect.Struct {
				walk(f.Type, p)
			}
		}
	}
	walk(reflect.TypeFor[config.Config](), nil)
	if len(hidden) < 10 {
		t.Fatalf("found %d json:\"-\" fields; the walk is broken (SMTP*, CF*, ... are tagged so)", len(hidden))
	}
	rowsHit := 0
	for _, kv := range v.Keys {
		if hidden[kv.Name] {
			rowsHit++
			if kv.Value != nil || kv.Secret != nil {
				t.Errorf("%s is json:\"-\" and View shows %s", kv.Name, kv.Value)
			}
		}
	}
	// smtp_username is the field the leak was found on: it must be a row, or the
	// loop above proves nothing about it.
	if rowsHit == 0 || !slices.ContainsFunc(v.Keys, func(kv config.KeyView) bool { return kv.Name == "smtp_username" }) {
		t.Fatalf("no json:\"-\" field is a row of the view (hit %d)", rowsHit)
	}
}

// TestView_ValuesAreWhatHEADMarshalled: a key has a Value exactly when HEAD's
// json.Marshal(Config) holds its path and the row is not a write-only secret
// (HEAD masked those; View carries SecretMeta instead). Neither set is typed in:
// one is the marshalled tree, the other the table's classes.
func TestView_ValuesAreWhatHEADMarshalled(t *testing.T) {
	v, base := viewOverPopulated(t)
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	class := map[string]cutover.Class{}
	for _, r := range cutover.Table().Rows {
		class[r.Head] = r.Class
	}
	var got, want []string
	for _, kv := range v.Keys {
		if kv.Value != nil {
			got = append(got, kv.Name)
		}
		if class[kv.Name] != cutover.ClassSecret && hasPath(tree, strings.Split(kv.Name, ".")) {
			want = append(want, kv.Name)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if len(want) < 50 {
		t.Fatalf("only %d keys expected a value; the fixture is not populating Config", len(want))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("keys with a Value differ from HEAD's marshalled keys\n only in View: %v\n only in HEAD:  %v", diff(got, want), diff(want, got))
	}
}

func diff(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

// TestExposed_FollowsTheTags pins the rule itself on the cases that matter: a
// json:"-" field, a field below one, an unknown name, and a plain field.
func TestExposed_FollowsTheTags(t *testing.T) {
	for _, c := range []struct {
		head string
		want bool
	}{
		{"smtp_username", false}, {"smtp_host", false}, {"public_base_url", false}, {"abs_jwt_secret", false},
		{"cf_policy_api_token", false}, {"bootstrap_key_ttl", false}, {"log_level", true}, {"scan_on_startup", true},
		{"no_such_field", false}, {"log_level.deeper", false},
	} {
		if got := exposed(strings.Split(c.head, ".")); got != c.want {
			t.Errorf("exposed(%q) = %v, want %v", c.head, got, c.want)
		}
	}
}
