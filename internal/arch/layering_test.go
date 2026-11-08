// file: internal/arch/layering_test.go
// version: 1.5.6
// guid: 752fb2b9-4a13-4baf-9fb2-0fd2ac6f3ad5
// last-edited: 2026-11-08

// Package arch holds architecture guard tests. It has no non-test code.
//
// TestLayering enforces the package layering from
// docs/architecture/layering.md: a package may import only packages whose
// layer number is at most its own. Two maps drive it:
//
//   - layerOf assigns every package in the module a layer. A new package
//     fails the test until it is added here, so it must choose a layer.
//   - allowed lists the known wrong-way edges, each with a reason. It is a
//     one-way ratchet: the test fails when a listed edge no longer exists
//     (delete the entry in the PR that fixes the edge) and when a listed edge
//     is actually legal (the map cannot become a general whitelist).
//
// The engine is core's archtest.Layering (03 §3.5.1 item 3): this file holds
// only ABO's data (layerOf, allowed, the named table, the overrides, the
// directory families and the mapping of core imports onto ABO's scale).
package arch

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

const modulePath = "github.com/falkcorp/audiobook-organizer"

// corePrefix is the import prefix of the core module (03 §3.2).
const corePrefix = "github.com/falkcorp/mediacore/"

// The loader-health floor is a ratio of layerOf (03 §3.5.1 item 1), so moving
// packages out to core cannot trip it while a broken loader, which returns
// almost nothing, still does.
const loaderFloorRatio = 0.8

// minModulePackages is the same floor as a package count, for the other
// loaders of this package (the hash-cost, opstest and resume-restart guards).
// It was the literal 150 until the core module arrived; a ratio of layerOf
// follows packages that move to core instead of tripping on them.
var minModulePackages = int(math.Ceil(loaderFloorRatio * float64(len(layerOf))))

// inModuleControl is the in-module positive control: the root main package
// imports cmd (main.go). The CLI stays in ABO (13 §13.5.1), so the edge exists
// before, during and after the extraction. A PR that removes it names a
// replacement in-module edge in the same change.
var inModuleControl = edge{".", "cmd"}

// classifierControl is the classifier positive control (03 §3.5.1 item 1): a
// root package that imports a core path, which the guard must classify. At
// R1's first core PR the one root import of core is this guard's own: this
// package's test files import mediacore/archtest (a test import, so it
// changes no package's layer). A PR that removes the edge names its
// replacement in the same change.
var classifierControl = edge{"internal/arch", corePrefix + "archtest"}

// moduleAppImporters are the only packages that may import mediacore/module
// or mediacore/app in non-test code: the main package, cmd, and ABO's
// composition root internal/books (03 §3.5.1 item 1). Below layer 6 a package
// receives what it needs as funcs or interfaces that internal/books wires.
var moduleAppImporters = []string{".", "cmd", "internal/books"}

// edge is an import: [0] imports [1], both module-relative ("." is the
// module-root main package), or a full path for a core import.
type edge = archtest.Edge

