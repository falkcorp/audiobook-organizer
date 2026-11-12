// file: core/module/manifest.go
// version: 1.2.0
// guid: f5448853-c265-4559-bf8c-4cb61f16fa24
// last-edited: 2026-11-11

package module

import (
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// Manifest is everything the kernel validates before any storage is opened,
// and everything that must stay known while the module is disabled (06
// §6.4.2). These are the R1 fields; Settings arrives with config (06
// §6.4.10).
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

	// ResetHook is the module's part of a factory reset (05-storage
	// §5S.3.15 "The app's reset hook"): Plan runs when a reset is staged,
	// Run on the reset boot at 06 §6.5.2 Run step 2, after the fresh
	// install and before any listener, until it calls ResetRun.Done. At most
	// one module of an App declares one (P1, ProblemResetHook); nil
	// declares none, and a reset then deletes no file and refuses
	// DeleteLibraryFiles. A ResetStore reset runs no hook.
	ResetHook *storage.ResetHook
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
