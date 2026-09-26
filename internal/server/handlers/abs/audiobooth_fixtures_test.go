// file: internal/server/handlers/abs/audiobooth_fixtures_test.go
// version: 1.0.2
// guid: 5e8a2c17-94b3-4d6f-a0e1-7c3b9f24d8a6
// last-edited: 2026-09-25

package abs_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// The AudioBooth decode proof, Go half.
//
// tests/audiobooth-decode/manifest.json lists every request AudioBooth makes, one row
// per call site: method, path, query, body and the type the app decodes the answer
// into. This test replays those requests, in order, against the real ABS handlers over
// a seeded in-memory library, chaining ids out of earlier responses exactly the way
// the app does (item ids from the items page, the collection id from the create
// response, the progress row id from GET progress, ...).
//
// By default it asserts every answer is the status the app needs and that the keys the
// app reads are non-empty, so `make ci` exercises the whole matrix without Swift.
// With AUDIOBOOTH_FIXTURES_DIR set it also writes each raw body there; the Swift half
// (tests/audiobooth-decode, `make audiobooth-decode`) then decodes those exact bytes
// through AudioBooth's own model types.
//
// A decode oracle that copies the app's models but not its REQUEST manufactures
// failures on correct code (three of them on 2026-09-20). So the request is replayed
// as the app builds it: Content-Type on every request, a Bearer token, and a query
// string encoded the way Foundation's URLComponents.queryItems encodes it, which
// leaves '+' literal. See appQueryEncode.

const audioboothManifestDir = "../../../../tests/audiobooth-decode"

type abManifest struct {
	CallSites []string       `json:"callSites"`
	Requests  []abManifestRq `json:"requests"`
}

type abManifestRq struct {
	ID           string            `json:"id"`
	Extra        bool              `json:"extra"`
	CallSite     string            `json:"callSite"`
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Auth         *bool             `json:"auth"`
	Query        map[string]string `json:"query"`
	Headers      map[string]string `json:"headers"`
	Body         any               `json:"body"`
	Decode       string            `json:"decode"`
	NonEmpty     []string          `json:"nonEmpty"`
	Capture      map[string]string `json:"capture"`
	ExpectStatus int               `json:"expectStatus"`
	NA           string            `json:"na"`
}

// abFixtureIndexEntry is what the Swift half reads to mirror NetworkService.send():
// a non-2xx status throws before any decode, so the status must travel with the body.
type abFixtureIndexEntry struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	File        string `json:"file"`
}

func loadAudioBoothManifest(t *testing.T) *abManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(audioboothManifestDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // body numbers must round-trip as the app encodes them
	var m abManifest
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return &m
}

// TestAudioBoothManifest_CoversEveryCallSite keeps the manifest honest about its own
// denominator: every call-site string in the pinned app has at least one row, and no
// row names a call site outside the list (a typo would silently create coverage).
func TestAudioBoothManifest_CoversEveryCallSite(t *testing.T) {
	m := loadAudioBoothManifest(t)
	if len(m.CallSites) != 45 {
		t.Fatalf("manifest lists %d call sites; the pinned AudioBooth inventory has 45", len(m.CallSites))
	}
	listed := map[string]bool{}
	for _, cs := range m.CallSites {
		if listed[cs] {
			t.Fatalf("call site %q listed twice", cs)
		}
		listed[cs] = true
	}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for _, r := range m.Requests {
		if ids[r.ID] {
			t.Fatalf("duplicate request id %q", r.ID)
		}
		ids[r.ID] = true
		if r.Extra {
			continue
		}
		if !listed[r.CallSite] {
			t.Fatalf("request %q names call site %q, which is not in callSites", r.ID, r.CallSite)
		}
		seen[r.CallSite] = true
		if r.NA == "" && r.Decode == "" {
			t.Fatalf("request %q has no decode type", r.ID)
		}
	}
	for _, cs := range m.CallSites {
		if !seen[cs] {
			t.Errorf("call site %q has no request row", cs)
		}
	}
}

// audioboothNarratorPlus is a real-world narrator name whose base64 contains '+'.
// URLComponents leaves '+' literal in a query and Go's query parser reads it as a
// space, so this is the name that proves the narrator filter survives the app's
// actual encoding rather than Go's.
const audioboothNarratorPlus = "Seán O’Brien"