// layerOf maps every module package, keyed by import path relative to the
// module, to its layer. Layers: 0 leaf, 1 config, 2 storage, 3 domain,
// 4 jobs, 5 transport, 6 entry. The trailing comment records why.
//
// Classification rule for packages the table does not name: directory family
// (internal/plugins/*, internal/maintenance/jobs = 4; internal/server/**,
// internal/realtime, internal/syncapi = 5; cmd/*, tools/cmd/* = 6), else
// max(layer of the package's in-module imports) floored at 3, else 0 when it
// imports no module package. matcher and fingerprint are the table's domain
// packages that internal/database imports; each imports only layer-0
// packages (go list evidence: matcher -> personname; fingerprint ->
// audioutil), so both are layer 0. chaptershape, metastate and
// syncapi/progress import nothing from the module, so they compute to 0.
var layerOf = map[string]int{
	// Layer 0: leaf
	"internal/acoustid":              0, // computed: imports no module package
	"internal/applycap":              0, // computed: imports no module package
	"internal/arch":                  0, // computed: imports no module package
	"internal/audioext":              0, // table: leaf
	"internal/audioutil":             0, // computed: imports no module package
	"internal/authorname":            0, // table: leaf
	"internal/cache":                 0, // table: leaf
	"internal/cfaccesspolicy":        0, // computed: imports no module package
	"internal/cfservicetoken":        0, // computed: imports no module package
	"internal/chaptershape":          0, // computed: imports no module package
	"internal/covertext":             0, // computed: imports no module package
	"internal/database/aggtest":      0, // computed: imports no module package
	"internal/diagnosis":             0, // computed: imports no module package
	"internal/errhandling":           0, // computed: imports no module package
	"internal/filehash":              0, // computed: imports no module package
	"internal/fingerprint":           0, // reclassified from domain: imports only internal/audioutil
	"internal/fingerprint/workerapi": 0, // computed: imports no module package
	"internal/flight":                0, // computed: imports no module package
	"internal/franchise":             0, // computed: imports no module package
	"internal/httputil":              0, // table: leaf
	"internal/keyedmutex":            0, // computed: imports no module package
	"internal/linkintegrity":         0, // computed: imports no module package
	"internal/logger":                0, // table: leaf
	"internal/logger/logtest":        0, // computed: imports no module package
	"internal/logger/mocks":          0, // computed: imports no module package
	"internal/mail":                  0, // computed: imports no module package
	"internal/masterlist":            0, // computed: imports no module package
	"internal/matcher":               0, // reclassified from domain: imports only internal/personname
	"internal/metadata/dailyquota":   0, // computed: imports no module package
	"internal/metastate":             0, // computed: imports no module package
	"internal/metrics":               0, // table: leaf
	"internal/models":                0, // table: leaf
	"internal/mtls":                  0, // computed: imports no module package
	"internal/walkresume":            0, // computed: imports no module package
	"internal/operations/freshness":  0, // computed: imports no module package
	"internal/operations/opmode":     0, // computed: imports no module package
	"internal/operations/state":      0, // computed: imports no module package
	"internal/pathutil":              0, // table: leaf
	"internal/personname":            0, // table: leaf
	"internal/policy":                0, // computed: imports no module package
	"internal/querygrammar":          0, // table: leaf
	"internal/scanlock":              0, // computed: imports no module package
	"internal/security/safehttp":     0, // computed: imports no module package
	"internal/seqnum":                0, // table: leaf
	"internal/serverdecode":          0, // computed: imports no module package
	"internal/serviceregistry":       0, // computed: imports no module package
	"internal/startup":               0, // computed: imports no module package
	"internal/syncapi/conformance":   0, // computed: imports no module package
	"internal/syncapi/progress":      0, // computed: imports no module package
	"internal/telemetry":             0, // computed: imports no module package
	"internal/telemetrypolicy":       0, // computed: imports no module package (test-only; was internal/telemetry/contract)
	"internal/testutil/durtest":      0, // computed: imports no module package
	"internal/titleutil":             0, // table: leaf
	"internal/tools":                 0, // computed: imports no module package
	"internal/trackseq":              0, // computed: imports no module package
	"internal/util":                  0, // table: leaf

	// Layer 1: config
	"internal/config": 1, // table: config

	// Layer 2: storage
	"internal/database":    2, // table: storage
	"internal/openlibrary": 2, // table: storage
	"internal/search":      2, // table: storage

	// Layer 3: domain
	"internal/activity":                 3, // computed: max of imports=3, floored at 3
	"internal/ai":                       3, // computed: max of imports=3, floored at 3
	"internal/ai/aijobs":                3, // computed: max of imports=2, floored at 3
	"internal/ai/mocks":                 3, // computed: max of imports=3, floored at 3
	"internal/ai/resultjournal":         3, // computed: max of imports=2, floored at 3
	"internal/aidispatch":               3, // computed: max of imports=0, floored at 3
	"internal/aiscan":                   3, // computed: max of imports=3, floored at 3
	"internal/appdirs":                  3, // computed: max of imports=3, floored at 3
	"internal/applygate":                3, // computed: max of imports=3, floored at 3
	"internal/audio":                    3, // computed: max of imports=3, floored at 3
	"internal/audiobooks":               3, // table: domain
	"internal/auth":                     3, // computed: max of imports=2, floored at 3
	"internal/authorcredit":             3, // computed: max of imports=3, floored at 3
	"internal/authority":                3, // computed: max of imports=2, floored at 3
	"internal/authorjunk":               3, // computed: max of imports=0, floored at 3
	"internal/authormerge":              3, // computed: max of imports=3 (undo), floored at 3
	"internal/backup":                   3, // computed: max of imports=0, floored at 3
	"internal/batch":                    3, // computed: max of imports=3, floored at 3
	"internal/boilerplate":              3, // computed: max of imports=0, floored at 3
	"internal/bookfiles":                3, // computed: max of imports=2, floored at 3
	"internal/bookfileaudio":            3, // computed: max of imports=3, floored at 3
	"internal/covers":                   3, // computed: max of imports=0, floored at 3
	"internal/database/dbtest":          3, // computed: max of imports=2, floored at 3
	"internal/database/mocks":           3, // computed: max of imports=2, floored at 3
	"internal/decisions":                3, // computed: max of imports=2, floored at 3
	"internal/dedup":                    3, // table: domain
	"internal/dedup/dataset":            3, // computed: max of imports=3, floored at 3
	"internal/dedup/unified":            3, // computed: max of imports=0, floored at 3
	"internal/deluge":                   3, // computed: max of imports=3, floored at 3
	"internal/diagnostics":              3, // computed: max of imports=3, floored at 3
	"internal/fileops":                  3, // computed: max of imports=2, floored at 3
	"internal/fingerprint/workerclient": 3, // computed: max of imports=0, floored at 3
	"internal/foldercover":              3, // computed: max of imports=3, floored at 3
	"internal/foldernames":              3, // computed: max of imports=3, floored at 3
	"internal/identification":           3, // computed: max of imports=2 (database), floored at 3
	"internal/itunes":                   3, // table: domain
	"internal/itunesguard":              3, // computed: max of imports=3, floored at 3
	"internal/mail/mailtest":            3, // computed: max of imports=0, floored at 3
	"internal/mediainfo":                3, // computed: max of imports=0, floored at 3
	"internal/merge":                    3, // table: domain
	"internal/metadata":                 3, // table: domain
	"internal/metadata/mocks":           3, // computed: max of imports=3, floored at 3
	"internal/metadata/providerhttp":    3, // computed: max of imports=0, floored at 3
	"internal/metafetch":                3, // table: domain
	"internal/metrics/metricstest":      3, // computed: max of imports=0, floored at 3
	"internal/oauth":                    3, // computed: max of imports=2, floored at 3
	"internal/operations":               3, // computed: max of imports=0, floored at 3
	"internal/operations/childop":       3, // computed: max of imports=2, floored at 3
	"internal/operations/opswriter":     3, // computed: max of imports=3 (undo, pkg/ops), floored at 3
	"internal/opsmetrics":               3, // computed: max of imports=0, floored at 3
	"internal/organizer":                3, // table: domain
	"internal/playlist":                 3, // computed: max of imports=2, floored at 3
	"internal/plugin":                   3, // computed: max of imports=0, floored at 3
	"internal/quarantine":               3, // computed: max of imports=3, floored at 3
	"internal/readstatus":               3, // computed: max of imports=2, floored at 3
	"internal/reconcile":                3, // table: domain
	"internal/remux":                    3, // computed: max of imports=3, floored at 3
	"internal/repairs":                  3, // table: domain
	"internal/scanner":                  3, // table: domain
	"internal/searchcache":              3, // computed: max of imports=0, floored at 3
	"internal/sweep":                    3, // computed: max of imports=3, floored at 3
	"internal/sysinfo":                  3, // computed: max of imports=2, floored at 3
	"internal/tagger":                   3, // computed: max of imports=3, floored at 3
	"internal/testutil/rapidgen":        3, // computed: max of imports=2, floored at 3
	"internal/transcode":                3, // computed: max of imports=3, floored at 3
	"internal/transcribe":               3, // computed: max of imports=3, floored at 3
	"internal/undo":                     3, // table: domain
	"internal/usermerge":                3, // table: domain
	"internal/updater":                  3, // computed: max of imports=1, floored at 3
	"internal/versionprimary":           3, // table: domain
	"internal/versionprimary/vptest":    3, // computed: max of imports=2, floored at 3
	"internal/versions":                 3, // table: domain
	"internal/watcher":                  3, // computed: max of imports=3, floored at 3
	"internal/wirecontract":             0, // computed: imports no module package (test-only package)
	"internal/work":                     3, // computed: max of imports=2, floored at 3
	"internal/writeback":                3, // table: domain
	"pkg/ops":                           3, // computed: max of imports=3 (auth, undo), floored at 3

	// Layer 4: jobs
	"internal/authority/authoritybuild": 4, // computed: max of imports=4, floored at 3
	"internal/catalog":                  4, // computed: max of imports=4, floored at 3
	"internal/importer":                 4, // computed: max of imports=4, floored at 3
	"internal/maintenance":              4, // computed: max of imports=4, floored at 3
	"internal/maintenance/jobs":         4, // family: jobs
	"internal/metabatch":                4, // computed: max of imports=4, floored at 3
	"internal/operations/registry":      4, // table: jobs
	"internal/opscatalog":               4, // computed: max of imports=4 (registry, internal/plugins/*); the one list of op bundles (05-PR7)
	"internal/plugins":                  4, // family: internal/plugins/* is jobs
	"internal/plugins/acoustid":         4, // family: internal/plugins/* is jobs
	"internal/plugins/dedup":            4, // family: internal/plugins/* is jobs
	"internal/plugins/deluge":           4, // family: internal/plugins/* is jobs
	"internal/plugins/itunes":           4, // family: internal/plugins/* is jobs
	"internal/plugins/maintenance":      4, // family: internal/plugins/* is jobs
	"internal/plugins/metafetch":        4, // family: internal/plugins/* is jobs
	"internal/plugins/webhook":          4, // family: internal/plugins/* is jobs
	"internal/scheduler":                4, // table: jobs
	"internal/testutil":                 4, // computed: max of imports=4, floored at 3
	"pkg/ops/opstest":                   4, // computed: max of imports=4 (registry); a test harness, imported by tests only
	"pkg/plugin/sdk":                    4, // computed: max of imports=4, floored at 3

	// Layer 5: transport
	"internal/itunes/service":                   5, // computed: max of imports=5, floored at 3
	"internal/realtime":                         5, // table: transport
	"internal/server":                           5, // family: internal/server/** is transport
	"internal/server/absauth":                   5, // family: internal/server/** is transport
	"internal/server/handlers":                  5, // family: internal/server/** is transport
	"internal/server/handlers/abs":              5, // family: internal/server/** is transport
	"internal/server/handlers/admindebug":       5, // family: internal/server/** is transport
	"internal/server/handlers/aibackends":       5, // family: internal/server/** is transport
	"internal/server/handlers/audiobooks":       5, // family: internal/server/** is transport
	"internal/server/handlers/audiobooks/mocks": 5, // family: internal/server/** is transport
	"internal/server/handlers/dedup":            5, // family: internal/server/** is transport
	"internal/server/handlers/dedup/mocks":      5, // family: internal/server/** is transport
	"internal/server/handlers/duplicates":       5, // family: internal/server/** is transport
	"internal/server/handlers/duplicates/mocks": 5, // family: internal/server/** is transport
	"internal/server/handlers/entities":         5, // family: internal/server/** is transport
	"internal/server/handlers/entities/mocks":   5, // family: internal/server/** is transport
	"internal/server/handlers/fpworker":         5, // family: internal/server/** is transport
	"internal/server/handlers/metadata":         5, // family: internal/server/** is transport
	"internal/server/handlers/metadata/mocks":   5, // family: internal/server/** is transport
	"internal/server/handlers/mocks":            5, // family: internal/server/** is transport
	"internal/server/handlers/opaccess":         5, // family: internal/server/** is transport
	"internal/server/handlers/operations":       5, // family: internal/server/** is transport
	"internal/server/handlers/operations/mocks": 5, // family: internal/server/** is transport
	"internal/server/handlers/opsv3":            5, // family: internal/server/** is transport
	"internal/server/handlers/repairs":          5, // family: internal/server/** is transport
	"internal/server/handlers/review":           5, // family: internal/server/** is transport
	"internal/server/handlers/system":           5, // family: internal/server/** is transport
	"internal/server/handlers/system/mocks":     5, // family: internal/server/** is transport
	"internal/server/handlers/tools":            5, // family: internal/server/** is transport
	"internal/server/middleware":                5, // family: internal/server/** is transport

	// Layer 6: entry
	".":                                  6, // entry: module root main package
	"cmd":                                6, // family: entry
	"cmd/itl-audit-encoding":             6, // family: entry
	"cmd/itl-check":                      6, // family: entry
	"cmd/itl-diff":                       6, // family: entry
	"cmd/mtls-bridge":                    6, // family: entry
	"cmd/pebble-inject-skip":             6, // family: entry
	"cmd/pid-census":                     6, // family: entry
	"cmd/verify-suite":                   6, // family: entry
	"internal/books":                     6, // entry: ABO's composition root, imported only by . and cmd (03 §3.5.1 item 1)
	"tools/cmd/dedup-dataset-audit":      6, // family: entry
	"tools/cmd/itunes-group-preview":     6, // family: entry
	"tools/cmd/merge-split-books":        6, // family: entry
	"tools/cmd/oplint":                   6, // family: entry
	"tools/cmd/opsgen":                   6, // family: entry
	"tools/cmd/orphan-nonprimary-census": 6, // family: entry
	"tools/cmd/reconcile-book-counts":    6, // family: entry
	"tools/cmd/reconcile-paths":          6, // family: entry
	"tools/cmd/sdkguard":                 6, // family: entry
}

