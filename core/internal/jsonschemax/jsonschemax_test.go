// file: core/internal/jsonschemax/jsonschemax_test.go
// version: 1.0.0
// guid: 5016fd2a-6569-4222-bb3d-fb1c7b6a6639
// last-edited: 2026-11-06

package jsonschemax

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// TestBase_OwnsTimeOnly: Base is the one owner of time.Time and of nothing
// else (05-config §1.10.2), and returns fresh values every call (03 R5).
func TestBase_OwnsTimeOnly(t *testing.T) {
	b := Base()
	if len(b) != 1 {
		t.Fatalf("Base has %d entries, want exactly time.Time", len(b))
	}
	s := b[reflect.TypeFor[time.Time]()]
	if s == nil || s.Type != "string" || s.Format != "date-time" {
		t.Fatalf("Base()[time.Time] = %+v, want string/date-time", s)
	}

	// Fresh map and fresh schema per call: mutating one never leaks.
	b[reflect.TypeFor[int]()] = &jsonschema.Schema{Type: "string"}
	s.Format = "mutated"
	again := Base()
	if len(again) != 1 {
		t.Fatalf("a second Base() has %d entries after mutating the first", len(again))
	}
	if got := again[reflect.TypeFor[time.Time]()].Format; got != "date-time" {
		t.Fatalf("a second Base() time.Time format = %q, want date-time", got)
	}
}

// TestOverrides_DuplicateTypePanics: a type has one owner.
func TestOverrides_DuplicateTypePanics(t *testing.T) {
	a := Overrides{reflect.TypeFor[time.Duration](): {Type: "string"}}
	b := Overrides{reflect.TypeFor[time.Duration](): {Type: "integer"}}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("With did not panic on a duplicate type")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "time.Duration") {
			t.Fatalf("panic %q does not name the type", r)
		}
	}()
	a.With(b)
}

// TestOverrides_BaseDuplicatePanics: the composition's own rule, a second
// owner of time.Time is refused.
func TestOverrides_BaseDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Base().With(a table naming time.Time) did not panic")
		}
	}()
	Base().With(Overrides{reflect.TypeFor[time.Time](): {Type: "string"}})
}

// TestOverrides_WithIsAUnionOfCopies: With returns a new table and touches
// neither operand, nor shares schemas with them.
func TestOverrides_WithIsAUnionOfCopies(t *testing.T) {
	dur := reflect.TypeFor[time.Duration]()
	a := Base()
	b := Overrides{dur: {Type: "string", Format: "duration"}}
	got := a.With(b)

	if len(got) != 2 {
		t.Fatalf("union has %d entries, want 2", len(got))
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("With changed an operand: len(a)=%d len(b)=%d", len(a), len(b))
	}
	got[dur].Format = "mutated"
	got[reflect.TypeFor[time.Time]()].Format = "mutated"
	if b[dur].Format != "duration" || a[reflect.TypeFor[time.Time]()].Format != "date-time" {
		t.Fatal("With shares a schema with an operand")
	}

	// A nil or empty operand is the identity.
	if n := len(Overrides(nil).With(nil)); n != 0 {
		t.Fatalf("nil.With(nil) has %d entries", n)
	}
	if n := len(Base().With(nil)); n != 1 {
		t.Fatalf("Base().With(nil) has %d entries, want 1", n)
	}
}

type stamped struct {
	At    time.Time  `json:"at"`
	Maybe *time.Time `json:"maybe,omitempty"`
	Name  string     `json:"name"`
}

