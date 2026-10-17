// file: internal/server/handlers/opsv3/schema.go
// version: 1.0.0
// guid: ac48b1e9-48d5-4112-ba4c-7fc672487e52
// last-edited: 2026-10-17

package opsv3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Violation is one place params break the schema. Path is a JSON pointer into
// params ("" is the whole body, "/book_ids/2" an array element).
type Violation struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// maxViolations bounds the report so a hostile body cannot make the answer
// huge; the first ones are the ones to fix.
const maxViolations = 50

// ValidateParams checks params against a JSON Schema and returns every
// violation (up to maxViolations), nothing when params are valid. The error is
// for a schema this checker cannot honour: it supports the keywords below and
// refuses any other validation keyword instead of passing params it never
// checked.
//
// The repository imports no JSON Schema library (go.mod has none), and a new
// dependency is a decision for the owner, so this is a small checker for the
// subset a params struct produces: type, enum, const, properties, required,
// additionalProperties, items, minItems, maxItems, uniqueItems, minLength,
// maxLength, pattern, minimum, maximum, exclusiveMinimum, exclusiveMaximum,
// multipleOf, allOf, anyOf, oneOf and not. Annotation keywords ($schema, $id,
// title, description, default, examples, format, $comment, deprecated,
// readOnly, writeOnly) are accepted and ignored; format is advisory in the
// specification. $ref and the conditional keywords are not supported.
func ValidateParams(schema, params []byte) ([]Violation, error) {
	var root any
	if err := decodeJSON(schema, &root); err != nil {
		return nil, fmt.Errorf("params schema is not JSON: %w", err)
	}
	var doc any
	if err := decodeJSON(params, &doc); err != nil {
		return []Violation{{Path: "", Message: "params are not valid JSON: " + err.Error()}}, nil
	}
	v := &validator{}
	v.check(root, doc, "")
	if v.err != nil {
		return nil, v.err
	}
	sort.SliceStable(v.out, func(i, j int) bool { return v.out[i].Path < v.out[j].Path })
	return v.out, nil
}

func decodeJSON(b []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}

type validator struct {
	out []Violation
	err error
}

var annotationKeywords = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "title": true, "description": true,
	"default": true, "examples": true, "format": true, "deprecated": true,
	"readOnly": true, "writeOnly": true,
}

func (v *validator) add(path, msg string) {
	if len(v.out) < maxViolations {
		v.out = append(v.out, Violation{Path: path, Message: msg})
	}
}

func (v *validator) fail(err error) {
	if v.err == nil {
		v.err = err
	}
}

// check validates doc against one schema node. A boolean schema is true (any)
// or false (nothing).
func (v *validator) check(schema, doc any, path string) {
	switch s := schema.(type) {
	case bool:
		if !s {
			v.add(path, "no value is allowed here")
		}
		return
	case map[string]any:
		v.checkObject(s, doc, path)
	default:
		v.fail(fmt.Errorf("schema node at %q is neither an object nor a boolean", path))
	}
}

func (v *validator) checkObject(s map[string]any, doc any, path string) {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		arg := s[k]
		switch k {
		case "type":
			v.checkType(arg, doc, path)
		case "enum":
			list, _ := arg.([]any)
			if !slices.ContainsFunc(list, func(e any) bool { return jsonEqual(e, doc) }) {
				v.add(path, "must be one of the allowed values")
			}
		case "const":
			if !jsonEqual(arg, doc) {
				v.add(path, "must equal the required constant")
			}
		case "properties", "required", "additionalProperties":
			// handled together below, once per object
		case "items", "minItems", "maxItems", "uniqueItems":
		case "minLength", "maxLength", "pattern":
			v.checkString(k, arg, doc, path)
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
			v.checkNumber(k, arg, doc, path)
		case "allOf":
			for _, sub := range asList(arg) {
				v.check(sub, doc, path)
			}
		case "anyOf":
			if !v.anyMatch(asList(arg), doc, path) {
				v.add(path, "must match at least one of the allowed shapes")
			}
		case "oneOf":
			if n := v.countMatches(asList(arg), doc, path); n != 1 {
				v.add(path, fmt.Sprintf("must match exactly one of the allowed shapes (matches %d)", n))
			}
		case "not":
			if v.countMatches([]any{arg}, doc, path) > 0 {
				v.add(path, "must not match the excluded shape")
			}
		default:
			if !annotationKeywords[k] {
				v.fail(fmt.Errorf("schema keyword %q at %q is not supported", k, path))
			}
		}
	}
	switch d := doc.(type) {
	case map[string]any:
		v.checkProperties(s, d, path)
	case []any:
		v.checkArray(s, d, path)
	}
}

func asList(a any) []any { l, _ := a.([]any); return l }

// matches reports whether doc is valid against sub without recording its
// violations.
func (v *validator) matches(sub, doc any, path string) bool {
	probe := &validator{}
	probe.check(sub, doc, path)
	if probe.err != nil {
		v.fail(probe.err)
		return false
	}
	return len(probe.out) == 0
}

func (v *validator) anyMatch(subs []any, doc any, path string) bool {
	return v.countMatches(subs, doc, path) > 0
}