// allowed is the shrink-only list of known wrong-way edges (importer,
// imported) with a one-line reason. See the package comment for the ratchet.
var allowed = map[edge]string{
	{"internal/config", "internal/aidispatch"}:                  "config reaches a package that floors at domain; 07-M1 makes config a leaf",
	{"internal/config", "internal/auth"}:                        "config imports auth (which imports database); 07-M1",
	{"internal/config", "internal/backup"}:                      "config reaches a package that floors at domain; 07-M1",
	{"internal/config", "internal/database"}:                    "config reads the store directly; 07-M1",
	{"internal/config", "internal/dedup/unified"}:               "config reaches a package that floors at domain; 07-M1",
	{"internal/dedup", "internal/operations/registry"}:          "domain package registers operations itself; move registration into plugins/dedup",
	{"internal/metadata", "internal/operations/registry"}:       "provider clients import the operations registry; 07-M4",
	{"internal/plugins/itunes", "internal/itunes/service"}:      "itunes/service imports realtime (transport), so it computes to layer 5",
	{"internal/plugins/maintenance", "internal/itunes/service"}: "itunes/service imports realtime (transport), so it computes to layer 5",
	{"internal/reconcile", "internal/operations/registry"}:      "domain package registers operations itself",
	{"internal/reconcile", "pkg/plugin/sdk"}:                    "pkg/plugin/sdk imports the operations registry, so it computes to layer 4",
	{"internal/repairs", "internal/operations/registry"}:        "domain package imports the operations registry",
}

