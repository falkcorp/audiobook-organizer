// file: internal/ai/openai_parser_numeric_test.go
// version: 1.0.0
// guid: 5f0c2e7a-8d41-4b6e-9a3c-2e7d1b4f6a90
// last-edited: 2026-09-13

package ai

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// numericFieldGetters maps each numeric ParsedMetadata JSON key to its value.
var numericFieldGetters = map[string]func(*ParsedMetadata) int{
	"year":          func(m *ParsedMetadata) int { return m.Year },
	"series_number": func(m *ParsedMetadata) int { return m.SeriesNum },
}

func TestDecodeResultElement_LenientNumericFields(t *testing.T) {
	type outcome int
	const (
		plain   outcome = iota // accepted, nothing recorded
		coerced                // accepted, recorded
		dropped                // zeroed, recorded
		nulled                 // zeroed, nothing recorded
	)
	cases := []struct {
		raw  string
		want int
		kind outcome
	}{
		{`2015`, 2015, plain},
		{`"2015"`, 2015, coerced},
		{`" 2015 "`, 2015, coerced},
		{`"not available"`, 0, dropped},
		{`""`, 0, dropped},
		{`null`, 0, nulled},
		{`"3.5"`, 0, dropped},
		{`3.5`, 0, dropped},
		{`3.0`, 3, coerced},
		{`"3.0"`, 3, coerced},
		{`" 3 "`, 3, coerced},
		{`-2`, -2, plain},
		{`true`, 0, dropped},
		{`{}`, 0, dropped},
		{`[1]`, 0, dropped},
		{`1e30`, 0, dropped},
		{`"NaN"`, 0, dropped},
		{`"Inf"`, 0, dropped},
		{`"0x1F"`, 0, dropped},
		{`"9223372036854775808"`, 0, dropped},
	}
	for field, get := range numericFieldGetters {
		for _, tc := range cases {
			t.Run(field+"="+tc.raw, func(t *testing.T) {
				raw := fmt.Sprintf(`{"title": "T", "author": "A", %q: %s, "confidence": "high"}`, field, tc.raw)
				m, isMetadata, err := decodeResultElement(json.RawMessage(raw))
				if err != nil {
					t.Fatalf("decode failed; a bad numeric field must not fail the result: %v", err)
				}
				if !isMetadata || m == nil {
					t.Fatalf("want a metadata result, got m=%v isMetadata=%v", m, isMetadata)
				}
				if m.Title != "T" || m.Author != "A" || m.Confidence != "high" {
					t.Errorf("other fields lost: %+v", m)
				}
				if got := get(m); got != tc.want {
					t.Errorf("%s = %d, want %d", field, got, tc.want)
				}
				switch tc.kind {
				case plain, nulled:
					if m.numericCoercions != nil {
						t.Errorf("recorded %+v for a value that needs no coercion", *m.numericCoercions)
					}
				case coerced, dropped:
					if m.numericCoercions == nil || len(*m.numericCoercions) != 1 {
						t.Fatalf("want exactly one coercion record, got %v", m.numericCoercions)
					}
					c := (*m.numericCoercions)[0]
					if c.Field != field || c.Dropped != (tc.kind == dropped) || c.Raw != tc.raw {
						t.Errorf("record = %+v, want field %q dropped=%v raw %q", c, field, tc.kind == dropped, tc.raw)
					}
				}
			})
		}
	}
}

func TestParseBatch_OneNonNumericYearDoesNotFailTheBatch(t *testing.T) {
	var elems []string
	for i := 0; i < 8; i++ {
		year := fmt.Sprintf("%d", 2000+i)
		if i == 5 {
			year = `"not available"`
		}
		elems = append(elems, fmt.Sprintf(`{"title": "Book %d", "author": "Author %d", "series_number": %d, "year": %s, "confidence": "high"}`, i, i, i+1, year))
	}
	content := `{"results": [` + strings.Join(elems, ", ") + `]}`

	results, err := parseBatchMetadataFromJSON(content, 8)
	if err != nil {
		t.Fatalf("batch failed on one non-numeric year: %v", err)
	}
	if len(results) != 8 {
		t.Fatalf("got %d results, want 8", len(results))
	}
	for i, m := range results {
		if m == nil {
			t.Fatalf("result %d is nil", i)
		}
		if m.Title != fmt.Sprintf("Book %d", i) || m.SeriesNum != i+1 {
			t.Errorf("result %d = %+v", i, m)
		}
		wantYear := 2000 + i
		if i == 5 {
			wantYear = 0
		}
		if m.Year != wantYear {
			t.Errorf("result %d year = %d, want %d", i, m.Year, wantYear)
		}
	}

	// logNumericCoercions consumes the record so a cached result does not log again.
	logNumericCoercions("book5.m4b", results[5])
	if results[5].numericCoercions != nil {
		t.Error("logNumericCoercions left the coercion record in place")
	}
}

func TestParseBatch_CountMismatchStillFailsWithLenientNumbers(t *testing.T) {
	content := `{"results": [{"title": "A", "year": "not available"}, {"title": "B", "year": "2015"}]}`
	if _, err := parseBatchMetadataFromJSON(content, 3); err == nil {
		t.Fatal("2 results for 3 filenames must still fail the batch")
	}
}

func TestParseBatch_NonObjectResultStillFails(t *testing.T) {
	content := `{"results": [{"title": "A", "year": "2015"}, "not an object"]}`
	if _, err := parseBatchMetadataFromJSON(content, 2); err == nil {
		t.Fatal("a result that is not an object must still fail the batch")
	}
}

func TestDecodeResultElement_StringFieldTypeErrorStillFails(t *testing.T) {
	// Leniency is for the numeric fields only.
	if _, _, err := decodeResultElement(json.RawMessage(`{"title": 42, "year": 2015}`)); err == nil {
		t.Fatal("a non-string title must still fail the element")
	}
}

// TestParsedMetadataWire_ShadowsEveryNumericField fails when a numeric field
// is added to ParsedMetadata without a lenientInt shadow in
// parsedMetadataWire, which would bring back the whole-batch failure.
func TestParsedMetadataWire_ShadowsEveryNumericField(t *testing.T) {
	shadows := map[string]bool{}
	wt := reflect.TypeOf(parsedMetadataWire{})
	for i := 0; i < wt.NumField(); i++ {
		f := wt.Field(i)
		if f.Type == reflect.TypeOf(lenientInt{}) {
			shadows[strings.Split(f.Tag.Get("json"), ",")[0]] = true
		}
	}
	pt := reflect.TypeOf(ParsedMetadata{})
	for i := 0; i < pt.NumField(); i++ {
		f := pt.Field(i)
		if !f.IsExported() {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			key := strings.Split(f.Tag.Get("json"), ",")[0]
			if !shadows[key] {
				t.Errorf("ParsedMetadata.%s (%q) is numeric but has no lenient shadow in parsedMetadataWire", f.Name, key)
			}
			if _, ok := numericFieldGetters[key]; !ok {
				t.Errorf("ParsedMetadata.%s (%q) is missing from numericFieldGetters", f.Name, key)
			}
		}
	}
}

// Plain json.Unmarshal into ParsedMetadata is unchanged: only the LLM decode
// path is lenient.
func TestParsedMetadata_PlainUnmarshalStillStrict(t *testing.T) {
	var m ParsedMetadata
	if err := json.Unmarshal([]byte(`{"year": "2015"}`), &m); err == nil {
		t.Fatal("plain Unmarshal of a string year should still fail")
	}
}
