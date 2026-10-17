// file: internal/server/handlers/opsv3/schema.go
// version: 1.1.0
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
	"strconv"
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
//
// A supported keyword whose argument is malformed (a non-integer minItems, a
// required that is not a list of strings, a non-positive multipleOf, ...) is
// a schema error too, never ignored: ignoring it would pass params the schema
// meant to refuse. A number in params too large for float64 is a violation,
// not a pass, since no bound can be checked against it.
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
	// The whole schema is checked first, so a malformed keyword fails every
	// request, not only the ones whose params happen to reach it.
	v.wellFormed(root, "")
	if v.err != nil {
		return nil, v.err
	}
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
			if !slices.ContainsFunc(asList(arg), func(e any) bool { return jsonEqual(e, doc) }) {
				v.add(path, "must be one of the allowed values")
			}
		case "const":
			if !jsonEqual(arg, doc) {
				v.add(path, "must equal the required constant")
			}
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
		}
		// properties, required, additionalProperties, items, minItems,
		// maxItems and uniqueItems apply below, once per object or array.
	}
	switch d := doc.(type) {
	case map[string]any:
		v.checkProperties(s, d, path)
	case []any:
		v.checkArray(s, d, path)
	}
}

// wellFormed walks every schema node under node and checks each keyword's
// argument (keywordWellFormed). check() relies on it having passed.
func (v *validator) wellFormed(node any, path string) {
	if v.err != nil {
		return
	}
	s, ok := node.(map[string]any)
	if !ok {
		if _, isBool := node.(bool); !isBool {
			v.fail(fmt.Errorf("schema node at %q is neither an object nor a boolean", path))
		}
		return
	}
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !v.keywordWellFormed(k, s[k], path) {
			return
		}
		switch k {
		case "properties":
			props := s[k].(map[string]any)
			names := make([]string, 0, len(props))
			for n := range props {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				v.wellFormed(props[n], path+"/properties/"+escapePointer(n))
			}
		case "additionalProperties", "items", "not":
			v.wellFormed(s[k], path+"/"+k)
		case "allOf", "anyOf", "oneOf":
			for i, sub := range asList(s[k]) {
				v.wellFormed(sub, fmt.Sprintf("%s/%s/%d", path, k, i))
			}
		}
	}
}

// keywordWellFormed checks one keyword's argument. An unknown validation
// keyword fails too.
func (v *validator) keywordWellFormed(k string, arg any, path string) bool {
	bad := func(want string) bool {
		v.fail(fmt.Errorf("schema keyword %q at %q needs %s", k, path, want))
		return false
	}
	switch k {
	case "type":
		names, ok := typeNames(arg)
		if !ok {
			return bad("a type name or a non-empty list of them")
		}
		for _, n := range names {
			if !knownTypes[n] {
				return bad("a known type name, not " + strconv.Quote(n))
			}
		}
	case "enum":
		if _, ok := arg.([]any); !ok {
			return bad("a list")
		}
	case "const", "not", "additionalProperties", "items":
		// any value / a schema node (wellFormed walks into it)
	case "properties":
		if _, ok := arg.(map[string]any); !ok {
			return bad("an object")
		}
	case "required":
		if !isStringList(arg) {
			return bad("a list of strings")
		}
	case "minItems", "maxItems", "minLength", "maxLength":
		if _, ok := count(arg); !ok {
			return bad("a non-negative integer")
		}
	case "uniqueItems":
		if _, ok := arg.(bool); !ok {
			return bad("true or false")
		}
	case "pattern":
		p, ok := arg.(string)
		if !ok {
			return bad("a string")
		}
		if _, err := regexp.Compile(p); err != nil {
			v.fail(fmt.Errorf("schema pattern %q at %q does not compile: %w", p, path, err))
			return false
		}
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
		if _, ok := num(arg); !ok {
			return bad("a number")
		}
	case "multipleOf":
		if n, ok := num(arg); !ok || n <= 0 {
			return bad("a number greater than 0")
		}
	case "allOf", "anyOf", "oneOf":
		if l, ok := arg.([]any); !ok || len(l) == 0 {
			return bad("a non-empty list")
		}
	default:
		if !annotationKeywords[k] {
			v.fail(fmt.Errorf("schema keyword %q at %q is not supported", k, path))
			return false
		}
	}
	return true
}