// repoRoot walks up from the working directory to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found in any parent directory")
		}
		dir = parent
	}
}

// rel converts an import path to its module-relative key, or reports false
// when the path is outside the module.
func rel(importPath string) (string, bool) {
	switch {
	case importPath == modulePath:
		return ".", true
	case strings.HasPrefix(importPath, modulePath+"/"):
		return strings.TrimPrefix(importPath, modulePath+"/"), true
	}
	return "", false
}

// aboSpec is ABO's layering rule as an archtest.LayerSpec.
func aboSpec(root string) archtest.LayerSpec {
	return archtest.LayerSpec{
		ModulePath:     modulePath,
		Dir:            root,
		LayerOf:        layerOf,
		Override:       layerOverride,
		Named:          namedLayer,
		Family:         familyLayer,
		ImportFloor:    3,
		Allowed:        allowed,
		ForeignPrefix:  corePrefix,
		Foreign:        coreLayerOnABOScale,
		Restricted:     map[string][]string{corePrefix + "module": moduleAppImporters, corePrefix + "app": moduleAppImporters},
		FloorRatio:     loaderFloorRatio,
		Control:        inModuleControl,
		ForeignControl: classifierControl,
		// The default tag set: the graph layerOf and allowed were built
		// against. The tag-union tools are TestCoreImportsOnlyCore and
		// TestCoreHoldsNoAppKeyNames (03 §3.5.1 item 4).
		TagSets: archtest.DefaultTagSet(),
	}
}

