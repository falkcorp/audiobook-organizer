// file: core/health/diagnostics/bundle_test.go
// version: 1.0.2
// guid: d8634ac0-da50-4670-b2ca-6e84e324168a
// last-edited: 2026-11-14

package diagnostics_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// The fixtures are Batman-universe on purpose: titles, people and places only.
const (
	secretPass   = "alfred-pennyworth-pass-1939"
	secretBearer = "batsignal-over-gotham-city"
	secretCookie = "robin-session-cookie-1940"
	secretInvite = "gotham-invite-token-0042"
	secretURLPw  = "joker-ha-ha-ha-1940"
	secretAPIKey = "wayne-enterprises-gadget-fund"
)

var fixedNow = time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)

type sink struct {
	mu        sync.Mutex
	bundles   []string
	redaction map[string]int
}

func (s *sink) Bundle(outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundles = append(s.bundles, outcome)
}

func (s *sink) Redacted(layer string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.redaction == nil {
		s.redaction = map[string]int{}
	}
	s.redaction[layer] += n
}

type fixture struct {
	t    *testing.T
	dir  string
	root *os.Root
	sink *sink
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &fixture{t: t, dir: dir, root: root, sink: &sink{}}
}

// viewerOf resolves every actor to a viewer holding exactly perms.
func viewerOf(perms ...rbac.Permission) func(context.Context, tasks.Actor) (diagnostics.Viewer, error) {
	return func(_ context.Context, actor tasks.Actor) (diagnostics.Viewer, error) {
		return diagnostics.Viewer{ActorID: actor.UserID, Can: func(p rbac.Permission) bool { return slices.Contains(perms, p) }}, nil
	}
}

func (f *fixture) exporter(viewer func(context.Context, tasks.Actor) (diagnostics.Viewer, error), src diagnostics.CoreSources, contrib ...diagnostics.Contributor) *diagnostics.Exporter {
	f.t.Helper()
	e, err := diagnostics.New(diagnostics.Config{
		Root: f.root, ViewerFor: viewer, Sources: src, Contributors: contrib,
		Metrics: f.sink, Now: func() time.Time { return fixedNow }, App: "gotham-audio", Version: "9.9.9",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return e
}

type contributor []diagnostics.Section

func (c contributor) Sections() []diagnostics.Section { return c }

// zipFiles reads every entry of the run's bundle.
func (f *fixture) zipFiles(file string) map[string][]byte {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, file))
	if err != nil {
		f.t.Fatal(err)
	}
	return unzip(f.t, b)
}

func unzip(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[zf.Name] = data
	}
	return out
}

func manifestOf(t *testing.T, files map[string][]byte) diagnostics.Manifest {
	t.Helper()
	var m diagnostics.Manifest
	raw, ok := files[diagnostics.ManifestFile]
	if !ok {
		t.Fatalf("no manifest in %v", names(files))
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func names(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func assertNoneOf(t *testing.T, files map[string][]byte, secrets ...string) {
	t.Helper()
	for name, data := range files {
		for _, s := range secrets {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("%s contains %q", name, s)
			}
		}
	}
}

var manage = rbac.Need("core:system.manage")

func (f *fixture) build(e *diagnostics.Exporter, run string, p diagnostics.ExportParams) (diagnostics.ExportResult, error) {
	return f.buildAs(e, run, tasks.Actor{UserID: p.ActorID, AuthMethod: "session"}, p)
}

// buildAs builds a bundle for the run's stamped actor, the way Export passes
// rc.Actor(): ViewerFor sees the actor's method as well as the person.
func (f *fixture) buildAs(e *diagnostics.Exporter, run string, actor tasks.Actor, p diagnostics.ExportParams) (diagnostics.ExportResult, error) {
	return e.Build(context.Background(), run, actor, p, nil)
}

func TestDiagnostics_SecretsNeverSerialised(t *testing.T) {
	f := newFixture(t)
	type entry struct {
		Key    string `json:"key"`
		Value  any    `json:"value"`
		Source string `json:"source"`
	}
	cfg := func(context.Context, diagnostics.Viewer) (any, error) {
		return []entry{
			{"core.auth.session_key", diagnostics.Secret{Name: "core.auth.session_key"}, "file"},
			{"core.ui.title", "Gotham Audio Archive", "default"},
			{"core.updater.token", diagnostics.Redacted{}, "env"},
		}, nil
	}
	// A section that hands over the raw value under a credential key anyway:
	// the pattern layer is the second line of defence.
	leaky := contributor{{Name: "batcave.leaky", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return w.JSON("settings.json", map[string]any{
			"password": secretPass,
			"nested":   map[string]any{"api_key": secretAPIKey, "count": 3},
			"list":     []any{map[string]any{"secret": secretPass}},
		})
	}}}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{Config: cfg}, leaky)
	res, err := f.build(e, "run-secrets", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	assertNoneOf(t, files, secretPass, secretAPIKey)

	conf := string(files["core.config/config.json"])
	for _, want := range []string{"<secret:core.auth.session_key>", "<redacted>", "Gotham Audio Archive"} {
		if !strings.Contains(conf, want) {
			t.Errorf("config.json lacks %q:\n%s", want, conf)
		}
	}
	m := manifestOf(t, files)
	if m.Redactions["typed"] != 2 {
		t.Errorf("typed redactions = %d, want 2 (%v)", m.Redactions["typed"], m.Redactions)
	}
	if m.Redactions["pattern"] < 3 {
		t.Errorf("pattern redactions = %d, want at least 3 (%v)", m.Redactions["pattern"], m.Redactions)
	}
	if res.Redactions["typed"] != 2 {
		t.Errorf("ExportResult.Redactions = %v", res.Redactions)
	}
	if f.sink.redaction["typed"] != 2 {
		t.Errorf("metrics typed = %d", f.sink.redaction["typed"])
	}
}

func TestDiagnostics_PlaceholderLookalikeStillScanned(t *testing.T) {
	f := newFixture(t)
	c := contributor{{Name: "batcave.sneaky", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return w.JSON("s.json", map[string]string{"note": "<secret:x> password=" + secretPass + " <secret:y>"})
	}}}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{}, c)
	res, err := f.build(e, "run-sneaky", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	assertNoneOf(t, f.zipFiles(res.File), secretPass)
}

