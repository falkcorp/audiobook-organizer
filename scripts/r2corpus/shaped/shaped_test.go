// file: scripts/r2corpus/shaped/shaped_test.go
// version: 1.0.0
// guid: bb85c2f2-9e0b-4a55-8565-9d59d2d8125f
// last-edited: 2026-11-10

//go:build r2corpus_shaped

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

const ulid = "01J9ZBATMAN0GOTHAM0CITY0X1"

func TestTemplateResizesTheLongestStringAndKeepsJSONValid(t *testing.T) {
	value := `{"id":"` + ulid + `","kind":"scan","message":"the Batmobile is parked","n":7,"parent":"` + ulid + `"}`
	tpl := compileTemplate("opv2:op:", []byte("opv2:op:"+ulid), []byte(value))
	if !tpl.sized {
		t.Fatal("a JSON value with a string was not sized")
	}
	ns := "~00000a"
	// Both occurrences of the template's own key suffix name the new row.
	for _, want := range []int{tpl.size(len(ns)), tpl.size(len(ns)) + 1, tpl.size(len(ns)) + 500, tpl.size(len(ns)) - 5} {
		got := tpl.build(nil, ns, want)
		if len(got) != want {
			t.Errorf("want %d bytes, built %d: %s", want, len(got), got)
		}
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("clone of %d bytes is not JSON: %v: %s", want, err, got)
		}
		if m["id"] != ns || m["parent"] != ns {
			t.Errorf("clone of %d bytes keeps the template's id: %v", want, m)
		}
		if m["kind"] != "scan" || m["n"] != float64(7) {
			t.Errorf("clone of %d bytes changed another field: %v", want, m)
		}
	}
	// Growth lands in the longest string value, never in a key or the id.
	grown := tpl.build(nil, ns, tpl.size(len(ns))+40)
	var m map[string]string
	_ = json.Unmarshal([]byte(strings.ReplaceAll(string(grown), `"n":7,`, "")), &m)
	if !strings.HasPrefix(m["message"], "the Batmobile is parked") || len(m["message"]) != len("the Batmobile is parked")+40 {
		t.Errorf("padding is not in the longest string value: %q", m["message"])
	}
}

func TestTemplateShrinkStopsAtTheRemovableTail(t *testing.T) {
	// An escape at the end of the string is never cut into.
	value := `{"message":"ab\u00e9cd"}`
	tpl := compileTemplate("p:", []byte("p:1"), []byte(value))
	got := tpl.build(nil, "~1", 3)
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("shrunk clone is not JSON: %v: %s", err, got)
	}
	if !strings.HasPrefix(m["message"], "ab") {
		t.Errorf("shrink ate into the content: %q", m["message"])
	}
}

func TestNonJSONValueIsClonedVerbatim(t *testing.T) {
	tpl := compileTemplate("act:lvl:", []byte("act:lvl:1"), []byte("info\x00raw"))
	if tpl.sized {
		t.Fatal("a non-JSON value was marked resizable")
	}
	if got := tpl.build(nil, "~1", 99); string(got) != "info\x00raw" {
		t.Errorf("non-JSON clone = %q", got)
	}
}

func TestPadPointSkipsKeysAndTheRowsOwnID(t *testing.T) {
	doc := []byte(`{"a-very-long-key-name-that-is-not-a-value":"v","id":"` + ulid + `","list":["xy",{"k":"abcdef"}]}`)
	p, ok := padPoint(doc, ulid)
	if !ok {
		t.Fatal("no pad point")
	}
	if got := string(doc[p.at-6 : p.at]); got != "abcdef" {
		t.Errorf("pad point follows %q, want the longest value that is not a key or the id", got)
	}
	if _, ok := padPoint([]byte(`{"a":1,"b":[true,null]}`), ""); ok {
		t.Error("a document with no string value has a pad point")
	}
}

func TestSuffixForIsUniquePerRowAndFixedWidth(t *testing.T) {
	seen := map[string]int64{}
	for i := int64(0); i < 5000; i++ {
		s := suffixFor(i, 6)
		if len(s) != 6 || s[0] != '~' {
			t.Fatalf("suffix %d = %q", i, s)
		}
		if j, dup := seen[s]; dup {
			t.Fatalf("rows %d and %d share suffix %q", j, i, s)
		}
		seen[s] = i
	}
}

func TestScaledKeepsTheAverageSizes(t *testing.T) {
	f := family{Prefix: "opv2:log:", Keys: 43745789, RawKeyBytes: 2930967930, RawValueBytes: 11934321749}
	keys, kb, vb := scaled(f, 1)
	if keys != f.Keys || kb != f.RawKeyBytes || vb != f.RawValueBytes {
		t.Errorf("scale 1 changed the counts: %d %d %d", keys, kb, vb)
	}
	keys, kb, vb = scaled(f, 0.01)
	if keys != 437458 {
		t.Errorf("scale 0.01 keys = %d", keys)
	}
	if got, want := kb/keys, f.RawKeyBytes/f.Keys; got < want-1 || got > want+1 {
		t.Errorf("average key %d, want %d", got, want)
	}
	if got, want := vb/keys, f.RawValueBytes/f.Keys; got < want-1 || got > want+1 {
		t.Errorf("average value %d, want %d", got, want)
	}
	if keys, _, _ := scaled(family{Prefix: "x:", Keys: 3, RawKeyBytes: 30}, 0.001); keys != 1 {
		t.Errorf("a family with keys keeps at least one at any scale: %d", keys)
	}
	if keys, _, _ := scaled(family{Prefix: "x:"}, 0.5); keys != 0 {
		t.Errorf("an empty family stays empty: %d", keys)
	}
}