// seedAudioBoothLibrary extends the oracle seed so every key the app reads has data:
// narrators (one whose filter token carries '+'), a series holding both books, two
// genres, and a listening position so progress, continue-listening and the authorize
// payload are not vacuous.
func seedAudioBoothLibrary(t *testing.T) *oracleSeed {
	t.Helper()
	seed := seedOracleLibrary(t)
	lib := seed.lib
	one, two := 1, 2
	genres := "Speech, Epic Poetry"

	lib.mu.Lock()
	multi, single := lib.books[seed.multiID], lib.books[seed.singleID]
	// "The Iliad" sorts ahead of "The Odyssey", so the app's first title-sorted row
	// (and therefore the id every later request chains from) is the multi-file book.
	multi.Title = "The Iliad"
	multi.Genre = &genres
	lib.series[7] = &database.Series{ID: 7, Name: "Homeric Epics"}
	seven := 7
	multi.SeriesID, multi.SeriesSequence = &seven, &one
	single.SeriesID, single.SeriesSequence = &seven, &two
	lib.mu.Unlock()

	lib.attachNarrators(seed.multiID, audioboothNarratorPlus)
	lib.attachNarrators(seed.singleID, "Simon Vance")
	return seed
}

type audioboothRun struct {
	h    *harness
	tok  string
	vars map[string]any
	// roots are the seed's temp directory, as given and with symlinks resolved
	// (/var -> /private/var on macOS); normalizeFixture rewrites both.
	roots []string
}

func newAudioBoothRun(t *testing.T) *audioboothRun {
	t.Helper()
	seed := seedAudioBoothLibrary(t)
	bm := newFakeBookmarks()
	provider, err := abshandler.NewUserData(abshandler.UserDataOptions{
		Progress: seed.lib, Bookmarks: bm, Identity: seed.lib, Library: seed.lib, AliasUses: seed.lib,
	})
	if err != nil {
		t.Fatalf("NewUserData: %v", err)
	}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(provider),
		withCollections(&abscolFakeStore{}), withPlaylists(&absplFakeStore{}),
		func(o *abshandler.Options) { o.Bookmarks = bm })
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	abscolGrantManage(t, h, "u1")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	// A position part-way into the multi-file book, so GET /api/me/progress/:id
	// answers 200 (the app's reset path fetches the row id from it) and the
	// continue-listening shelf and authorize payload carry a real progress row.
	if err := seed.lib.SetUserPosition("u1", seed.multiID, "abs", 420); err != nil {
		t.Fatalf("SetUserPosition: %v", err)
	}
	roots := []string{seed.root}
	if resolved, err := filepath.EvalSymlinks(seed.root); err == nil && resolved != seed.root {
		// Longest first, so /private/var/... is rewritten before its /var/... suffix.
		roots = []string{resolved, seed.root}
	}
	return &audioboothRun{h: h, tok: tok, roots: roots, vars: map[string]any{
		"username": "oracle", "password": "pw-pw-pw-pw",
	}}
}

var abPlaceholder = regexp.MustCompile(`\{([A-Za-z0-9]+)\}`)

func (r *audioboothRun) fill(t *testing.T, id, s string) string {
	t.Helper()
	return abPlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		v, ok := r.vars[name]
		if !ok {
			t.Fatalf("%s: placeholder %s has no captured value yet", id, m)
		}
		return fmt.Sprint(v)
	})
}

// fillBody substitutes placeholders inside a JSON body. A string that is exactly one
// placeholder takes the captured value WITH its JSON type, so a captured number (a
// bookmark's time) is sent back as a number, as the app's Codable struct sends it.
func (r *audioboothRun) fillBody(t *testing.T, id string, v any) any {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = r.fillBody(t, id, e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.fillBody(t, id, e)
		}
		return out
	case string:
		if m := abPlaceholder.FindStringSubmatch(x); m != nil && m[0] == x {
			val, ok := r.vars[m[1]]
			if !ok {
				t.Fatalf("%s: body placeholder %s has no captured value yet", id, x)
			}
			return val
		}
		return r.fill(t, id, x)
	default:
		return v
	}
}

// appQueryEncode builds the query string the way Foundation's
// URLComponents.queryItems does on Apple platforms (NetworkService.buildURLRequest).
// Measured with swift 6.4: `+`, `/`, `,`, `?` stay literal; `=`, `&`, space and
// non-ASCII are percent-encoded. url.Values.Encode escapes `+` as %2B, which would
// hide exactly the class of bug a base64 filter value can hit.
func appQueryEncode(q map[string]string) string {
	const literal = "-._~!$'()*+,;:@/?"
	enc := func(s string) string {
		var b strings.Builder
		for _, c := range []byte(s) {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
				strings.IndexByte(literal, c) >= 0:
				b.WriteByte(c)
			default:
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
		return b.String()
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, enc(k)+"="+enc(q[k]))
	}
	return strings.Join(parts, "&")
}

