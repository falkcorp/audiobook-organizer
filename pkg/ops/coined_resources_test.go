// file: pkg/ops/coined_resources_test.go
// version: 1.0.2
// guid: 5e2a9b73-0c41-4d68-b1f7-93a6d0c8e214
// last-edited: 2026-11-19

package ops

import "testing"

// The two resource names F5-13 gave constants are a contract: books declares
// them with Registrar.Resources (internal/books taskResourceKinds), the native
// kinds' write sets name them, and the seams record lists them for F5-18.
func TestResources_F513Names(t *testing.T) {
	for res, want := range map[Resource]string{
		ResAIScans:     "ai_scans",
		ResCollections: "collections",
	} {
		if string(res) != want {
			t.Errorf("resource %q, want %q", res, want)
		}
	}
}
