// file: internal/books/cutover_flip_test.go
// version: 1.3.0
// guid: b94e07d2-6a1c-4c35-8d7f-2e0a5f3c9b18
// last-edited: 2026-11-19

package books

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	coreauth "github.com/falkcorp/mediacore/auth"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// flipOn is a books module with F3's cutover declared, the way serve ships it:
// Options' paths set (declaresFlip is a database named).
func flipOn() *booksModule {
	return &booksModule{stateDir: "/srv/gotham/state", dbPath: "/srv/gotham/main.pebble"}
}

// flipOnAt is flipOn over a state directory and a database path that exist: the
// crash matrix's fixtures, whose key file M2 reads and whose paths the level step
// and the legacy key search name (flipOn's /srv/gotham paths exist nowhere).
func flipOnAt(stateDir, dbPath string) *booksModule {
	m := flipOn()
	m.stateDir, m.dbPath = stateDir, dbPath
	return m
}

// versionOf finds a versioned step by name.
func versionOf(ms []storage.Migration, name string) (storage.Migration, bool) {
	i := slices.IndexFunc(ms, func(m storage.Migration) bool { return m.Name == name })
	if i < 0 {
		return storage.Migration{}, false
	}
	return ms[i], true
}

// TestManifest_FlipNeedsADatabase: a books module no database was named for
// (Module() as the help listing builds it, or an offline open that never ran
// Options) declares none of F3's cutover: no level step, no raised Level, no
// Cutover, no Rewrite. A level must never rise without M2 behind it. (The
// switch-off cells went with the switch: with a database named the flip is
// always declared, TestManifest_FlipDeclaresM2M7M8.)
func TestManifest_FlipNeedsADatabase(t *testing.T) {
	withConfig(t, config.Config{})
	identityOnly := Module().Manifest()
	stateOnly := (&booksModule{stateDir: "/srv/gotham/state"}).Manifest()
	for name, man := range map[string]module.Manifest{"identity only": identityOnly, "a state directory without a database": stateOnly} {
		if man.Cutover != nil || man.Rewrite != nil || man.Storage.Level != 0 {
			t.Errorf("%s: Cutover=%v Rewrite=%v Level=%d, want none declared", name, man.Cutover != nil, man.Rewrite, man.Storage.Level)
		}
		if _, ok := versionOf(man.Storage.Migrations, cutover.BooksCutoverLevelStepID); ok {
			t.Errorf("%s: declares %s", name, cutover.BooksCutoverLevelStepID)
		}
	}
}

