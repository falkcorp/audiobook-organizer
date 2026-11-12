// file: internal/books/cutover/config_plan_url_test.go
// version: 1.0.0
// guid: 8e3b5d61-0c47-4f92-a1d8-7b92e60c4f13
// last-edited: 2026-11-11

package cutover

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// A credential M2 cannot prove absent from a URL does not reach a staged
// config key: config rows are plain, readable by every operator and every
// log. M2 splits a well-formed userinfo into a secret; a URL it cannot parse,
// or whose userinfo net/url does not report, quarantines the whole member
// sealed and stages nothing. These are the review's probes (cases A-F) made
// into tests, and the table-wide check that no URL-typed key escapes the guard.

func TestSplitURLUserinfo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		verdict urlVerdict
		clean   string
		info    string
	}{
		{"empty", "", urlClean, "", ""},
		{"plain", "http://192.0.2.20:9000/asr", urlClean, "http://192.0.2.20:9000/asr", ""},
		{"host only", "192.0.2.20:9000", urlClean, "192.0.2.20:9000", ""},
		{"an @ in the path is not userinfo", "http://192.0.2.20:9000/a@b", urlClean, "http://192.0.2.20:9000/a@b", ""},
		{"an @ in the query is not userinfo", "http://192.0.2.20:9000/asr?mail=bruce@batcave.example", urlClean, "http://192.0.2.20:9000/asr?mail=bruce@batcave.example", ""},
		{"an @ in the fragment is not userinfo", "http://192.0.2.20:9000/#a@b", urlClean, "http://192.0.2.20:9000/#a@b", ""},
		{"a schemeless path with an @", "/mnt/bat/a@b", urlClean, "/mnt/bat/a@b", ""},
		{"well-formed userinfo", "http://batman:Joker-0401@192.0.2.20:9000/asr", urlSplit, "http://192.0.2.20:9000/asr", "batman:Joker-0401"},
		{"a user and no password", "http://batman@192.0.2.20:9000", urlSplit, "http://192.0.2.20:9000", "batman"},
		{"percent-escaped userinfo", "http://batman:p%40ss-0402@192.0.2.20", urlSplit, "http://192.0.2.20", "batman:p%40ss-0402"},
		// Probe case A: a / in the password ends the authority early; the
		// remainder is not a host, and net/url refuses the parse.
		{"A: slash in the password", "http://batman:p/ss-Joker0301@192.0.2.20:9000/asr", urlUnproven, "http://batman:p/ss-Joker0301@192.0.2.20:9000/asr", ""},
		// Probe case B: a space is not a valid userinfo byte.
		{"B: space in the password", "http://batman:Joker0302 x@192.0.2.20:9000", urlUnproven, "", ""},
		// Probe case C: a bad percent-escape.
		{"C: bad percent escape", "http://batman:Joker0303%zz@192.0.2.20:9000", urlUnproven, "", ""},
		// Probe case D: no scheme, so net/url reads user:pw@host:port as an
		// opaque URL and reports no User.
		{"D: schemeless user:pw@host:port", "batman:Joker0304@192.0.2.20:4317", urlUnproven, "", ""},
		{"schemeless user@host", "batman@192.0.2.20", urlUnproven, "", ""},
		{"a scheme of letters only before an @", "smb://batman:Joker-0403@192.0.2.25/books", urlSplit, "smb://192.0.2.25/books", "batman:Joker-0403"},
		{"no @, no userinfo, whatever it parses as", "%zz", urlClean, "%zz", ""},
		{"an @ and not a URL", "%zz@x", urlUnproven, "", ""},
		{"an opaque URL with an @", "http:batman:Joker0306@192.0.2.20", urlUnproven, "", ""},
		{"a network-path reference with userinfo", "//batman:Joker0307@192.0.2.20", urlSplit, "//192.0.2.20", "batman:Joker0307"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clean, info, v := splitURLUserinfo(tc.in)
			if v != tc.verdict {
				t.Fatalf("verdict %d, want %d", v, tc.verdict)
			}
			switch v {
			case urlClean, urlSplit:
				if clean != tc.clean || info != tc.info {
					t.Errorf("clean %q info %q, want %q and %q", clean, info, tc.clean, tc.info)
				}
			case urlUnproven:
				if info != "" {
					t.Errorf("an unproven URL returned userinfo %q", info)
				}
			}
		})
	}
}

