// file: core/kernel/core_topics_test.go
// version: 0.1.0
// guid: c41e86d0-5a73-4f2b-b9d8-0e63a1f7c258
// last-edited: 2026-11-20

package kernel

import "testing"

// TestCoreTopics_CarryTheTaskTopics: the catalog core declares lists the three
// core.task.* entries beside the status topic, each exactly once (a duplicate
// would fail declareCore at boot).
func TestCoreTopics_CarryTheTaskTopics(t *testing.T) {
	var names []string
	for _, s := range coreTopics() {
		names = append(names, s.Name)
	}
	for _, want := range []string{"core.task.run-changed.v1", "core.task.log-appended.v1", "core.task.finished.v1", "core.system.status-changed.v1"} {
		if n := countOf(names, want); n != 1 {
			t.Errorf("coreTopics has %s %d times, want 1 (%v)", want, n, names)
		}
	}
}

func countOf(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}
