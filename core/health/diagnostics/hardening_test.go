// file: core/health/diagnostics/hardening_test.go
// version: 1.0.5
// guid: 8d3f1b6a-47c2-4e90-a5d8-1f7b9c2e6a03
// last-edited: 2026-11-13

package diagnostics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// exporterWith builds an Exporter over the fixture with cfg adjusted.
func (f *fixture) exporterWith(viewer func(context.Context, string) (diagnostics.Viewer, error), adjust func(*diagnostics.Config)) *diagnostics.Exporter {
	f.t.Helper()
	cfg := diagnostics.Config{
		Root: f.root, ViewerFor: viewer, Metrics: f.sink,
		Now: func() time.Time { return fixedNow }, App: "gotham-audio", Version: "9.9.9",
	}
	adjust(&cfg)
	e, err := diagnostics.New(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	return e
}

func jsonSection(name string, v any) contributor {
	return contributor{{Name: name, Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return w.JSON("d.json", v)
	}}}
}

// TestDiagnostics_MapKeysRedacted pins that secrets are redacted in object KEYS
// as well as values: URL userinfo, bearer values and token= text, in a map
// keyed by them, at any depth, in JSON files and in JSONL rows. Keys that
// collapse to the same text stay apart, so no member is lost.
func TestDiagnostics_MapKeysRedacted(t *testing.T) {
	f := newFixture(t)
	urlKey1 := "https://bruce:" + secretURLPw + "@gotham.example/feed"
	urlKey2 := "https://bruce:" + secretURLPw + "x@gotham.example/feed"
	data := map[string]any{
		urlKey1:                                 1,
		urlKey2:                                 2,
		"Authorization: Bearer " + secretBearer: 3,
		"token=" + secretAPIKey:                 4,
		"nested": map[string]any{
			"cookie: session=" + secretCookie: map[string]any{"Bearer " + secretBearer: "ok"},
		},
		"Robin": 5,
	}
	rows := func(yield func(map[string]int) bool) {
		yield(map[string]int{"https://bruce:" + secretURLPw + "@gotham.example/row": 9})
	}
	c := contributor{
		{Name: "batcave.keys", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("keys.json", data)
		}},
		{Name: "batcave.rows", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return diagnostics.WriteJSONL(w, "rows.jsonl", func(yield func(map[string]int) bool) { rows(yield) })
		}},
	}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{}, c)
	res, err := f.build(e, "run-keys", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	assertNoneOf(t, files, secretURLPw, secretBearer, secretAPIKey, secretCookie)

	var got map[string]any
	if err := json.Unmarshal(files["batcave.keys/keys.json"], &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(data) {
		t.Errorf("%d members, want %d (a collapsed key lost its member): %v", len(got), len(data), got)
	}
	if got["Robin"] != float64(5) {
		t.Errorf("an innocent key was altered: %v", got)
	}
	var suffixed int
	for k := range got {
		if strings.Contains(k, "#2") {
			suffixed++
		}
	}
	if suffixed == 0 {
		t.Errorf("two keys collapsed to the same text and none got a #2 suffix: %v", got)
	}
	if m := manifestOf(t, files); m.Redactions["pattern"] < 6 {
		t.Errorf("pattern redactions = %d, want the keys counted too (%v)", m.Redactions["pattern"], m.Redactions)
	}
}

// TestDiagnostics_AbsolutePathsRedacted pins 5H.3.5 layer 2: an absolute path
// in a known root is "<root:NAME>/...", any other absolute path is "<path>",
// in JSON values and keys, in File lines and in the manifest's errors[].
func TestDiagnostics_AbsolutePathsRedacted(t *testing.T) {
	f := newFixture(t)
	roots := []diagnostics.PathRoot{
		{Name: "audiobooks", Path: "/srv/gotham/audiobooks"},
		{Name: "vault", Path: "/srv/gotham/archive vault"},
	}
	text := func(context.Context) (io.ReadCloser, error) {
		lines := []string{
			"open /var/lib/batcave/state.db: permission denied",
			"scanned /srv/gotham/audiobooks/Joker/ch1.m4b ok",
			"see https://gotham.example/api/x and/or the Batmobile",
			"plain line",
		}
		return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
	}
	c := contributor{
		{Name: "batcave.values", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("v.json", map[string]any{
				"in_root":          "/srv/gotham/audiobooks/Joker/ch1.m4b",
				"root_only":        "/srv/gotham/audiobooks",
				"spaced":           "/srv/gotham/archive vault/Penguin/ch2.m4b",
				"outside":          "/etc/batcave/secret.conf",
				"sibling":          "/srv/gotham/audiobooks2/x",
				"windows":          `C:\Users\bruce\Documents\plans.txt`,
				"embedded":         "failed: open /root/.ssh/id_ed25519: denied.",
				"file_url":         "file:///home/bruce/plans.txt",
				"not_a_path":       "and/or 3/4 https://gotham.example/api/x",
				"/home/bruce/keys": "map key path",
			})
		}},
		{Name: "batcave.broken", Perm: manage, Write: func(context.Context, diagnostics.Writer, diagnostics.Viewer) error {
			return errors.New("cannot read /var/lib/batcave/cowl.db and /srv/gotham/audiobooks/Joker")
		}},
	}
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Roots = roots
		cfg.Contributors = []diagnostics.Contributor{c}
		cfg.Sources.Logs = text
	})
	res, err := f.build(e, "run-paths", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	assertNoneOf(t, files, "/var/lib/batcave", "/etc/batcave", "/root/.ssh", "/home/bruce", `C:\Users`, "/srv/gotham/audiobooks2", "/srv/gotham/archive vault", "/srv/gotham/audiobooks/")

	var v map[string]string
	if err := json.Unmarshal(files["batcave.values/v.json"], &v); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"in_root":    "<root:audiobooks>/Joker/ch1.m4b",
		"root_only":  "<root:audiobooks>",
		"spaced":     "<root:vault>/Penguin/ch2.m4b",
		"outside":    "<path>",
		"sibling":    "<path>",
		"windows":    "<path>",
		"embedded":   "failed: open <path>: denied.",
		"file_url":   "file://<path>",
		"not_a_path": "and/or 3/4 https://gotham.example/api/x",
		"<path>":     "map key path",
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%q = %q, want %q", k, v[k], w)
		}
	}

	logs := string(files["core.logs/logs.jsonl"])
	for _, w := range []string{
		"open <path>: permission denied",
		"scanned <root:audiobooks>/Joker/ch1.m4b ok",
		"see https://gotham.example/api/x and/or the Batmobile",
		"plain line",
	} {
		if !strings.Contains(logs, w) {
			t.Errorf("logs.jsonl lacks %q:\n%s", w, logs)
		}
	}

	m := manifestOf(t, files)
	if len(m.Errors) != 1 || m.Errors[0].Section != "batcave.broken" ||
		m.Errors[0].Error != "cannot read <path> and <root:audiobooks>/Joker" {
		t.Errorf("manifest errors = %+v", m.Errors)
	}
	if m.Redactions["pattern"] < 10 {
		t.Errorf("pattern redactions = %d (%v)", m.Redactions["pattern"], m.Redactions)
	}
}