func TestAuthorityHasAt(t *testing.T) {
	for in, want := range map[string]bool{
		"batman:pw@192.0.2.20:4317":            true,
		"http://batman@192.0.2.20":             true,
		"http://192.0.2.20/x@y":                false,
		"http://192.0.2.20?x=a@b":              false,
		"http://192.0.2.20#a@b":                false,
		"192.0.2.20:4317":                      false,
		"":                                     false,
		"//batman:pw@192.0.2.20":               true,
		"a/b://c@d":                            false,
		"http://192.0.2.20/redirect?to=a://b@": false,
	} {
		if got := authorityHasAt(in); got != want {
			t.Errorf("authorityHasAt(%q) = %v, want %v", in, got, want)
		}
	}
}

// where lists every place a credential is recoverable from or leaks into after
// the cutover: staged config values and plain core rows (leaks), the secrets
// store and the sealed quarantine (safe), and the log (a leak).
type where struct{ staged, rawCore, logs, secret, quarantined bool }

func locate(r *rig, snap *snapshot, cred string) where {
	var w where
	for _, c := range snap.Calls {
		if strings.Contains(c.Value, cred) {
			w.staged = true
		}
	}
	for k, v := range snap.RawCore {
		if !strings.Contains(string(v), cred) {
			continue
		}
		if strings.Contains(k, "secrets") || strings.Contains(k, "storage/quarantine") {
			continue // sealed
		}
		w.rawCore = true
	}
	w.logs = strings.Contains(r.logs.String(), cred)
	for name, v := range snap.Secrets {
		if strings.Contains(v, cred) {
			if strings.HasPrefix(name, "core.storage.quarantine.") {
				w.quarantined = true
			} else {
				w.secret = true
			}
		}
	}
	for _, q := range snap.Quarantine {
		if q.Sealed && strings.Contains(q.Value, cred) {
			w.quarantined = true
		}
	}
	return w
}

func requireNoLeakOnly(t *testing.T, label string, w where) {
	t.Helper()
	if w.staged || w.rawCore || w.logs {
		t.Errorf("%s: the credential leaked (staged config %v, plain core row %v, log %v)", label, w.staged, w.rawCore, w.logs)
	}
}

func requireNoLeak(t *testing.T, label string, w where) {
	t.Helper()
	requireNoLeakOnly(t, label, w)
	if !w.secret && !w.quarantined {
		t.Errorf("%s: the credential is nowhere recoverable (secret %v, quarantine %v)", label, w.secret, w.quarantined)
	}
}

// TestConfigPlan_URLUserinfoUnprovenIsQuarantined: probes A-D of the review. A
// URL that does not parse, or whose userinfo net/url does not report, is
// quarantined sealed with its credential recoverable; nothing is staged for it.
func TestConfigPlan_URLUserinfoUnprovenIsQuarantined(t *testing.T) {
	for _, tc := range []struct {
		name, head, cred, value string
	}{
		{"A: slash in the password", "whisper_remote_url", "Joker0301", "http://batman:p/ss-Joker0301@192.0.2.20:9000/asr"},
		{"B: space in the password", "whisper_remote_url", "Joker0302", "http://batman:Joker0302 x@192.0.2.20:9000"},
		{"C: bad percent escape", "whisper_remote_url", "Joker0303", "http://batman:Joker0303%zz@192.0.2.20:9000"},
		{"D: schemeless user:pw@host:port", "otel_exporter_otlp_endpoint", "Joker0304", "batman:Joker0304@192.0.2.20:4317"},
		{"redirect base with a bad escape", "oauth_redirect_base_url", "Joker0305", "https://batman:Joker0305%zz@bat.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, ok := Table().Row(tc.head)
			if !ok || !row.URL {
				t.Fatalf("%s is not a URL row of the table", tc.head)
			}
			path := row.Targets[0].Path
			r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { tree[tc.head] = tc.value }))
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			if got, staged := stagedValue(snap, path); staged {
				t.Errorf("%s was staged as %s though its userinfo could not be split out", path, got)
			}
			q, ok := quarantineFor(snap, string(quarantineKey(tc.head)))
			if !ok || q.Reason != reasonURLUnproven || !q.Sealed {
				t.Fatalf("%s: quarantined %v, reason %q, sealed %v; records %+v", tc.head, ok, q.Reason, q.Sealed, snap.Quarantine)
			}
			if !strings.Contains(q.Value, tc.cred) {
				t.Errorf("%s: the quarantine does not hold the original value", tc.head)
			}
			requireNoLeak(t, tc.name, locate(r, snap, tc.cred))
			checkQuarantined(t, r, snap)
		})
	}
}

