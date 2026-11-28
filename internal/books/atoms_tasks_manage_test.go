// file: internal/books/atoms_tasks_manage_test.go
// version: 1.0.0
// guid: 5b1e84a2-9d37-4c60-8f12-3a7c0e6d9b45
// last-edited: 2026-11-28

package books

import (
	"slices"
	"testing"
)

// TestAtomTable_OnlySettingsManageCarriesTasksManage (F5-18 reach R1): core's
// GET /schedules floor is core:tasks.manage, chosen because HEAD's
// GET /api/v1/tasks needs settings.manage. That equivalence holds only while
// no other atom row carries the scope: a row that did (library.view above all,
// the role every viewer holds) would let that role list the schedules HEAD
// refuses it. The row that carries it is named, so a rename fails here too.
func TestAtomTable_OnlySettingsManageCarriesTasksManage(t *testing.T) {
	const scope = "core:tasks.manage"
	var carriers []string
	for _, a := range AtomTable() {
		for _, s := range a.Scopes {
			if string(s) == scope {
				carriers = append(carriers, a.Name)
			}
		}
	}
	if !slices.Equal(carriers, []string{"settings.manage"}) {
		t.Fatalf("atom rows carrying %s = %v, want only settings.manage", scope, carriers)
	}
}
