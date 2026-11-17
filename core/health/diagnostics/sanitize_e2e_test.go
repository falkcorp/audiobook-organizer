// file: core/health/diagnostics/sanitize_e2e_test.go
// version: 1.1.0
// guid: 17158067-310c-4881-b709-9b9df6b06410
// last-edited: 2026-11-16

package diagnostics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/health/diagnostics"
)

// The usernames and directory names below are Batman-universe people. The
// invariant of this file: not one of them is in any byte of the bundle, in an
// entry's content or its name, in the zip's own headers, or in the manifest.
var plantedNames = []string{"selina", "harvey", "harleen", "Quinzel", "Kyle", "Dent", "Wayne", "Hush", "Brucie", "Pennyworth", "Gordon", "Halloween", "Nightwing", "Grayson"}

// scanBundle fails the test for every planted name in the bundle: each entry's
// name and decompressed content, and the raw bytes of the file.
func scanBundle(t *testing.T, f *fixture, file string, extra ...string) map[string][]byte {
	t.Helper()
	files := f.zipFiles(file)
	raw, err := os.ReadFile(filepath.Join(f.dir, file))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range append(slices.Clone(plantedNames), extra...) {
		if bytes.Contains(raw, []byte(n)) {
			t.Errorf("the raw bundle holds %q", n)
		}
		for name, data := range files {
			if strings.Contains(name, n) {
				t.Errorf("entry name %q holds %q", name, n)
			}
			if bytes.Contains(data, []byte(n)) {
				t.Errorf("%s holds %q:\n%s", name, n, data)
			}
		}
	}
	return files
}

// ringLines is a fake log ring: structured lines in the shapes a producer
// writes, each carrying a location in a different spelling.
func ringLines() []string {
	return []string{
		`{"time":"2026-11-13T09:30:00Z","level":"ERROR","msg":"open /Users/selina/My Books/Hush.m4b: permission denied","route":"GET /api/v1/books/{id}","method":"GET","component":"core.scan","reason":"no_roots:read"}`,
		`{"time":"2026-11-13T09:30:01Z","level":"WARN","msg":"scan failed","err":"scan /Volumes/Harvey Dent: open /Volumes/Harvey Dent/x: no such file or directory","path":"/Users/selina/My Books/Hush"}`,
		`{"msg":"request","path":"https://abs.example\",\"q\":\"a?b\",\"p\":\"/Users%2F@z%2FHush%2FBooks"}`,
		`{"msg":"stat \"C:\\Users\\Harleen Quinzel\\Music\\Hush.m4b\": Access is denied.","error":"rename /srv/lib/a b /Users/Kyle/c d: permission denied"}`,
		`{"msg":"payload","detail":"{\"path\":\"/Users/selina/My Books/Hush\"}","file":"~selina/Hush","dir":"\\Users\\selina\\Hush"}`,
		`{"msg":"mounted","url":"smb://nas.example/home/selina/Hush","nested":{"list":["nfs://nas.example/export/home/selina/Hush","&#47;Users&#47;selina&#47;Hush","&amp;#47;Users&amp;#47;selina"]}}`,
		`{"/Users/selina/My Books/Hush":"as a key","C:\\Users\\Harleen Quinzel\\x":"another key","msg":"keys"}`,
		`CGO_CFLAGS=-I/Users/selina/include -L/Users/selina/lib`,
		`not json: open /Users/Bruce Wayne/Hush: permission denied`,
		`文件/Users/selina/Hush`,
		`x \\u002FUsers\\u002Fselina\\u002FHush`,
		`HD%2FUsers%2Fselina%2Fa b%2FHush`,
		`sftp://selina:pw@host.example/home/selina/Hush`,
		`C:Users\selina\Hush`,
		"goroutine 1 [running]:\nmain.main()\n\t/Users/selina/src/app/main.go:12 +0x1d",
		// A spaced final segment, not glued to a separator, in prose that is not an error.
		`{"level":"INFO","msg":"copied /srv/lib/Alfred Pennyworth/Batman Hush.m4b to the shelf"}`,
		`{"level":"INFO","msg":"organizing /srv/lib/Jim Gordon/The Long Halloween now"}`,
		`{"msg":"open /Users/Brucie (1)/Hush Part 2: permission denied"}`,
		// A tail that is not an error text, and names that hold ": ", quotes and backticks.
		`{"msg":"write tags /srv/lib/Jim Gordon/Batman: Hush.m4b: unsupported format"}`,
		`{"msg":"import \"/srv/lib/Alfred Pennyworth/Batman Hush\" failed: unsupported codec"}`,
		"open /Users/selina/Brucie `Nightwing`/Hush Part 2: permission denied",
		`open /srv/downloads/Dick Grayson: Nightwing/Hush: no such file or directory`,
		// logfmt, and JSON text inside a JSON string.
		`level=INFO msg=organized path="/srv/lib/Jim Gordon/The Long Halloween"`,
		`{"msg":"payload","detail":"{\"path\":\"/Users/Brucie (1)/Hush\",\"n\":1}"}`,
		// A relative name has no known start.
		`planned Alfred Pennyworth/Batman Hush/Part 1.m4b`,
		`open Jim Gordon/The Long Halloween/Hush.m4b: permission denied`,
	}
}

