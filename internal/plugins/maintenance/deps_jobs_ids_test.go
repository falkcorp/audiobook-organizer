// file: internal/plugins/maintenance/deps_jobs_ids_test.go
// version: 0.1.0
// guid: 8c4e2b71-6a35-4d09-b1f8-2e7a9d5c3f46
// last-edited: 2026-11-20

package maintenance

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	_ "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs" // registers every job
)

// TestJobCapabilityScanCoversEveryRegisteredJob: the scan finds jobs by looking
// for types with an ID and a Run method, so a job the scan cannot see (an ID
// that is not a constant string is an error, but a registration through some
// other shape would be silently absent) is a job whose store assertions are
// never classified. The IDs the scan found must be exactly the registry's.
func TestJobCapabilityScanCoversEveryRegisteredJob(t *testing.T) {
	var scanned []string
	for id := range jobSites(t) {
		if !strings.HasPrefix(id, "package ") {
			scanned = append(scanned, id)
		}
	}
	var registered []string
	for _, j := range maintenance.All() {
		registered = append(registered, j.ID())
	}
	sort.Strings(scanned)
	sort.Strings(registered)
	require.NotEmpty(t, registered)
	require.Equal(t, registered, scanned, "the scan and maintenance.All() disagree about which jobs exist")
}