// TestConfigPlan_URLUserinfoSplitInNestedAndListValues: probes E and F. A
// well-formed userinfo is moved to a secret named by its position, and the
// staged value keeps the URL and every other field.
func TestConfigPlan_URLUserinfoSplitInNestedAndListValues(t *testing.T) {
	t.Run("E: a sub-key URL", func(t *testing.T) {
		r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) {
			e, _ := tree["embedding"].(map[string]any)
			if e == nil {
				e = map[string]any{}
				tree["embedding"] = e
			}
			e["base_url"] = "http://batman:Joker0305@192.0.2.21:11434/v1"
		}))
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		if got, _ := stagedValue(snap, "modules.books.embedding.base_url"); strings.TrimSpace(got) != `"http://192.0.2.21:11434/v1"` {
			t.Errorf("staged as %s", got)
		}
		if got := snap.Secrets["modules.books.embedding.base_url.userinfo"]; got != "batman:Joker0305" {
			t.Errorf("the userinfo secret does not read back (set %v)", got != "")
		}
		requireNoLeak(t, "E", locate(r, snap, "Joker0305"))
	})

	whisper := func(elements ...any) func(*rig) {
		return mutateBlob(func(tree map[string]any) { tree["whisper_endpoints"] = elements })
	}
	t.Run("F: a list element's URL", func(t *testing.T) {
		r := newRig(t, "stage1", whisper(
			map[string]any{"url": "http://batman:Joker0306@192.0.2.22:9000", "concurrency": 3, "label": "bat", "priority": 2, "require_gpu": true, "capabilities": []any{"asr"}},
			map[string]any{"url": "http://192.0.2.23:9000", "concurrency": 1, "label": "robin"},
			map[string]any{"url": "https://robin:Joker0307@192.0.2.24:9443/asr", "label": "nightwing"},
		))
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		raw, ok := stagedValue(snap, "modules.books.whisper.endpoints")
		if !ok {
			t.Fatal("modules.books.whisper.endpoints was not staged")
		}
		var got []map[string]any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&got); err != nil || len(got) != 3 {
			t.Fatalf("staged %s (err %v), want three elements", raw, err)
		}
		if got[0]["url"] != "http://192.0.2.22:9000" || got[1]["url"] != "http://192.0.2.23:9000" || got[2]["url"] != "https://192.0.2.24:9443/asr" {
			t.Errorf("urls = %v, %v, %v", got[0]["url"], got[1]["url"], got[2]["url"])
		}
		if got[0]["label"] != "bat" || got[0]["require_gpu"] != true || got[0]["concurrency"] != json.Number("3") || got[0]["priority"] != json.Number("2") {
			t.Errorf("the element's other fields changed: %v", got[0])
		}
		if caps, _ := got[0]["capabilities"].([]any); len(caps) != 1 || caps[0] != "asr" {
			t.Errorf("the element's list field changed: %v", got[0]["capabilities"])
		}
		for name, want := range map[string]string{
			"modules.books.whisper.endpoints.item-0.url.userinfo": "batman:Joker0306",
			"modules.books.whisper.endpoints.item-2.url.userinfo": "robin:Joker0307",
		} {
			if snap.Secrets[name] != want {
				t.Errorf("secret %s does not read back (set %v)", name, snap.Secrets[name] != "")
			}
		}
		if _, ok := snap.Secrets["modules.books.whisper.endpoints.item-1.url.userinfo"]; ok {
			t.Error("a secret was made for the element with no userinfo")
		}
		requireNoLeak(t, "F item 0", locate(r, snap, "Joker0306"))
		requireNoLeak(t, "F item 2", locate(r, snap, "Joker0307"))
	})

	t.Run("one element that cannot be proven quarantines the whole list", func(t *testing.T) {
		r := newRig(t, "stage1", whisper(
			map[string]any{"url": "http://batman:Joker0308@192.0.2.22:9000", "label": "bat"},
			map[string]any{"url": "http://robin:Joker0309%zz@192.0.2.23:9000", "label": "robin"},
		))
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		if got, staged := stagedValue(snap, "modules.books.whisper.endpoints"); staged {
			t.Errorf("a list with an unprovable element was staged as %s", got)
		}
		q, ok := quarantineFor(snap, string(quarantineKey("whisper_endpoints")))
		if !ok || q.Reason != reasonURLUnproven || !q.Sealed {
			t.Fatalf("quarantined %v, reason %q, sealed %v", ok, q.Reason, q.Sealed)
		}
		// The good element's credential is not split out (nothing was staged),
		// so it is recoverable from the same record as the bad one's.
		for _, cred := range []string{"Joker0308", "Joker0309"} {
			if !strings.Contains(q.Value, cred) {
				t.Errorf("the record does not hold the original value (%s)", cred)
			}
			requireNoLeak(t, cred, locate(r, snap, cred))
		}
	})

	t.Run("a credential in a field that is not URL-named is not this guard's", func(t *testing.T) {
		r := newRig(t, "stage1", whisper(map[string]any{"url": "http://192.0.2.22:9000", "label": "token=Joker0310@x"}))
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		if _, ok := stagedValue(snap, "modules.books.whisper.endpoints"); !ok {
			t.Error("a free-text label with an @ quarantined the list")
		}
	})
}

