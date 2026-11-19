// file: internal/books/cutover/m4/activity_test.go
// version: 1.5.0
// guid: 8e43c7a1-2f95-4b60-a8d3-5c71b9e0f246
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
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

// kindReferencesSynthetic is the synthetic half of TestCutover_KindReferencesRewritten: every activity row that stored a HEAD def
// ID names the renamed kind afterwards in type, details.def_id and its def: tag;
// act:typ: holds the re-keyed entry and not the old one; the digest's items are
// renamed; a type that is no def ID stays as stored; a repairs def is renamed to
// its run's books.repairs.<fixer_id> kind, and held (as stored) when no run says
// the fixer; every other index and every other field is
// untouched; a row that does not decode is held in place.
func kindReferencesSynthetic(t *testing.T) {
	raw := map[string]string{}
	n := func(i int) string { return fmt.Sprintf("%020d", 1_700_000_000_000_000_000+int64(i)) }
	rows := []act{
		{"info", n(1), "01HBAT0001", map[string]any{"id": 1, "type": "dedup.book-scan", "level": "info", "source": "scanner", "summary": "<Gotham> scan & sweep",
			"operation_id": "op-1", "tags": []string{"def:dedup.book-scan", "action:scan"}, "details": map[string]any{"def_id": "dedup.book-scan", "books": 12, "nested": map[string]any{"def_id": "dedup.book-scan"}}}},
		{"change", n(2), "01HBAT0002", map[string]any{"type": "maintenance.itunes-heal", "level": "info", "source": "scanner", "summary": "alias",
			"tags": []string{"def:maintenance.itunes-heal"}, "details": map[string]any{"def_id": "maintenance.itunes-heal"}}},
		{"system", n(3), "01HBAT0003", map[string]any{"type": "scan_complete", "level": "info", "source": "scanner", "summary": "not a def id",
			"tags": []string{"def:dedup.book-scan", "source:scan"}, "details": map[string]any{"def_id": "scan_complete"}}},
		{"audit", n(4), "01HBAT0004", map[string]any{"type": "repairs.plan", "level": "info", "source": "scanner", "summary": "repairs", "operation_id": "01HBATREPAIRS1",
			"tags": []string{"def:repairs.plan"}, "details": map[string]any{"def_id": "repairs.plan"}}},
		{"audit", n(9), "01HBAT0009", map[string]any{"type": "repairs.apply", "level": "info", "source": "scanner", "summary": "repairs apply", "operation_id": "01HBATREPAIRS2",
			"tags": []string{"def:repairs.apply", "action:apply"}, "details": map[string]any{"def_id": "repairs.apply"}}},
		// Held, as stored: no operation_id, a run the task store does not hold, and
		// a run whose kind is no repairs kind.
		{"audit", n(10), "01HBAT0010", map[string]any{"type": "repairs.plan", "level": "info", "source": "scanner", "summary": "no operation",
			"tags": []string{"def:repairs.plan"}, "details": map[string]any{"def_id": "repairs.plan"}}},
		{"audit", n(11), "01HBAT0011", map[string]any{"type": "repairs.apply", "level": "info", "source": "scanner", "summary": "pruned run", "operation_id": "01HBATGONE",
			"tags": []string{"def:repairs.apply"}}},
		{"audit", n(12), "01HBAT0012", map[string]any{"type": "repairs.plan", "level": "info", "source": "scanner", "summary": "wrong kind", "operation_id": "01HBATSCAN",
			"details": map[string]any{"def_id": "repairs.plan"}}},
		// The same, with nothing but the type to resolve: the held decision must
		// come from the first lookup, not from a later one hitting the cache.
		{"audit", n(15), "01HBAT0015", map[string]any{"type": "repairs.apply", "level": "info", "source": "scanner", "summary": "pruned run, type only", "operation_id": "01HBATGONE"}},
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
		// A digest item of a repairs def takes its kind from its op: tag; one with
		// no operation stays as stored, and the digest's other items are renamed.
		{"digest", n(13), "01HBAT0013", map[string]any{"type": "daily_digest", "level": "info", "source": "compaction", "summary": "Daily digest",
			"details": map[string]any{"date": "2026-11-04", "items": []map[string]any{
				{"type": "repairs.plan", "summary": "p", "tags": []string{"def:repairs.plan", "op:01HBATREPAIRS1"}},
				{"type": "scan_complete", "summary": "s"},
			}}}},
		// A digest item naming two operations has no single run to take its kind from.
		{"digest", n(16), "01HBAT0016", map[string]any{"type": "daily_digest", "level": "info", "source": "compaction", "summary": "Daily digest",
			"details": map[string]any{"date": "2026-11-06", "items": []map[string]any{
				{"type": "repairs.plan", "summary": "two operations", "tags": []string{"def:repairs.plan", "op:01HBATREPAIRS1", "op:01HBATREPAIRS2"}},
			}}}},
		{"digest", n(14), "01HBAT0014", map[string]any{"type": "daily_digest", "level": "info", "source": "compaction", "summary": "Daily digest",
			"details": map[string]any{"date": "2026-11-05", "items": []map[string]any{
				{"type": "dedup.book-scan", "summary": "a", "tags": []string{"def:dedup.book-scan"}},
				{"type": "repairs.apply", "summary": "no operation", "tags": []string{"def:repairs.apply"}},
			}}}},
	}
	rows = append(rows,
		// Digest items before 2026-05-20 carry no tags: the item's own
		// operation_id names its run.
		act{"digest", n(17), "01HBAT0017", map[string]any{"type": "daily_digest", "level": "info", "source": "compaction", "summary": "Daily digest",
			"details": map[string]any{"date": "2026-05-01", "items": []map[string]any{
				{"type": "repairs.plan", "summary": "tagless", "operation_id": "01HBATREPAIRS1"},
				{"type": "dedup.book-scan", "summary": "tagless scan", "operation_id": "01HBATSCAN"},
			}}}},
		// One item spanning several operations of one kind resolves.
		act{"digest", n(18), "01HBAT0018", map[string]any{"type": "daily_digest", "level": "info", "source": "compaction", "summary": "Daily digest",
			"details": map[string]any{"date": "2026-11-07", "items": []map[string]any{
				{"type": "repairs.apply", "summary": "summarized", "tags": []string{"def:repairs.apply", "op:01HBATREPAIRS1", "op:01HBATREPAIRS3"}},
			}}}},
		// A mixed row: what resolves is renamed, and only the items that cannot
		// be (an absent run; operations of two fixers) stay as stored.
		act{"digest", n(19), "01HBAT0019", map[string]any{"type": "daily_digest", "level": "info", "source": "compaction", "summary": "Daily digest",
			"details": map[string]any{"date": "2026-11-08", "items": []map[string]any{
				{"type": "repairs.plan", "summary": "by operation_id", "operation_id": "01HBATREPAIRS2"},
				{"type": "repairs.apply", "summary": "pruned", "operation_id": "01HBATGONE"},
				{"type": "repairs.plan", "summary": "two fixers", "tags": []string{"op:01HBATREPAIRS1", "op:01HBATREPAIRS2"}},
				{"type": "maintenance.itunes-heal", "summary": "plain", "tags": []string{"def:maintenance.itunes-heal"}},
			}}}},
	)
	for _, a := range rows {
		seedAct(raw, a)
	}
	raw["act:info:"+n(8)+":01HBAT0008"] = "{not json"
	raw["act:info:not-a-key"] = `{"type":"dedup.book-scan"}`
	before := map[string]string{}
	for k, v := range raw {
		before[k] = v
	}

	// The runs the task store holds, by operation: two repairs runs with
	// different fixers, and a scan run (a kind that is no repairs kind).
	repairsKinds := map[string]string{"01HBATREPAIRS1": "books.repairs.two-face-asins", "01HBATREPAIRS2": "books.repairs.hush-credits", "01HBATREPAIRS3": "books.repairs.two-face-asins"}
	r := newRig(t, withRaw(raw))
	r.actRuns = map[string]tasks.Kind{"01HBATSCAN": "books.dedup.book-scan"}
	for id, k := range repairsKinds {
		r.actRuns[id] = tasks.Kind(k)
	}
	s, err := r.runWith(t, r.seed(t), r.activity())
	if err != nil {
		t.Fatalf("%v\n%s", err, r.logs)
	}

	// held is every row that must stay exactly as stored, with its reason: the
	// repairs rows no run can name a fixer for. A digest holding such an item is
	// not held: the item stays as stored and the rest of the row is renamed.
	held := map[string]bool{}
	for _, i := range []int{10, 11, 12, 15} {
		held[act{tier: "audit", nanos: n(i), ulid: fmt.Sprintf("01HBAT%04d", i)}.key()] = true
	}

	kind := map[string]string{"dedup.book-scan": "books.dedup.book-scan", "maintenance.itunes-heal": "books.itunes.heal", "operations.backfill-legacy-status": "books.retired.operations.backfill-legacy-status"}
	// rename is the kind a def ID is renamed to; op is the operation the
	// reference belongs to, which names a repairs def's run.
	rename := func(v any, op string) any {
		if s, ok := v.(string); ok {
			if k, ok := kind[s]; ok {
				return k
			}
			if s == "repairs.plan" || s == "repairs.apply" {
				if k, ok := repairsKinds[op]; ok {
					return k
				}
			}
		}
		return v
	}
	renameTags := func(in any, op string) any {
		tags, ok := in.([]any)
		if !ok {
			return in
		}
		out := make([]any, len(tags))
		for i, tg := range tags {
			out[i] = tg
			if s, ok := tg.(string); ok {
				if id, ok := strings.CutPrefix(s, "def:"); ok {
					out[i] = "def:" + fmt.Sprint(rename(id, op))
				}
			}
		}
		return out
	}
	// opsOfItem is the operations a digest item names: its op: tags, else its
	// operation_id.
	opsOfItem := func(m map[string]any) []string {
		var ops []string
		tags, _ := m["tags"].([]any)
		for _, tg := range tags {
			if s, ok := tg.(string); ok {
				if id, ok := strings.CutPrefix(s, "op:"); ok {
					ops = append(ops, id)
				}
			}
		}
		if id, ok := m["operation_id"].(string); ok && len(ops) == 0 {
			ops = append(ops, id)
		}
		return ops
	}
	// itemKind renames v for an item of ops: a repairs def resolves when every
	// operation is a repairs run of one kind. ok is false when it cannot.
	itemKind := func(v any, ops []string) (any, bool) {
		s, _ := v.(string)
		if s != "repairs.plan" && s != "repairs.apply" {
			return rename(v, ""), true
		}
		kind := ""
		for _, op := range ops {
			k, found := repairsKinds[op]
			if !found || (kind != "" && k != kind) {
				return v, false
			}
			kind = k
		}
		return rename(v, ops[0]), kind != ""
	}

	for _, a := range rows {
		got := decodeAct(t, s.Act[a.key()])
		want := decodeAct(t, a.json())
		op, _ := a.body["operation_id"].(string)
		switch {
		case held[a.key()]:
			// Held: as stored, whole (a digest holding one unresolvable item keeps its
			// resolvable items' old IDs too, rather than half a rename).
			if s.Act[a.key()] != before[a.key()] {
				t.Errorf("%s: a held row was written: %s", a.key(), s.Act[a.key()])
			}
			want = decodeAct(t, a.json())
		case a.tier != "digest":
			want["type"] = rename(want["type"], op)
			if d, ok := want["details"].(map[string]any); ok {
				d["def_id"] = rename(d["def_id"], op) // the top-level def_id only: a nested one is the row's own data
			}
			if tg, ok := want["tags"]; ok {
				want["tags"] = renameTags(tg, op)
			}
		default:
			d := want["details"].(map[string]any)
			for _, it := range d["items"].([]any) {
				m := it.(map[string]any)
				ops := opsOfItem(m)
				if len(ops) == 0 {
					ops = []string{""}
				}
				// An item whose repairs reference does not resolve stays whole.
				whole := true
				if _, ok := itemKind(m["type"], ops); !ok {
					whole = false
				}
				if tg, ok := m["tags"].([]any); ok {
					for _, t := range tg {
						if id, ok := strings.CutPrefix(fmt.Sprint(t), "def:"); ok {
							if _, ok := itemKind(id, ops); !ok {
								whole = false
							}
						}
					}
				}
				if !whole {
					continue
				}
				m["type"], _ = itemKind(m["type"], ops)
				if tg, ok := m["tags"].([]any); ok {
					for i, t := range tg {
						if id, ok := strings.CutPrefix(fmt.Sprint(t), "def:"); ok {
							k, _ := itemKind(id, ops)
							tg[i] = "def:" + fmt.Sprint(k)
						}
					}
				}
			}
			if tg, ok := want["tags"]; ok {
				want["tags"] = renameTags(tg, "")
			}
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
	var digestRow act
	for _, a := range rows {
		if a.ulid == "01HBAT0007" {
			digestRow = a
		}
	}
	// The digest's own counts are not part of the rename (5T.8.2 names the
	// items); they keep the stored type keys.
	if d := decodeAct(t, s.Act[digestRow.key()])["details"].(map[string]any); !reflect.DeepEqual(d["counts"], map[string]any{"dedup.book-scan": float64(2)}) {
		t.Errorf("digest counts = %v", d["counts"])
	}

	// The two rows that do not decode are held where they are: reported, never
	// dropped, never rewritten.
	for _, k := range []string{"act:info:" + n(8) + ":01HBAT0008", "act:info:not-a-key"} {
		if s.Act[k] != before[k] {
			t.Errorf("%s = %q, want the held row untouched (%q)", k, s.Act[k], before[k])
		}
	}
	// Two rows that do not decode, four repairs rows no run names a fixer for, and
	// the three digest rows (14, 16, 19) written or kept with an item left as
	// stored, each recorded once however many items it holds.
	if s.Verify.Held != 9 || len(s.Quarantine) != 0 {
		t.Errorf("held %d, quarantined %d; want 9 held, none quarantined: %+v", s.Verify.Held, len(s.Quarantine), s.Verify)
	}
	// Read counts each primary row once: the seeded rows and the two that do not
	// decode, the rows with held items among them.
	if want := int64(len(rows) + 2); s.Verify.Read != want {
		t.Errorf("Read = %d, want %d (one per primary row)", s.Verify.Read, want)
	}
	// The digest items that stay as stored are logged, one line each: row 14's
	// operation-less item, row 16's item of two fixers, and row 19's pruned run
	// and two fixers.
	if got := strings.Count(r.logs.String(), "a digest item is left as stored"); got != 4 {
		t.Errorf("%d item-held log lines, want 4:\n%s", got, r.logs)
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
	if s.Verify.Written != 13 {
		t.Errorf("the report wrote %d rows, want 13 (the rows that named a def ID, held ones excluded): %+v", s.Verify.Written, s.Verify)
	}
}

// TestCutover_KindReferencesRewritten: the activity feed's rewrite, over
// synthetic rows that cover every shape of reference, and over stage 1 of the
// R2 corpus, the feed R0 wrote.
func TestCutover_KindReferencesRewritten(t *testing.T) {
	t.Run("synthetic", kindReferencesSynthetic)
	t.Run("corpus", kindReferencesCorpus)
}

var (
	actPrimary = regexp.MustCompile(`^act:(audit|batch|change|debug|digest|info|system):[0-9]{20}:[0-9A-Za-z]+$`)
	// dotted is the shape of a HEAD def ID (and of a kind): two or more
	// dot-separated lowercase words.
	dotted = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)
)

// defRefs lists every def ID or kind a feed row names, wherever it names one:
// type, details.def_id, the def: tags and the digest's items.
func defRefs(row map[string]any) []string {
	var out []string
	add := func(v any) {
		if s, ok := v.(string); ok && dotted.MatchString(s) {
			out = append(out, s)
		}
	}
	tags := func(v any) {
		ts, _ := v.([]any)
		for _, tg := range ts {
			if s, ok := tg.(string); ok {
				if id, ok := strings.CutPrefix(s, "def:"); ok {
					add(id)
				}
			}
		}
	}
	add(row["type"])
	tags(row["tags"])
	if d, ok := row["details"].(map[string]any); ok {
		add(d["def_id"])
		if items, ok := d["items"].([]any); ok {
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					add(m["type"])
					tags(m["tags"])
				}
			}
		}
	}
	return out
}

