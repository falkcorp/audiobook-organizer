// file: internal/books/cutover_flip_test.go
// version: 1.1.0
// guid: b94e07d2-6a1c-4c35-8d7f-2e0a5f3c9b18
// last-edited: 2026-11-19

package books

import (
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// flipOn is a books module with F3's cutover declared, the way slice (e) will
// ship it: the switch on and Options' paths set.
func flipOn() *booksModule {
	return &booksModule{flip: true, stateDir: "/srv/gotham/state", dbPath: "/srv/gotham/main.pebble"}
}

// versionOf finds a versioned step by name.
func versionOf(ms []storage.Migration, name string) (storage.Migration, bool) {
	i := slices.IndexFunc(ms, func(m storage.Migration) bool { return m.Name == name })
	if i < 0 {
		return storage.Migration{}, false
	}
	return ms[i], true
}

// TestManifest_FlipIsDarkUnlessSwitchedOn: the shipped Module() declares none of
// F3's cutover, so the manifest is the pre-flip one: no level step, no raised
// Level, no Cutover, no Rewrite. The same module with the switch on but no
// database path (an offline open that never ran Options) declares none either:
// a level must never rise without M2 behind it.
func TestManifest_FlipIsDarkUnlessSwitchedOn(t *testing.T) {
	withConfig(t, config.Config{})
	shipped := Module().Manifest()
	offline := (&booksModule{flip: true}).Manifest()
	switchedOffWithPaths := (&booksModule{stateDir: "/srv/gotham/state", dbPath: "/srv/gotham/main.pebble"}).Manifest()
	for name, man := range map[string]module.Manifest{"shipped": shipped, "flip without paths": offline, "paths without flip": switchedOffWithPaths} {
		if man.Cutover != nil || man.Rewrite != nil || man.Storage.Level != 0 {
			t.Errorf("%s: Cutover=%v Rewrite=%v Level=%d, want none declared", name, man.Cutover != nil, man.Rewrite, man.Storage.Level)
		}
		if _, ok := versionOf(man.Storage.Migrations, cutover.BooksCutoverLevelStepID); ok {
			t.Errorf("%s: declares %s", name, cutover.BooksCutoverLevelStepID)
		}
	}
}

// TestManifest_FlipDeclaresM2M7M8 (F3-07 d): switched on, the manifest carries
// books.level-1 at the pinned version raising books to level 1 with Spec.Level
// to match, exactly M2 behind it, and M8 after M2.
func TestManifest_FlipDeclaresM2M7M8(t *testing.T) {
	man := flipOn().Manifest()
	lvl, ok := versionOf(man.Storage.Migrations, cutover.BooksCutoverLevelStepID)
	if !ok {
		t.Fatalf("no %s in books' migrations", cutover.BooksCutoverLevelStepID)
	}
	if lvl.Version != booksLevelStepVersion || lvl.RaisesLevel != cutover.BooksCutoverLevel || lvl.Compat != storage.Breaking ||
		man.Storage.Level != cutover.BooksCutoverLevel {
		t.Errorf("level step = %+v, Spec.Level %d; want version %d raising to %d Breaking, Level %d",
			lvl, man.Storage.Level, booksLevelStepVersion, cutover.BooksCutoverLevel, cutover.BooksCutoverLevel)
	}
	if man.Cutover == nil {
		t.Fatal("no Manifest.Cutover")
	}
	data := man.Cutover(module.Importers{})
	if len(data) != 1 || data[0].ID != cutover.ConfigCutoverID || data[0].Owner != cutover.BooksOwner ||
		!slices.Contains(data[0].After, cutover.BooksLevelStepRef(booksLevelStepVersion)) {
		t.Errorf("Manifest.Cutover = %+v, want exactly M2 behind %s", data, cutover.BooksCutoverLevelStepID)
	}
	if man.Rewrite == nil || man.Rewrite.ID != cutover.RewriteID || man.Rewrite.Owner != cutover.BooksOwner ||
		!slices.Contains(man.Rewrite.After, storage.StepRef{Owner: cutover.BooksOwner, DataMigration: cutover.ConfigCutoverID}) {
		t.Errorf("Manifest.Rewrite = %+v, want %s after %s", man.Rewrite, cutover.RewriteID, cutover.ConfigCutoverID)
	}
	// M2 drains the four legacy secret rows, so exactly those claims retire; the
	// blob stays read-write (M6 deletes it) and no other claim changes.
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
	if n := len(man.Storage.Migrations); man.Storage.Migrations[n-1].Name != cutover.BooksCutoverLevelStepID || n != len(database.BooksSpec(nil).Migrations)+1 {
		t.Errorf("migrations = %d, last %q; want the R1 steps then %s", n, man.Storage.Migrations[n-1].Name, cutover.BooksCutoverLevelStepID)
	}
}

// TestManifest_FlipPinsBooksNextVersion: the level step's pinned version is
// books' next contiguous one. A versioned step added ahead of the level step
// moves database.NextMigrationVersion and fails here before it renumbers a step
// that may already have run.
func TestManifest_FlipPinsBooksNextVersion(t *testing.T) {
	if got := database.NextMigrationVersion(); got != booksLevelStepVersion {
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

// TestFlipOptions_WireTheKernelsSeams: M2's inputs come from Options' paths and
// the Importers the kernel binds at P4b: the legacy reader is books' own range
// (never another owner's) and the key-regen switch is the bound value; with no
// seam bound (an unbound Importers) the reader is nil, which M2 refuses as "not
// open yet", and the switch is off.
func TestFlipOptions_WireTheKernelsSeams(t *testing.T) {
	m := flipOn()
	var asked []storage.Owner
	imp := module.Importers{
		Legacy: func(o storage.Owner) storage.Reader {
			asked = append(asked, o)
			return nil
		},
		AllowKeyRegen: func() bool { return true },
	}
	opts := m.flipOptions(imp)
	if opts.LevelStepVersion != booksLevelStepVersion {
		t.Errorf("LevelStepVersion = %d, want %d", opts.LevelStepVersion, booksLevelStepVersion)
	}
	c := opts.Config
	if c.StateDir != m.stateDir || c.DatabasePath != m.dbPath || c.ConfigFile != "/srv/gotham/config.yaml" {
		t.Errorf("paths = state %q db %q config %q, want Options' and config.yaml beside the database", c.StateDir, c.DatabasePath, c.ConfigFile)
	}
	if c.Legacy == nil || c.AllowKeyRegen == nil {
		t.Fatal("Legacy or AllowKeyRegen is not wired")
	}
	_ = c.Legacy()
	if !slices.Equal(asked, []storage.Owner{cutover.BooksOwner}) {
		t.Errorf("Legacy asked for %v, want books' own range only", asked)
	}
	if !c.AllowKeyRegen() {
		t.Error("AllowKeyRegen did not report the bound value")
	}
	if un := m.flipOptions(module.Importers{}).Config; un.Legacy != nil || (un.AllowKeyRegen != nil && un.AllowKeyRegen()) {
		t.Error("an unbound Importers produced a Legacy reader or a true AllowKeyRegen")
	}
}

// oracleModule is a module with no storage and no settings: the smallest module
// an app.New boot needs to exercise core config with the books lists.
type oracleModule struct{}

func (oracleModule) Manifest() module.Manifest {
	return module.Manifest{ID: "oracle", Version: "1.0.0", MinCoreVersion: "0.1.0", DefaultEnabled: true}
}
func (oracleModule) Register(*module.Registrar) error { return nil }

// TestFlipConfigNames_DarkUntilDeclared: the retired names and the OPENAI
// bindings reach app.Options only with the flip declared. Dark, Options and
// OfflineOptions carry neither, so `config check` never calls a name HEAD still
// reads "retired". Declared, the lists are the rename table's: the retired names
// exclude the ecosystem names, and the bindings are exactly the two OPENAI ones.
func TestFlipConfigNames_DarkUntilDeclared(t *testing.T) {
	withConfig(t, config.Config{DatabasePath: filepath.Join(t.TempDir(), "gotham.pebble")})
	opts, err := OfflineOptions()
	if err != nil {
		t.Fatal(err)
	}
	if opts.RetiredConfigNames != nil || opts.ConfigEnvBindings != nil {
		t.Errorf("OfflineOptions carries retired=%d bindings=%d while the flip is dark", len(opts.RetiredConfigNames), len(opts.ConfigEnvBindings))
	}
	if r, b := flipConfigNames(false); r != nil || b != nil {
		t.Errorf("flipConfigNames(false) = %v, %v, want nothing", r, b)
	}

	old := flipDeclared
	flipDeclared = true
	t.Cleanup(func() { flipDeclared = old })
	opts, err = OfflineOptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.RetiredConfigNames) == 0 {
		t.Error("a declared flip's OfflineOptions has no retired names")
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
	if Module().(*booksModule).flip != true {
		t.Error("Module() did not copy the switch")
	}
}

// TestFlipConfigNames_RetiredListAcceptedByCoreConfig: core config accepts the
// books retired list at boot (a name listed twice or a malformed entry would
// refuse it). The OPENAI bindings are deliberately not booted here: core config
// places an env binding only onto a path some section owns or Options.Passthrough
// accepts, and nothing sets Passthrough yet, so booting them with OPENAI_API_KEY
// set fails "no registered section owns integrations.ai.openai.api_key". Slice (e)
// or the integrations store (F7-08) must supply it before the switch flips
// (todo.d/2026-11-19-f3-07-passthrough-for-openai-bindings.md).
func TestFlipConfigNames_RetiredListAcceptedByCoreConfig(t *testing.T) {
	retired, _ := flipConfigNames(true)
	a, err := app.New(t.Context(), app.Options{
		AppID: AppID, StateDir: t.TempDir(), CoreConfigBoot: true,
		Args: []string{}, Env: []string{}, RetiredConfigNames: retired,
	}, oracleModule{})
	if err != nil {
		t.Fatalf("app.New with the books retired list: %v", err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestOptions_CarriesTheConfigListsOnlyWithTheSwitch: serve's Options hands the
// retired names and OPENAI bindings to app.Options exactly when the module it
// is given declares the flip; Module() as shipped hands neither.
func TestOptions_CarriesTheConfigListsOnlyWithTheSwitch(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run("flip="+strconv.FormatBool(on), func(t *testing.T) {
			initConfigForTest(t)
			sandboxStateDir(t)
			config.AppConfig.DatabasePath = filepath.Join(tempDir(t), "gotham.pebble")
			mod := Module().(*booksModule)
			mod.flip = on
			opts, err := Options(mod, ServeInputs{Server: server.GetDefaultServerConfig(), Telemetry: config.Snapshot()})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(opts.RetiredConfigNames) > 0 && len(opts.ConfigEnvBindings) == 2; got != on {
				t.Errorf("Options carries retired=%d bindings=%d", len(opts.RetiredConfigNames), len(opts.ConfigEnvBindings))
			}
		})
	}
}