// urlLeaf is one URL-named string field of HEAD's Config.
type urlLeaf struct {
	top     string   // the blob's top-level member
	segs    []string // object fields below it; a slice of structs is "[]" before its field
	name    string   // "top.seg.name", with [] for a list
	strList bool     // the field is a list of strings
}

// urlLeaves walks the frozen HEAD Config and returns every string field whose
// JSON name isURLName, at any depth, through struct fields and slices of
// structs (maps are the plugins' and the credentials' own handling).
func urlLeaves() []urlLeaf {
	var out []urlLeaf
	var walk func(t reflect.Type, top string, segs []string)
	walk = func(t reflect.Type, top string, segs []string) {
		for i := range t.NumField() {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			here, nextTop := segs, top
			if top == "" {
				nextTop = tag
			} else {
				here = append(slices.Clone(segs), tag)
			}
			ft := f.Type
			isList := false
			if ft.Kind() == reflect.Slice {
				isList = true
				ft = ft.Elem()
			}
			switch {
			case ft.Kind() == reflect.String && isURLName(tag):
				out = append(out, urlLeaf{top: nextTop, segs: here, name: leafName(nextTop, here, isList), strList: isList})
			case ft.Kind() == reflect.Struct:
				next := here
				if isList {
					next = append(slices.Clone(here), "[]")
				}
				walk(ft, nextTop, next)
			}
		}
	}
	walk(reflect.TypeOf(headConfig{}), "", nil)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func leafName(top string, segs []string, list bool) string {
	n := top
	for _, s := range segs {
		if s == "[]" {
			n += "[]"
		} else {
			n += "." + s
		}
	}
	if list {
		n += "[]"
	}
	return n
}

// plant writes value at leaf in tree, creating objects and one-element lists.
func (l urlLeaf) plant(tree map[string]any, value string) {
	cur := tree
	path := append([]string{l.top}, l.segs...)
	for i, s := range path {
		last := i == len(path)-1
		if s == "[]" {
			continue
		}
		if last {
			if l.strList {
				cur[s] = []any{value}
			} else {
				cur[s] = value
			}
			return
		}
		inList := i+1 < len(path) && path[i+1] == "[]"
		if inList {
			list, _ := cur[s].([]any)
			if len(list) == 0 {
				list = []any{map[string]any{}}
				cur[s] = list
			}
			el, ok := list[0].(map[string]any)
			if !ok {
				el = map[string]any{}
				list[0] = el
			}
			cur = el
			continue
		}
		next, ok := cur[s].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[s] = next
		}
		cur = next
	}
}

