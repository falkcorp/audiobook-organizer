// file: internal/books/cutover_flip_test.go
// version: 1.3.0
// guid: b94e07d2-6a1c-4c35-8d7f-2e0a5f3c9b18
// last-edited: 2026-11-19

package books

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// flipOn is a books module with F3's cutover declared, the way serve ships it:
// Options' paths set (declaresFlip is a database named).
func flipOn() *booksModule {
	return &booksModule{stateDir: "/srv/gotham/state", dbPath: "/srv/gotham/main.pebble"}
}

// versionOf finds a versioned step by name.
func versionOf(ms []storage.Migration, name string) (storage.Migration, bool) {
	i := slices.IndexFunc(ms, func(m storage.Migration) bool { return m.Name == name })
	if i < 0 {
		return storage.Migration{}, false
	}
	return ms[i], true
}

// TestManifest_LevelNeedsADatabase: a books module no database was named for
// (Module() as the help listing builds it, or an offline open that never ran
// Options) declares no level: no level step, no raised Level, no Cutover. A
// level is declared only for a build that serves a store.
func TestManifest_LevelNeedsADatabase(t *testing.T) {
	withConfig(t, config.Config{})
	identityOnly := Module().Manifest()
	stateOnly := (&booksModule{stateDir: "/srv/gotham/state"}).Manifest()
	for name, man := range map[string]module.Manifest{"identity only": identityOnly, "a state directory without a database": stateOnly} {
		if man.Cutover != nil || man.Storage.Level != 0 {
			t.Errorf("%s: Cutover=%v Level=%d, want none declared", name, man.Cutover != nil, man.Storage.Level)
		}
		if _, ok := versionOf(man.Storage.Migrations, booksLevelStepID); ok {
			t.Errorf("%s: declares %s", name, booksLevelStepID)
		}
	}
}

// TestManifest_DeclaresTheLevelStep: over a database, the manifest carries
// books.level-1 last, at the pinned version, raising books to level 1 with
// Spec.Level to match; an empty Manifest.Cutover (the kernel registers core's
// level owner for it and books has no data step of its own); no Rewrite; and
// exactly the four drained secret rows' claims retired.
func TestManifest_DeclaresTheLevelStep(t *testing.T) {
	man := flipOn().Manifest()
	lvl, ok := versionOf(man.Storage.Migrations, booksLevelStepID)
	if !ok {
		t.Fatalf("no %s in books' migrations", booksLevelStepID)
	}
	if lvl.Version != booksLevelStepVersion || lvl.RaisesLevel != booksLevel || lvl.Compat != storage.Breaking ||
		man.Storage.Level != booksLevel {
		t.Errorf("level step = %+v, Spec.Level %d; want version %d raising to %d Breaking, Level %d",
			lvl, man.Storage.Level, booksLevelStepVersion, booksLevel, booksLevel)
	}
	if lvl.Name != storage.LevelStepName(database.BooksOwner, booksLevel) {
		t.Errorf("level step name %q is not storage.LevelStepName(books, %d)", lvl.Name, booksLevel)
	}
	if man.Cutover == nil {
		t.Fatal("no Manifest.Cutover: core's level owner would not be registered")
	}
	if data := man.Cutover(module.Importers{}); len(data) != 0 {
		t.Errorf("Manifest.Cutover = %+v, want no data step", data)
	}
	if man.Rewrite != nil {
		t.Errorf("Manifest.Rewrite = %+v, want none", man.Rewrite)
	}
	// The blob stays read-write and no other claim changes.
	retired := map[string]bool{}
	for i, c := range man.Storage.Claims {
		if c.Mode == storage.ClaimRetired {
			retired[c.Key] = true
		}
		if c.Mode != database.BooksClaims()[i].Mode && !retired[c.Key] {
			t.Errorf("claim %q changed mode to %v", c.Key+c.Prefix, c.Mode)
		}
	}
	wantRetired := map[string]bool{"setting:openai_api_key": true, "setting:google_books_api_key": true,
		"setting:hardcover_api_token": true, "setting:basic_auth_password": true}
	if !maps.Equal(retired, wantRetired) {
		t.Errorf("retired claims = %v, want exactly the four legacy secret rows %v", retired, wantRetired)
	}
	// The R1 steps are untouched and the level step comes after them.
	if n := len(man.Storage.Migrations); man.Storage.Migrations[n-1].Name != booksLevelStepID || n != len(database.BooksSpec(nil).Migrations)+1 {
		t.Errorf("migrations = %d, last %q; want the R1 steps then %s", n, man.Storage.Migrations[n-1].Name, booksLevelStepID)
	}
}

