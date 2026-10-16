// file: internal/server/op_id_aliases_test.go
// version: 1.19.0
// guid: 2a7c5e93-1d4b-4f60-8e2a-b9c3d7f15e48
// last-edited: 2026-10-16

// Guard tests for operation-ID renames.
//
// An op ID is persisted (operations_v2 rows, op_definitions_v2, activity-log
// attrs) and typed by hand (POST /operations/v2 {def_id}, scripts, bookmarks),
// so renaming a def's ID without recording the old one as a FormerID strands
// every one of those: resume drops the row as "unknown def at startup", an
// enqueue 404s, and a timeline filter silently matches nothing. That already
// happened once -- "maintenance.job" was retired on 2026-08-19 with no alias
// (maintenance_job_op.go says so) -- and nothing in the build noticed.
//
// testdata/op_ids.golden is the ledger of every op ID this server has ever
// registered, seeded from HEAD on 2026-09-25 and append-only after that. The
// guard fails when a ledger ID stops resolving, which is exactly "renamed
// without an alias".
package server

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/opscatalog"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
)

const opIDLedgerPath = "testdata/op_ids.golden"

// The retired-ID map moved to opscatalog.Tombstones (05-PR7), where the
// production startup check (opCatalogGate) reads it too. Each entry still
// carries why no FormerIDs alias points the ID somewhere.

// bootServerForOpIDs boots a server the way TestNewServer_RegistersOpsWithEmptyRootDir
// does: a mock store and no plugin dependencies, so the service-registry
// plugins' gated ops are not registered.
func bootServerForOpIDs(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	origCfg := config.AppConfig
	config.AppConfig.RootDir = ""
	t.Cleanup(func() { config.AppConfig = origCfg })

	store := mocks.NewMockStore(t)
	store.EXPECT().SetRootDir(mock.Anything).Return().Maybe()
	allowOpDefinitionUpserts(store)

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	srv := newTestServer(t, store)
	t.Cleanup(func() {
		if srv.fileIOPool != nil {
			srv.fileIOPool.Stop()
		}
	})
	return srv
}

// bootRegisteredOpIDs boots a server with bootServerForOpIDs, registers the
// plugins' static op lists, and returns its registry's canonical IDs and alias
// table.
func bootRegisteredOpIDs(t *testing.T) (*Server, map[string]bool, map[string]string) {
	t.Helper()
	srv := bootServerForOpIDs(t)

	// The sdk plugins register from serviceregistry PostInit and only when
	// their dependencies are wired (a dedup engine, a Deluge client, an enabled
	// iTunes service), none of which this boot has. Their OperationDefs() lists
	// are dependency-free, so register them here: that both enumerates their
	// IDs for the ledger and proves their FormerIDs do not collide with
	// anything the server registered.
	// opscatalog.StaticPluginDefs is the same five lists the startup check
	// resolves ledger IDs against.
	for _, defs := range opscatalog.StaticPluginDefs() {
		for _, d := range defs {
			if got, ok := srv.opRegistry.Def(d.ID); ok && got.ID == d.ID {
				continue // ungated plugin (metafetch) already registered at boot
			}
			if err := srv.opRegistry.RegisterOp(d); err != nil {
				t.Fatalf("registering plugin op %s alongside the server's ops: %v", d.ID, err)
			}
		}
	}

	registered := map[string]bool{}
	for _, d := range srv.opRegistry.ActiveDefs() {
		registered[d.ID] = true
	}
	if len(registered) == 0 {
		t.Fatal("server registered zero operations")
	}
	return srv, registered, srv.opRegistry.Aliases()
}

// readOpIDLedger returns the ledger's IDs, parsed by opscatalog.ParseLedger
// (comments and blank lines skipped), the parser the startup check uses.
func readOpIDLedger(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(opIDLedgerPath)
	if err != nil {
		t.Fatalf("read %s: %v", opIDLedgerPath, err)
	}
	return opscatalog.ParseLedger(b)
}

