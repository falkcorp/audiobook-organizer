// file: internal/server/domain_services_test.go
// version: 0.1.0
// guid: 3d8f1a96-5e27-4b40-8c73-a1e9d604b2f5
// last-edited: 2026-11-22

package server

import (
	"reflect"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/stretchr/testify/require"
)

// constructedClusters are the registry names R3-05a took out of serviceDefs:
// each is built by a plain constructor in domainServices.
var constructedClusters = []string{
	"work", "audiobook", "organize", "quarantine", "scan", "merge", "importpath", "batch",
}

// registryResidual is the one cluster of R3-05's nine that is still a registry
// def, and why (see serviceDefs). The first of F5-18 (itunesplugin) and F7-09
// (eventbus, metafetch) to merge lets it become a constructor; that change
// empties this list, and this test is where it has to say so.
var registryResidual = []string{"itunes"}

// TestServiceDefs_OmitTheConstructedClusters: none of the constructed names is
// in the explicit list (either variant of it), and the residual one still is.
// R3-05's end state (TestBooksServiceDefsEmpty: none of the nine) is this test
// with registryResidual empty.
func TestServiceDefs_OmitTheConstructedClusters(t *testing.T) {
	for _, databasePathSet := range []bool{true, false} {
		var names []string
		for _, d := range serviceDefs(databasePathSet, (*merge.Service)(nil)) {
			names = append(names, d.Name)
		}
		for _, n := range constructedClusters {
			require.NotContains(t, names, n, "serviceDefs(%v) still registers %q", databasePathSet, n)
		}
		for _, n := range registryResidual {
			require.Contains(t, names, n, "serviceDefs(%v) lost the residual %q: move it to domainServices and empty registryResidual", databasePathSet, n)
		}
	}
	// Nothing in the list may still Need a constructed name: the container
	// would refuse to resolve, but say which def and which name.
	for _, d := range serviceDefs(true, (*merge.Service)(nil)) {
		for _, need := range d.Needs {
			require.NotContains(t, constructedClusters, need, "def %q still Needs the constructed cluster %q", d.Name, need)
		}
	}
}

// TestNewServer_ConstructsTheClustersOnceInDependencyOrder: NewServer calls
// each constructor exactly once, in an order where every service follows what
// it is built from. The recorder is serverOptions.trace; the order is the
// explicit one in newDomainServices (stage 1, before the registry) then
// completeFromRegistry (stage 2, after its Build).
func TestNewServer_ConstructsTheClustersOnceInDependencyOrder(t *testing.T) {
	var steps []string
	s, cleanup := setupTestServerWithOptions(t, func(o *serverOptions) {
		o.trace = func(step string) { steps = append(steps, step) }
	})
	defer cleanup()

	// Exactly once each.
	want := slices.Clone(constructedClusters)
	got := slices.Clone(steps)
	slices.Sort(want)
	slices.Sort(got)
	require.Equal(t, want, got, "each constructor runs exactly once")

	// Dependency order. A service's dependencies that are themselves
	// constructors must appear before it; the registry-built ones
	// (quarantine's event bus, scan's embedding store) exist only after the
	// registry's Build, so stage 2 follows all of stage 1.
	needs := map[string][]string{
		"batch":      {"audiobook"},
		"quarantine": {"merge", "audiobook", "organize", "work", "importpath", "batch"},
		"scan":       {"merge", "audiobook", "organize", "work", "importpath", "batch"},
	}
	pos := map[string]int{}
	for i, n := range steps {
		pos[n] = i
	}
	for svc, deps := range needs {
		for _, dep := range deps {
			require.Less(t, pos[dep], pos[svc], "%s must be constructed before %s (order: %v)", dep, svc, steps)
		}
	}

	// And the handles the order produced are the ones that matter.
	require.NotNil(t, s.batchService)
	require.Equal(t, fieldPtr(t, s.batchService, "purger"), reflect.ValueOf(s.audiobookService).Pointer(),
		"batch was built before the audiobook service it purges through")
}

// TestRegistryWire_DedupEngineUsesTheConstructedMergeService: the dedup def no
// longer reads merge out of the container; it closes over the service
// serviceDefs was given, so the engine's merge service is the server's.
func TestRegistryWire_DedupEngineUsesTheConstructedMergeService(t *testing.T) {
	w, err := buildDedupEngineFromContainer(t, dedupWiringConfig(config.DedupSignalConfig{}))
	require.NoError(t, err)
	require.NotNil(t, w.merge)
	require.Equal(t, reflect.ValueOf(w.merge).Pointer(), fieldPtr(t, w.engine, "mergeService"),
		"dedup engine holds the merge service it was constructed with")
}