func TestDiagnostics_PatternRedaction(t *testing.T) {
	f := newFixture(t)
	logs := func(context.Context) (io.ReadCloser, error) {
		lines := []string{
			`{"msg":"fetched","url":"https://bruce:` + secretURLPw + `@gotham.example/feed"}`,
			`Authorization: Bearer ` + secretBearer,
			`{"msg":"login","token":"` + secretBearer + `x"}`,
			`GET /api/core/v1/auth/invites/` + secretInvite,
			`plain line about the Batmobile`,
		}
		return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
	}
	health := func(context.Context, diagnostics.Viewer) (any, error) {
		return map[string]any{"detail": "dial https://alfred:" + secretURLPw + "@db.example failed", "cookie": secretCookie}, nil
	}
	items := func(yield func(map[string]string) bool) {
		for _, p := range []string{"Robin", "Batgirl", "Nightwing"} {
			if !yield(map[string]string{"name": p, "api_key": secretAPIKey}) {
				return
			}
		}
	}
	jl := contributor{{Name: "batcave.roster", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return diagnostics.WriteJSONL(w, "roster.jsonl", iter.Seq[map[string]string](items))
	}}}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{Logs: logs, Health: health}, jl)
	res, err := f.build(e, "run-pattern", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	assertNoneOf(t, files, secretURLPw, secretBearer, secretInvite, secretCookie, secretAPIKey)
	if got := string(files["core.logs/logs.jsonl"]); !strings.Contains(got, "plain line about the Batmobile") {
		t.Errorf("an innocent line was altered or lost:\n%s", got)
	}
	roster := strings.Split(strings.TrimSpace(string(files["batcave.roster/roster.jsonl"])), "\n")
	if len(roster) != 3 || !strings.Contains(roster[0], "Robin") {
		t.Errorf("roster.jsonl = %q", roster)
	}
	if m := manifestOf(t, files); m.Redactions["pattern"] < 6 {
		t.Errorf("pattern redactions = %d, want at least 6 (%v)", m.Redactions["pattern"], m.Redactions)
	}
}