func TestLayering(t *testing.T) {
	archtest.Layering(t, aboSpec(repoRoot(t)))
}

// coreABOLayer maps core packages onto ABO's layer scale (0 leaf, 1 config,
// 2 storage, 3 domain, 4 jobs, 5 transport, 6 entry) by their 03 R4 layer, so
// core imports keep the domain/transport/entry split instead of hiding
// wrong-way edges (03 §3.5.1 item 1): core L0-L5 -> -1 (platform, legal from
// every ABO layer); L6 (httpapi, realtime, arrfacade, ui) -> 5; L7-L9 (module,
// kernel, app, examples/*) -> 6. Packages with no R4 row are mapped
// explicitly: coretest, archtest, the <pkg>/<pkg>test suites and storage/* ->
// -1; shared/... -> 3. A sub-package takes its nearest listed ancestor's
// mapping; core/internal/... is unreachable from ABO (Go's internal rule).
var coreABOLayer = map[string]int{
	// R4 L0-L5
	"entity": -1, "lifecycle": -1, "version": -1, "buildid": -1, "log": -1, "metrics": -1,
	"storage": -1, "secrets": -1, "config": -1,
	"events": -1, "outbox": -1, "tasks": -1, "library": -1, "auth": -1, "rbac": -1, "audit": -1,
	"integrations": -1, "backup": -1, "updater": -1, "health": -1, "acquire": -1,
	// test-support (no R4 row)
	"coretest": -1, "archtest": -1,
	// shared/... (domain)
	"shared": 3,
	// R4 L6
	"httpapi": 5, "realtime": 5, "arrfacade": 5, "ui": 5,
	// R4 L7-L9
	"module": 6, "kernel": 6, "app": 6, "examples": 6,
}