func ringSource(lines []string) diagnostics.TextSource {
	return func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
	}
}

// TestSanitizeE2E_BuildOverAFakeRing (g): the whole Build, with the planted
// names in a fake log ring, a JSON section's values and keys, a File's lines, a
// JSONL section's rows, a section's error text, and the manifest.
func TestSanitizeE2E_BuildOverAFakeRing(t *testing.T) {
	f := newFixture(t)
	rows := func(yield func(map[string]string) bool) {
		for _, p := range []string{"/Users/selina/My Books/Hush", `C:\Users\Harleen Quinzel\Hush`, "open /Users/Kyle/x y: denied"} {
			if !yield(map[string]string{"path": p, "/Users/selina/key": "v", "route": "GET /api/v1/books/{id}"}) {
				return
			}
		}
	}
	var seq iter.Seq[map[string]string] = rows
	contrib := contributor{
		{Name: "batcave.json", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("d.json", map[string]any{
				"cwd":                          "/Users/selina/My Books",
				"err":                          fmt.Sprintf("open %s: %v", "/Users/Harvey Dent/Hush.m4b", fs.ErrPermission),
				"/Users/Harvey Dent/key":       1,
				`C:\Users\Harleen Quinzel\key`: 2,
				"list":                         []string{"smb://nas.example/home/selina/Hush", "%2FUsers%2Fselina%2Fa b%2FHush", "~selina/Hush"},
				"nested":                       map[string]any{"detail": `error: stat /Users/Kyle/a b: no such file or directory`},
				"route":                        "GET /api/v1/books/{id}",
				"route_wrong":                  "/Users/selina/Hush",
				"component":                    "core.scan",
				"reason":                       "no_roots:read",
				"time":                         "2026-11-13T09:30:00Z",
				"level":                        "INFO",
				"version":                      "v1.2.3",
				"id":                           "8d1e0a7c-1111-2222-3333-444455556666",
				"pattern":                      "/api/v1/files/{path...}",
			})
		}},
		{Name: "batcave.file", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.File("notes.txt", strings.NewReader(strings.Join(ringLines(), "\n")+"\n"))
		}},
		{Name: "batcave.rows", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return diagnostics.WriteJSONL(w, "rows.jsonl", seq)
		}},
		{Name: "batcave.fails", Perm: manage, Write: func(context.Context, diagnostics.Writer, diagnostics.Viewer) error {
			return fmt.Errorf("scan /Users/Harvey Dent: %w", &fs.PathError{Op: "open", Path: "/Users/Harvey Dent/Hush.m4b", Err: fs.ErrPermission})
		}},
		{Name: "batcave.names", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			// Entry names that are, or hold, a location are refused, and the
			// error that says so does not echo the name.
			var errs []error
			for _, n := range []string{"Users/selina/x.json", "C:Users.json", "a%2Fb.json", "~selina.json", "&#47;x.json", "selina/Hush.json", "fork/exec", "ab: input/output error"} {
				errs = append(errs, w.JSON(n, 1))
			}
			for i, err := range errs {
				if err == nil {
					return fmt.Errorf("entry name %d was accepted", i)
				}
				if strings.Contains(err.Error(), "selina") || strings.Contains(err.Error(), "Users") {
					return fmt.Errorf("the refusal echoes the name: %v", err)
				}
			}
			return w.JSON("fine.json", 1)
		}},
	}
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Roots = []diagnostics.PathRoot{{Name: "audiobooks", Path: "/srv/gotham/audiobooks"}}
		cfg.Contributors = []diagnostics.Contributor{contrib}
		cfg.Sources.Logs = ringSource(ringLines())
		cfg.App = "/Users/selina/gotham-audio"
		cfg.Version = "9.9.9"
	})
	res, err := f.build(e, "run-e2e", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := scanBundle(t, f, res.File)

	m := manifestOf(t, files)
	if m.App != "<path>" {
		t.Errorf("manifest app = %q: a location in the app name is wiped", m.App)
	}
	var sawFails bool
	for _, se := range m.Errors {
		if se.Section == "batcave.fails" {
			sawFails = true
			if se.Error != "scan <path>: permission denied" {
				t.Errorf("the section error text = %q", se.Error)
			}
		}
	}
	if !sawFails {
		t.Errorf("the failing section is not in the manifest: %+v", m.Errors)
	}
	if files["batcave.names/fine.json"] == nil {
		t.Errorf("a plain entry name was refused: %v", names(files))
	}
	for name := range files {
		if strings.HasPrefix(name, "batcave.names/") && name != "batcave.names/fine.json" {
			t.Errorf("a location-like entry %q was written", name)
		}
	}

	// The structured fields keep their validated value, and a route field that
	// is not a template does not.
	var d map[string]any
	if err := json.Unmarshal(files["batcave.json/d.json"], &d); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"route": "GET /api/v1/books/{id}", "route_wrong": "<path>", "component": "core.scan", "reason": "no_roots:read",
		"time": "2026-11-13T09:30:00Z", "level": "INFO", "version": "v1.2.3", "pattern": "/api/v1/files/{path...}",
		"id": "8d1e0a7c-1111-2222-3333-444455556666", "cwd": "<path>",
	} {
		if d[k] != want {
			t.Errorf("d.json %s = %v, want %q", k, d[k], want)
		}
	}

	// Every log line is still one JSON object or one line of text.
	logs := strings.Split(strings.TrimSuffix(string(files["core.logs/logs.jsonl"]), "\n"), "\n")
	if len(logs) < len(ringLines()) {
		t.Fatalf("%d log lines, want at least %d:\n%s", len(logs), len(ringLines()), files["core.logs/logs.jsonl"])
	}
	for _, l := range logs {
		if strings.HasPrefix(l, "{") && !json.Valid([]byte(l)) {
			t.Errorf("a redacted JSON line is not valid JSON: %s", l)
		}
	}
}

