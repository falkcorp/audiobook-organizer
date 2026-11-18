// file: internal/books/cutover/m4/activity_test.go
// version: 1.0.0
// guid: 8e43c7a1-2f95-4b60-a8d3-5c71b9e0f246
// last-edited: 2026-11-18

package m4_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// act is one activity row and its index keys, as the feed's store writes them.
type act struct {
	tier, nanos, ulid string
	body              map[string]any
}

func (a act) key() string { return fmt.Sprintf("act:%s:%s:%s", a.tier, a.nanos, a.ulid) }
func (a act) ref() string { return fmt.Sprintf("%s:%s:%s", a.tier, a.nanos, a.ulid) }
func (a act) typ(v string) string {
	return fmt.Sprintf("act:typ:%s:%s:%s:%s", strings.ReplaceAll(strings.ReplaceAll(v, "%", "%25"), ":", "%3A"), a.tier, a.nanos, a.ulid)
}

// json is the row as stored. HTML is not escaped, so a test can tell a rewrite
// that re-escapes the row's text from one that keeps it.
func (a act) json() string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(a.body); err != nil {
		panic(err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// seedAct writes a, its type index entry and the indexes that must stay
// untouched, into raw.
func seedAct(raw map[string]string, a act) {
	raw[a.key()] = a.json()
	if t, _ := a.body["type"].(string); t != "" {
		raw[a.typ(t)] = a.ref()
	}
	raw["act:src:scanner:"+a.tier+":"+a.nanos+":"+a.ulid] = a.ref()
	raw["act:lvl:info:"+a.tier+":"+a.nanos+":"+a.ulid] = a.ref()
}

func decodeAct(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("a rewritten row does not decode: %v\n%s", err, raw)
	}
	return m
}

// TestCutover_KindReferencesRewritten: every activity row that stored a HEAD def
// ID names the renamed kind afterwards in type, details.def_id and its def: tag;
// act:typ: holds the re-keyed entry and not the old one; the digest's items are
// renamed; a type that is no def ID (and the repairs defs, whose kind needs a
// run's fixer) stay as stored; every other index and every other field is
// untouched; a row that does not decode is held in place.
func TestCutover_KindReferencesRewritten(t *testing.T) {
	raw := map[string]string{}
	n := func(i int) string { return fmt.Sprintf("%020d", 1_700_000_000_000_000_000+int64(i)) }
	rows := []act{
		{"info", n(1), "01HBAT0001", map[string]any{"id": 1, "type": "dedup.book-scan", "level": "info", "source": "scanner", "summary": "<Gotham> scan & sweep",
			"operation_id": "op-1", "tags": []string{"def:dedup.book-scan", "action:scan"}, "details": map[string]any{"def_id": "dedup.book-scan", "books": 12, "nested": map[string]any{"def_id": "dedup.book-scan"}}}},
		{"change", n(2), "01HBAT0002", map[string]any{"type": "maintenance.itunes-heal", "level": "info", "source": "scanner", "summary": "alias",
			"tags": []string{"def:maintenance.itunes-heal"}, "details": map[string]any{"def_id": "maintenance.itunes-heal"}}},
		{"system", n(3), "01HBAT0003", map[string]any{"type": "scan_complete", "level": "info", "source": "scanner", "summary": "not a def id",
			"tags": []string{"def:dedup.book-scan", "source:scan"}, "details": map[string]any{"def_id": "scan_complete"}}},
		{"audit", n(4), "01HBAT0004", map[string]any{"type": "repairs.plan", "level": "info", "source": "scanner", "summary": "repairs",
			"tags": []string{"def:repairs.plan"}, "details": map[string]any{"def_id": "repairs.plan"}}},
		{"debug", n(5), "01HBAT0005", map[string]any{"type": "operations.backfill-legacy-status", "level": "info", "source": "scanner", "summary": "retired",
			"details": map[string]any{"def_id": "operations.backfill-legacy-status"}}},
		{"batch", n(6), "01HBAT0006", map[string]any{"type": "dedup.book-scan", "level": "info", "source": "scanner", "summary": "no details, no tags"}},
		{"digest", n(7), "01HBAT0007", map[string]any{"type": "daily_digest", "level": "info", "source": "compaction", "summary": "Daily digest",
			"tags": []string{"def:dedup.book-scan"}, "details": map[string]any{"date": "2026-11-03", "original_count": 3, "counts": map[string]any{"dedup.book-scan": 2},
				"items": []map[string]any{
					{"type": "dedup.book-scan", "summary": "a", "tags": []string{"def:dedup.book-scan", "op:x"}},
					{"type": "scan_complete", "summary": "b"},
					{"type": "maintenance.itunes-heal", "summary": "c", "tags": []string{"def:maintenance.itunes-heal"}},
				}}}},
	}
	for _, a := range rows {
		seedAct(raw, a)
	}
	raw["act:info:"+n(8)+":01HBAT0008"] = "{not json"
	raw["act:info:not-a-key"] = `{"type":"dedup.book-scan"}`
	before := map[string]string{}
	for k, v := range raw {
		before[k] = v
	}

	r := newRig(t, withRaw(raw))
	s, err := r.runWith(t, r.seed(t), r.activity())
	if err != nil {
		t.Fatalf("%v\n%s", err, r.logs)
	}

	kind := map[string]string{"dedup.book-scan": "books.dedup.book-scan", "maintenance.itunes-heal": "books.itunes.heal", "operations.backfill-legacy-status": "books.retired.operations.backfill-legacy-status"}
	rename := func(v any) any {
		if s, ok := v.(string); ok {
			if k, ok := kind[s]; ok {
				return k
			}
		}
		return v
	}
	renameTags := func(in any) any {
		tags, ok := in.([]any)
		if !ok {
			return in
		}
		out := make([]any, len(tags))
		for i, tg := range tags {
			out[i] = tg
			if s, ok := tg.(string); ok {
				if id, ok := strings.CutPrefix(s, "def:"); ok {
					out[i] = "def:" + fmt.Sprint(rename(id))
				}
			}
		}
		return out
	}

	for _, a := range rows {
		got := decodeAct(t, s.Act[a.key()])
		want := decodeAct(t, a.json())
		if a.tier != "digest" {
			want["type"] = rename(want["type"])
			if d, ok := want["details"].(map[string]any); ok {
				d["def_id"] = rename(d["def_id"]) // the top-level def_id only: a nested one is the row's own data
			}
		} else {
			d := want["details"].(map[string]any)
			for _, it := range d["items"].([]any) {
				m := it.(map[string]any)
				m["type"] = rename(m["type"])
				if tg, ok := m["tags"]; ok {
					m["tags"] = renameTags(tg)
				}
			}
		}
		if tg, ok := want["tags"]; ok {
			want["tags"] = renameTags(tg)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got  %v\n want %v", a.key(), got, want)
		}
		// act:typ: holds the re-keyed entry and not the old one.
		old, _ := a.body["type"].(string)
		now, _ := want["type"].(string)
		if old != now {
			if _, still := s.Act[a.typ(old)]; still {
				t.Errorf("%s: the type index entry under the old type %q is still there", a.key(), old)
			}
			if ref, ok := s.Act[a.typ(now)]; !ok || ref != a.ref() {
				t.Errorf("%s: type index entry under %q = %q, %v; want %q", a.key(), now, ref, ok, a.ref())
			}
		} else if ref, ok := s.Act[a.typ(old)]; !ok || ref != a.ref() {
			t.Errorf("%s: the type index entry of an unchanged type moved: %q, %v", a.key(), ref, ok)
		}
		// Every other index is byte for byte what it was.
		for _, k := range []string{"act:src:scanner:" + a.tier + ":" + a.nanos + ":" + a.ulid, "act:lvl:info:" + a.tier + ":" + a.nanos + ":" + a.ulid} {
			if s.Act[k] != before[k] {
				t.Errorf("%s changed: %q", k, s.Act[k])
			}
		}
	}

	// The HTML in a summary is not escaped by the rewrite, and a nested def_id is
	// the row's own data.
	if !strings.Contains(s.Act[rows[0].key()], "<Gotham> scan & sweep") {
		t.Errorf("the rewrite re-escaped the row's text: %s", s.Act[rows[0].key()])
	}
	if got := decodeAct(t, s.Act[rows[0].key()]); got["summary"] != "<Gotham> scan & sweep" || got["details"].(map[string]any)["nested"].(map[string]any)["def_id"] != "dedup.book-scan" {
		t.Errorf("row 1 lost data in the rewrite: %v", got)
	}
	// The digest's own counts are not part of the rename (5T.8.2 names the
	// items); they keep the stored type keys.
	if d := decodeAct(t, s.Act[rows[6].key()])["details"].(map[string]any); !reflect.DeepEqual(d["counts"], map[string]any{"dedup.book-scan": float64(2)}) {
		t.Errorf("digest counts = %v", d["counts"])
	}

	// The two rows that do not decode are held where they are: reported, never
	// dropped, never rewritten.
	for _, k := range []string{"act:info:" + n(8) + ":01HBAT0008", "act:info:not-a-key"} {
		if s.Act[k] != before[k] {
			t.Errorf("%s = %q, want the held row untouched (%q)", k, s.Act[k], before[k])
		}
	}
	if s.Verify.Held != 2 || len(s.Quarantine) != 0 {
		t.Errorf("held %d, quarantined %d; want 2 held, none quarantined: %+v", s.Verify.Held, len(s.Quarantine), s.Verify)
	}
	// No HEAD def ID is left in a def:, type or def_id position of any feed row.
	for k, v := range s.Act {
		if strings.HasPrefix(k, "act:typ:") {
			for id := range kind {
				if strings.HasPrefix(k, "act:typ:"+id+":") {
					t.Errorf("%s: an index entry is still keyed by the HEAD def ID", k)
				}
			}
		}
		if strings.HasPrefix(k, "act:info:") || strings.HasPrefix(k, "act:change:") || strings.HasPrefix(k, "act:batch:") {
			if strings.Contains(v, `"def:dedup.book-scan"`) || strings.Contains(v, `"def_id":"dedup.book-scan"`) && !strings.Contains(v, "nested") {
				t.Errorf("%s still names a HEAD def ID: %s", k, v)
			}
		}
	}
	if s.Verify.Written != 6 {
		t.Errorf("the report wrote %d rows, want 6 (the rows that named a def ID): %+v", s.Verify.Written, s.Verify)
	}
}