// coreLayerOnABOScale is the archtest Foreign mapping over coreABOLayer.
func coreLayerOnABOScale(importPath string) (int, bool) {
	rel, ok := strings.CutPrefix(importPath, corePrefix)
	if !ok || rel == "" {
		return 0, false
	}
	// <pkg>/<pkg>test conformance suites are test-support.
	if i := strings.LastIndex(rel, "/"); i > 0 {
		parent := rel[:i]
		if rel[i+1:] == parent[strings.LastIndex(parent, "/")+1:]+"test" {
			return -1, true
		}
	}
	for p := rel; ; {
		if l, ok := coreABOLayer[p]; ok {
			return l, true
		}
		i := strings.LastIndex(p, "/")
		if i < 0 {
			return 0, false
		}
		p = p[:i]
	}
}

// TestLayeringModuleAppImporters checks, on a synthetic graph through the
// engine, that only ., cmd and internal/books may import mediacore/module or
// mediacore/app, and that core imports are classified onto ABO's scale.
func TestLayeringModuleAppImporters(t *testing.T) {
	spec := aboSpec("")
	spec.LayerOf = map[string]int{".": 6, "cmd": 6, "internal/books": 6, "internal/plugins/x": 4, "internal/util": 0}
	spec.Named = map[string]int{".": 6, "internal/util": 0}
	spec.Override = nil
	spec.Allowed = nil
	spec.FloorRatio = 1
	spec.ForeignControl = edge{"internal/util", corePrefix + "storage/keys"}
	g := archtest.Graph{
		Imports: map[string][]string{".": {"cmd"}, "cmd": {"internal/books"}, "internal/books": {"internal/plugins/x"}, "internal/plugins/x": {}, "internal/util": {}},
		Foreign: map[string][]string{
			".":                  {corePrefix + "app"},
			"cmd":                {corePrefix + "app"},
			"internal/books":     {corePrefix + "module"},
			"internal/plugins/x": {corePrefix + "module"},
			"internal/util":      {corePrefix + "storage/keys"},
		},
	}
	problems := archtest.CheckGraph(spec, g)
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "internal/plugins/x imports "+corePrefix+"module; only ., cmd, internal/books may import it") {
		t.Errorf("a layer-4 package importing mediacore/module was not reported:\n%s", joined)
	}
	for _, ok := range []string{". imports", "cmd imports", "internal/books imports"} {
		if strings.Contains(joined, ok+" "+corePrefix) {
			t.Errorf("an allowed importer of module/app was reported (%s):\n%s", ok, joined)
		}
	}
	if !strings.Contains(joined, "internal/plugins/x (layer 4) imports "+corePrefix+"module (mapped layer 6)") {
		t.Errorf("a wrong-way core import was not checked against the importer's layer:\n%s", joined)
	}
	if strings.Contains(joined, "internal/util") {
		t.Errorf("a core L0 import (mapped -1) from a leaf was reported:\n%s", joined)
	}

	// The mapping itself: R4 rows, sub-packages, test-support and shared.
	for path, want := range map[string]int{
		"storage/keys": -1, "auth/oidc": -1, "tasks/taskstest": -1, "coretest/fakes": -1,
		"httpapi": 5, "module": 6, "examples/hello": 6, "shared/x": 3,
	} {
		if got, ok := coreLayerOnABOScale(corePrefix + path); !ok || got != want {
			t.Errorf("coreLayerOnABOScale(%s) = %d, %v; want %d", path, got, ok, want)
		}
	}
	if _, ok := coreLayerOnABOScale(corePrefix + "nosuch"); ok {
		t.Error("an unknown core package was mapped; it must fail as unmapped")
	}
}