// kindReferencesCorpus: the activity rewrite over the rows R0 wrote. Every row
// is still there, every row that named a def ID names the kind the map gives it,
// act:typ: has one entry per row under the row's new type, and a scan of the
// whole feed afterwards finds no HEAD def ID that the table renames and no ID
// the table does not know. A hit is a gap in the table (the brief's stop
// condition): it is reported, never added to an allowlist here.
func kindReferencesCorpus(t *testing.T) {
	var pre map[string]string
	r := newRig(t, withCorpus("stage1"))
	r.mutate = func(ctx context.Context, t testing.TB, lr storage.LegacyRange) {
		pre = map[string]string{}
		for kv, err := range lr.Scan(ctx, storage.ScanOptions{Start: []byte("act:"), End: []byte("act;")}) {
			if err != nil {
				t.Fatal(err)
			}
			pre[string(kv.Key)] = string(kv.Value)
		}
	}
	s, err := r.runWith(t, r.seed(t), r.activity())
	if err != nil {
		t.Fatalf("%v\n%s", err, r.logs)
	}
	if len(s.Quarantine) != 0 || s.Verify.Held != 0 {
		t.Errorf("held %d, quarantined %+v: R0's feed holds no row the rewrite cannot read", s.Verify.Held, s.Quarantine)
	}

	var rows, renamed, indexed int
	for k, v := range pre {
		if !actPrimary.MatchString(k) {
			// Every index other than act:typ: is untouched.
			if !strings.HasPrefix(k, "act:typ:") && s.Act[k] != v {
				t.Errorf("%s changed in the rewrite", k)
			}
			continue
		}
		rows++
		old := decodeAct(t, v)
		got, ok := s.Act[k]
		if !ok {
			t.Errorf("%s was dropped", k)
			continue
		}
		now := decodeAct(t, got)
		oldType, _ := old["type"].(string)
		wantType := oldType
		if tg, ok := kindrename.Lookup(oldType); ok && tg.Kind != "" {
			wantType = string(tg.Kind)
		}
		if now["type"] != wantType {
			t.Errorf("%s: type %v, want %q", k, now["type"], wantType)
		}
		if wantType != oldType {
			renamed++
		}
		if wantType != "" {
			indexed++
			parts := strings.SplitN(strings.TrimPrefix(k, "act:"), ":", 3)
			a := act{tier: parts[0], nanos: parts[1], ulid: parts[2]}
			if ref, ok := s.Act[a.typ(wantType)]; !ok || ref != a.ref() {
				t.Errorf("%s: type index entry under %q = %q, %v; want %q", k, wantType, ref, ok, a.ref())
			}
			if wantType != oldType {
				if _, still := s.Act[a.typ(oldType)]; still {
					t.Errorf("%s: the index entry under the old type %q is still there", k, oldType)
				}
			}
		}
	}
	if rows == 0 {
		t.Fatal("stage 1 holds no activity row: the test would compare nothing with nothing")
	}
	var typ int
	for k := range s.Act {
		if strings.HasPrefix(k, "act:typ:") {
			typ++
		}
	}
	if typ != indexed {
		t.Errorf("act:typ: holds %d entries after the rewrite, %d rows have a type", typ, indexed)
	}

	// The whole-feed scan: no def ID in any position that the table renames or
	// does not know.
	seen := map[string]int{}
	for k, v := range s.Act {
		if !actPrimary.MatchString(k) {
			continue
		}
		for _, id := range defRefs(decodeAct(t, v)) {
			seen[id]++
		}
	}
	for id, n := range seen {
		tg, known := kindrename.Lookup(id)
		switch {
		case strings.HasPrefix(id, "books.") || strings.HasPrefix(id, "core."):
			// A kind already.
		case known && tg.Kind == "":
			// A repairs def: the kind needs a run's fixer; this feed (activity step alone,
			// no runs in the task store) holds such rows, so the feed keeps the def ID.
		case known:
			t.Errorf("STOP: %d feed positions still name the HEAD def ID %q, which the table renames to %q", n, id, tg.Kind)
		default:
			t.Errorf("STOP: %d feed positions name %q, which is neither a kind nor in the rename table", n, id)
		}
	}
	checkAnswers(t, "activity_stage1", map[string]int{"rows": rows, "renamed": renamed, "typed": indexed, "type_index_entries": typ,
		"distinct_references_after": len(seen), "verify_written": int(s.Verify.Written), "verify_held": int(s.Verify.Held)})
	t.Logf("stage 1 feed: %d rows, %d renamed, %d typed, %d distinct def or kind references after the rewrite", rows, renamed, indexed, len(seen))
}