func TestDiagnostics_ViewerFiltersSections(t *testing.T) {
	f := newFixture(t)
	adminOnly := contributor{{Name: "batcave.vault", Perm: rbac.Need("core:admin"), Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return w.JSON("vault.json", map[string]string{"gadget": "grapnel"})
	}}}
	open := contributor{{Name: "batcave.garage", Perm: rbac.AnyAuthenticated, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return w.JSON("garage.json", map[string]string{"car": "Batmobile"})
	}}}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{}, adminOnly, open)
	res, err := f.build(e, "run-filter", diagnostics.ExportParams{ActorID: "alfred"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	if _, ok := files["batcave.vault/vault.json"]; ok {
		t.Error("the admin-only section was written for a viewer without core:admin")
	}
	if _, ok := files["batcave.garage/garage.json"]; !ok {
		t.Errorf("an any-authenticated section is missing: %v", names(files))
	}
	m := manifestOf(t, files)
	if !slices.Equal(m.Omitted, []string{"batcave.vault"}) {
		t.Errorf("Omitted = %v", m.Omitted)
	}
	if m.Redactions["permission"] != 1 {
		t.Errorf("permission count = %d", m.Redactions["permission"])
	}

	// The same sections for an admin.
	e2 := f.exporter(viewerOf("core:system.manage", "core:admin"), diagnostics.CoreSources{}, adminOnly, open)
	res2, err := f.build(e2, "run-filter-admin", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.zipFiles(res2.File)["batcave.vault/vault.json"]; !ok {
		t.Error("an admin's bundle lacks the vault section")
	}
}

func TestDiagnostics_RequestedSectionsOnly(t *testing.T) {
	f := newFixture(t)
	mk := func(name string) contributor {
		return contributor{{Name: name, Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("x.json", name)
		}}}
	}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{}, mk("batcave.a"), mk("batcave.b"))
	res, err := f.build(e, "run-only", diagnostics.ExportParams{ActorID: "bruce", Sections: []string{"batcave.b"}})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	if _, ok := files["batcave.a/x.json"]; ok {
		t.Error("an unrequested section was written")
	}
	if _, ok := files["batcave.b/x.json"]; !ok {
		t.Error("the requested section is missing")
	}
	if _, err := f.build(e, "run-typo", diagnostics.ExportParams{ActorID: "bruce", Sections: []string{"batcave.c"}}); err == nil {
		t.Error("an unknown requested section was accepted")
	}
}

func TestDiagnostics_SectionErrorAndPanicGivePartialBundle(t *testing.T) {
	f := newFixture(t)
	bad := contributor{
		{Name: "batcave.failing", Perm: manage, Write: func(context.Context, diagnostics.Writer, diagnostics.Viewer) error {
			return errors.New("dial https://oswald:" + secretURLPw + "@penguin.example: refused")
		}},
		{Name: "batcave.panicking", Perm: manage, Write: func(context.Context, diagnostics.Writer, diagnostics.Viewer) error {
			panic("riddler was here")
		}},
		{Name: "batcave.fine", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("ok.json", map[string]bool{"fine": true})
		}},
	}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{}, bad)
	var seen [][2]int
	res, err := e.Build(context.Background(), "run-partial", tasks.Actor{UserID: "bruce", AuthMethod: "session"}, diagnostics.ExportParams{ActorID: "bruce"}, func(done, total int) { seen = append(seen, [2]int{done, total}) })
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	assertNoneOf(t, files, secretURLPw)
	m := manifestOf(t, files)
	if len(m.Errors) != 2 {
		t.Fatalf("Errors = %+v", m.Errors)
	}
	if _, ok := files["batcave.fine/ok.json"]; !ok {
		t.Error("the section after the failures is missing: the bundle did not continue")
	}
	if got := f.sink.bundles; !slices.Equal(got, []string{"partial"}) {
		t.Errorf("bundle outcomes = %v", got)
	}
	if len(seen) == 0 || seen[len(seen)-1] != [2]int{len(seen), len(seen)} {
		t.Errorf("progress = %v, want one call per section ending at done == total", seen)
	}
}