// namedLayer is the layer table from docs/architecture/layering.md: the
// packages that are assigned a layer by name.
var namedLayer = map[string]int{
	// Layer 0 leaf.
	"internal/util": 0, "internal/pathutil": 0, "internal/personname": 0, "internal/titleutil": 0,
	"internal/authorname": 0, "internal/audioext": 0, "internal/httputil": 0, "internal/logger": 0,
	"internal/metrics": 0, "internal/cache": 0, "internal/models": 0,
	"internal/seqnum": 0, "internal/querygrammar": 0,
	// Layer 1 config.
	"internal/config": 1,
	// Layer 2 storage.
	"internal/database": 2, "internal/openlibrary": 2, "internal/search": 2,
	// Layer 3 domain.
	"internal/merge": 3, "internal/versionprimary": 3, "internal/versions": 3, "internal/dedup": 3,
	"internal/matcher": 3, "internal/fingerprint": 3, "internal/metadata": 3, "internal/organizer": 3,
	"internal/scanner": 3, "internal/itunes": 3, "internal/audiobooks": 3, "internal/metafetch": 3,
	"internal/reconcile": 3, "internal/repairs": 3, "internal/undo": 3, "internal/usermerge": 3, "internal/writeback": 3,
	// Layer 4 jobs.
	"internal/operations/registry": 4, "internal/scheduler": 4, "internal/maintenance/jobs": 4,
	// Layer 5 transport.
	"internal/realtime": 5, "internal/syncapi": 5,
	// Layer 6 entry: the module-root main package.
	".": 6,
}

// layerOverride lists the only deliberate departures from the table, each
// with go list evidence. An override applies before namedLayer.
var layerOverride = map[string]int{
	// matcher imports only internal/personname (layer 0).
	"internal/matcher": 0,
	// fingerprint imports only internal/audioutil (no module imports, layer 0).
	"internal/fingerprint": 0,
}

// familyLayer returns the layer of a directory family, or false.
func familyLayer(pkg string) (int, bool) {
	switch {
	case pkg == "internal/plugins" || strings.HasPrefix(pkg, "internal/plugins/"):
		return 4, true
	case pkg == "internal/server" || strings.HasPrefix(pkg, "internal/server/"):
		return 5, true
	case pkg == "cmd" || strings.HasPrefix(pkg, "cmd/") || strings.HasPrefix(pkg, "tools/cmd/"):
		return 6, true
	}
	return 0, false
}
