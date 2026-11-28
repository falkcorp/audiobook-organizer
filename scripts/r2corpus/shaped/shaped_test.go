// file: scripts/r2corpus/shaped/shaped_test.go
// version: 1.0.0
// guid: bb85c2f2-9e0b-4a55-8565-9d59d2d8125f
// last-edited: 2026-11-10

//go:build r2corpus_shaped

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

const ulid = "01J9ZBATMAN0GOTHAM0CITY0X1"

func TestTemplateResizesOnlyThePadField(t *testing.T) {
	// started_at is the longest string: the old generator padded it, which
	// made a timestamp that no longer parses. Only the named field grows.
	value := `{"id":"` + ulid + `","kind":"scan","message":"parked","n":7,"parent":"` + ulid + `","started_at":"2026-11-10T11:33:10.797541296Z"}`
	tpl := compileTemplate("opv2:op:", []byte("opv2:op:"+ulid), []byte(value), rowShape{pad: "message"})
	if !tpl.sized {
		t.Fatal("a JSON value holding its pad field was not sized")
	}
	ns := "z00000a"
	// Both occurrences of the template's own key suffix name the new row.
	for _, want := range []int{tpl.size(len(ns)), tpl.size(len(ns)) + 1, tpl.size(len(ns)) + 500, tpl.size(len(ns)) - 3} {
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
		if m["kind"] != "scan" || m["n"] != float64(7) || m["started_at"] != "2026-11-10T11:33:10.797541296Z" {
			t.Errorf("clone of %d bytes changed a field other than the pad field: %v", want, m)
		}
	}
	grown := tpl.build(nil, ns, tpl.size(len(ns))+40)
	var m map[string]any
	if err := json.Unmarshal(grown, &m); err != nil {
		t.Fatal(err)
	}
	if msg, _ := m["message"].(string); !strings.HasPrefix(msg, "parked") || len(msg) != len("parked")+40 {
		t.Errorf("padding is not in the pad field: %q", msg)
	}
}

func TestTemplateWithoutItsPadFieldKeepsItsSize(t *testing.T) {
	// A succeeded run has no error message: its clone is the template's size.
	value := `{"id":"` + ulid + `","def_id":"maintenance.external-id-backfill","state":"succeeded"}`
	tpl := compileTemplate("opv3:run:", []byte("opv3:run:"+ulid), []byte(value), rowShapes["opv3:run:"])
	if tpl.sized {
		t.Fatal("a row without its family's pad field was marked resizable")
	}
	ns := "z1f"
	got := tpl.build(nil, ns, tpl.size(len(ns))+300)
	if len(got) != tpl.size(len(ns)) {
		t.Errorf("an unsized clone was resized to %d bytes, want its own %d", len(got), tpl.size(len(ns)))
	}
}

func TestTemplateShrinkStopsAtTheRemovableTail(t *testing.T) {
	// An escape at the end of the string is never cut into.
	value := `{"message":"ab\u00e9cd"}`
	tpl := compileTemplate("p:", []byte("p:1"), []byte(value), rowShape{pad: "message"})
	got := tpl.build(nil, "z1", 3)
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("shrunk clone is not JSON: %v: %s", err, got)
	}
	if !strings.HasPrefix(m["message"], "ab") {
		t.Errorf("shrink ate into the content: %q", m["message"])
	}
}

func TestNonJSONValueIsClonedVerbatim(t *testing.T) {
	tpl := compileTemplate("act:lvl:", []byte("act:lvl:1"), []byte("info\x00raw"), rowShape{pad: "summary"})
	if tpl.sized {
		t.Fatal("a non-JSON value was marked resizable")
	}
	if got := tpl.build(nil, "z1", 99); string(got) != "info\x00raw" {
		t.Errorf("non-JSON clone = %q", got)
	}
}

// TestFamiliesM3DrainsAreNeverPadded: every field of u:, role:, apikey:,
// invite: and oauthid: is one M3 decodes, checks or normalises (a key's
// description is trimmed and capped, its token hash must be a SHA-256), so a
// padded one is a cutover blocker or a recorded change the corpus never had.
func TestFamiliesM3DrainsAreNeverPadded(t *testing.T) {
	for _, p := range []string{"u:", "role:", "apikey:", "invite:", "oauthid:"} {
		if pad := rowShapes[p].pad; pad != "" {
			t.Errorf("%s pads %q: M3 reads every field of the family", p, pad)
		}
	}
}

