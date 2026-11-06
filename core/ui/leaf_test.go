// file: core/ui/leaf_test.go
// version: 1.0.0
// guid: f2a1b729-8570-404a-a916-9cf103451517
// last-edited: 2026-11-06

package ui

import (
	"reflect"
	"slices"
	"testing"
)

// TestReservedSegments_Segments: the first segment of every shell route
// (07 §7.4.3), sorted, each a valid module-ID-shaped lower-case word.
func TestReservedSegments_Segments(t *testing.T) {
	want := []string{"activity", "invite", "login", "settings", "setup", "system", "users"}
	got := ReservedSegments()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReservedSegments() = %v, want %v", got, want)
	}
	if !slices.IsSorted(got) {
		t.Errorf("ReservedSegments() = %v is not sorted", got)
	}
	// No "logout" page (FE2-7: "logout" is a server segment in ABO).
	if slices.Contains(got, "logout") {
		t.Error("ReservedSegments() names logout, which is not a shell route")
	}
}

// TestReservedSegments_FreshSlice: two calls return equal, distinct slices, so
// a caller that sorts, appends to or edits its copy cannot change another's
// (03 R5: no exported slice variable).
func TestReservedSegments_FreshSlice(t *testing.T) {
	a, b := ReservedSegments(), ReservedSegments()
	if !slices.Equal(a, b) {
		t.Fatalf("two calls differ: %v vs %v", a, b)
	}
	if &a[0] == &b[0] {
		t.Fatal("two calls share a backing array")
	}
	a[0] = "mutated"
	_ = append(a[:1], "extra")
	if c := ReservedSegments(); c[0] != "activity" || len(c) != len(b) {
		t.Fatalf("mutating one result changed the next call: %v", c)
	}
}

// TestUIIDs_MirrorsUIDescriptor pins the nine list fields of 06 §6.4.2 by name
// and type: module.UIDescriptor mirrors them field by field, so a drift here
// is a drift there.
func TestUIIDs_MirrorsUIDescriptor(t *testing.T) {
	want := []string{"Pages", "Nav", "SettingsPanels", "Widgets", "Banners", "TopBarItems", "UserPage", "ActivityPanes", "SetupSteps"}
	rt := reflect.TypeFor[UIIDs]()
	var got []string
	for f := range rt.Fields() {
		got = append(got, f.Name)
		if f.Type != reflect.TypeFor[[]string]() {
			t.Errorf("UIIDs.%s is %v, want []string", f.Name, f.Type)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("UIIDs fields = %v, want %v", got, want)
	}

	m := ModuleManifest{ID: "hello", UI: UIIDs{Pages: []string{"home"}}, Permissions: []string{"hello:view"}}
	if m.UI.Pages[0] != "home" {
		t.Fatal("ModuleManifest.UI does not carry the lists")
	}
}
