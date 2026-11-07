// file: core/rbac/namespaces_test.go
// version: 1.0.0
// guid: d1faa48a-a2fb-4110-b0b6-ba2b8a000a43
// last-edited: 2026-11-07

package rbac

import "testing"

func TestReservedNamespaces(t *testing.T) {
	got := ReservedNamespaces()
	if len(got) != 2 || got[0] != "core" || got[1] != "custom" {
		t.Fatalf("ReservedNamespaces() = %q", got)
	}
	got[0] = "x"
	if ReservedNamespaces()[0] != "core" {
		t.Fatal("ReservedNamespaces shares its slice between calls")
	}
}