// nextBooksVersion is the version the next versioned books step must take: one
// above the last of the R1 steps. It lives in the tests, not in the store
// package, because nothing in production needs it: the level step's version is
// pinned (booksLevelStepVersion) and these tests check the pin against it.
func nextBooksVersion() int {
	ms := database.BooksSpec(nil).Migrations
	return ms[len(ms)-1].Version + 1
}

// TestManifest_LevelStepIsBooksNextVersion: the level step's pinned version is
// books' next contiguous one. A versioned step added ahead of the level step
// moves nextBooksVersion and fails here before it renumbers a step
// that may already have run.
func TestManifest_LevelStepIsBooksNextVersion(t *testing.T) {
	if got := nextBooksVersion(); got != booksLevelStepVersion {
		t.Fatalf("books' next contiguous version is %d but books.level-1 is pinned at %d", got, booksLevelStepVersion)
	}
}

// TestBooksFlipBootsGateOn (F3-07 d, the gate-on twin of TestBooksDisabledInert):
// booted through app.New with the config boot on and the flip declared, the
// kernel registers core.level-1 beside books.level-1, accepts books' spec (the
// level, its pinned version, M2's After, M8 last), plans and runs both level
// steps, M2 and M8 over an empty store, and a second boot of the same store
// finds nothing left to do. Books stays disabled so its Register never builds
// HEAD's server.
func TestBooksFlipBootsGateOn(t *testing.T) {
	withConfig(t, config.Config{})
	mod := flipOn()
	h := coretest.New(t, coretest.WithModules(mod), coretest.WithDisabled(ID))
	coretest.AssertModuleInert(t, h, ID)
	h = h.Reboot(t, coretest.WithModules(mod), coretest.WithDisabled(ID))
	coretest.AssertModuleInert(t, h, ID)
}

// oracleModule is a module with no storage and no settings: the smallest module
// an app.New boot needs to exercise core config with the books lists.
type oracleModule struct{}

func (oracleModule) Manifest() module.Manifest {
	return module.Manifest{ID: "oracle", Version: "1.0.0", MinCoreVersion: "0.1.0", DefaultEnabled: true}
}
func (oracleModule) Register(*module.Registrar) error { return nil }

// TestFlipConfigNames_Lists: OfflineOptions carries the retired names, the
// OPENAI bindings and the passthrough predicate, so `config check` calls a name
// the flip retires "retired". They are the rename table's: the retired names
// exclude the ecosystem names, and the bindings are exactly the two OPENAI ones.
// (The dark half, the lists absent until the flip was declared, went with the
// switch.)
func TestFlipConfigNames_Lists(t *testing.T) {
	gotham := filepath.Join(t.TempDir(), "gotham.pebble")
	withConfig(t, config.Config{DatabasePath: gotham})
	t.Setenv(storagePathEnv, gotham)
	opts, err := OfflineOptions(BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.RetiredConfigNames) == 0 {
		t.Error("OfflineOptions has no retired names")
	}
	for _, r := range opts.RetiredConfigNames {
		if r.Name == "OPENAI_API_KEY" || r.Name == "OPENAI_BASE_URL" {
			t.Errorf("%s is an ecosystem binding, not a retired name", r.Name)
		}
	}
	want := []coreconfig.EnvName{{Name: "OPENAI_API_KEY", Path: "integrations.ai.openai.api_key"},
		{Name: "OPENAI_BASE_URL", Path: "integrations.ai.openai.base_url"}}
	if !slices.Equal(opts.ConfigEnvBindings, want) {
		t.Errorf("bindings = %v, want %v", opts.ConfigEnvBindings, want)
	}
	if opts.ConfigPassthrough == nil || !opts.ConfigPassthrough("integrations.ai.openai.api_key") {
		t.Error("OfflineOptions has no passthrough predicate for the OPENAI bindings")
	}
}

