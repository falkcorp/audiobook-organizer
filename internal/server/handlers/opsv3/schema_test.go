// file: internal/server/handlers/opsv3/schema_test.go
// version: 1.1.0
// guid: f13805e1-d927-47ab-a430-ed168f26e8e2
// last-edited: 2026-10-17

package opsv3

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestValidateParams(t *testing.T) {
	const schema = `{"type":"object","required":["ids","mode"],"additionalProperties":false,
	 "properties":{
	  "ids":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"string","pattern":"^b[0-9]+$"}},
	  "mode":{"enum":["a","b"]},
	  "n":{"type":"integer","minimum":1,"maximum":10},
	  "name":{"type":["string","null"],"minLength":2,"maxLength":4},
	  "nested":{"type":"object","properties":{"x":{"type":"boolean"}}},
	  "either":{"oneOf":[{"type":"string"},{"type":"integer"}]}
	 }}`
	cases := []struct {
		name, params string
		want         []string // violation paths
	}{
		{"valid", `{"ids":["b1","b2"],"mode":"a","n":3,"name":null,"either":4}`, nil},
		{"missing required", `{}`, []string{"/ids", "/mode"}},
		{"wrong types", `{"ids":"b1","mode":"c","n":1.5}`, []string{"/ids", "/mode", "/n"}},
		{"array items", `{"ids":["b1","x","b1"],"mode":"a"}`, []string{"/ids/1", "/ids/2"}},
		{"bounds", `{"ids":["b1"],"mode":"a","n":11,"name":"abcde"}`, []string{"/n", "/name"}},
		{"unknown key", `{"ids":["b1"],"mode":"a","zzz":1}`, []string{"/zzz"}},
		{"nested", `{"ids":["b1"],"mode":"a","nested":{"x":"no"}}`, []string{"/nested/x"}},
		{"oneOf neither", `{"ids":["b1"],"mode":"a","either":true}`, []string{"/either"}},
		{"not json", `{`, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateParams([]byte(schema), []byte(tc.params))
			if err != nil {
				t.Fatal(err)
			}
			var paths []string
			for _, v := range got {
				paths = append(paths, v.Path)
			}
			if strings.Join(paths, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("violations %v, want %v (%+v)", paths, tc.want, got)
			}
		})
	}
}

func TestValidateParamsRefusesWhatItCannotCheck(t *testing.T) {
	for _, schema := range []string{
		`{"$ref":"#/definitions/x"}`,
		`{"if":{"type":"string"},"then":{"minLength":1}}`,
		`{"type":"object","properties":{"a":{"patternProperties":{}}}}`,
		`{"type":"object","properties":{"a":{"pattern":"("}}}`,
	} {
		if _, err := ValidateParams([]byte(schema), []byte(`{"a":"x"}`)); err == nil {
			t.Errorf("schema %s was accepted although it cannot be checked", schema)
		}
	}
	if _, err := ValidateParams([]byte(`{`), []byte(`{}`)); err == nil {
		t.Error("an unparsable schema must be an error, not a pass")
	}
	// Annotations are fine.
	if v, err := ValidateParams([]byte(`{"$schema":"x","title":"t","description":"d","type":"object","default":{}}`), []byte(`{}`)); err != nil || len(v) != 0 {
		t.Errorf("annotations: %v %v", v, err)
	}
}

// TestValidateParamsMalformedKeywordsAreSchemaErrors: a supported keyword
// with an argument the checker cannot use is a schema error (the handler's
// 500), never silently skipped, and it fails whether or not the params reach
// that keyword.
func TestValidateParamsMalformedKeywordsAreSchemaErrors(t *testing.T) {
	for _, schema := range []string{
		`{"type":"array","minItems":"3"}`,
		`{"type":"array","maxItems":-1}`,
		`{"type":"array","maxItems":1.5}`,
		`{"type":"string","minLength":true}`,
		`{"type":"string","maxLength":"10"}`,
		`{"type":"object","required":"a"}`,
		`{"type":"object","required":[1]}`,
		`{"type":"object","properties":[]}`,
		`{"enum":"a"}`,
		`{"type":"array","uniqueItems":"yes"}`,
		`{"multipleOf":0}`,
		`{"minimum":"1"}`,
		`{"maximum":1e400}`,
		`{"anyOf":[]}`,
		`{"type":"strng"}`,
		`{"type":[]}`,
		`{"pattern":5}`,
		// Nested under a property the params do not carry.
		`{"type":"object","properties":{"absent":{"minLength":"x"}}}`,
		`{"type":"object","properties":{"absent":{"items":{"maxItems":"x"}}}}`,
	} {
		if v, err := ValidateParams([]byte(schema), []byte(`{"a":"x"}`)); err == nil {
			t.Errorf("schema %s was accepted (violations %v); a malformed keyword must be a schema error", schema, v)
		}
	}
}

// TestValidateParamsNumbersBeyondFloat64AreRefused: a number too large for
// float64 cannot be compared with a bound, so it is a violation, not a pass.
func TestValidateParamsNumbersBeyondFloat64AreRefused(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"n":{"type":"number","maximum":100},"m":{"type":"number","minimum":0}}}`)
	for _, params := range []string{`{"n":1e400}`, `{"m":-1e400}`} {
		v, err := ValidateParams(schema, []byte(params))
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 1 {
			t.Errorf("%s: violations %v, want one", params, v)
		}
	}
	if v, err := ValidateParams(schema, []byte(`{"n":5,"m":5}`)); err != nil || len(v) != 0 {
		t.Errorf("in-range numbers: %v %v", v, err)
	}
}

// TestValidateParamsUniqueItemsIsLinear: uniqueItems finds repeats by value
// (numbers by value, objects regardless of key order) and a large array is
// checked in linear time, not by comparing every pair.
func TestValidateParamsUniqueItemsIsLinear(t *testing.T) {
	schema := []byte(`{"type":"array","uniqueItems":true}`)
	v, err := ValidateParams(schema, []byte(`[1, 1.0, {"a":1,"b":2}, {"b":2,"a":1}, "x", 2, 1e400, 1e400]`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, x := range v {
		got[x.Path] = true
	}
	if len(v) != 2 || !got["/1"] || !got["/3"] {
		t.Errorf("violations %v, want repeats at /1 and /3 only", v)
	}

	const n = 200000
	var b strings.Builder
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(i))
	}
	b.WriteByte(']')
	start := time.Now()
	v, err = ValidateParams(schema, []byte(b.String()))
	if err != nil || len(v) != 0 {
		t.Fatalf("distinct items: %v %v", v, err)
	}
	// Pairwise comparison of 200k items is 2e10 comparisons (minutes); the
	// hash check is well under a second even under -race.
	if el := time.Since(start); el > 20*time.Second {
		t.Fatalf("uniqueItems over %d items took %s", n, el)
	}
}