// abJSONPath walks a dotted path; numeric segments index arrays. "" is the root.
func abJSONPath(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	cur := v
	for seg := range strings.SplitSeq(path, ".") {
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

var (
	abJWT     = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	abFakeJWT = "eyJhbGciOiJIUzI1NiJ9.eyJmaXh0dXJlIjp0cnVlfQ.Zml4dHVyZQ"
)

// normalizeFixture makes a body safe to commit: the seed's temp directory becomes a
// fixed library root and signed tokens become an inert placeholder. Neither is read
// by any decoder (User.credentials parses the JWT lazily, outside init(from:)).
func normalizeFixture(body []byte, roots []string) []byte {
	out := body
	for _, root := range roots {
		if root != "" {
			out = bytes.ReplaceAll(out, []byte(root), []byte("/audiobooks"))
		}
	}
	return abJWT.ReplaceAll(out, []byte(abFakeJWT))
}

func TestAudioBoothFixtures_ReplayEveryAppRequest(t *testing.T) {
	m := loadAudioBoothManifest(t)
	run := newAudioBoothRun(t)
	outDir := os.Getenv("AUDIOBOOTH_FIXTURES_DIR")

	index := map[string]abFixtureIndexEntry{}
	for _, rq := range m.Requests {
		if rq.NA != "" {
			continue
		}
		t.Run(rq.ID, func(t *testing.T) {
			path := run.fill(t, rq.ID, rq.Path)
			if len(rq.Query) > 0 {
				q := make(map[string]string, len(rq.Query))
				for k, v := range rq.Query {
					q[k] = run.fill(t, rq.ID, v)
				}
				path += "?" + appQueryEncode(q)
			}
			headers := map[string]string{}
			if rq.Auth == nil || *rq.Auth {
				headers["Authorization"] = "Bearer " + run.tok
			}
			for k, v := range rq.Headers {
				headers[k] = run.fill(t, rq.ID, v)
			}
			var body any
			if rq.Body != nil {
				body = run.fillBody(t, rq.ID, rq.Body)
			}
			w, _ := run.h.do(t, request{method: rq.Method, path: path, body: body, headers: headers})
			raw := w.Body.Bytes()

			want := rq.ExpectStatus
			if want == 0 && (w.Code < 200 || w.Code > 299) {
				t.Fatalf("%s %s = %d, the app needs 2xx (NetworkService.send throws otherwise): %s",
					rq.Method, path, w.Code, raw)
			}
			if want != 0 && w.Code != want {
				t.Fatalf("%s %s = %d, want %d: %s", rq.Method, path, w.Code, want, raw)
			}

			var decoded any
			if rq.Decode != "Data" {
				if len(raw) == 0 {
					t.Fatalf("%s: empty body; send() throws cannotDecodeContentData for a non-Data type", rq.ID)
				}
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.UseNumber()
				if err := dec.Decode(&decoded); err != nil {
					t.Fatalf("%s: body is not JSON: %v: %s", rq.ID, err, raw)
				}
			}
			for _, p := range rq.NonEmpty {
				v, ok := abJSONPath(decoded, p)
				if !ok {
					t.Errorf("%s: key %q is absent; the fixture would pass vacuously", rq.ID, p)
					continue
				}
				switch x := v.(type) {
				case []any:
					if len(x) == 0 {
						t.Errorf("%s: %q is an empty array; the fixture would pass vacuously", rq.ID, p)
					}
				case string:
					if x == "" {
						t.Errorf("%s: %q is an empty string", rq.ID, p)
					}
				default:
					t.Errorf("%s: %q is %T, want a non-empty array or string", rq.ID, p, v)
				}
			}
			for name, p := range rq.Capture {
				v, ok := abJSONPath(decoded, p)
				if !ok || v == nil {
					t.Fatalf("%s: cannot capture %s from %q", rq.ID, name, p)
				}
				run.vars[name] = v
				run.vars[name+"B64"] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprint(v)))
			}
			if bytes.Contains(raw, []byte("abk_")) {
				t.Fatalf("%s: body carries an abk_ API key; it must never reach a committed fixture", rq.ID)
			}

			ext := ".json"
			if rq.Decode == "Data" {
				ext = ".body"
			}
			index[rq.ID] = abFixtureIndexEntry{Status: w.Code, ContentType: w.Header().Get("Content-Type"), File: rq.ID + ext}
			if outDir != "" {
				norm := normalizeFixture(raw, run.roots)
				if err := os.WriteFile(filepath.Join(outDir, rq.ID+ext), norm, 0o644); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}
		})
	}

	if outDir != "" && !t.Failed() {
		raw, err := json.MarshalIndent(index, "", "  ")
		if err != nil {
			t.Fatalf("marshal index: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "index.json"), append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("write index: %v", err)
		}
	}
}
