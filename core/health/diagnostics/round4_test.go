// file: core/health/diagnostics/round4_test.go
// version: 1.1.1
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
	assertNoneOf(t, files, "bruce", "k k", "Joker", "Wild")
	var got map[string]int
	if err := json.Unmarshal(files["batcave.v/d.json"], &got); err != nil {
		t.Fatal(err)
	}
	if got["<path>"] != 1 || got["<root:lib>"] != 2 {
		t.Errorf("keys = %v, want <path> and <root:lib> (the tail under a root is hidden)", got)
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
