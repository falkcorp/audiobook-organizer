// file: core/health/diagnostics/round4_test.go
// version: 1.0.0
// guid: 7c2e9a40-5d18-4b63-a0f7-1e94c6b83d25
// last-edited: 2026-11-14

package diagnostics_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// pathCase is one input to the path rule and what each road writes. A road
// whose want is "" is not checked, so a case can pin only the road it is about.
type pathCase struct {
	in string
	// value is the output for in as a whole JSON string; text for in as a line
	// of a non-JSON log.
	value, text string
}

// same is a case whose value and text roads agree.
func same(in, want string) pathCase { return pathCase{in: in, value: want, text: want} }

var round4Roots = []diagnostics.PathRoot{{Name: "lib", Path: "/srv/lib"}, {Name: "vault", Path: "/srv/vault"}}

// runPathCases sends every case through the value road and the text-line road
// of a real bundle and compares.
func runPathCases(t *testing.T, cases []pathCase) {
	t.Helper()
	f := newFixture(t)
	vals := map[string]string{}
	var lines []string
	for i, c := range cases {
		vals[string(rune('A'+i))] = c.in
		lines = append(lines, c.in)
	}
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Roots = round4Roots
		cfg.Contributors = []diagnostics.Contributor{jsonSection("batcave.v", vals)}
		cfg.Sources.Logs = func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
		}
	})
	res, err := f.build(e, "run-round4", diagnostics.ExportParams{ActorID: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	var got map[string]string
	if err := json.Unmarshal(files["batcave.v/d.json"], &got); err != nil {
		t.Fatal(err)
	}
	logLines := strings.Split(strings.TrimSuffix(string(files["core.logs/logs.jsonl"]), "\n"), "\n")
	if len(logLines) != len(cases) {
		t.Fatalf("got %d log lines, want %d: %q", len(logLines), len(cases), logLines)
	}
	for i, c := range cases {
		if v := got[string(rune('A'+i))]; c.value != "" && v != c.value {
			t.Errorf("value %q = %q, want %q", c.in, v, c.value)
		}
		if c.text != "" && logLines[i] != c.text {
			t.Errorf("text line %q = %q, want %q", c.in, logLines[i], c.text)
		}
	}
	assertNoneOf(t, files, "bruce", "/home")
}

// TestDiagnostics_QueryStringIsValuesNotRoute: a route is left as written, but
// its query is values. filesystem/browse logs "?path=/Users/bruce/x" (the
// access log writes the raw query), and the route allowlist used to pass the
// whole target through untouched, in a value, a text line and a JSON log line.
func TestDiagnostics_QueryStringIsValuesNotRoute(t *testing.T) {
	runPathCases(t, []pathCase{
		same("/api/v1/filesystem/browse?path=/Users/bruce/x", "/api/v1/filesystem/browse?path=<path>"),
		same("/api/v1/filesystem/browse?path=%2FUsers%2Fbruce", "/api/v1/filesystem/browse?path=<path>"),
		same("/api/v1/filesystem/browse?path=%252FUsers%252Fbruce", "/api/v1/filesystem/browse?path=<path>"),
		same("/api/v1/filesystem/browse?path=/srv/lib/Joker&show=all", "/api/v1/filesystem/browse?path=<root:lib>/Joker&show=all"),
		same("/api/v1/filesystem/browse?path=/srv/lib/Joker%20Wild%2F..%2F..%2Fhome%2Fbruce", "/api/v1/filesystem/browse?path=<path>"),
		same("/api/v1/books?sort=title&page=2", "/api/v1/books?sort=title&page=2"),
		same("/api/v1/books?next=/api/v1/series", "/api/v1/books?next=/api/v1/series"),
		same("/api/v1/books?sort=a%20b", "/api/v1/books?sort=a%20b"),
		same("/metrics?path=/Users/bruce/x", "/metrics?path=<path>"),
		same("/api/v1/books#/home/bruce/y", "/api/v1/books#<path>"),
		same("/api/v1/x?a=/api/x?b=/api/x?c=/api/x?d=/Users/bruce", ""), // nested: no leak, shape not pinned
	})

	// The access log's own JSON line.
	jl := logsOf(t, round4Roots, []string{
		`{"msg":"request","path":"/api/v1/filesystem/browse?path=/Users/bruce/x","status":200}`,
		`{"msg":"request","path":"/api/v1/filesystem/browse?path=%2FUsers%2Fbruce","status":200}`,
	})
	if strings.Contains(jl, "bruce") || strings.Count(jl, `filesystem/browse?path=<path>`) != 2 {
		t.Errorf("access-log JSON lines:\n%s", jl)
	}
}

// TestDiagnostics_BackslashDotDotUnderPosixRoot: filepath.Clean on a POSIX
// host does not read "\" as a separator, and the rule's own rewrite did, so
// "/srv/lib\..\..\home\bruce\x" came out as "<root:lib>/../../home/bruce/x".
// A backslash is a separator only in a Windows path; a POSIX path whose
// backslash reading leaves the root is "<path>", and one that stays keeps its
// backslash (it is a character of a name there).
func TestDiagnostics_BackslashDotDotUnderPosixRoot(t *testing.T) {
	runPathCases(t, []pathCase{
		same(`/srv/lib\..\..\home\bruce\x`, "<path>"),
		same(`/srv/lib/x\..\..\..\home\bruce`, "<path>"),
		same(`/srv/lib/x\..\..\vault\y`, "<path>"),
		same(`/srv/lib/a\b/c`, `<root:lib>/a\b/c`),
		same(`/srv/lib/a/\..\..\..\home\bruce`, "<path>"),
		// n-2: the tail of a POSIX root is not rewritten.
		same(`/srv/lib/a\b\c`, `<root:lib>/a\b\c`),
	})
}

// TestDiagnostics_PathAfterAnyNonNameByte: the boundary is closed over the
// bytes a name can hold, not an allowlist of delimiters, so a path is found
// after "@", "+", "#", "!", "&", "*", a backslash escape, U+2028 and a doubled
// slash, and inside a root's own tail.
func TestDiagnostics_PathAfterAnyNonNameByte(t *testing.T) {
	runPathCases(t, []pathCase{
		same("see user@/home/bruce/x", "see user@<path>"),
		same("a+/home/bruce/x", "a+<path>"),
		same("note#/home/bruce/x", "note#<path>"),
		same("wow!/home/bruce/x", "wow!<path>"),
		same("a&/home/bruce/x", "a&<path>"),
		same("a*/home/bruce/x", "a*<path>"),
		same(`msg="l1\n/home/bruce/x"`, `msg="l1\n<path>"`),
		same(`l1\t/home/bruce/x`, `l1\t<path>`),
		same(`l1 /home/bruce/x`, `l1 <path>`),
		same("l1 /home/bruce/x", "l1 <path>"),
		same("//home/bruce/y", "<path>"),
		same("open //home/bruce/y now", "open <path> now"),
		same("file://localhost/home/bruce/z", "file://localhost<path>"),
		same("file:///home/bruce/z", "file://<path>"),
		same("/srv/lib/a b@/home/bruce/x", "<root:lib>/a b@<path>"),
		same("/srv/lib/a@/home/bruce/x", "<root:lib>/a@<path>"),
		same("/srv/lib/a=/home/bruce/x", "<root:lib>/a=<path>"),
		same(`/srv/lib/a \n/home/bruce/x`, `<root:lib>/a \n<path>`),
		// Not paths.
		same("https://example.com/api/x", "https://example.com/api/x"),
		same("and/or a/b w/o", "and/or a/b w/o"),
		same("</div>", "</div>"),
		// A name with letters of another script before a slash is one path as a
		// whole value, spaces and all (a text line cuts at its first space).
		{in: "/Users/Bruce Wayne/Café/x", value: "<path>"},
	})
}

// TestDiagnostics_ExactEndpointsAndEncodedDotDot: an exact endpoint ends there
// (or at a query); a ".." that arrives percent-encoded is a ".." to the router
// and so to the rule; a bad escape or a backslash reading does not shelter.
func TestDiagnostics_ExactEndpointsAndEncodedDotDot(t *testing.T) {
	runPathCases(t, []pathCase{
		same("/metrics/home/bruce/y", "<path>"),
		same("/livez/home/bruce/z", "<path>"),
		same("/readyz/home/bruce/z", "<path>"),
		same("/api/%2e%2e/home/bruce", "<path>"),
		same("/api/%2E%2E/home/bruce", "<path>"),
		same("/api/%252e%252e/home/bruce", "<path>"),
		same("/api/v1%2F..%2F..%2Fhome%2Fbruce", "<path>"),
		same("/api/v1/x%zz/home/bruce", "<path>"),
		same(`/api/..\..\home\bruce`, "<path>"),
		same("/metrics/", "<path>"),
		same("/livez/", "<path>"),
		same("/metrics", "/metrics"),
		same("/livez?x=1", "/livez?x=1"),
		same("/readyz", "/readyz"),
		same("/api/v1/books/%7Bid%7D", "/api/v1/books/%7Bid%7D"),
		same("GET /api/v1/books/{id}", "GET /api/v1/books/{id}"),
	})
}

// TestDiagnostics_DotRunsAndSentenceTail: a final segment of dots is a name
// ("..." is legal), sentence punctuation after a path is cut, and neither
// misnames the path.
func TestDiagnostics_DotRunsAndSentenceTail(t *testing.T) {
	runPathCases(t, []pathCase{
		same("/srv/lib/...", "<root:lib>/..."),
		same("/srv/lib/a/...", "<root:lib>/a/..."),
		// A backslash escape ends a POSIX path: the text after it is not its tail.
		same(`open /home/bruce/x\nnext line`, `open <path>\nnext line`),
		same(`open /srv/lib/a\tnext`, `open <root:lib>/a\tnext`),
		same("see /srv/lib/a.", "see <root:lib>/a."),
		same("see /srv/lib/a..:", "see <root:lib>/a..:"),
		same("open /srv/lib/a/b: denied", "open <root:lib>/a/b: denied"),
		same("see /home/bruce/x.", "see <path>."),
		same("/srv/lib/a/..", "<root:lib>"),
	})
}

// TestDiagnostics_KeyThatIsOnePathIsReplacedWhole: a map key is a whole string
// like a value, so a path with a space in it does not leak the part after the
// space.
func TestDiagnostics_KeyThatIsOnePathIsReplacedWhole(t *testing.T) {
	f := newFixture(t)
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Roots = round4Roots
		cfg.Contributors = []diagnostics.Contributor{jsonSection("batcave.v", map[string]int{
			"/Users/bruce/k k": 1, "/srv/lib/Joker Wild/ch1": 2,
		})}
	})
	res, err := f.build(e, "run-keys", diagnostics.ExportParams{ActorID: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	assertNoneOf(t, files, "bruce", "k k")
	var got map[string]int
	if err := json.Unmarshal(files["batcave.v/d.json"], &got); err != nil {
		t.Fatal(err)
	}
	if got["<path>"] != 1 || got["<root:lib>/Joker Wild/ch1"] != 2 {
		t.Errorf("keys = %v, want <path> and <root:lib>/Joker Wild/ch1", got)
	}
}

// TestDiagnostics_ViewerForSeesTheStampedMethod: what a person may read depends
// on how they started the run. ViewerFor takes the executor-stamped actor
// (person and AuthMethod) and compiles with Interactive from the method, so an
// administrator's API key (snapshot [core:admin]) gets no Sensitive section
// while the same administrator's session does. With the person's ID alone,
// ViewerFor could not tell, and handed the key the interactive checker.
func TestDiagnostics_ViewerForSeesTheStampedMethod(t *testing.T) {
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	var seen []tasks.Actor
	viewerFor := func(_ context.Context, actor tasks.Actor) (diagnostics.Viewer, error) {
		seen = append(seen, actor)
		c := authz.Compile(rbac.CompileInput{
			Roles: []rbac.RoleName{"admin"}, Interactive: actor.AuthMethod == "session",
			RolePatterns: []rbac.Permission{"core:admin"},
		}, cat)
		return diagnostics.Viewer{ActorID: actor.UserID, Can: c.Has}, nil
	}
	section := func(name string, perm rbac.Requirement) diagnostics.Section {
		return diagnostics.Section{Name: name, Perm: perm, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("d.json", "x")
		}}
	}
	contrib := contributor{
		section("batcave.manage", rbac.Need("core:system.manage")),
		section("batcave.restore", rbac.Need("core:backup.restore")), // Sensitive
	}
	// The snapshot an administrator's key takes: PermsOf of the admin checker.
	snap := []string{"core:admin"}

	f := newFixture(t)
	e := f.exporter(viewerFor, diagnostics.CoreSources{}, contrib)
	exportAs := func(id string, stamped tasks.Actor) map[string][]byte {
		rc := tasks.NewRC(&actorRun{FakeRun: taskstest.NewFakeRun(context.Background(), diagnostics.ExportKind), id: id, actor: stamped})
		res, err := e.Export(rc, diagnostics.ExportParams{ActorID: "alfred", Perms: snap})
		if err != nil {
			t.Fatal(err)
		}
		return f.zipFiles(res.File)
	}

	key := exportAs("run-key", tasks.Actor{UserID: "alfred", AuthMethod: "token"})
	if key["batcave.manage/d.json"] == nil || key["batcave.restore/d.json"] != nil {
		t.Errorf("an administrator's API key: files %v, want the manage section and not the Sensitive one", names(key))
	}
	sess := exportAs("run-session", tasks.Actor{UserID: "alfred", AuthMethod: "session"})
	if sess["batcave.manage/d.json"] == nil || sess["batcave.restore/d.json"] == nil {
		t.Errorf("an administrator's session: files %v, want every section", names(sess))
	}
	if len(seen) != 2 || seen[0].AuthMethod != "token" || seen[1].AuthMethod != "session" || seen[0].UserID != "alfred" {
		t.Errorf("ViewerFor saw %+v, want the stamped actors with their methods", seen)
	}
}

