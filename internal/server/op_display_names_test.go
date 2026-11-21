// file: internal/server/op_display_names_test.go
// version: 1.0.1
// guid: 8d4b2f61-3e07-4a95-b1c8-52e9a7d03f14
// last-edited: 2026-11-21

package server

import (
	"sort"
	"strings"
	"testing"
)

// registeredDisplayNameCollisions boots the registry the way
// TestKindRenameInputs_RegistryDump does and returns every DisplayName that two
// or more registered defs share, keyed by the name folded to lower case, with
// the def IDs sorted. handlers.displayNameFor sends a def's DisplayName to the
// UI as display_name, and the bell and the Activity page prefer it over their
// own labels, so two defs with one name are indistinguishable on screen.
func registeredDisplayNameCollisions(t *testing.T) map[string][]string {
	t.Helper()
	srv, registered, _ := bootRegisteredOpIDs(t)
	byName := map[string][]string{}
	for id := range registered {
		// Probe jobs registered from test code never exist in a production
		// build (kindRenameRegistryBody skips them for the same reason).
		if strings.Contains(id, ".test-probe-") {
			continue
		}
		d, ok := srv.opRegistry.Def(id)
		if !ok {
			t.Fatalf("registered op %s has no Def", id)
		}
		name := strings.ToLower(strings.TrimSpace(d.DisplayName))
		byName[name] = append(byName[name], id)
	}
	out := map[string][]string{}
	for name, ids := range byName {
		if len(ids) > 1 {
			sort.Strings(ids)
			out[name] = ids
		}
	}
	return out
}

// TestOpDisplayNames_NoTwoDefsShareOne fails when two registered defs carry the
// same DisplayName (compared ignoring case and surrounding space). It replaces
// the baseline that pinned library.purge-soft-deleted and maintenance.purge-deleted
// sharing "Purge soft-deleted books": the first is the operator's dry-run-first
// purge of the trash, the second the nightly, unattended purge of books past
// retention, and the UI could not tell them apart.
func TestOpDisplayNames_NoTwoDefsShareOne(t *testing.T) {
	if got := registeredDisplayNameCollisions(t); len(got) != 0 {
		t.Fatalf("registered defs share a DisplayName (name: def IDs): %v", got)
	}
}