// TestDiagnostics_NoRootsEveryPathRedacted: with no roots configured nothing
// is "known", so every absolute path is "<path>".
func TestDiagnostics_NoRootsEveryPathRedacted(t *testing.T) {
	f := newFixture(t)
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{},
		jsonSection("batcave.p", map[string]string{"p": "/srv/gotham/audiobooks/x"}))
	res, err := f.build(e, "run-noroots", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(f.zipFiles(res.File)["batcave.p/d.json"]); !strings.Contains(got, `"p": "<path>"`) {
		t.Errorf("d.json = %s", got)
	}
}

// TestDiagnostics_ExportUsesRequestTimePerms: the bundle holds what the actor
// could read when the request was made (ExportParams.Perms), not what the
// current ViewerFor says by the time the run starts.
func TestDiagnostics_ExportUsesRequestTimePerms(t *testing.T) {
	f := newFixture(t)
	vault := contributor{{Name: "batcave.vault", Perm: rbac.Need("core:admin"), Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return w.JSON("d.json", "x")
	}}}
	// ViewerFor would grant everything now.
	e := f.exporter(viewerOf("core:system.manage", "core:admin"), diagnostics.CoreSources{}, vault)

	res, err := f.build(e, "run-snap", diagnostics.ExportParams{ActorID: "bruce", Perms: []string{"core:system.manage"}})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	if files["batcave.vault/d.json"] != nil {
		t.Error("a permission the actor did not hold at request time was used")
	}
	if m := manifestOf(t, files); !slices.Equal(m.Omitted, []string{"batcave.vault"}) {
		t.Errorf("omitted = %v", m.Omitted)
	}

	// An empty snapshot is not "no snapshot": it grants nothing.
	res, err = f.build(e, "run-empty", diagnostics.ExportParams{ActorID: "bruce", Perms: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if files := f.zipFiles(res.File); files["core.build/build.json"] != nil || files["batcave.vault/d.json"] != nil {
		t.Errorf("an empty permission snapshot still produced sections: %v", names(files))
	}

	// No snapshot at all: the actor's permissions as ViewerFor resolves them.
	res, err = f.build(e, "run-nosnap", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	if f.zipFiles(res.File)["batcave.vault/d.json"] == nil {
		t.Error("params without a snapshot should fall back to ViewerFor")
	}

	// The snapshot survives the task store's JSON, empty and nil kept apart.
	for _, in := range []diagnostics.ExportParams{{ActorID: "a", Perms: []string{}}, {ActorID: "a"}, {ActorID: "a", Perms: []string{"core:self"}}} {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out diagnostics.ExportParams
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if (in.Perms == nil) != (out.Perms == nil) || !slices.Equal(in.Perms, out.Perms) {
			t.Errorf("%s round-tripped %v as %v", raw, in.Perms, out.Perms)
		}
	}
}

type fakeChecker struct{ held []rbac.Permission }

func (c fakeChecker) Has(p rbac.Permission) bool      { return slices.Contains(c.held, p) }
func (c fakeChecker) Satisfies(rbac.Requirement) bool { return false }
func (c fakeChecker) Confined() bool                  { return false }
func (c fakeChecker) Concrete() []rbac.Permission     { return c.held }

func TestDiagnostics_PermsOfAndViewerOf(t *testing.T) {
	if got := diagnostics.PermsOf(nil); got == nil || len(got) != 0 {
		t.Errorf("PermsOf(nil) = %#v, want empty non-nil", got)
	}
	got := diagnostics.PermsOf(fakeChecker{held: []rbac.Permission{"core:self", "core:system.manage"}})
	if !slices.Equal(got, []string{"core:self", "core:system.manage"}) {
		t.Errorf("PermsOf = %v", got)
	}
	v := diagnostics.ViewerOf("bruce", got)
	if v.ActorID != "bruce" || !v.Can("core:self") || v.Can("core:admin") {
		t.Errorf("ViewerOf: %+v", v)
	}
	if !v.Satisfies(rbac.Need("core:system.manage")) || v.Satisfies(rbac.Need("core:admin")) {
		t.Error("Satisfies disagrees with the held set")
	}
}

// TestDiagnostics_FilteredSourceCountsPermissionRows: a Source filters for the
// viewer it is handed and reports what it left out; the manifest counts those
// under the permission layer, beside the omitted sections.
func TestDiagnostics_FilteredSourceCountsPermissionRows(t *testing.T) {
	f := newFixture(t)
	var seen diagnostics.Viewer
	tasks := func(_ context.Context, v diagnostics.Viewer) (any, error) {
		seen = v
		rows := []map[string]string{{"kind": "core.diagnostics.export"}}
		omitted := 0
		for _, k := range []string{"batcave.secret-kind", "batcave.other-secret-kind"} {
			if !v.Can("core:admin") {
				omitted++
				_ = k
			}
		}
		return diagnostics.Filtered{Value: rows, Omitted: omitted}, nil
	}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{Tasks: tasks})
	res, err := f.build(e, "run-filtered", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	if seen.ActorID != "bruce" || seen.Can == nil || seen.Can("core:admin") {
		t.Errorf("the source got viewer %+v", seen)
	}
	files := f.zipFiles(res.File)
	if m := manifestOf(t, files); m.Redactions["permission"] != 2 {
		t.Errorf("permission redactions = %v, want 2 omitted rows", m.Redactions)
	}
	var rows []map[string]string
	if err := json.Unmarshal(files["core.tasks/tasks.json"], &rows); err != nil || len(rows) != 1 {
		t.Errorf("tasks.json = %s (%v): the wrapper must not be serialised", files["core.tasks/tasks.json"], err)
	}
}

// TestDiagnostics_OpenBundleRefusesPastRetention: a bundle older than
// core.diagnostics.retention is ErrExpired even though the prune task has not
// removed it yet.
func TestDiagnostics_OpenBundleRefusesPastRetention(t *testing.T) {
	f := newFixture(t)
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{})
	res, err := f.build(e, "run-old", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dir, res.File)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	written := fi.ModTime()

	open := func(retention time.Duration, now time.Time) error {
		b, err := diagnostics.OpenBundle(f.root, res.File, retention, now)
		if err == nil {
			_ = b.Close()
		}
		return err
	}
	if err := open(time.Hour, written.Add(59*time.Minute)); err != nil {
		t.Errorf("inside retention: %v", err)
	}
	if err := open(time.Hour, written.Add(61*time.Minute)); !errors.Is(err, diagnostics.ErrExpired) {
		t.Errorf("past retention: %v, want ErrExpired", err)
	}
	// Zero retention is the 24 h default, not "never expires" and not "always".
	if err := open(0, written.Add(23*time.Hour)); err != nil {
		t.Errorf("inside the default retention: %v", err)
	}
	if err := open(0, written.Add(25*time.Hour)); !errors.Is(err, diagnostics.ErrExpired) {
		t.Errorf("past the default retention: %v, want ErrExpired", err)
	}
}

// TestDiagnostics_StreamManifestDescribesTheView: the manifest in a streamed
// zip lists only what the zip holds. A section dropped at download leaves its
// section entry, its errors and its truncations, and is added to omitted.
func TestDiagnostics_StreamManifestDescribesTheView(t *testing.T) {
	f := newFixture(t)
	big := contributor{
		{Name: "batcave.vault", Perm: rbac.Need("core:admin"), Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			if err := w.JSON("a.json", strings.Repeat("x", 2048)); err != nil {
				return err
			}
			return errors.New("vault jammed")
		}},
		{Name: "batcave.garage", Perm: manage, Write: func(context.Context, diagnostics.Writer, diagnostics.Viewer) error {
			return errors.New("garage door stuck")
		}},
	}
	e := f.exporterWith(viewerOf("core:system.manage", "core:admin"), func(cfg *diagnostics.Config) {
		cfg.Contributors = []diagnostics.Contributor{big}
		cfg.MaxFileBytes = 256
	})
	res, err := f.build(e, "run-view", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := diagnostics.OpenBundle(f.root, res.File, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	full := manifestOf(t, f.zipFiles(res.File))
	if len(full.Truncated) == 0 || len(full.Errors) != 2 {
		t.Fatalf("fixture did not truncate and fail: %+v", full)
	}

	var buf bytes.Buffer
	if _, err := b.Stream(&buf, func(p rbac.Permission) bool { return p == "core:system.manage" }); err != nil {
		t.Fatal(err)
	}
	m := manifestOf(t, unzip(t, buf.Bytes()))
	for _, s := range m.Sections {
		if s.Name == "batcave.vault" {
			t.Error("the manifest still lists a section the zip does not hold")
		}
	}
	for _, e := range m.Errors {
		if e.Section == "batcave.vault" {
			t.Errorf("the manifest still carries the dropped section's error %+v", e)
		}
	}
	if len(m.Errors) != 1 || m.Errors[0].Section != "batcave.garage" {
		t.Errorf("errors = %+v, want only the garage's", m.Errors)
	}
	for _, tr := range m.Truncated {
		if strings.HasPrefix(tr, "batcave.vault/") {
			t.Errorf("the manifest still lists %s as truncated", tr)
		}
	}
	if !slices.Contains(m.Omitted, "batcave.vault") {
		t.Errorf("omitted = %v, want the dropped section", m.Omitted)
	}
}

// TestDiagnostics_WidenedSnapshotIsClamped: ExportParams.Perms are task params,
// which the start request supplies, so a snapshot that lists more than the actor
// holds now must not grant it. The bundle holds the intersection.
func TestDiagnostics_WidenedSnapshotIsClamped(t *testing.T) {
	f := newFixture(t)
	vault := contributor{{Name: "batcave.vault", Perm: rbac.Need("core:admin"), Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return w.JSON("d.json", "x")
	}}}
	// The actor holds only core:system.manage.
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{}, vault)

	res, err := f.build(e, "run-widen", diagnostics.ExportParams{ActorID: "robin", Perms: []string{"core:system.manage", "core:admin"}})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	if files["batcave.vault/d.json"] != nil {
		t.Error("a snapshot listing core:admin granted it to an actor who does not hold it")
	}
	if m := manifestOf(t, files); !slices.Equal(m.Omitted, []string{"batcave.vault"}) {
		t.Errorf("omitted = %v, want the admin-only section", m.Omitted)
	}

	// Narrowing still works: an actor who holds both, with a snapshot of one.
	e = f.exporter(viewerOf("core:system.manage", "core:admin"), diagnostics.CoreSources{}, vault)
	res, err = f.build(e, "run-narrow", diagnostics.ExportParams{ActorID: "robin", Perms: []string{"core:system.manage"}})
	if err != nil {
		t.Fatal(err)
	}
	if f.zipFiles(res.File)["batcave.vault/d.json"] != nil {
		t.Error("a narrower snapshot did not narrow")
	}

	// An actor who no longer resolves fails the run closed, snapshot or not.
	gone := func(context.Context, string) (diagnostics.Viewer, error) {
		return diagnostics.Viewer{}, errors.New("no such actor")
	}
	e = f.exporter(gone, diagnostics.CoreSources{}, vault)
	if _, err := f.build(e, "run-gone", diagnostics.ExportParams{ActorID: "robin", Perms: []string{"core:admin"}}); err == nil {
		t.Error("a run for an actor ViewerFor cannot resolve succeeded on the strength of its snapshot")
	}
}

