// file: core/module/manifest.go
// version: 1.4.0
// guid: f5448853-c265-4559-bf8c-4cb61f16fa24
// last-edited: 2026-11-19

package module

import (
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// Manifest is everything the kernel validates before any storage is opened,
// and everything that must stay known while the module is disabled (06
// §6.4.2). Later members arrive additively with their subsystems (06
// §6.4.10); a member named in the spec and absent here has not landed.
type Manifest struct {
	ID          ID
	Version     string // semver, e.g. "1.12.0"; reported by GET /api/core/v1/modules
	DisplayName string // human name, e.g. "Audiobooks"

	// MinCoreVersion is the lowest core version this module needs (semver).
	// P1 compares it with version.Version, the hand-maintained constant of
	// the L0 package version (MOD-12), never debug.ReadBuildInfo. Empty = no
	// check.
	MinCoreVersion string

	// DefaultEnabled is the value of core.modules.<id>.enabled when unset.
	// Until config lands (F3) nothing sets the key, so it is the module's
	// enablement.
	DefaultEnabled bool

	// RBAC declarations, types owned by 05-rbac §3.2: permissions in this
	// module's namespace (each with its DefaultGrants into core seed roles
	// "editor"/"viewer" or the module's own roles) and the module's own seed
	// roles ("<ID>.<name>", 05-rbac §3.3). There are no permission-string
	// aliases: stored roles and keys hold native scopes from R2's identity
	// step, and books' gin routes map HEAD's atoms to native scopes in books'
	// own code (05-rbac §6). The kernel feeds them to rbac's Declarer in P5,
	// after rbac.New has its role store (COR2-7); a seed role's stored row is
	// its seed (HEAD's overwrite at boot), and a restricted addition waits in
	// PendingSeed until an admin accepts it with AcceptSeed (05-rbac §4.8).
	// Nothing reads them until F4-08's boot wiring (B3).
	Permissions []rbac.PermissionSpec
	Roles       []rbac.RoleSeed

	// ImportedRoleIDs are the un-namespaced role IDs this module's seed roles
	// may keep as their RoleSeed.Name, because data the module imported names
	// them (books: "fp-worker", HEAD's worker role, whose ID stored user rows
	// name and R2's identity step keeps, like every ID; 05-rbac §3.3, §6).
	// Every other seed role is "<ID>.<name>". P1 refuses an ID listed by two
	// manifests or equal to a core seed role (ProblemImportedRole); the kernel
	// hands the list to rbac's Declarer with Roles (COR5-5). R2, identity flip.
	ImportedRoleIDs []rbac.RoleName

	// UI lists the descriptor IDs the module's client bundle may contribute.
	// The server reports them in /api/core/v1/modules; the shell refuses
	// client entries whose IDs are not listed (G3, 07 §7.5).
	UI UIDescriptor

	// LegacyPrefixes are un-namespaced HTTP subtrees ("/api/v1/", "/public/")
	// or exact paths ("/ping", "/login") the module occupies (09); each
	// becomes a claim, and its first segment is a reserved module ID known in
	// P1 (§6.4.1, FE2-11). A prefix does NOT make its segment NonSPA: that
	// comes only from the mount's LegacyMount.TopSegments (FE3-1, R3-5).
	// httpapi.Router.Build refuses any two owners whose claims overlap
	// (05-http-api §4.9). "/" is not a claim: it is the SPA fallback, owned by
	// at most one LegacyMount with ServesSPA (FE-1).
	LegacyPrefixes []string

	// PublicRoutes names the native routes of this module (httpapi.Route.Name)
	// that may use Perm: rbac.Public; httpapi.Router.Build refuses rbac.Public
	// on any other native route (ErrPublicNotAllowed, 05-http-api §3), so the
	// public set is a reviewed manifest list (COR3-3). hello lists
	// "getGreeting" until core authentication lands (R2's identity flip).
	PublicRoutes []string

	// TaskLedger lists every task kind this module registers ("<ID>.<name>"),
	// current names only: R2's tasks step renames HEAD's stored kinds through
	// books' kind-rename map, so no former kind exists. It is readable while
	// the module is disabled, so 05-tasks classifies the module's persisted
	// runs as held instead of failing boot (5T.3.11, COR-13). A registered
	// kind missing from it is a problem (ProblemTaskLedger).
	TaskLedger []tasks.Kind
	// Storage is the module's storage declaration (05-storage §5S.3.4,
	// §5S.3.5): its keyspace m/<ID>/, its legacy claims and its versioned
	// migrations, Seed steps included (a Seed step and every later step of
	// the owner run on a fresh install too, §6.5.1 P4a, COR5-8, COR6-4). It
	// stays known while the module is disabled, so its claims are validated
	// and its keyspace stays reserved. No core subsystem reads a module's
	// rows (03 R1). The zero value declares nothing; a module that declares
	// anything boots only with core.storage.path set.
	Storage storage.Spec

	// DataMigrations are the module's large, resumable rewrites
	// (05-storage §5S.3.6). They live outside storage.Spec because
	// storage/datamigrate (L4) imports tasks and storage (L1) cannot. In
	// R0–R3 every one is BootBlocking: it runs in process at P4b through
	// tasks.ForEach with persisted shard cursors, with its dry run
	// (05-storage). The Online mode (a run of the kernel-synthesised module
	// kind "<ID>.datamigrate.<name>" after Run step 5, with its read adapters,
	// Writes/RewritesInPlace and conditional-write steps, COR4-5, COR4-6) has
	// no consumer in R0–R3 and is deferred-build: 05-storage §5S.3.14 lists its
	// members, and it lands additively with its first consumer (SIMP5-7).
	//
	// Each migration's Owner is the module's own ID: P1 refuses a module that
	// migrates another owner's rows (ProblemMigrationOrder).
	DataMigrations []datamigrate.DataMigration

	// Cutover returns the module's R2 cutover steps (06 §6.4.2; books: 03
	// §3.8 M2-M6): BootBlocking data migrations that decode HEAD's rows from
	// the module's own Legacy range, stage them through the importers in imp
	// into the step's batch, and delete them, one batch per chunk. The
	// kernel calls it once per boot, while it builds the migration list
	// (P2), with importers it binds to their stores at the start of P4b; the
	// steps join the module's DataMigrations in the DAG and run in 03 §3.8's
	// order, after the versioned steps (M1) and the level step (M7) and
	// before the store rewrite (M8), whether the module is enabled or not
	// (A5). nil for an app with no pre-core rows (hello, TV, Movies). A step
	// names only the importers of the families it moves.
	//
	// The func must be pure: the offline commands and Describe call it too,
	// with importers that refuse every call (ErrImporterNotBound), and a
	// step it returns must do no work until it runs.
	Cutover func(imp Importers) []datamigrate.DataMigration

	// Rewrite declares M8, the store rewrite (05-storage §5S.3.13 "M8: the
	// rewrite"; 03 §3.8), which runs last in the R2 cutover once, over
	// everything the final R2 build migrated. At most one manifest of an App
	// declares it (its ID is fixed for all of R2, D15); the kernel appends
	// it after every registered migration and wires its swapper. nil = the
	// module declares none. The spec's 06 §6.4.2 has no member for it (its
	// F3 row lists M8 among the manifest's cutover members without naming
	// one); this name is coined by F3-07.
	Rewrite *storage.RewriteStep

	// Settings are the typed config sections the module owns, wrapped with
	// Settings(section) (06 §6.4.2). Paths must be "modules.<ID>" or below
	// (ProblemSettingsNamespace). The kernel registers each of an enabled
	// module's sections with config before the store loads (06 §6.5.1 P3,
	// 05-config §1.3.2); a disabled module's are not registered and its
	// stored values are kept, unvalidated. A module reads its own sections
	// through Setting in Register.
	Settings []SettingsSection

	// ResetHook is the module's part of a factory reset (05-storage
	// §5S.3.15 "The app's reset hook"): Plan runs when a reset is staged,
	// Run on the reset boot at 06 §6.5.2 Run step 2, after the fresh
	// install and before any listener, until it calls ResetRun.Done. At most
	// one module of an App declares one (P1, ProblemResetHook); nil
	// declares none, and a reset then deletes no file and refuses
	// DeleteLibraryFiles. A ResetStore reset runs no hook.
	ResetHook *storage.ResetHook

	// AppConfig makes the module the App's own configuration surface: only a
	// module that declares it receives Deps.Config (the layered store's reads,
	// its one Apply and an OnCommit hook) and, through it, the secrets the
	// declaration names. nil = the module gets neither (Deps.Config is nil).
	// ABO's books module declares it for the Facade that projects HEAD's flat
	// Config over core keys; a module that only reads its own sections has no
	// use for it, so the access is a reviewed manifest line, never ambient.
	AppConfig *AppConfigSpec

	// AuthRefresh declares that the module re-derives the principal stage 12
	// bound to a request (ABO: books' legacy-auth bridge, which must answer
	// again after a password reset or a role change), and so receives
	// Deps.AuthRefresh. false, the default, leaves Deps.AuthRefresh nil: the
	// handle is never ambient, a reviewed manifest line like AppConfig. The
	// declaration admits the module to auth.Service.Refresh and nothing wider;
	// Refresh itself refuses any principal core did not build or whose identity
	// fields its holder edited.
	AuthRefresh bool
}

// AppConfigSpec is a module's declaration of App-wide config access.
type AppConfigSpec struct {
	// Secrets says which secret names Deps.Config.Secret, PutSecret and
	// DeleteSecret may touch: names outside it are refused with
	// secrets.ErrOutOfScope, so the module reaches exactly the secrets its
	// own table maps (for books, derived from the rename table), not the
	// whole store. nil = no secret access.
	Secrets func(name secrets.Name) bool
}

// UIDescriptor lists the IDs of client-side contributions (07 §7.4.2).
// Every slot a module can fill in the shell has a list here, so G3 can drop
// an undeclared item of any kind (FE-16).
type UIDescriptor struct {
	Pages          []string // route IDs, e.g. "library", "book"
	Nav            []string // nav entry IDs
	SettingsPanels []string // settings panel IDs
	Widgets        []string // optional home/system/diagnostics widgets
	Banners        []string // layout banners rendered by the shell's main layout
	TopBarItems    []string // items in the shell's top bar
	UserPage       []string // tab and row-action IDs on the shell Users page
	ActivityPanes  []string // panes on the shell Activity page (05-logging §5)
	SetupSteps     []string // first-run wizard steps (with the module's isRequired predicate)

	// There is no redirect list (D13, D10): a module declares no former page
	// URLs (07 §7.19 step 5).
}
