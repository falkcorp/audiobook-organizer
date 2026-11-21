// file: internal/server/op_display_names_test.go
// version: 1.0.0
// guid: 8d4b2f61-3e07-4a95-b1c8-52e9a7d03f14
// last-edited: 2026-11-21

package server

import (
	"reflect"
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

// BASELINE, replaced in the commit that renames the two defs below: HEAD
// registers exactly one shared DisplayName.
func TestOpDisplayNames_BaselineCollisions(t *testing.T) {
	got := registeredDisplayNameCollisions(t)
	want := map[string][]string{
		"purge soft-deleted books": {"library.purge-soft-deleted", "maintenance.purge-deleted"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shared DisplayNames = %v, want %v", got, want)
	}
}