// TestManifest_FlipDeclaresM2M7M8 (F3-07 d, with F4-08's identity half since
// B10g turned it on): over a database, the manifest carries books.level-1 at the
// pinned version raising books to level 1, books.level-2 at the next version
// raising it to BooksIdentityLevel with Spec.Level to match, exactly M2 behind
// level-1 and M3 behind level-2 and M2, and M8 after both data steps.
func TestManifest_FlipDeclaresM2M7M8(t *testing.T) {
	man := flipOn().Manifest()
	lvl, ok := versionOf(man.Storage.Migrations, cutover.BooksCutoverLevelStepID)
	if !ok {
		t.Fatalf("no %s in books' migrations", cutover.BooksCutoverLevelStepID)
	}
	if lvl.Version != booksLevelStepVersion || lvl.RaisesLevel != cutover.BooksCutoverLevel || lvl.Compat != storage.Breaking {
		t.Errorf("level-1 = %+v; want version %d raising to %d Breaking", lvl, booksLevelStepVersion, cutover.BooksCutoverLevel)
	}
	lvl2, ok := versionOf(man.Storage.Migrations, cutover.BooksIdentityLevelStepID)
	if !ok {
		t.Fatalf("no %s in books' migrations", cutover.BooksIdentityLevelStepID)
	}
	if lvl2.Version != booksLevelStepVersion+1 || lvl2.RaisesLevel != cutover.BooksIdentityLevel || lvl2.Compat != storage.Breaking ||
		man.Storage.Level != cutover.BooksIdentityLevel {
		t.Errorf("level-2 = %+v, Spec.Level %d; want version %d raising to %d Breaking, Level %d",
			lvl2, man.Storage.Level, booksLevelStepVersion+1, cutover.BooksIdentityLevel, cutover.BooksIdentityLevel)
	}
	if man.Cutover == nil {
		t.Fatal("no Manifest.Cutover")
	}
	data := man.Cutover(shapeImporters())
	if len(data) != 2 {
		t.Fatalf("Manifest.Cutover = %d steps (%+v), want M2 then M3", len(data), data)
	}
	m2Ref := storage.StepRef{Owner: cutover.BooksOwner, DataMigration: cutover.ConfigCutoverID}
	if data[0].ID != cutover.ConfigCutoverID || data[0].Owner != cutover.BooksOwner ||
		!slices.Contains(data[0].After, cutover.BooksLevelStepRef(booksLevelStepVersion)) {
		t.Errorf("Manifest.Cutover[0] = %+v, want M2 behind %s", data[0], cutover.BooksCutoverLevelStepID)
	}
	if data[1].ID != cutover.IdentityCutoverID || data[1].Owner != cutover.BooksOwner ||
		!slices.Contains(data[1].After, cutover.BooksLevelStepRef(booksLevelStepVersion+1)) || !slices.Contains(data[1].After, m2Ref) {
		t.Errorf("Manifest.Cutover[1] = %+v, want M3 behind %s and M2", data[1], cutover.BooksIdentityLevelStepID)
	}
	if man.Rewrite == nil || man.Rewrite.ID != cutover.RewriteID || man.Rewrite.Owner != cutover.BooksOwner ||
		!slices.Contains(man.Rewrite.After, m2Ref) ||
		!slices.Contains(man.Rewrite.After, storage.StepRef{Owner: cutover.BooksOwner, DataMigration: cutover.IdentityCutoverID}) {
		t.Errorf("Manifest.Rewrite = %+v, want %s after %s and %s", man.Rewrite, cutover.RewriteID, cutover.ConfigCutoverID, cutover.IdentityCutoverID)
	}
	// M2 drains the four legacy secret rows and M3 the identity families and
	// HEAD's two bootstrap rows, so exactly those claims retire; the blob stays
	// read-write (M6 deletes it) and no other claim changes. The bootstrap rows
	// had no claim at R1, so the flip adds them as exact retired keys.
	r1 := map[string]storage.ClaimMode{}
	for _, c := range database.BooksClaims() {
		r1[c.Key+"\x00"+c.Prefix] = c.Mode
	}
	retired := map[string]bool{}
	for _, c := range man.Storage.Claims {
		id := c.Key + "\x00" + c.Prefix
		if c.Mode == storage.ClaimRetired {
			retired[c.Key+c.Prefix] = true
			continue
		}
		if mode, ok := r1[id]; !ok || mode != c.Mode {
			t.Errorf("claim %q is %v, R1 declares %v (present %t)", c.Key+c.Prefix, c.Mode, mode, ok)
		}
	}
	wantRetired := map[string]bool{"setting:openai_api_key": true, "setting:google_books_api_key": true,
		"setting:hardcover_api_token": true, "setting:basic_auth_password": true}
	for _, p := range m3.SourcePrefixes() {
		wantRetired[p] = true
	}
	for _, k := range m3.SourceKeys() {
		wantRetired[k] = true
	}
	if !maps.Equal(retired, wantRetired) {
		t.Errorf("retired claims = %v, want exactly the four legacy secret rows and M3's Source %v", retired, wantRetired)
	}
	// The R1 steps are untouched and the two level steps come after them.
	n := len(man.Storage.Migrations)
	if n != len(database.BooksSpec(nil).Migrations)+2 || man.Storage.Migrations[n-2].Name != cutover.BooksCutoverLevelStepID ||
		man.Storage.Migrations[n-1].Name != cutover.BooksIdentityLevelStepID {
		t.Errorf("migrations = %d, last %q; want the R1 steps then %s and %s", n, man.Storage.Migrations[n-1].Name,
			cutover.BooksCutoverLevelStepID, cutover.BooksIdentityLevelStepID)
	}
}

