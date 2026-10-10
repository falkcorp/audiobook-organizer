// file: internal/arch/layering_test.go
// version: 1.1.3
// guid: 752fb2b9-4a13-4baf-9fb2-0fd2ac6f3ad5
// last-edited: 2026-10-10

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
package arch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/falkcorp/audiobook-organizer"

// minModulePackages and the positive-control edge below guard the loader: a
// graph that is empty or missing the best-known edge means the loader is
// broken, and an empty graph would pass every rule vacuously.
const minModulePackages = 150

var positiveControlEdge = edge{"internal/server", "internal/database"}

// edge is an import: [0] imports [1], both module-relative ("." is the
// module-root main package).
type edge = [2]string

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
	"internal/acoustid":                0, // computed: imports no module package
	"internal/applycap":                0, // computed: imports no module package
	"internal/arch":                    0, // computed: imports no module package
	"internal/audioext":                0, // table: leaf
	"internal/audioutil":               0, // computed: imports no module package
	"internal/authorname":              0, // table: leaf
	"internal/cache":                   0, // table: leaf
	"internal/chaptershape":            0, // computed: imports no module package
	"internal/covertext":               0, // computed: imports no module package
	"internal/database/aggtest":        0, // computed: imports no module package
	"internal/diagnosis":               0, // computed: imports no module package
	"internal/diskstats":               0, // computed: imports no module package
	"internal/errhandling":             0, // computed: imports no module package
	"internal/filehash":                0, // computed: imports no module package
	"internal/fingerprint":             0, // reclassified from domain: imports only internal/audioutil
	"internal/fingerprint/workerapi":   0, // computed: imports no module package
	"internal/flight":                  0, // computed: imports no module package
	"internal/franchise":               0, // computed: imports no module package
	"internal/httputil":                0, // table: leaf
	"internal/lifecycle":               0, // computed: imports no module package
	"internal/linkintegrity":           0, // computed: imports no module package
	"internal/logger":                  0, // table: leaf
	"internal/logger/logtest":          0, // computed: imports no module package
	"internal/logger/mocks":            0, // computed: imports no module package
	"internal/logging":                 0, // table: leaf
	"internal/matcher":                 0, // reclassified from domain: imports only internal/personname
	"internal/metadata/dailyquota":     0, // computed: imports no module package
	"internal/metastate":               0, // computed: imports no module package
	"internal/metrics":                 0, // table: leaf
	"internal/models":                  0, // table: leaf
	"internal/mtls":                    0, // computed: imports no module package
	"internal/operations/freshness":    0, // computed: imports no module package
	"internal/operations/opmode":       0, // computed: imports no module package
	"internal/operations/state":        0, // computed: imports no module package
	"internal/pathutil":                0, // table: leaf
	"internal/personname":              0, // table: leaf
	"internal/querygrammar":            0, // table: leaf
	"internal/scanlock":                0, // computed: imports no module package
	"internal/security/pathvalidation": 0, // computed: imports no module package
	"internal/security/safehttp":       0, // computed: imports no module package
	"internal/security/safepath":       0, // computed: imports no module package
	"internal/seqnum":                  0, // table: leaf
	"internal/serverdecode":            0, // computed: imports no module package
	"internal/serviceregistry":         0, // computed: imports no module package
	"internal/syncapi/conformance":     0, // computed: imports no module package
	"internal/syncapi/progress":        0, // computed: imports no module package
	"internal/telemetry":               0, // computed: imports no module package
	"internal/telemetry/contract":      0, // computed: imports no module package
	"internal/titleutil":               0, // table: leaf
	"internal/tools":                   0, // computed: imports no module package
	"internal/trackseq":                0, // computed: imports no module package
	"internal/util":                    0, // table: leaf

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
	"internal/backup":                   3, // computed: max of imports=0, floored at 3
	"internal/batch":                    3, // computed: max of imports=3, floored at 3
	"internal/boilerplate":              3, // computed: max of imports=0, floored at 3
	"internal/bookfiles":                3, // computed: max of imports=2, floored at 3
	"internal/bookfileaudio":            3, // computed: max of imports=3, floored at 3
	"internal/compactprogress":          3, // computed: max of imports=2, floored at 3
	"internal/covers":                   3, // computed: max of imports=0, floored at 3
	"internal/database/dbtest":          3, // computed: max of imports=2, floored at 3
	"internal/database/mocks":           3, // computed: max of imports=2, floored at 3
	"internal/dedup":                    3, // table: domain
	"internal/dedup/dataset":            3, // computed: max of imports=3, floored at 3
	"internal/dedup/unified":            3, // computed: max of imports=0, floored at 3
	"internal/deluge":                   3, // computed: max of imports=3, floored at 3
	"internal/diagnostics":              3, // computed: max of imports=3, floored at 3
	"internal/download":                 3, // computed: max of imports=1, floored at 3
	"internal/fileops":                  3, // computed: max of imports=2, floored at 3
	"internal/fingerprint/workerclient": 3, // computed: max of imports=0, floored at 3
	"internal/foldercover":              3, // computed: max of imports=3, floored at 3
	"internal/foldernames":              3, // computed: max of imports=3, floored at 3
	"internal/itunes":                   3, // table: domain
	"internal/itunesguard":              3, // computed: max of imports=3, floored at 3
	"internal/mediainfo":                3, // computed: max of imports=0, floored at 3
	"internal/merge":                    3, // table: domain
	"internal/metadata":                 3, // table: domain
	"internal/metadata/mocks":           3, // computed: max of imports=3, floored at 3
	"internal/metadata/providerhttp":    3, // computed: max of imports=0, floored at 3
	"internal/metafetch":                3, // table: domain
	"internal/oauth":                    3, // computed: max of imports=2, floored at 3
	"internal/operations":               3, // computed: max of imports=0, floored at 3
	"internal/operations/childop":       3, // computed: max of imports=2, floored at 3
	"internal/organizer":                3, // table: domain
	"internal/playlist":                 3, // computed: max of imports=2, floored at 3
	"internal/plugin":                   3, // computed: max of imports=0, floored at 3
	"internal/policy":                   3, // computed: max of imports=2, floored at 3
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
	"internal/updater":                  3, // computed: max of imports=1, floored at 3
	"internal/versionprimary":           3, // table: domain
	"internal/versionprimary/vptest":    3, // computed: max of imports=2, floored at 3
	"internal/versions":                 3, // table: domain
	"internal/watcher":                  3, // computed: max of imports=3, floored at 3
	"internal/work":                     3, // computed: max of imports=2, floored at 3
	"internal/writeback":                3, // table: domain

	// Layer 4: jobs
	"internal/authority/authoritybuild": 4, // computed: max of imports=4, floored at 3
	"internal/catalog":                  4, // computed: max of imports=4, floored at 3
	"internal/importer":                 4, // computed: max of imports=4, floored at 3
	"internal/maintenance":              4, // computed: max of imports=4, floored at 3
	"internal/maintenance/jobs":         4, // family: jobs
	"internal/metabatch":                4, // computed: max of imports=4, floored at 3
	"internal/operations/registry":      4, // table: jobs
	"internal/plugins":                  4, // family: internal/plugins/* is jobs
	"internal/plugins/acoustid":         4, // family: internal/plugins/* is jobs
	"internal/plugins/dedup":            4, // family: internal/plugins/* is jobs
	"internal/plugins/deluge":           4, // family: internal/plugins/* is jobs
	"internal/plugins/itunes":           4, // family: internal/plugins/* is jobs
	"internal/plugins/maintenance":      4, // family: internal/plugins/* is jobs
	"internal/plugins/metafetch":        4, // family: internal/plugins/* is jobs
	"internal/plugins/webhook":          4, // family: internal/plugins/* is jobs
	"internal/scheduler":                4, // table: jobs
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
	"internal/server/handlers/operations":       5, // family: internal/server/** is transport
	"internal/server/handlers/operations/mocks": 5, // family: internal/server/** is transport
	"internal/server/handlers/repairs":          5, // family: internal/server/** is transport
	"internal/server/handlers/review":           5, // family: internal/server/** is transport
	"internal/server/handlers/system":           5, // family: internal/server/** is transport
	"internal/server/handlers/system/mocks":     5, // family: internal/server/** is transport
	"internal/server/handlers/tools":            5, // family: internal/server/** is transport
	"internal/server/middleware":                5, // family: internal/server/** is transport
	"internal/testutil":                         5, // computed: max of imports=5, floored at 3

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
	"tools/cmd/dedup-dataset-audit":      6, // family: entry
	"tools/cmd/itunes-group-preview":     6, // family: entry
	"tools/cmd/merge-split-books":        6, // family: entry
	"tools/cmd/oplint":                   6, // family: entry
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