// actorRun is a run host whose run id is a plain name and whose stamped actor
// the test chooses.
type actorRun struct {
	*taskstest.FakeRun
	id    string
	actor tasks.Actor
}

func (a *actorRun) RunID() tasks.RunID { return tasks.RunID(a.id) }
func (a *actorRun) Actor() tasks.Actor { return a.actor }

// TestDiagnostics_MismatchedActorRefused: params naming an actor other than the
// executor-stamped one are refused before any file is written, and so is a run
// with no stamped person (fail closed); only the matching actor is not.
func TestDiagnostics_MismatchedActorRefused(t *testing.T) {
	f := newFixture(t)
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{})
	run := func(id string, stamped tasks.Actor, p diagnostics.ExportParams) error {
		rc := tasks.NewRC(&actorRun{FakeRun: taskstest.NewFakeRun(context.Background(), diagnostics.ExportKind), id: id, actor: stamped})
		_, err := e.Export(rc, p)
		return err
	}
	err := run("run-mismatch", tasks.Actor{UserID: "robin", AuthMethod: "session"}, diagnostics.ExportParams{ActorID: "bruce", Perms: []string{"core:system.manage"}})
	if !errors.Is(err, diagnostics.ErrActorMismatch) {
		t.Fatalf("err = %v, want ErrActorMismatch", err)
	}
	if _, serr := f.root.Stat("run-mismatch.zip"); serr == nil {
		t.Error("a refused run wrote a bundle")
	}
	if err := run("run-match", tasks.Actor{UserID: "bruce", AuthMethod: "session"}, diagnostics.ExportParams{ActorID: "bruce"}); err != nil {
		t.Errorf("the stamped actor was refused: %v", err)
	}
	for name, stamped := range map[string]tasks.Actor{"run-system": {System: "core"}, "run-blank": {}} {
		if err := run(name, stamped, diagnostics.ExportParams{ActorID: "bruce"}); !errors.Is(err, diagnostics.ErrActorMismatch) {
			t.Errorf("%s: a run with no stamped person: err = %v, want ErrActorMismatch", name, err)
		}
		if _, serr := f.root.Stat(name + ".zip"); serr == nil {
			t.Errorf("%s: a refused run wrote a bundle", name)
		}
	}
}