func (v *validator) countMatches(subs []any, doc any, path string) int {
	n := 0
	for _, sub := range subs {
		if v.matches(sub, doc, path) {
			n++
		}
	}
	return n
}

func (v *validator) checkType(arg, doc any, path string) {
	var want []string
	switch t := arg.(type) {
	case string:
		want = []string{t}
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				want = append(want, s)
			}
		}
	}
	for _, w := range want {
		if typeIs(w, doc) {
			return
		}
	}
	v.add(path, "must be "+strings.Join(want, " or ")+", not "+typeName(doc))
}

func typeIs(want string, doc any) bool {
	switch want {
	case "object":
		_, ok := doc.(map[string]any)
		return ok
	case "array":
		_, ok := doc.([]any)
		return ok
	case "string":
		_, ok := doc.(string)
		return ok
	case "boolean":
		_, ok := doc.(bool)
		return ok
	case "null":
		return doc == nil
	case "number":
		_, ok := doc.(json.Number)
		return ok
	case "integer":
		n, ok := doc.(json.Number)
		if !ok {
			return false
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f) && !math.IsInf(f, 0)
	}
	return false
}

func typeName(doc any) string {
	switch doc.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	case json.Number:
		return "a number"
	}
	return "an unknown value"
}

func (v *validator) checkProperties(s, d map[string]any, path string) {
	props, _ := s["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if val, ok := d[n]; ok {
			v.check(props[n], val, path+"/"+escapePointer(n))
		}
	}
	for _, r := range asList(s["required"]) {
		if name, ok := r.(string); ok {
			if _, present := d[name]; !present {
				v.add(path+"/"+escapePointer(name), "is required")
			}
		}
	}
	extra, has := s["additionalProperties"]
	if !has {
		return
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		if _, declared := props[k]; !declared {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if b, isBool := extra.(bool); isBool {
			if !b {
				v.add(path+"/"+escapePointer(k), "is not a known parameter")
			}
			continue
		}
		v.check(extra, d[k], path+"/"+escapePointer(k))
	}
}

func (v *validator) checkArray(s map[string]any, d []any, path string) {
	if n, ok := num(s["minItems"]); ok && float64(len(d)) < n {
		v.add(path, fmt.Sprintf("needs at least %d items", int(n)))
	}
	if n, ok := num(s["maxItems"]); ok && float64(len(d)) > n {
		v.add(path, fmt.Sprintf("allows at most %d items", int(n)))
	}
	if u, _ := s["uniqueItems"].(bool); u {
		for i := range d {
			for j := range i {
				if jsonEqual(d[i], d[j]) {
					v.add(fmt.Sprintf("%s/%d", path, i), fmt.Sprintf("repeats item %d", j))
					break
				}
			}
		}
	}
	if items, ok := s["items"]; ok {
		for i, e := range d {
			v.check(items, e, fmt.Sprintf("%s/%d", path, i))
		}
	}
}

func (v *validator) checkString(kw string, arg, doc any, path string) {
	str, ok := doc.(string)
	if !ok {
		return
	}
	switch kw {
	case "minLength":
		if n, ok := num(arg); ok && float64(len([]rune(str))) < n {
			v.add(path, fmt.Sprintf("needs at least %d characters", int(n)))
		}
	case "maxLength":
		if n, ok := num(arg); ok && float64(len([]rune(str))) > n {
			v.add(path, fmt.Sprintf("allows at most %d characters", int(n)))
		}
	case "pattern":
		p, _ := arg.(string)
		re, err := regexp.Compile(p)
		if err != nil {
			v.fail(fmt.Errorf("schema pattern %q at %q does not compile: %w", p, path, err))
			return
		}
		if !re.MatchString(str) {
			v.add(path, "does not match the required pattern")
		}
	}
}

func (v *validator) checkNumber(kw string, arg, doc any, path string) {
	n, ok := doc.(json.Number)
	if !ok {
		return
	}
	got, err := n.Float64()
	if err != nil {
		return
	}
	lim, ok := num(arg)
	if !ok {
		v.fail(fmt.Errorf("schema keyword %q at %q needs a number", kw, path))
		return
	}
	switch kw {
	case "minimum":
		if got < lim {
			v.add(path, fmt.Sprintf("must be at least %v", lim))
		}
	case "maximum":
		if got > lim {
			v.add(path, fmt.Sprintf("must be at most %v", lim))
		}
	case "exclusiveMinimum":
		if got <= lim {
			v.add(path, fmt.Sprintf("must be greater than %v", lim))
		}
	case "exclusiveMaximum":
		if got >= lim {
			v.add(path, fmt.Sprintf("must be less than %v", lim))
		}
	case "multipleOf":
		if lim > 0 && math.Abs(got/lim-math.Round(got/lim)) > 1e-9 {
			v.add(path, fmt.Sprintf("must be a multiple of %v", lim))
		}
	}
}

func num(a any) (float64, bool) {
	n, ok := a.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// jsonEqual compares two decoded JSON values (numbers by value).
func jsonEqual(a, b any) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok || bok {
		if !(aok && bok) {
			return false
		}
		af, e1 := an.Float64()
		bf, e2 := bn.Float64()
		return e1 == nil && e2 == nil && af == bf
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !jsonEqual(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}