// shapeImporters are importers that carry what M3's config needs (an auth
// importer that reads users, a role importer that reads roles, a legacy range)
// and refuse every call: the manifest's Cutover func builds the step list over
// them and does no work, so nothing here is called. module.Importers{} is not
// enough since B10g: with the identity flip on, M3 refuses importers that cannot
// stage it (errNoIdentityImporters), and the cutover func records that and
// returns no steps.
func shapeImporters() module.Importers {
	return module.Importers{
		Auth:   shapeAuthImporter{},
		Roles:  shapeRoleImporter{},
		Legacy: func(storage.Owner) storage.Reader { return nil },
	}
}

var errShapeImporter = errors.New("the shape importers refuse every call")

type shapeAuthImporter struct{ coreauth.Importer }

func (shapeAuthImporter) GetUser(context.Context, coreauth.UserID) (*coreauth.User, error) {
	return nil, errShapeImporter
}

type shapeRoleImporter struct{ rbac.Importer }

func (shapeRoleImporter) GetRole(context.Context, rbac.RoleName) (*rbac.Role, error) {
	return nil, errShapeImporter
}

// nextBooksVersion is the version the next versioned books step must take: one
// above the last of the R1 steps. It lives in the tests, not in the store
// package, because nothing in production needs it: the level step's version is
// pinned (booksLevelStepVersion) and these tests check the pin against it.
func nextBooksVersion() int {
	ms := database.BooksSpec(nil).Migrations
	return ms[len(ms)-1].Version + 1
}

// TestManifest_FlipPinsBooksNextVersion: the level step's pinned version is
// books' next contiguous one. A versioned step added ahead of the level step
// moves nextBooksVersion and fails here before it renumbers a step
// that may already have run.
func TestManifest_FlipPinsBooksNextVersion(t *testing.T) {
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
		OperatorSet: func(p coreconfig.Path) (string, bool) {
			return "env", p == "core.auth.oidc.providers"
		},
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
	if c.OperatorSet == nil {
		t.Fatal("OperatorSet is not wired")
	}
	if _, set := c.OperatorSet("core.auth.oidc.providers"); !set {
		t.Error("OperatorSet did not report the bound lock")
	}
	if _, set := c.OperatorSet("core.log.level"); set {
		t.Error("OperatorSet reported a lock the bound func does not")
	}
	if un := m.flipOptions(module.Importers{}).Config; un.Legacy != nil || (un.AllowKeyRegen != nil && un.AllowKeyRegen()) || un.OperatorSet != nil {
		t.Error("an unbound Importers produced a Legacy reader, a true AllowKeyRegen or an OperatorSet")
	}
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
// commands plan from carries the flip's steps exactly when it was built over
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
		if got := man.Cutover != nil && man.Rewrite != nil; got != c.want {
			t.Errorf("%s: declares cutover+rewrite = %t, want %t", c.name, got, c.want)
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
	cowV1 := filepath.Join(tempDir(t), "gotham.pebble")
	config.SetForTest(func(c *config.Config) { c.DatabasePath = cowV1 })
	t.Setenv(storagePathEnv, config.Get().DatabasePath)
	opts, err := Options(Module().(*booksModule), ServeInputs{Server: server.GetDefaultServerConfig(), Telemetry: config.Snapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(opts.RetiredConfigNames) > 0 && len(opts.ConfigEnvBindings) == 2 && opts.ConfigPassthrough != nil; !got {
		t.Errorf("Options carries retired=%d bindings=%d passthrough=%t", len(opts.RetiredConfigNames), len(opts.ConfigEnvBindings), opts.ConfigPassthrough != nil)
	}
}