// TestDiagnostics_AdminSnapshotKeepsEverySection is the seeded administrator
// through the real checker. authz.Compile gives an admin the core:admin flag
// and nothing else concrete, so PermsOf is ["core:admin"] and its reach over
// every other scope is the checker's rule, not a member of the snapshot. A
// snapshot read as an exact-name list dropped every section from an
// administrator's bundle.
func TestDiagnostics_AdminSnapshotKeepsEverySection(t *testing.T) {
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	compile := func(interactive bool, scopes []rbac.Permission) rbac.Checker {
		return authz.Compile(rbac.CompileInput{
			Roles: []rbac.RoleName{"admin"}, Scopes: scopes, Interactive: interactive,
			RolePatterns: []rbac.Permission{"core:admin"},
		}, cat)
	}
	viewerFor := func(c rbac.Checker) func(context.Context, string) (diagnostics.Viewer, error) {
		return func(_ context.Context, actor string) (diagnostics.Viewer, error) {
			return diagnostics.Viewer{ActorID: actor, Can: c.Has}, nil
		}
	}
	section := func(name string, perm rbac.Requirement) diagnostics.Section {
		return diagnostics.Section{Name: name, Perm: perm, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("d.json", "x")
		}}
	}
	contrib := contributor{
		section("batcave.manage", rbac.Need("core:system.manage")),
		section("batcave.view", rbac.Need("core:system.view")),
		section("batcave.restore", rbac.Need("core:backup.restore")), // Sensitive
	}

	// An interactive administrator: every section, however the snapshot was taken.
	f := newFixture(t)
	admin := compile(true, nil)
	snap := diagnostics.PermsOf(admin)
	if !slices.Equal(snap, []string{"core:admin"}) {
		t.Fatalf("the seeded admin's snapshot = %v, want [core:admin]", snap)
	}
	res, err := f.build(f.exporter(viewerFor(admin), diagnostics.CoreSources{}, contrib), "run-admin", diagnostics.ExportParams{ActorID: "alfred", Perms: snap})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	for _, name := range []string{"batcave.manage", "batcave.view", "batcave.restore"} {
		if files[name+"/d.json"] == nil {
			t.Errorf("%s missing from the administrator's bundle (omitted %v)", name, manifestOf(t, files).Omitted)
		}
	}

	// A non-interactive credential naming core:admin still holds no Sensitive
	// scope: the snapshot never widens what the live checker refuses.
	f = newFixture(t)
	key := compile(false, []rbac.Permission{"core:admin"})
	res, err = f.build(f.exporter(viewerFor(key), diagnostics.CoreSources{}, contrib), "run-key", diagnostics.ExportParams{ActorID: "alfred", Perms: diagnostics.PermsOf(key)})
	if err != nil {
		t.Fatal(err)
	}
	files = f.zipFiles(res.File)
	if files["batcave.manage/d.json"] == nil || files["batcave.restore/d.json"] != nil {
		t.Errorf("a non-interactive admin key: files %v, want the manage section and not the Sensitive one", names(files))
	}

	// A snapshot that lists core:admin does not grant it to an actor whose live
	// checker does not hold it.
	f = newFixture(t)
	editor := authz.Compile(rbac.CompileInput{
		Roles: []rbac.RoleName{"editor"}, Interactive: true,
		RolePatterns: []rbac.Permission{"core:system.view"},
	}, cat)
	res, err = f.build(f.exporter(viewerFor(editor), diagnostics.CoreSources{}, contrib), "run-editor", diagnostics.ExportParams{ActorID: "robin", Perms: []string{"core:admin"}})
	if err != nil {
		t.Fatal(err)
	}
	files = f.zipFiles(res.File)
	if files["batcave.view/d.json"] == nil || files["batcave.manage/d.json"] != nil {
		t.Errorf("a forged admin snapshot over a viewer: files %v", names(files))
	}
}