// TestSanitizeE2E_PasswordLikeUserinfoJoinedToAPath (M6): the log redactor's
// credential scrub on a JSON value that spells a URL, a quote and a path with
// encoded separators builds "https://z%2FHush%2FBooks\"" from it. The location
// rule runs after the redactor, so the authority it built is a location too.
func TestSanitizeE2E_PasswordLikeUserinfoJoinedToAPath(t *testing.T) {
	f := newFixture(t)
	line := `{"msg":"request","path":"https://abs.example\",\"q\":\"a?b\",\"p\":\"/Users%2F@z%2FHush%2FBooks"}`
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Sources.Logs = ringSource([]string{line})
	})
	res, err := f.build(e, "run-m6", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := scanBundle(t, f, res.File, "Books", "%2F", "Users")
	out := string(files["core.logs/logs.jsonl"])
	if !json.Valid([]byte(strings.TrimSpace(out))) {
		t.Errorf("the line is not valid JSON: %s", out)
	}
	// And on the value alone, as a JSON string and as a text line.
	val := `https://abs.example","q":"a?b","p":"/Users%2F@z%2FHush%2FBooks"`
	f2 := newFixture(t)
	e2 := f2.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Contributors = []diagnostics.Contributor{jsonSection("batcave.v", map[string]string{"A": val})}
		cfg.Sources.Logs = ringSource([]string{val})
	})
	res2, err := f2.build(e2, "run-m6b", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	scanBundle(t, f2, res2.File, "Books", "%2F", "Users")
}