// seedStore writes a small stage-1 stand-in: three families, one with a JSON
// template, one with an empty-value template, one with no rows at all.
func seedStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audiobooks.pebble")
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	set := func(k, v string) {
		if err := db.Set([]byte(k), []byte(v), pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("01J9ZGOTHAMCITY%011d", i)
		set("u:"+id, fmt.Sprintf(`{"id":%q,"username":"selina%d","note":"cat"}`, id, i))
	}
	set("opv3:ix:done:01J9ZGOTHAMCITY00000000000", "")
	set("book:untouched", "x") // outside every counts family
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGenerateGrowsFamiliesToTheCountsAndScanAgrees(t *testing.T) {
	path := seedStore(t)
	fams := []family{
		{Prefix: "u:", Keys: 40, RawKeyBytes: 40 * 32, RawValueBytes: 40 * 120},
		{Prefix: "opv3:ix:done:", Keys: 25, RawKeyBytes: 25 * 44, RawValueBytes: 0},
		{Prefix: "act:digest:", Keys: 9, RawKeyBytes: 9 * 30, RawValueBytes: 9 * 500},
		{Prefix: "role:", Keys: 0},
	}
	rep, err := generate(path, "", fams, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanStore(path, fams)
	if err != nil {
		t.Fatal(err)
	}
	if got.OtherKeys != 1 {
		t.Errorf("a key outside every family changed: other = %d", got.OtherKeys)
	}
	byPrefix := map[string]scanFamily{}
	for _, f := range got.Families {
		byPrefix[f.Prefix] = f
	}
	for _, r := range rep.Families {
		s := byPrefix[r.Prefix]
		if s.Keys != r.Keys || s.RawKeyBytes != r.RawKeyBytes || s.RawValueBytes != r.RawValueBytes {
			t.Errorf("%s: the report says %d/%d/%d, the store holds %d/%d/%d", r.Prefix,
				r.Keys, r.RawKeyBytes, r.RawValueBytes, s.Keys, s.RawKeyBytes, s.RawValueBytes)
		}
	}
	if s := byPrefix["u:"]; s.Keys != 40 || s.RawKeyBytes != 40*32 || s.RawValueBytes != 40*120 {
		t.Errorf("u: = %+v, want exactly the counts", s)
	}
	if s := byPrefix["opv3:ix:done:"]; s.Keys != 25 || s.RawKeyBytes != 25*44 || s.RawValueBytes != 0 {
		t.Errorf("opv3:ix:done: = %+v", s)
	}
	if s := byPrefix["act:digest:"]; s.Keys != 0 {
		t.Errorf("a family with no template was invented: %+v", s)
	}
	var skipped string
	for _, r := range rep.Families {
		if r.Prefix == "act:digest:" {
			skipped = r.Skipped
		}
	}
	if skipped == "" {
		t.Error("the family with no template is not reported as skipped")
	}
	// Every generated user row is the template's shape, with its own id.
	db, err := pebble.Open(path, &pebble.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte("u:~"), UpperBound: []byte("u:\x7f")})
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	n := 0
	for ok := it.First(); ok; ok = it.Next() {
		var u struct{ ID, Username, Note string }
		if err := json.Unmarshal(it.Value(), &u); err != nil {
			t.Fatalf("generated row %q is not JSON: %v", it.Key(), err)
		}
		if u.ID != strings.TrimPrefix(string(it.Key()), "u:") {
			t.Errorf("row %q names id %q", it.Key(), u.ID)
		}
		if !strings.HasPrefix(u.Username, "selina") {
			t.Errorf("row %q lost the template's username: %q", it.Key(), u.Username)
		}
		n++
	}
	if n != 37 {
		t.Errorf("generated %d user rows, want 37", n)
	}
}

func TestGenerateNeverDeletesStageOneRows(t *testing.T) {
	path := seedStore(t)
	// Stage 1 holds 3 users; a target of 2 leaves them all and writes nothing.
	fams := []family{{Prefix: "u:", Keys: 2, RawKeyBytes: 64, RawValueBytes: 200}}
	rep, err := generate(path, "", fams, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if f := rep.Families[0]; f.Keys != 3 || f.GeneratedKeys != 0 {
		t.Errorf("report = %+v, want the 3 stage-1 rows kept and none generated", f)
	}
}

func TestLoadCountsRejectsABadFile(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"empty":     `{"families":[]}`,
		"duplicate": `{"families":[{"prefix":"a:","keys":1},{"prefix":"a:","keys":2}]}`,
		"negative":  `{"families":[{"prefix":"a:","keys":-1}]}`,
		"bytes":     `{"families":[{"prefix":"a:","keys":0,"raw_key_bytes":4}]}`,
		"noprefix":  `{"families":[{"keys":1}]}`,
	} {
		p := filepath.Join(dir, name+".json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCounts(p); err == nil {
			t.Errorf("%s: loadCounts accepted it", name)
		}
	}
}