// logsOf builds a bundle whose core.logs is lines and returns the file's text.
func logsOf(t *testing.T, roots []diagnostics.PathRoot, lines []string) string {
	t.Helper()
	f := newFixture(t)
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Roots = roots
		cfg.Sources.Logs = func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
		}
	})
	res, err := f.build(e, "run-logs", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	return string(f.zipFiles(res.File)["core.logs/logs.jsonl"])
}

// TestDiagnostics_JSONLogLinesStayValid: the structured log's lines are JSON
// objects, and a path inside a quoted message ("config file \"/etc/x\" not
// found", what %q writes) used to lose its closing escape to the path rule and
// leave an invalid line. Every line is decoded and walked like any JSON value,
// so the escapes stay, the path is redacted inside its string, and a deny-listed
// member name still replaces its value.
func TestDiagnostics_JSONLogLinesStayValid(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewJSONHandler(&buf, nil))
	lg.Info(`config file "/etc/batcave/app.yaml" not found`, "path", "/srv/gotham/audiobooks/Joker/ch1.m4b", "password", "hunter2-not-real", "cwd", "/Users/bruce/Library/Application Support/app/db")
	lg.Warn("no quote here", "err", `open "/var/lib/batcave/state.db": denied`, "n", 7)
	roots := []diagnostics.PathRoot{{Name: "audiobooks", Path: "/srv/gotham/audiobooks"}}
	out := logsOf(t, roots, strings.Split(strings.TrimSpace(buf.String()), "\n"))

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Fatalf("a redacted log line is not valid JSON:\n%s", l)
		}
	}
	var first, second map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if first["msg"] != `config file "<path>" not found` {
		t.Errorf("msg = %q", first["msg"])
	}
	if first["path"] != "<root:audiobooks>/Joker/ch1.m4b" {
		t.Errorf("path = %q", first["path"])
	}
	if first["cwd"] != "<path>" {
		t.Errorf("cwd = %q: a whole-value path with spaces must not leak its tail", first["cwd"])
	}
	if first["password"] == "hunter2-not-real" {
		t.Errorf("password survived: %v", first["password"])
	}
	if second["err"] != `open "<path>": denied` || second["n"] != float64(7) {
		t.Errorf("second line = %v", second)
	}
	assertNoneOf(t, map[string][]byte{"logs": []byte(out)}, "/etc/batcave", "/var/lib/batcave", "Application Support", "hunter2-not-real")

	// A line that is not JSON keeps the text rule, and a quote escape still
	// ends a path in text.
	text := logsOf(t, nil, []string{`msg=\"/etc/batcave/app.yaml\" gone`, "{not json /etc/batcave/x"})
	if !strings.Contains(text, `msg=\"<path>\" gone`) || strings.Contains(text, "/etc/batcave") {
		t.Errorf("text lines:\n%s", text)
	}
}