// TestDiagnostics_LongPathShapesThroughThePipeline: the log.Redactor runs before
// the path rule and bounds the text it passes on (64 KiB, the rest is replaced),
// so a shape is only a cost to the rule if it survives the redactor. These are the shapes that could (a long segment of mixed characters
// with a trailing run of dots and colons, a run of dot segments, a long query),
// sent through the real exporter; each must finish in a bounded time and leave
// no path behind. The rule is linear, so the bound is far from the cost.
func TestDiagnostics_LongPathShapesThroughThePipeline(t *testing.T) {
	lines := []string{
		"open /x/a" + strings.Repeat(".", 60_000), // under the redactor's 64 KiB bound: reaches the path rule whole
		"open /x/a" + strings.Repeat(".", 300_000),
		"open /x/" + strings.Repeat("a.", 150_000) + "...",
		"open /home/bruce/" + strings.Repeat("ab1-_", 40_000) + strings.Repeat(".:", 50_000),
		"open /srv/lib/" + strings.Repeat("seg/", 50_000) + "a" + strings.Repeat(".", 50_000),
		"GET /api/v1/x?" + strings.Repeat("p=/home/bruce&", 20_000),
		"GET " + strings.Repeat("/api/x?a=", 40_000) + "/home/bruce",
		strings.Repeat("/srv/lib/a@", 40_000) + "/home/bruce",
	}
	f := newFixture(t)
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Roots = round4Roots
		cfg.Sources.Logs = func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
		}
	})
	start := time.Now()
	res, err := f.build(e, "run-long", diagnostics.ExportParams{ActorID: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("redacting %d long lines took %s: some shape is not linear", len(lines), d)
	}
	assertNoneOf(t, f.zipFiles(res.File), "/home/bruce")
}