// TestAPIKeyCloneHoldsItsOwnSHA256: M3 refuses a key whose token hash is not
// 64 hex digits and treats two keys sharing one as duplicates, so each clone
// holds a SHA-256 of its own, and every other field is the template's.
func TestAPIKeyCloneHoldsItsOwnSHA256(t *testing.T) {
	const tmplHash = "5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8"
	value := `{"id":"` + ulid + `","user_id":"01M72KQJFNXWXQMGSX75S0JC6H","name":"oracle","description":"the clocktower key","token_hash":"` + tmplHash + `","scopes":["library.view"],"status":"active","created_at":"2026-11-10T11:00:00Z"}`
	tpl := compileTemplate("apikey:", []byte("apikey:"+ulid), []byte(value), rowShapes["apikey:"])
	if tpl.sized {
		t.Fatal("an API key row was marked resizable")
	}
	var tmpl map[string]any
	if err := json.Unmarshal([]byte(value), &tmpl); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{tmplHash: "the template"}
	for i := range int64(40) {
		ns := suffixFor(i, 4)
		got := tpl.build(nil, ns, tpl.size(len(ns))+200)
		if len(got) != tpl.size(len(ns)) {
			t.Errorf("clone %s was resized: %d bytes, want %d", ns, len(got), tpl.size(len(ns)))
		}
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("clone %s is not JSON: %v: %s", ns, err, got)
		}
		h, _ := m["token_hash"].(string)
		if raw, err := hex.DecodeString(h); err != nil || len(raw) != sha256.Size {
			t.Errorf("clone %s holds token hash %q: not a SHA-256", ns, h)
		}
		if prev, dup := seen[h]; dup {
			t.Errorf("clone %s shares its token hash with %s", ns, prev)
		}
		seen[h] = ns
		if m["id"] != ns {
			t.Errorf("clone %s keeps id %v", ns, m["id"])
		}
		for _, k := range []string{"user_id", "name", "description", "scopes", "status", "created_at"} {
			if fmt.Sprint(m[k]) != fmt.Sprint(tmpl[k]) {
				t.Errorf("clone %s changed %s: %v, template %v", ns, k, m[k], tmpl[k])
			}
		}
	}
}

func TestStringFieldFindsOnlyATopLevelStringValue(t *testing.T) {
	doc := []byte(`{"message":{"message":"nested"},"list":["message"],"n":1,"message2":"x","msg":"a\"b"}`)
	if _, _, ok := stringField(doc, "message"); ok {
		t.Error("an object value, a nested key or an array element was taken for a top-level string field")
	}
	if _, _, ok := stringField(doc, "n"); ok {
		t.Error("a number was taken for a string field")
	}
	start, end, ok := stringField(doc, "msg")
	if !ok || string(doc[start:end]) != `a\"b` {
		t.Errorf("msg = %q, %v; want its escaped content", doc[start:end], ok)
	}
	start, end, ok = stringField([]byte(`{"a":"1","a":"22"}`), "a")
	if !ok || end-start != 2 {
		t.Errorf("a duplicate name: got %d bytes, want the last value's", end-start)
	}
}