// TestDiagnostics_RouteShapesAndEdgesOfPathRule pins what the path rule leaves
// alone and what it still names: router-mounted routes (a request line, a
// Prometheus label) are the same text on every host; a closing tag is not a
// path; punctuation after a root keeps the root's name; a UNC share is a path;
// the rewrite is a fixed point.
func TestDiagnostics_RouteShapesAndEdgesOfPathRule(t *testing.T) {
	roots := []diagnostics.PathRoot{{Name: "lib", Path: "/srv/lib"}}
	in := []string{
		`GET /api/v1/books/123 200`,
		`http_requests_total{route="/api/v1/books/{id}"} 3`,
		`probe /readyz and /livez?x=1 ok but /livez-backup/db is a file`,
		`see /srv/lib. and /srv/lib: gone`,
		`<div>x</div> and <root:lib>/x already done`,
		`share \\gotham-nas\batcave\x unreachable`,
	}
	got := strings.Split(strings.TrimSpace(logsOf(t, roots, in)), "\n")
	want := []string{
		`GET /api/v1/books/123 200`,
		`http_requests_total{route="/api/v1/books/{id}"} 3`,
		`probe /readyz and /livez?x=1 ok but <path> is a file`,
		`see <root:lib>. and <root:lib>: gone`,
		`<div>x</div> and <root:lib>/x already done`,
		`share <path> unreachable`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d\n got %q\nwant %q", i, got[i], want[i])
		}
	}
	// Redacting the output again changes nothing.
	again := strings.Split(strings.TrimSpace(logsOf(t, roots, got)), "\n")
	for i := range got {
		if again[i] != got[i] {
			t.Errorf("not idempotent, line %d: %q -> %q", i, got[i], again[i])
		}
	}
}