// ownedByF708 are the URL-typed fields of the class-i members: M2 leaves each
// member whole in the blob for M6 (F7-08), which owns splitting their userinfo
// into secrets. M2's guard covers every other URL-typed field.
var ownedByF708 = []string{
	"ai_endpoints[].url",
	"deluge_web_url",
	"metadata_sources[].base_url",
	"openai_base_url",
	"path_aliases[].smb_url",
}

// TestConfigPlan_NoURLTypedKeyEscapesTheGuard: plant a credential in every
// URL-named string field of HEAD's Config, well-formed and then unprovable,
// and require it appears in no staged config value, no plain core row and no
// log, but is recoverable (a userinfo secret, or the sealed quarantine). The
// class-i members' fields are the explicit exception, pinned to F7-08.
func TestConfigPlan_NoURLTypedKeyEscapesTheGuard(t *testing.T) {
	leaves := urlLeaves()
	if len(leaves) < 12 {
		t.Fatalf("only %d URL-named leaves found, the reflection walk is broken", len(leaves))
	}
	classI := func(l urlLeaf) bool {
		row, ok := Table().Row(l.top)
		return ok && row.Class == ClassInput
	}
	var owned []string
	for _, l := range leaves {
		if classI(l) {
			owned = append(owned, l.name)
		}
	}
	if !slices.Equal(owned, ownedByF708) {
		t.Fatalf("the class-i URL fields are %v, want exactly %v: a new one belongs to F7-08, and the seams note names them", owned, ownedByF708)
	}

	for _, form := range []struct {
		name string
		url  func(cred string) string
	}{
		{"well-formed userinfo", func(c string) string { return "http://batman:" + c + "@192.0.2.9:1/x" }},
		{"unparsable userinfo", func(c string) string { return "http://batman:" + c + "%zz@192.0.2.9:1/x" }},
		{"schemeless userinfo", func(c string) string { return "batman:" + c + "@192.0.2.9:1" }},
	} {
		t.Run(form.name, func(t *testing.T) {
			creds := map[string]urlLeaf{}
			r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) {
				for i, l := range leaves {
					cred := "Cred" + strings.Repeat("0", 3-len(itoa(i))) + itoa(i) + "Joker"
					creds[cred] = l
					l.plant(tree, form.url(cred))
				}
			}))
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			guarded := 0
			for cred, l := range creds {
				w := locate(r, snap, cred)
				if classI(l) {
					continue
				}
				guarded++
				// A dropped member (class x) is read by nothing and HEAD's codec has
				// already folded its scalars into the class-i member that stays; its
				// value is not kept, but it must not leak either.
				if row, _ := Table().Row(l.top); row.Class == ClassDropped {
					requireNoLeakOnly(t, l.name, w)
					continue
				}
				requireNoLeak(t, l.name, w)
			}
			if guarded < 7 {
				t.Fatalf("only %d guarded leaves were checked", guarded)
			}
			checkQuarantined(t, r, snap)
		})
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// TestRenameTable_URLRowsAreDerivedFromNames: the generator derives a row's
// URL flag from its name, so a new URL-named HEAD field is guarded without
// anyone listing it.
func TestRenameTable_URLRowsAreDerivedFromNames(t *testing.T) {
	n := 0
	for _, r := range Table().Rows {
		if want := isURLName(lastSegment(r.Head)); r.URL != want {
			t.Errorf("%s: URL = %v, want %v from its name", r.Head, r.URL, want)
		}
		if r.URL {
			n++
		}
	}
	if n < 8 {
		t.Errorf("%d URL rows, want at least the eight the golden pins", n)
	}
	for _, name := range []string{"url", "endpoint", "base_url", "smb_url", "otel_exporter_otlp_endpoint", "whisper_remote_url"} {
		if !isURLName(name) {
			t.Errorf("isURLName(%q) = false", name)
		}
	}
	for _, name := range []string{"label", "urls", "curl", "endpoints", "hurl_count", ""} {
		if isURLName(name) {
			t.Errorf("isURLName(%q) = true", name)
		}
	}
}