// TestSanitizeE2E_LeakTableThroughBothRoads sends the review's strings as a
// JSON value and as a line of a non-JSON log, so the two roads write the same
// text, and asserts it (runPathCases).
func TestSanitizeE2E_LeakTableThroughBothRoads(t *testing.T) {
	runPathCases(t, []pathCase{
		same(`open /Volumes/Macintosh HD/Users/bruce/Audiobooks/Hush.m4b: permission denied`, `open <path>: permission denied`),
		same(`open C:\Users\Bruce Wayne\Music\Hush.m4b: The system cannot find the file specified.`, `open <path>: The system cannot find the file specified.`),
		same(`open "/Users/bruce/My Books/Hush.m4b": permission denied`, `open "<path>: permission denied`),
		same(`scan /Users/bruce/<Last>, <First>/Hush failed`, `scan <path>`),
		same("/Users/bruce/a\tb/Hush", `<path>`),
		same(`%2FUsers%2Fbruce%2Fa b%2FHush`, `<path>`),
		same(`CGO_CFLAGS=-I/Users/bruce/include -L/Users/bruce/lib`, `<path>`),
		same(`é/Users/bruce/Hush`, `<path>`),
		same(`\Users\bruce\Hush`, `<path>`),
		same(`C:Users\bruce\Hush`, `<path>`),
		same(`~bruce/Hush`, `<path>`),
		same(`x \\u002FUsers\\u002Fbruce\\u002FHush`, `x <path>`),
		same(`&#47;Users&#47;bruce&#47;Hush`, `<path>`),
		same(`smb://nas.example/home/bruce/Hush`, `<path>`),
		same(`sftp://bruce:pw@host.example/home/bruce/Hush`, `<path>`),
		same(`/srv/lib/Jim Gordon/Hush`, `<root:lib>`),
		same(`scan /srv/vault/Bruce Wayne/Hush failed`, `scan <root:vault>`),
	})
}

// TestSanitizeE2E_WhatSurvivesInTheBundle: a bundle shows what the sanitizer
// leaves, and no more: the prose before a location, bare names, and not a word
// after a location on its line.
func TestSanitizeE2E_WhatSurvivesInTheBundle(t *testing.T) {
	runPathCases(t, []pathCase{
		same(`user dir /Users/Bruce Wayne is full`, `user dir <path>`),
		same(`ffprobe /srv/lib/Jim Gordon/The Long Halloween.m4b: exit status 1`, `ffprobe <root:lib>: exit status 1`),
		same(`organizing /srv/lib/Jim Gordon/The Long Halloween now`, `organizing <root:lib>`),
		same(`/srv/lib/Alfred Pennyworth/Batman: Hush.m4b`, `<root:lib>`),
		same(`user alfred logged in`, `user alfred logged in`),
	})
}

// TestSanitizeE2E_UnreadableRingIsSanitized: the error of an unreadable ring is
// recorded in the manifest without the location it names.
func TestSanitizeE2E_UnreadableRingIsSanitized(t *testing.T) {
	f := newFixture(t)
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Sources.Logs = func(context.Context) (io.ReadCloser, error) {
			return nil, &fs.PathError{Op: "open", Path: "/Users/selina/My Books/Hush.log", Err: errors.New("permission denied")}
		}
	})
	res, err := f.build(e, "run-ring", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := scanBundle(t, f, res.File)
	var found bool
	for _, se := range manifestOf(t, files).Errors {
		if se.Section == "core.logs" {
			found = true
			if se.Error != "open <path>: permission denied" {
				t.Errorf("error = %q", se.Error)
			}
		}
	}
	if !found {
		t.Errorf("no core.logs error in the manifest: %v", names(files))
	}
}