// TestDiagnostics_PathRuleDelimitersDoNotHideAPath: "<" and ">" are path
// boundaries like any delimiter, so a path put right after markup is redacted;
// only the redactor's own placeholders and a real closing tag are skipped.
func TestDiagnostics_PathRuleDelimitersDoNotHideAPath(t *testing.T) {
	roots := []diagnostics.PathRoot{{Name: "lib", Path: "/srv/lib"}}
	in := []string{
		`<b>/home/bruce/secret.txt`,
		`x=<i>/etc/shadow and </home/bruce/y>`,
		"a</etc/passwd b",
		`<path>/home/bruce/z and <root:cowl>/home/bruce/w`,
		`</div> <root:lib>/x <path> done`,
	}
	got := strings.Split(strings.TrimSpace(logsOf(t, roots, in)), "\n")
	want := []string{
		`<b><path>`,
		`x=<i><path> and <<path>>`,
		`a<<path> b`,
		`<path><path> and <root:cowl><path>`,
		`</div> <root:lib>/x <path> done`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d\n got %q\nwant %q", i, got[i], want[i])
		}
	}
	assertNoneOf(t, map[string][]byte{"logs": []byte(strings.Join(got, "\n"))}, "/home/bruce", "/etc/shadow", "/etc/passwd")
}

