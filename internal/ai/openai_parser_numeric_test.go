// file: internal/ai/openai_parser_numeric_test.go
// version: 1.1.0
// guid: 5f0c2e7a-8d41-4b6e-9a3c-2e7d1b4f6a90
// last-edited: 2026-09-13

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
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
		{`"+3"`, 3, coerced},
		{`"03"`, 3, coerced},
		{`"3.00"`, 3, coerced},
		{`"3."`, 0, dropped},
		{`".5e1"`, 0, dropped},
		{`"1e3"`, 0, dropped},
		{`1e3`, 0, dropped},
		{`"-1"`, 0, dropped},
		{`-2`, 0, dropped},
		{`0`, 0, dropped},
		{`"0"`, 0, dropped},
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

func TestLenientInt_RawTruncatedAtRuneBoundary(t *testing.T) {
	// Byte 80 falls inside the two-byte "é" (bytes 79-80), so the cut must
	// back up to byte 79 and the result must stay valid UTF-8.
	val := `"` + strings.Repeat("a", 78) + "é" + strings.Repeat("b", 20) + `"`
	var l lenientInt
	if err := l.UnmarshalJSON([]byte(val)); err != nil {
		t.Fatalf("UnmarshalJSON must never fail: %v", err)
	}
	if !l.dropped || l.value != 0 {
		t.Fatalf("want dropped zero, got %+v", l)
	}
	if !utf8.ValidString(l.raw) {
		t.Fatalf("truncated raw is not valid UTF-8: %q", l.raw)
	}
	if want := val[:79] + "..."; l.raw != want {
		t.Errorf("raw = %q, want %q", l.raw, want)
	}
}

func TestDecodeResultElement_DroppedOnlyElementIsNotMetadataEvidence(t *testing.T) {
	cases := []struct {
		raw        string
		isMetadata bool
	}{
		{`{"year": "not available"}`, false},
		{`{"series_number": {}, "year": 0}`, false},
		{`{"YEAR": "n/a", "filename": "x.m4b"}`, false},
		{`{"title": "T", "year": "not available"}`, true},
		{`{"year": "2015"}`, true},
		{`{"year": null}`, true}, // null is "found nothing", not a dropped value
	}
	for _, tc := range cases {
		m, isMetadata, err := decodeResultElement(json.RawMessage(tc.raw))
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.raw, err)
			continue
		}
		if m == nil {
			t.Errorf("%s: want an (empty) result for the slot, got nil", tc.raw)
		}
		if isMetadata != tc.isMetadata {
			t.Errorf("%s: isMetadata = %v, want %v", tc.raw, isMetadata, tc.isMetadata)
		}
	}
}

func TestWrapper_NotAcceptedOnDroppedNumericValuesAlone(t *testing.T) {
	// Counts match the element counts exactly, so a rejection here can only
	// come from the wrapper rule, not from the count check.
	for _, tc := range []struct {
		content  string
		expected int
	}{
		{`{"data": [{"series_number": {}}]}`, 1},
		{`{"data": [{"year": "not available"}, {"series_number": "n/a"}]}`, 2},
	} {
		_, err := parseBatchMetadataFromJSON(tc.content, tc.expected)
		if err == nil {
			t.Errorf("batch %s: a one-key wrapper holding only dropped numeric values must be rejected", tc.content)
		} else if !strings.Contains(err.Error(), "not a results wrapper") {
			t.Errorf("batch %s: rejected for the wrong reason: %v", tc.content, err)
		}
	}
	if _, err := parseMetadataFromJSON(`{"data": {"series_number": {}}}`); err == nil {
		t.Error(`single {"data": {"series_number": {}}} must be rejected`)
	}

	// Still accepted: the "results" key needs no evidence, and a foreign key
	// is accepted once any element carries a usable metadata key.
	for _, content := range []string{
		`{"results": [{"year": "not available"}, {"title": "B"}]}`,
		`{"data": [{"year": "not available"}, {"title": "B"}]}`,
	} {
		got, err := parseBatchMetadataFromJSON(content, 2)
		if err != nil {
			t.Fatalf("%s: %v", content, err)
		}
		if got[0] == nil || got[0].Year != 0 || got[1] == nil || got[1].Title != "B" {
			t.Errorf("%s: got %+v, %+v", content, got[0], got[1])
		}
	}
}

// captureSlog routes the default slog logger into a buffer at Debug level for
// the rest of the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestLogNumericCoercions_LogsFieldFileAndOutcome(t *testing.T) {
	buf := captureSlog(t)
	m, _, err := decodeResultElement(json.RawMessage(`{"title": "T", "year": "not available", "series_number": "3"}`))
	if err != nil {
		t.Fatal(err)
	}
	logNumericCoercions("dir/Book One.m4b", m)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d:\n%s", len(lines), buf.String())
	}
	var sawDropped, sawAccepted bool
	for _, line := range lines {
		if !strings.Contains(line, `file="dir/Book One.m4b"`) {
			t.Errorf("line lacks the filename: %s", line)
		}
		switch {
		case strings.Contains(line, "field=year"):
			sawDropped = strings.Contains(line, "level=INFO") && strings.Contains(line, "dropped") &&
				strings.Contains(line, `not available`)
		case strings.Contains(line, "field=series_number"):
			sawAccepted = strings.Contains(line, "level=DEBUG") && strings.Contains(line, "accepted") &&
				strings.Contains(line, `raw="\"3\""`)
		}
	}
	if !sawDropped {
		t.Errorf("no Info 'dropped' line for year:\n%s", buf.String())
	}
	if !sawAccepted {
		t.Errorf("no Debug 'accepted' line for series_number:\n%s", buf.String())
	}
	if m.numericCoercions != nil {
		t.Error("record not cleared after logging")
	}

	buf.Reset()
	logNumericCoercions("dir/Book One.m4b", m)
	if buf.Len() != 0 {
		t.Errorf("second log of the same result logged again: %s", buf.String())
	}
}

func TestParseFilename_CoercedResultIsCachedWithRecordClearedAndLogsOnce(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":0,"model":"m",` +
			`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant",` +
			`"content":"{\"title\":\"T\",\"author\":\"A\",\"year\":\"2015\",\"series_number\":\"n/a\"}"}}]}`))
	}))
	defer srv.Close()

	p := NewOpenAIParserWithBaseURL(nil, "test-key", srv.URL+"/v1", "test-model", true)
	buf := captureSlog(t)
	filename := "numeric-cache-test/Unique Title 2015.m4b"

	first, err := p.ParseFilename(context.Background(), filename)
	if err != nil {
		t.Fatalf("first ParseFilename: %v", err)
	}
	if first.Year != 2015 || first.SeriesNum != 0 || first.Title != "T" {
		t.Fatalf("first = %+v", first)
	}
	if first.numericCoercions != nil {
		t.Error("the result handed back and cached still carries its coercion record")
	}
	firstLog := buf.String()
	if strings.Count(firstLog, "field=year") != 1 || strings.Count(firstLog, "field=series_number") != 1 {
		t.Fatalf("want one line per coerced field on the fresh parse, got:\n%s", firstLog)
	}

	buf.Reset()
	second, err := p.ParseFilename(context.Background(), filename)
	if err != nil {
		t.Fatalf("second ParseFilename: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("backend hit %d times; the second call should come from the cache", hits.Load())
	}
	if second.Year != 2015 {
		t.Errorf("cached year = %d", second.Year)
	}
	if strings.Contains(buf.String(), "AI parse:") {
		t.Errorf("cache hit logged coercions again:\n%s", buf.String())
	}
}