func TestDiagnostics_PathsConfinedToRoot(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"", "..", "../escape", "a/b", `a\b`, "run.zip", "run id", strings.Repeat("x", 200)} {
		if diagnostics.ValidRunID(id) {
			t.Errorf("ValidRunID(%q) = true", id)
		}
	}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{})
	if _, err := f.build(e, "../escape", diagnostics.ExportParams{ActorID: "bruce"}); err == nil {
		t.Error("a traversing run id was accepted")
	}
	parent := filepath.Dir(f.dir)
	before, _ := os.ReadDir(parent)

	// A section may not name a file that climbs out of its directory, or write
	// one name twice.
	evil := contributor{{Name: "batcave.evil", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		var errs []error
		for _, n := range []string{"../escape.json", "/abs.json", `back\slash.json`, "a/../../b.json", ""} {
			errs = append(errs, w.JSON(n, 1))
		}
		if err := w.JSON("same.json", 1); err != nil {
			errs = append(errs, err)
		}
		if err := w.JSON("same.json", 2); err == nil {
			return errors.New("duplicate accepted")
		}
		for i, err := range errs[:5] {
			if err == nil {
				return errors.New("bad entry name accepted: " + string(rune('0'+i)))
			}
		}
		return nil
	}}}
	e2 := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{}, evil)
	res, err := f.build(e2, "run-evil", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	if m := manifestOf(t, files); len(m.Errors) != 0 {
		t.Errorf("the evil section reported %+v", m.Errors)
	}
	for name := range files {
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
			t.Errorf("entry %q escapes", name)
		}
	}
	after, _ := os.ReadDir(parent)
	if len(after) != len(before) {
		t.Errorf("the parent directory changed: %d -> %d entries", len(before), len(after))
	}

	// The reader refuses a name that is not a bundle's, and a symlink that
	// points out of the root.
	if _, err := diagnostics.OpenBundle(f.root, "../x.zip", 0, time.Now()); !errors.Is(err, diagnostics.ErrExpired) {
		t.Errorf("OpenBundle(../x.zip) = %v", err)
	}
	outside := filepath.Join(parent, filepath.Base(f.dir)+"-outside.zip")
	if err := os.WriteFile(outside, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	if err := os.Symlink(outside, filepath.Join(f.dir, "link.zip")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	_, err = diagnostics.OpenBundle(f.root, "link.zip", 0, time.Now())
	if err == nil || errors.Is(err, diagnostics.ErrCorrupt) {
		t.Errorf("OpenBundle followed a symlink out of the root: %v", err)
	}
}

func TestDiagnostics_StreamRefiltersAtDownload(t *testing.T) {
	f := newFixture(t)
	sec := func(name string, perm rbac.Requirement) contributor {
		return contributor{{Name: name, Perm: perm, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("d.json", name)
		}}}
	}
	e := f.exporter(viewerOf("core:system.manage", "core:admin"), diagnostics.CoreSources{},
		sec("batcave.vault", rbac.Need("core:admin")), sec("batcave.garage", manage), sec("batcave.any", rbac.AnyAuthenticated),
		sec("batcave.either", rbac.AnyOf("core:admin", "core:system.status")))
	res, err := f.build(e, "run-stream", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := diagnostics.OpenBundle(f.root, res.File, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	stream := func(perms ...rbac.Permission) (map[string][]byte, []string) {
		var buf bytes.Buffer
		omitted, err := b.Stream(&buf, func(p rbac.Permission) bool { return slices.Contains(perms, p) })
		if err != nil {
			t.Fatal(err)
		}
		return unzip(t, buf.Bytes()), omitted
	}
	files, omitted := stream("core:system.manage", "core:admin")
	if len(omitted) != 0 || files["batcave.vault/d.json"] == nil {
		t.Errorf("an admin should see everything: omitted=%v files=%v", omitted, names(files))
	}
	files, omitted = stream("core:system.manage") // demoted
	want := []string{"batcave.either", "batcave.vault"}
	if !slices.Equal(omitted, want) {
		t.Errorf("omitted = %v, want %v", omitted, want)
	}
	for _, gone := range []string{"batcave.vault/d.json", "batcave.either/d.json"} {
		if files[gone] != nil {
			t.Errorf("%s survived the demotion", gone)
		}
	}
	if files["batcave.garage/d.json"] == nil || files["batcave.any/d.json"] == nil {
		t.Errorf("sections the checker still satisfies are missing: %v", names(files))
	}
	var listing struct{ Omitted []string }
	if err := json.Unmarshal(files[diagnostics.OmittedAtDownloadFile], &listing); err != nil || !slices.Equal(listing.Omitted, want) {
		t.Errorf("omitted-at-download listing = %q (%v)", files[diagnostics.OmittedAtDownloadFile], err)
	}
	files, omitted = stream() // a checker with no permissions at all
	if files["batcave.garage/d.json"] != nil || len(omitted) != 4 {
		t.Errorf("a checker with no scopes got %v (omitted %v)", names(files), omitted)
	}
}

func TestDiagnostics_StreamDropsUnknownSections(t *testing.T) {
	f := newFixture(t)
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{})
	res, err := f.build(e, "run-unknown", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	// Append an entry no manifest section accounts for.
	path := filepath.Join(f.dir, res.File)
	raw, _ := os.ReadFile(path)
	zr, _ := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, zf := range zr.File {
		if err := zw.Copy(zf); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := zw.Create("stray.section/x.json")
	_, _ = w.Write([]byte("{}"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := diagnostics.OpenBundle(f.root, res.File, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	var buf bytes.Buffer
	omitted, err := b.Stream(&buf, func(rbac.Permission) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(omitted, []string{"stray.section"}) || unzip(t, buf.Bytes())["stray.section/x.json"] != nil {
		t.Errorf("a section the manifest does not know was streamed (omitted %v)", omitted)
	}
}

func TestDiagnostics_OpenBundleExpired(t *testing.T) {
	f := newFixture(t)
	if _, err := diagnostics.OpenBundle(f.root, "gone.zip", 0, time.Now()); !errors.Is(err, diagnostics.ErrExpired) {
		t.Errorf("missing bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "junk.zip"), []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := diagnostics.OpenBundle(f.root, "junk.zip", 0, time.Now()); !errors.Is(err, diagnostics.ErrCorrupt) {
		t.Errorf("junk bundle: %v", err)
	}
}

func TestDiagnostics_FileSizeCapAndTextOnly(t *testing.T) {
	f := newFixture(t)
	big := contributor{{Name: "batcave.big", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		if err := w.File("lines.txt", strings.NewReader(strings.Repeat("Gotham city line\n", 100))); err != nil {
			return err
		}
		seq := func(yield func(int) bool) {
			for i := 0; ; i++ { // endless: the cap, not the producer, must stop it
				if !yield(i) {
					return
				}
			}
		}
		if err := diagnostics.WriteJSONL(w, "rows.jsonl", iter.Seq[int](seq)); err != nil {
			return err
		}
		return w.JSON("huge.json", map[string]string{"blob": strings.Repeat("x", 400)})
	}}, {Name: "batcave.binary", Perm: manage, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		return w.File("bin.dat", bytes.NewReader([]byte{'o', 'k', 0, 'x'}))
	}}}
	e, err := diagnostics.New(diagnostics.Config{
		Root: f.root, ViewerFor: viewerOf("core:system.manage"), Contributors: []diagnostics.Contributor{big}, MaxFileBytes: 256, Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Build(context.Background(), "run-caps", tasks.Actor{UserID: "bruce", AuthMethod: "session"}, diagnostics.ExportParams{ActorID: "bruce"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := f.zipFiles(res.File)
	if n := len(files["batcave.big/lines.txt"]); n != 256 {
		t.Errorf("lines.txt = %d bytes, want the 256 cap", n)
	}
	if n := len(files["batcave.big/rows.jsonl"]); n == 0 || n > 256 {
		t.Errorf("rows.jsonl = %d bytes", n)
	}
	if got := string(files["batcave.big/huge.json"]); !strings.Contains(got, "too_large") {
		t.Errorf("an oversize document was written whole: %q", got)
	}
	m := manifestOf(t, files)
	for _, want := range []string{"batcave.big/lines.txt", "batcave.big/rows.jsonl", "batcave.big/huge.json"} {
		if !slices.Contains(m.Truncated, want) {
			t.Errorf("Truncated = %v lacks %s", m.Truncated, want)
		}
	}
	if len(m.Errors) != 1 || m.Errors[0].Section != "batcave.binary" {
		t.Errorf("a binary file should be refused: %+v", m.Errors)
	}
}

func TestDiagnostics_CancelRemovesPartialFile(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	c := contributor{{Name: "batcave.slow", Perm: manage, Write: func(ctx context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
		cancel()
		return ctx.Err()
	}}}
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{}, c)
	if _, err := e.Build(ctx, "run-cancel", tasks.Actor{UserID: "bruce", AuthMethod: "session"}, diagnostics.ExportParams{ActorID: "bruce"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Build = %v, want context.Canceled", err)
	}
	ents, _ := os.ReadDir(f.dir)
	if len(ents) != 0 {
		t.Errorf("files left behind: %v", ents)
	}
	if got := f.sink.bundles; !slices.Equal(got, []string{"error"}) {
		t.Errorf("bundle outcomes = %v", got)
	}
}

func TestDiagnostics_NewRefusesMalformedSections(t *testing.T) {
	f := newFixture(t)
	ok := func(context.Context, diagnostics.Writer, diagnostics.Viewer) error { return nil }
	cases := map[string]diagnostics.Section{
		"bad name":   {Name: "Gotham", Perm: manage, Write: ok},
		"no write":   {Name: "batcave.x", Perm: manage},
		"zero perm":  {Name: "batcave.x", Write: ok},
		"delegated":  {Name: "batcave.x", Perm: rbac.Delegated("owned by a module"), Write: ok},
		"reserved":   {Name: "core.manifest", Perm: manage, Write: ok},
		"core clash": {Name: "core.build", Perm: manage, Write: ok},
	}
	for name, s := range cases {
		_, err := diagnostics.New(diagnostics.Config{Root: f.root, ViewerFor: viewerOf(), Contributors: []diagnostics.Contributor{contributor{s}}})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dup := contributor{{Name: "batcave.x", Perm: manage, Write: ok}, {Name: "batcave.x", Perm: manage, Write: ok}}
	if _, err := diagnostics.New(diagnostics.Config{Root: f.root, ViewerFor: viewerOf(), Contributors: []diagnostics.Contributor{dup}}); err == nil {
		t.Error("duplicate section accepted")
	}
	if _, err := diagnostics.New(diagnostics.Config{ViewerFor: viewerOf()}); err == nil {
		t.Error("nil Root accepted")
	}
}

func TestDiagnostics_ViewerSatisfiesFailsClosed(t *testing.T) {
	can := func(p rbac.Permission) bool { return p == "core:system.manage" }
	v := diagnostics.Viewer{ActorID: "bruce", Can: can}
	for name, tc := range map[string]struct {
		req  rbac.Requirement
		want bool
	}{
		"public":        {rbac.Public, true},
		"authenticated": {rbac.AnyAuthenticated, true},
		"all held":      {rbac.AllOf("core:system.manage"), true},
		"all one short": {rbac.AllOf("core:system.manage", "core:admin"), false},
		"any one":       {rbac.AnyOf("core:admin", "core:system.manage"), true},
		"any none":      {rbac.AnyOf("core:admin", "core:system.status"), false},
		"delegated":     {rbac.Delegated("module checks"), false},
		"legacy":        {rbac.LegacyAtom("books.view"), false},
		"zero":          {rbac.Requirement{}, false},
	} {
		if got := v.Satisfies(tc.req); got != tc.want {
			t.Errorf("%s: Satisfies = %v, want %v", name, got, tc.want)
		}
	}
	if (diagnostics.Viewer{}).Satisfies(rbac.AllOf("core:admin")) {
		t.Error("a viewer with no checker satisfied a scope requirement")
	}
}

func TestDiagnostics_ManifestAndCoreBuild(t *testing.T) {
	f := newFixture(t)
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{
		System: func(context.Context, diagnostics.Viewer) (any, error) { return map[string]int{"cpus": 8}, nil },
	})
	res, err := f.build(e, "run-manifest", diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	if res.File != "run-manifest.zip" {
		t.Errorf("File = %q", res.File)
	}
	files := f.zipFiles(res.File)
	m := manifestOf(t, files)
	if m.BundleVersion != diagnostics.BundleVersion || m.App != "gotham-audio" || m.Version != "9.9.9" || !m.CreatedAt.Equal(fixedNow) || m.ActorID != "bruce" {
		t.Errorf("manifest = %+v", m)
	}
	if !slices.Equal(e.SectionNames(), []string{"core.system", "core.build"}) {
		t.Errorf("SectionNames = %v", e.SectionNames())
	}
	if files["core.build/build.json"] == nil || files["core.system/info.json"] == nil {
		t.Errorf("entries = %v", names(files))
	}
	if info, err := os.Stat(filepath.Join(f.dir, res.File)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode = %v (%v), want 0600", info, err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "run-manifest.zip.tmp")); err == nil {
		t.Error("the temp file survived the rename")
	}
}

func TestDiagnostics_Prune(t *testing.T) {
	f := newFixture(t)
	old, fresh := filepath.Join(f.dir, "old.zip"), filepath.Join(f.dir, "fresh.zip")
	partial, other := filepath.Join(f.dir, "dead.zip.tmp"), filepath.Join(f.dir, "notes.txt")
	for _, p := range []string{old, fresh, partial, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := fixedNow.Add(-48 * time.Hour)
	for _, p := range []string{old, partial, other} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(fresh, fixedNow, fixedNow); err != nil {
		t.Fatal(err)
	}
	n, err := diagnostics.Prune(f.root, 24*time.Hour, fixedNow)
	if err != nil || n != 2 {
		t.Fatalf("Prune = %d, %v; want 2", n, err)
	}
	for p, want := range map[string]bool{old: false, partial: false, fresh: true, other: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}