// TestFor_UsesOverrides: For renders time.Time per the table it is given.
func TestFor_UsesOverrides(t *testing.T) {
	plain, err := For[stamped](nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.Properties["at"].Format; got != "" {
		t.Fatalf("nil overrides: at.format = %q, want none", got)
	}

	s, err := For[stamped](Base())
	if err != nil {
		t.Fatal(err)
	}
	if at := s.Properties["at"]; at.Type != "string" || at.Format != "date-time" {
		t.Fatalf("at = %+v, want string/date-time", at)
	}
	// A pointer keeps the override and gains null.
	maybe := s.Properties["maybe"]
	if maybe.Format != "date-time" || !reflect.DeepEqual(maybe.Types, []string{"null", "string"}) {
		t.Fatalf("maybe = %+v, want [null string]/date-time", maybe)
	}
}

type section struct {
	Concurrent int      `json:"concurrent" schema:"minimum=1,maximum=64"`
	Ratio      *float64 `json:"ratio,omitempty" schema:"minimum=0.5"`
	Level      string   `json:"level" schema:"enum=debug|info|warn"`
	Retries    int      `json:"retries" schema:"enum=1|2|4"`
	Strict     bool     `json:"strict" schema:"enum=true"`
	URL        string   `json:"url" schema:"format=uri" sensitive:"true"`
	Slug       string   `json:"slug" schema:"format=path,pattern=^[a-z]+(,[a-z]+)*$"`
	Dir        string   `json:"dir" reload:"restart" protect:"admin"`
	Boot       string   `json:"boot" reload:"boot" protect:"operator"`
	Plain      string   `json:"plain" sensitive:"false"`
	Skipped    string   `json:"-" schema:"minimum=oops"`
	hidden     string   `schema:"minimum=oops"` // unexported: never walked
}

// TestFor_TagPass maps the 05-config §1.3 tags onto keywords and Extra.
func TestFor_TagPass(t *testing.T) {
	_ = section{}.hidden
	s, err := For[section](nil)
	if err != nil {
		t.Fatal(err)
	}
	p := s.Properties

	if p["concurrent"].Minimum == nil || *p["concurrent"].Minimum != 1 || *p["concurrent"].Maximum != 64 {
		t.Errorf("concurrent min/max = %v/%v, want 1/64", p["concurrent"].Minimum, p["concurrent"].Maximum)
	}
	if r := p["ratio"]; r.Minimum == nil || *r.Minimum != 0.5 {
		t.Errorf("ratio minimum = %v, want 0.5 (a pointer field keeps its keywords)", r.Minimum)
	}
	if !reflect.DeepEqual(p["level"].Enum, []any{"debug", "info", "warn"}) {
		t.Errorf("level enum = %#v", p["level"].Enum)
	}
	if !reflect.DeepEqual(p["retries"].Enum, []any{int64(1), int64(2), int64(4)}) {
		t.Errorf("retries enum = %#v, want integers", p["retries"].Enum)
	}
	if !reflect.DeepEqual(p["strict"].Enum, []any{true}) {
		t.Errorf("strict enum = %#v", p["strict"].Enum)
	}
	if u := p["url"]; u.Format != "uri" || u.Extra["x-mediacore-sensitive"] != true {
		t.Errorf("url = format %q extra %v, want uri + sensitive", u.Format, u.Extra)
	}
	// pattern takes the rest of the tag, commas included.
	if sl := p["slug"]; sl.Format != "path" || sl.Pattern != "^[a-z]+(,[a-z]+)*$" {
		t.Errorf("slug = format %q pattern %q", sl.Format, sl.Pattern)
	}
	if d := p["dir"]; d.Extra["x-mediacore-reload"] != "restart" || d.Extra["x-mediacore-protect"] != "admin" {
		t.Errorf("dir extra = %v", d.Extra)
	}
	if b := p["boot"]; b.Extra["x-mediacore-reload"] != "boot" || b.Extra["x-mediacore-protect"] != "operator" {
		t.Errorf("boot extra = %v", b.Extra)
	}
	if len(p["plain"].Extra) != 0 {
		t.Errorf("plain extra = %v, want none (sensitive:false is a no-op)", p["plain"].Extra)
	}
	if _, ok := p["Skipped"]; ok {
		t.Error(`a json:"-" field became a property`)
	}

	// The vendor keywords survive marshalling as top-level keywords.
	out, err := json.Marshal(p["dir"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"x-mediacore-reload":"restart"`) || !strings.Contains(string(out), `"x-mediacore-protect":"admin"`) {
		t.Errorf("dir marshals to %s, want the x-mediacore-* keywords", out)
	}
}

type inner struct {
	Port int `json:"port" schema:"minimum=1,maximum=65535"`
}

type base struct {
	Name string `json:"name" schema:"enum=a|b"`
}

type nested struct {
	base                    // embedded: its field is promoted, its tags still apply
	Direct inner            `json:"direct"`
	Ptr    *inner           `json:"ptr,omitempty"`
	List   []inner          `json:"list"`
	ByName map[string]inner `json:"by_name"`
	Last   time.Time        `json:"last"`
}

// TestFor_TagPassWalksNestedTypes: structs behind fields, pointers, slices,
// maps and embedded structs all get their tags applied.
func TestFor_TagPassWalksNestedTypes(t *testing.T) {
	s, err := For[nested](Base())
	if err != nil {
		t.Fatal(err)
	}
	port := func(sc *jsonschema.Schema) *jsonschema.Schema { return sc.Properties["port"] }
	for name, sc := range map[string]*jsonschema.Schema{
		"direct":  s.Properties["direct"],
		"ptr":     s.Properties["ptr"],
		"list":    s.Properties["list"].Items,
		"by_name": s.Properties["by_name"].AdditionalProperties,
	} {
		p := port(sc)
		if p == nil || p.Minimum == nil || *p.Minimum != 1 || *p.Maximum != 65535 {
			t.Errorf("%s.port = %+v, want min 1 max 65535", name, p)
		}
	}
	if !reflect.DeepEqual(s.Properties["name"].Enum, []any{"a", "b"}) {
		t.Errorf("promoted embedded field enum = %#v", s.Properties["name"].Enum)
	}
}

// TestFor_TagPassRefusesWhatItCannotHonour: a tag the pass cannot apply is an
// error naming the field, never a silent drop.
func TestFor_TagPassRefusesWhatItCannotHonour(t *testing.T) {
	type badReload struct {
		X string `json:"x" reload:"live"`
	}
	type badProtect struct {
		X string `json:"x" protect:"root"`
	}
	type badSensitive struct {
		X string `json:"x" sensitive:"yes"`
	}
	type unknownKey struct {
		X int `json:"x" schema:"multipleOf=2"`
	}
	type noEquals struct {
		X int `json:"x" schema:"minimum"`
	}
	type emptyValue struct {
		X int `json:"x" schema:"minimum="`
	}
	type dupKey struct {
		X int `json:"x" schema:"minimum=1,minimum=2"`
	}
	type badNumber struct {
		X int `json:"x" schema:"minimum=abc"`
	}
	type minOnString struct {
		X string `json:"x" schema:"minimum=1"`
	}
	type formatOnInt struct {
		X int `json:"x" schema:"format=uri"`
	}
	type badFormat struct {
		X string `json:"x" schema:"format=ipv4"`
	}
	type patternOnInt struct {
		X int `json:"x" schema:"pattern=^1$"`
	}
	type enumOnSlice struct {
		X []string `json:"x" schema:"enum=a|b"`
	}
	type enumBadInt struct {
		X int `json:"x" schema:"enum=1|b"`
	}
	type enumEmptyItem struct {
		X string `json:"x" schema:"enum=a||b"`
	}

	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{"reload", func() error { _, err := For[badReload](nil); return err }, "reload"},
		{"protect", func() error { _, err := For[badProtect](nil); return err }, "protect"},
		{"sensitive", func() error { _, err := For[badSensitive](nil); return err }, "sensitive"},
		{"unknown key", func() error { _, err := For[unknownKey](nil); return err }, "multipleOf"},
		{"no equals", func() error { _, err := For[noEquals](nil); return err }, "key=value"},
		{"empty value", func() error { _, err := For[emptyValue](nil); return err }, "empty value"},
		{"duplicate key", func() error { _, err := For[dupKey](nil); return err }, "duplicate"},
		{"bad number", func() error { _, err := For[badNumber](nil); return err }, "minimum"},
		{"minimum on string", func() error { _, err := For[minOnString](nil); return err }, "integer or number"},
		{"format on int", func() error { _, err := For[formatOnInt](nil); return err }, "string field"},
		{"bad format", func() error { _, err := For[badFormat](nil); return err }, "ipv4"},
		{"pattern on int", func() error { _, err := For[patternOnInt](nil); return err }, "string field"},
		{"enum on slice", func() error { _, err := For[enumOnSlice](nil); return err }, "enum needs"},
		{"enum bad int", func() error { _, err := For[enumBadInt](nil); return err }, "enum"},
		{"enum empty item", func() error { _, err := For[enumEmptyItem](nil); return err }, "empty value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run()
			if err == nil {
				t.Fatal("For accepted the tag")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
			if !strings.Contains(err.Error(), ".X") {
				t.Fatalf("error %q does not name the field", err)
			}
		})
	}
}

// TestFor_ReflectionErrorIsWrapped: jsonschema-go's own refusals surface.
func TestFor_ReflectionErrorIsWrapped(t *testing.T) {
	type withFunc struct {
		F func() `json:"f"`
	}
	if _, err := For[withFunc](nil); err == nil || !strings.HasPrefix(err.Error(), "jsonschemax: ") {
		t.Fatalf("For[struct with func] error = %v, want a jsonschemax-prefixed error", err)
	}
}