// TestOfflineModule_PlansTheFlipOnlyOverADatabase: the module core's offline
// commands plan from carries the level step exactly when it was built over
// the database OfflineOptions names. Built the way the help listing builds it
// (identity-only Options) or by Module() it plans none, so a dry run over it
// would report a list serve does not run; built over OfflineOptions' paths it
// plans what Options' module does.
func TestOfflineModule_PlansTheFlipOnlyOverADatabase(t *testing.T) {
	gotham := filepath.Join(t.TempDir(), "gotham.pebble")
	withConfig(t, config.Config{DatabasePath: gotham})
	t.Setenv(storagePathEnv, gotham)
	opts, err := OfflineOptions(BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		mod  module.Module
		want bool
	}{
		{"module with no database", Module(), false},
		{"offline module over identity-only options", OfflineModule(app.Options{AppID: AppID}), false},
		{"offline module over OfflineOptions", OfflineModule(opts), true},
	}
	for _, c := range cases {
		man := c.mod.Manifest()
		if got := man.Cutover != nil; got != c.want {
			t.Errorf("%s: declares a cutover = %t, want %t", c.name, got, c.want)
		}
		if got := man.Storage.Level != 0; got != c.want {
			t.Errorf("%s: storage level declared = %t, want %t", c.name, got, c.want)
		}
	}
	wantDB, err := OfflineStorePath(opts)
	if err != nil {
		t.Fatal(err)
	}
	if m := OfflineModule(opts).(*booksModule); m.dbPath != wantDB || m.stateDir != opts.StateDir || m.version != opts.Version {
		t.Errorf("OfflineModule carries db=%q state=%q version=%q, want %q %q %q", m.dbPath, m.stateDir, m.version, wantDB, opts.StateDir, opts.Version)
	}
}

// TestFlipConfigNames_BootsWithTheOpenAIBindings: the whole F3 config list the
// books module hands app.Options (retired names, the two OPENAI bindings and the
// passthrough predicate) is accepted by core config's boot with both ecosystem
// names set, and the value an operator supplies through OPENAI_API_KEY is what
// the integrations path reads. Without the predicate the same boot is refused
// ("no registered section owns"), which is what the todo.d fragment of slice (d)
// recorded: the predicate is what makes the bindings bootable.
func TestFlipConfigNames_BootsWithTheOpenAIBindings(t *testing.T) {
	retired, bindings, passthrough := flipConfigNames()
	env := []string{"OPENAI_API_KEY=bane", "OPENAI_BASE_URL=https://api.gotham.invalid/v1"}
	boot := func(pt func(coreconfig.Path) bool) error {
		a, err := app.New(t.Context(), app.Options{
			AppID: AppID, StateDir: t.TempDir(),
			Args: []string{}, Env: env, RetiredConfigNames: retired,
			ConfigEnvBindings: bindings, ConfigPassthrough: pt,
		}, oracleModule{})
		if err != nil {
			return err
		}
		return a.Close(t.Context())
	}
	if err := boot(passthrough); err != nil {
		t.Fatalf("app.New with the books config lists and both OPENAI names set: %v", err)
	}
	err := boot(nil)
	if err == nil {
		t.Fatal("the bindings booted with no passthrough predicate; the predicate is not what makes them bootable")
	}
	if !strings.Contains(err.Error(), "no registered section owns") {
		t.Errorf("without the predicate the boot failed for another reason: %v", err)
	}
}

// TestOptions_CarriesTheConfigLists: serve's Options hands the retired names,
// OPENAI bindings and passthrough predicate to app.Options.
func TestOptions_CarriesTheConfigLists(t *testing.T) {
	restoreConfigForTest(t)
	sandboxStateDir(t)
	config.AppConfig.DatabasePath = filepath.Join(tempDir(t), "gotham.pebble")
	t.Setenv(storagePathEnv, config.AppConfig.DatabasePath)
	opts, err := Options(Module().(*booksModule), ServeInputs{Server: server.GetDefaultServerConfig(), Telemetry: config.Snapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(opts.RetiredConfigNames) > 0 && len(opts.ConfigEnvBindings) == 2 && opts.ConfigPassthrough != nil; !got {
		t.Errorf("Options carries retired=%d bindings=%d passthrough=%t", len(opts.RetiredConfigNames), len(opts.ConfigEnvBindings), opts.ConfigPassthrough != nil)
	}
}