// TestDiagnostics_WholeValuePathRuleOnlyForASinglePath: a JSON string that
// merely starts with a path, root or not, is prose: every path in it is
// redacted and its text kept. Only a value that is one clean path is replaced
// as a whole.
func TestDiagnostics_WholeValuePathRuleOnlyForASinglePath(t *testing.T) {
	f := newFixture(t)
	vals := map[string]string{
		"msg_root":     "/srv/lib/x: open /home/bruce/cowl: denied",
		"quoted_root":  "/srv/lib/x '/home/bruce/y'",
		"msg_outside":  "/etc/a: /home/bruce/b",
		"two_outside":  "/etc/a /home/bruce/c",
		"dq_outside":   `/etc/a "/home/bruce/d"`,
		"spaced":       "/Users/bruce/Library/Application Support/app/db",
		"apostrophe":   "/Users/bruce/Bruce's Music/x",
		"clean_root":   "/srv/lib/Joker/ch1.m4b",
		"trailing_dot": "/srv/lib.",
	}
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.Roots = []diagnostics.PathRoot{{Name: "lib", Path: "/srv/lib"}}
		cfg.Contributors = []diagnostics.Contributor{jsonSection("batcave.v", vals)}
	})
	res, err := f.build(e, "run-single", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	var got map[string]string
	if err := json.Unmarshal(files["batcave.v/d.json"], &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"msg_root":     "<root:lib>/x: open <path>: denied",
		"quoted_root":  "<root:lib>/x '<path>'",
		"msg_outside":  "<path>: <path>",
		"two_outside":  "<path> <path>",
		"dq_outside":   `<path> "<path>"`,
		"spaced":       "<path>",
		"apostrophe":   "<path>",
		"clean_root":   "<root:lib>/Joker/ch1.m4b",
		"trailing_dot": "<root:lib>.",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	assertNoneOf(t, files, "/home/bruce", "/Users/bruce", "Application Support", "Bruce's")
}
