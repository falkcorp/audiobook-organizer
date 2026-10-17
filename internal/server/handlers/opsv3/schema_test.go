// file: internal/server/handlers/opsv3/schema_test.go
// version: 1.0.0
// guid: f13805e1-d927-47ab-a430-ed168f26e8e2
// last-edited: 2026-10-17

package opsv3

import (
	"strings"
	"testing"
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