// loadGraph returns package -> sorted in-module non-test imports for every
// package under the module root. It shells out to `go list` so the test needs
// no dependency beyond the toolchain.
func loadGraph(t *testing.T, root string) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-json=ImportPath,Imports", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("go list stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("go list start: %v", err)
	}
	graph := map[string][]string{}
	dec := json.NewDecoder(stdout)
	for {
		var p struct {
			ImportPath string
			Imports    []string
		}
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkg, ok := rel(p.ImportPath)
		if !ok {
			continue
		}
		deps := []string{}
		for _, imp := range p.Imports {
			if d, ok := rel(imp); ok {
				deps = append(deps, d)
			}
		}
		sort.Strings(deps)
		graph[pkg] = deps
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("go list failed: %v\n%s", err, stderr.String())
	}
	return graph
}

func TestLayering(t *testing.T) {
	graph := loadGraph(t, repoRoot(t))

	// Positive control: the loader must see the module.
	if len(graph) < minModulePackages {
		t.Fatalf("the loader is broken, not the code: loaded %d module packages, want at least %d",
			len(graph), minModulePackages)
	}
	if !contains(graph[positiveControlEdge[0]], positiveControlEdge[1]) {
		t.Fatalf("the loader is broken, not the code: edge %s -> %s not found",
			positiveControlEdge[0], positiveControlEdge[1])
	}

	var problems []string

	// Every package must have a layer, and every layerOf key must be a real
	// package (a deleted package must leave the map).
	for _, pkg := range sortedKeys(graph) {
		if _, ok := layerOf[pkg]; !ok {
			problems = append(problems, fmt.Sprintf(
				"package %q has no entry in layerOf: choose its layer (see docs/architecture/layering.md)", pkg))
		}
	}
	for _, pkg := range sortedLayerKeys() {
		if _, ok := graph[pkg]; !ok {
			problems = append(problems, fmt.Sprintf(
				"layerOf lists %q, which is not a package in the module: remove the entry", pkg))
		}
	}

	// layerOf must equal the layer the classification rule computes from the
	// import graph, except for layerOverride. Without this a PR could clear a
	// violation by raising the importer's layer.
	computed := computeLayers(graph)
	for _, pkg := range sortedKeys(graph) {
		declared, ok := layerOf[pkg]
		if !ok {
			continue // already reported as a missing layerOf entry
		}
		if want := computed[pkg]; declared != want {
			problems = append(problems, fmt.Sprintf(
				"layerOf[%q] is %d but the classification rule computes %d: set it to %d, or fix the imports that change the computed layer. "+
					"A layer change needs an entry in layerOverride with go list evidence",
				pkg, declared, want, want))
		}
	}

	// (a) no edge violates the rule unless listed, and (b) every listed edge
	// still exists, (c) every listed edge really violates the rule.
	exists := map[edge]bool{}
	for _, pkg := range sortedKeys(graph) {
		from, okFrom := layerOf[pkg]
		for _, dep := range graph[pkg] {
			e := edge{pkg, dep}
			exists[e] = true
			to, okTo := layerOf[dep]
			if !okFrom || !okTo {
				continue // already reported as a missing layerOf entry
			}
			if to > from {
				if _, ok := allowed[e]; !ok {
					problems = append(problems, fmt.Sprintf(
						"layering violation: %s (layer %d) imports %s (layer %d); a package may import only its own layer or lower. "+
							"Fix the import (invert the dependency, pass a function or interface in), or list the edge in allowed with a reason. "+
							"Do not raise the importer's layer: layerOf must match the computed layer",
						pkg, from, dep, to))
				}
			}
		}
	}
	for _, e := range sortedEdges(allowed) {
		if !exists[e] {
			problems = append(problems, fmt.Sprintf(
				"allowed lists %s -> %s, but that import no longer exists: remove this entry, the edge is fixed", e[0], e[1]))
			continue
		}
		from, okFrom := layerOf[e[0]]
		to, okTo := layerOf[e[1]]
		if okFrom && okTo && to <= from {
			problems = append(problems, fmt.Sprintf(
				"allowed lists %s -> %s, but that edge is legal (layer %d imports layer %d): remove this entry",
				e[0], e[1], from, to))
		}
	}

	if len(problems) > 0 {
		t.Errorf("%d layering problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedEdges(m map[edge]string) []edge {
	keys := make([]edge, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	return keys
}

func sortedLayerKeys() []string {
	keys := make([]string, 0, len(layerOf))
	for k := range layerOf {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// namedLayer is the layer table from docs/architecture/layering.md: the
// packages that are assigned a layer by name.
var namedLayer = map[string]int{
	// Layer 0 leaf.
	"internal/util": 0, "internal/pathutil": 0, "internal/personname": 0, "internal/titleutil": 0,
	"internal/authorname": 0, "internal/audioext": 0, "internal/httputil": 0, "internal/logger": 0,
	"internal/logging": 0, "internal/metrics": 0, "internal/cache": 0, "internal/models": 0,
	"internal/seqnum": 0, "internal/querygrammar": 0,
	// Layer 1 config.
	"internal/config": 1,
	// Layer 2 storage.
	"internal/database": 2, "internal/openlibrary": 2, "internal/search": 2,
	// Layer 3 domain.
	"internal/merge": 3, "internal/versionprimary": 3, "internal/versions": 3, "internal/dedup": 3,
	"internal/matcher": 3, "internal/fingerprint": 3, "internal/metadata": 3, "internal/organizer": 3,
	"internal/scanner": 3, "internal/itunes": 3, "internal/audiobooks": 3, "internal/metafetch": 3,
	"internal/reconcile": 3, "internal/repairs": 3, "internal/undo": 3, "internal/writeback": 3,
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

// computeLayers applies the classification rule: override, then table name,
// then directory family, then max(layer of in-module imports) floored at 3,
// then 0 for a package that imports no module package.
func computeLayers(graph map[string][]string) map[string]int {
	out := map[string]int{}
	var layer func(pkg string) int
	layer = func(pkg string) int {
		if l, ok := out[pkg]; ok {
			return l
		}
		var l int
		if o, ok := layerOverride[pkg]; ok {
			l = o
		} else if n, ok := namedLayer[pkg]; ok {
			l = n
		} else if f, ok := familyLayer(pkg); ok {
			l = f
		} else if deps := graph[pkg]; len(deps) == 0 {
			l = 0
		} else {
			for _, d := range deps {
				if dl := layer(d); dl > l {
					l = dl
				}
			}
			if l < 3 {
				l = 3
			}
		}
		out[pkg] = l
		return l
	}
	for pkg := range graph {
		layer(pkg)
	}
	return out
}