// TestOpLedgerEmbedMatchesFile: the startup check reads the embedded copy of
// the ledger; this test and every census PR read the file on disk. They are
// the same bytes by construction (go:embed), so a failure here means the
// embed directive points somewhere else.
func TestOpLedgerEmbedMatchesFile(t *testing.T) {
	b, err := os.ReadFile(opIDLedgerPath)
	if err != nil {
		t.Fatalf("read %s: %v", opIDLedgerPath, err)
	}
	if !bytes.Equal(b, embeddedOpLedger) {
		t.Fatalf("embedded op-ID ledger (%d bytes) differs from %s (%d bytes)", len(embeddedOpLedger), opIDLedgerPath, len(b))
	}
	if got, want := opscatalog.ParseLedger(embeddedOpLedger), readOpIDLedger(t); !slices.Equal(got, want) {
		t.Fatalf("embedded ledger parses to %d IDs, the file to %d", len(got), len(want))
	}
}

// TestOpIDs_NoRenameWithoutAlias is the guard the naming audit asked for: every
// op ID the server has ever registered must still resolve, either as a
// registered def or as a FormerID of one.
func TestOpIDs_NoRenameWithoutAlias(t *testing.T) {
	srv, registered, aliases := bootRegisteredOpIDs(t)
	ledger := readOpIDLedger(t)

	inLedger := map[string]bool{}
	for _, id := range ledger {
		inLedger[id] = true
		if _, ok := srv.opRegistry.Def(id); ok {
			if why, retired := opscatalog.Tombstones[id]; retired {
				t.Errorf("op ID %q is listed as retired (%s) but resolves again; drop it from opscatalog.Tombstones", id, why)
			}
			continue
		}
		if _, retired := opscatalog.Tombstones[id]; retired {
			continue
		}
		t.Errorf("op ID %q was registered before and no longer resolves.\n"+
			"  Renamed it? Add %q to the renamed def's FormerIDs -- stored rows, scripts and\n"+
			"  bookmarks still carry the old ID, and without the alias resume drops those rows.\n"+
			"  Deliberately retired it? Point its FormerIDs at the op that replaces it; delete\n"+
			"  the ledger line only if no stored row can still carry it.", id, id)
	}

	var missing []string
	for id := range registered {
		// maintenance_dryrun_default_test.go registers probe jobs from test
		// code; they never exist in a production build, so never in a stored row.
		if strings.Contains(id, ".test-probe-") {
			continue
		}
		if !inLedger[id] {
			missing = append(missing, id)
		}
	}
	for old := range aliases {
		if !inLedger[old] {
			missing = append(missing, old)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d registered op ID(s) are not in %s; append them (the ledger is how a later "+
			"rename of these IDs gets caught):\n%s", len(missing), opIDLedgerPath, strings.Join(missing, "\n"))
	}
}

// TestOpIDs_AliasesNeverShadowRegisteredIDs asserts on the fully booted set what
// RegisterOp enforces one registration at a time: an ID is either a def or an
// alias, never both.
// A retired ID must be one the ledger records: the list excuses a ledger line,
// it never invents one.
func TestOpIDs_RetiredIDsAreLedgerIDs(t *testing.T) {
	inLedger := map[string]bool{}
	for _, id := range readOpIDLedger(t) {
		inLedger[id] = true
	}
	for id := range opscatalog.Tombstones {
		if !inLedger[id] {
			t.Errorf("opscatalog.Tombstones lists %q, which is not in %s", id, opIDLedgerPath)
		}
	}
}

func TestOpIDs_AliasesNeverShadowRegisteredIDs(t *testing.T) {
	_, registered, aliases := bootRegisteredOpIDs(t)
	if len(aliases) == 0 {
		t.Fatal("no aliases registered; the 2026-09-25 renames declare FormerIDs, so boot lost them")
	}
	for old, canon := range aliases {
		if registered[old] {
			t.Errorf("alias %q shadows a registered op of the same ID", old)
		}
		if !registered[canon] {
			t.Errorf("alias %q points at %q, which is not registered", old, canon)
		}
	}
}

// renamedOpIDs are the 2026-09-25 renames (naming audit class 8). Pinned here
// so the aliases cannot be dropped as "unused" without this test noticing.
var renamedOpIDs = map[string]string{
	"maintenance.itunes-regroup":            "itunes.regroup",
	"maintenance.itunes-playlist-import":    "itunes.playlist-import",
	"maintenance.itunes-heal":               "itunes.heal",
	"maintenance.itunes-clone-into-library": "itunes.clone-into-library",
	"library.optimize":                      "maintenance.library-optimize",
	"maintenance.dedup-llm-review":          "dedup.llm-review",
	"maintenance.series-prune":              "dedup.series-prune",
	"maintenance.series-normalize":          "dedup.series-normalize",
	"scheduler.temp-file-cleanup":           "maintenance.temp-file-cleanup",
	"scheduler.trash-cleanup":               "maintenance.trash-cleanup",
	"scheduler.tombstone-cleanup":           "maintenance.tombstone-cleanup",
	"scheduler.db-optimize":                 "maintenance.db-optimize",
	"scheduler.purge-deleted":               "maintenance.purge-deleted",
	"scheduler.metadata-refresh":            "maintenance.metadata-refresh",
	"scheduler.resolve-production-authors":  "maintenance.resolve-production-authors",
	"scheduler.author-split-scan":           "maintenance.author-split-scan",
	"scheduler.cleanup-old-backups":         "maintenance.cleanup-old-backups",
	"maintenance.author-dedup-scan":         "dedup.author-scan",
	"scheduler.dedup-llm-review":            "dedup.llm-review",
	"maintenance.reconcile-scan":            "reconcile.scan",
}

func TestOpIDs_EveryAliasResolves(t *testing.T) {
	srv, _, aliases := bootRegisteredOpIDs(t)
	for old, want := range renamedOpIDs {
		if got := aliases[old]; got != want {
			t.Errorf("alias %q -> %q, want %q", old, got, want)
		}
	}
	for old, canon := range aliases {
		def, ok := srv.opRegistry.Def(old)
		if !ok || def.ID != canon {
			t.Errorf("Def(%q) = (%q, %v), want %q", old, def.ID, ok, canon)
		}
	}
}

// TestOpIDs_CodeNeverNamesAnAlias: aliases exist for input that arrives from
// outside the build (stored rows, HTTP callers, scripts). Code in this repo must
// use the canonical ID, or the deprecation metric can never reach zero and the
// alias can never be removed. Test files are exempt (the alias tests name them
// on purpose), as are FormerIDs declarations and comments.
func TestOpIDs_CodeNeverNamesAnAlias(t *testing.T) {
	_, _, aliases := bootRegisteredOpIDs(t)
	root := filepath.Join("..", "..")
	dirs := []string{"internal", "pkg", "cmd", filepath.Join("web", "src"), "scripts"}
	exts := map[string]bool{".go": true, ".ts": true, ".tsx": true, ".py": true, ".sh": true}

	for _, d := range dirs {
		base := filepath.Join(root, d)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if e.Name() == "node_modules" || e.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			name := e.Name()
			if !exts[filepath.Ext(name)] || strings.HasSuffix(name, "_test.go") ||
				strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for i, line := range strings.Split(string(data), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
					strings.HasPrefix(trimmed, "*") || strings.Contains(line, "FormerIDs") {
					continue
				}
				for old := range aliases {
					if strings.Contains(line, `"`+old+`"`) || strings.Contains(line, `'`+old+`'`) ||
						strings.Contains(line, "`"+old+"`") {
						t.Errorf("%s:%d names former op ID %q; use %q", path, i+1, old, aliases[old])
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
}