var knownTypes = map[string]bool{"object": true, "array": true, "string": true, "boolean": true, "null": true, "number": true, "integer": true}

// typeNames reads a type keyword: one name or a non-empty list of names.
func typeNames(arg any) ([]string, bool) {
	switch t := arg.(type) {
	case string:
		return []string{t}, true
	case []any:
		if len(t) == 0 {
			return nil, false
		}
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

func isStringList(a any) bool {
	l, ok := a.([]any)
	if !ok {
		return false
	}
	for _, e := range l {
		if _, ok := e.(string); !ok {
			return false
		}
	}
	return true
}

// count reads a non-negative integer keyword argument.
func count(a any) (int, bool) {
	n, ok := a.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		f, ferr := n.Float64()
		if ferr != nil || f != math.Trunc(f) || f < 0 || f > math.MaxInt32 {
			return 0, false
		}
		return int(f), true
	}
	if i < 0 || i > math.MaxInt32 {
		return 0, false
	}
	return int(i), true
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
	want, _ := typeNames(arg)
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
	if n, ok := count(s["minItems"]); ok && len(d) < n {
		v.add(path, fmt.Sprintf("needs at least %d items", n))
	}
	if n, ok := count(s["maxItems"]); ok && len(d) > n {
		v.add(path, fmt.Sprintf("allows at most %d items", n))
	}
	if u, _ := s["uniqueItems"].(bool); u {
		// One canonical encoding per item and a map: linear in the size of
		// the array, where comparing every pair would be quadratic in
		// caller-supplied input.
		first := make(map[string]int, len(d))
		for i, e := range d {
			key, comparable := canonicalJSON(e)
			if !comparable {
				continue // holds a number jsonEqual never calls equal
			}
			if j, seen := first[key]; seen {
				v.add(fmt.Sprintf("%s/%d", path, i), fmt.Sprintf("repeats item %d", j))
				continue
			}
			first[key] = i
		}
	}
	if items, ok := s["items"]; ok {
		for i, e := range d {
			v.check(items, e, fmt.Sprintf("%s/%d", path, i))
		}
	}
}

// canonicalJSON encodes a decoded JSON value so that two values jsonEqual
// calls equal encode identically: object keys sorted, numbers by their
// float64 value (the comparison jsonEqual makes). comparable is false when the
// value holds a number beyond float64, which jsonEqual calls equal to nothing.
func canonicalJSON(x any) (key string, comparable bool) {
	var b strings.Builder
	comparable = true
	writeCanonical(&b, x, &comparable)
	return b.String(), comparable
}

func writeCanonical(b *strings.Builder, x any, comparable *bool) {
	switch t := x.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(k))
			b.WriteByte(':')
			writeCanonical(b, t[k], comparable)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonical(b, e, comparable)
		}
		b.WriteByte(']')
	case json.Number:
		if f, err := t.Float64(); err == nil {
			b.WriteString("n" + strconv.FormatFloat(f, 'g', -1, 64))
		} else {
			*comparable = false
		}
	case string:
		b.WriteString(strconv.Quote(t))
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case nil:
		b.WriteString("null")
	}
}

func (v *validator) checkString(kw string, arg, doc any, path string) {
	str, ok := doc.(string)
	if !ok {
		return
	}
	switch kw {
	case "minLength":
		if n, _ := count(arg); len([]rune(str)) < n {
			v.add(path, fmt.Sprintf("needs at least %d characters", n))
		}
	case "maxLength":
		if n, _ := count(arg); len([]rune(str)) > n {
			v.add(path, fmt.Sprintf("allows at most %d characters", n))
		}
	case "pattern":
		// wellFormed compiled it already.
		re := regexp.MustCompile(arg.(string))
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
		// Beyond float64: no bound can be compared, so it fails rather than
		// passing unchecked.
		v.add(path, "is too large a number to check against "+kw)
		return
	}
	lim, _ := num(arg) // wellFormed checked it
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
		if math.Abs(got/lim-math.Round(got/lim)) > 1e-9 {
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