func TestSuffixForIsUniquePerRowAndFixedWidth(t *testing.T) {
	seen := map[string]int64{}
	for i := int64(0); i < 5000; i++ {
		s := suffixFor(i, 6)
		if len(s) != 6 || s[0] != 'z' {
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
	// Runs: one failed (it carries the error message, the family's pad
	// field) and three that succeeded (no error: cloned at their own size).
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("01J9ZRUN0000000000%08d", i)
		if i == 0 {
			set("opv3:run:"+id, fmt.Sprintf(`{"id":%q,"def_id":"scan","state":"failed","error":"the Batcave flooded"}`, id))
		} else {
			set("opv3:run:"+id, fmt.Sprintf(`{"id":%q,"def_id":"scan","state":"succeeded"}`, id))
		}
	}
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("01J9ZKEY0000000000%08d", i)
		set("apikey:"+id, fmt.Sprintf(`{"id":%q,"user_id":"01J9ZGOTHAMCITY00000000000","name":"k%d","token_hash":%q,"status":"active"}`,
			id, i, strings.Repeat(fmt.Sprintf("%x", i+10), 64)))
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
		{Prefix: "opv3:run:", Keys: 44, RawKeyBytes: 44 * 34, RawValueBytes: 44 * 300},
		{Prefix: "apikey:", Keys: 30, RawKeyBytes: 30 * 33, RawValueBytes: 30 * 400},
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
	// u: names no pad field (M3 reads all of it): the key counts are met and
	// each clone is its template's size, reported, not forced.
	if s := byPrefix["u:"]; s.Keys != 40 || s.RawKeyBytes != 40*32 || s.RawValueBytes >= 40*120 {
		t.Errorf("u: = %+v, want the key counts and values at the templates' size", s)
	}
	// opv3:run: only its failed template is sized; it takes the bytes the
	// others' fixed size leaves, so the family total is the target exactly.
	if s := byPrefix["opv3:run:"]; s.Keys != 44 || s.RawKeyBytes != 44*34 || s.RawValueBytes != 44*300 {
		t.Errorf("opv3:run: = %+v, want exactly the counts", s)
	}
	for _, r := range rep.Families {
		if r.Prefix == "u:" && !slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, "keeps its own size") }) {
			t.Errorf("u:'s report does not say its values keep the template size: %v", r.Notes)
		}
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
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte("u:z"), UpperBound: []byte("u:{")})
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
	// Every API key, stage 1's and generated, holds its own SHA-256.
	kit, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte("apikey:"), UpperBound: []byte("apikey;")})
	if err != nil {
		t.Fatal(err)
	}
	defer kit.Close()
	hashes := map[string]string{}
	for ok := kit.First(); ok; ok = kit.Next() {
		var k struct {
			TokenHash string `json:"token_hash"`
		}
		if err := json.Unmarshal(kit.Value(), &k); err != nil {
			t.Fatalf("key row %q is not JSON: %v", kit.Key(), err)
		}
		if raw, err := hex.DecodeString(k.TokenHash); err != nil || len(raw) != sha256.Size {
			t.Errorf("key row %q holds %q: not a SHA-256", kit.Key(), k.TokenHash)
		}
		if prev, dup := hashes[k.TokenHash]; dup {
			t.Errorf("key rows %q and %q share a token hash", prev, kit.Key())
		}
		hashes[k.TokenHash] = string(kit.Key())
	}
	if len(hashes) != 30 {
		t.Errorf("%d API keys hold a distinct hash, want 30", len(hashes))
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

// TestGenerateRefusesAStage1KeyUnderTheSuffixLead: generated keys are the
// family prefix, suffixLead and hex, so a stage-1 key already under
// prefix+suffixLead could be overwritten. The generator refuses before it
// writes a row, and the store keeps that key's value.
func TestGenerateRefusesAStage1KeyUnderTheSuffixLead(t *testing.T) {
	path := seedStore(t)
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	held := []byte("u:" + suffixLead + "0001")
	if err := db.Set(held, []byte(`{"id":"z0001","username":"zsasz"}`), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	fams := []family{{Prefix: "u:", Keys: 40, RawKeyBytes: 40 * 32, RawValueBytes: 40 * 120}}
	_, err = generate(path, "", fams, 1, 2)
	if err == nil || !strings.Contains(err.Error(), "already holds") || !strings.Contains(err.Error(), string(held)) {
		t.Fatalf("generate over a store holding %q: %v; want a refusal naming it", held, err)
	}
	db, err = pebble.Open(path, &pebble.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, closer, err := db.Get(held)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if !strings.Contains(string(v), "zsasz") {
		t.Errorf("the refused run changed %q: %s", held, v)
	}
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte("u:"), UpperBound: []byte("u;")})
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	n := 0
	for ok := it.First(); ok; ok = it.Next() {
		n++
	}
	if n != 4 {
		t.Errorf("the refused run wrote rows: %d u: keys, want the 4 it found", n)
	}
}

// TestGenerateNeverClonesARowOutOfItsCutoverRange: an idx: row under one of
// M3's idx: children (idx:user:) is never an idx: template, because its clone
// at idx:z… would sit in no step's Source and keep the ID M3 remaps; an idx:
// row of no narrower range is, and the report says how many were left out. A
// family whose every row is in such a range is skipped and says why.
func TestGenerateNeverClonesARowOutOfItsCutoverRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audiobooks.pebble")
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const system = "01J9ZGOTHAMCITY00000000000"
	for k, v := range map[string]string{
		"idx:user:username:_system":    system,
		"idx:user:email:_system@local": system,
		"idx:playlist:name:arkham":     "01J9ZPLAYLIST0000000000000",
		"setting:bootstrap_token_hash": strings.Repeat("ab", 32),
		"setting:openai_api_key":       "enc:joker",
		"book:untouched":               "x",
	} {
		if err := db.Set([]byte(k), []byte(v), pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	fams := []family{
		{Prefix: "idx:", Keys: 30, RawKeyBytes: 30 * 40, RawValueBytes: 30 * 26},
		{Prefix: "setting:", Keys: 10, RawKeyBytes: 10 * 30, RawValueBytes: 10 * 40},
	}
	rep, err := generate(path, "", fams, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	byPrefix := map[string]familyReport{}
	for _, fr := range rep.Families {
		byPrefix[fr.Prefix] = fr
	}
	if fr := byPrefix["idx:"]; fr.Templates != 1 || fr.GeneratedKeys != 27 || len(fr.Notes) == 0 || !strings.Contains(strings.Join(fr.Notes, "\n"), "2 row(s)") {
		t.Errorf("idx: report %+v: want one template (the playlist row), 27 rows generated and a note naming the 2 left out", fr)
	}
	if fr := byPrefix["setting:"]; fr.GeneratedKeys != 0 || !strings.Contains(fr.Skipped, "2 in a narrower cutover Source") {
		t.Errorf("setting: report %+v: want it skipped, naming the 2 rows in a narrower range", fr)
	}

	db, err = pebble.Open(path, &pebble.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte("idx:" + suffixLead), UpperBound: []byte("idx:" + string(suffixLead[0]+1))})
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	n := 0
	for ok := it.First(); ok; ok = it.Next() {
		n++
		if string(it.Value()) == system {
			t.Fatalf("%s is a clone of an idx:user: row: %s", it.Key(), it.Value())
		}
	}
	if n != 27 {
		t.Errorf("%d generated idx: rows, want 27", n)
	}
}
