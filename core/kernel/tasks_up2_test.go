// file: core/kernel/tasks_up2_test.go
// version: 1.0.0
// guid: 1c8e5b37-4d92-4a06-b7f3-9a2d6e0c4f58
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// penguinKind is a kind of a second module, enabled throughout, so that tasks
// are built and the snapshots upserted on every boot of the cells below.
const penguinKind tasks.Kind = "oswald.umbrella"

func penguin() module.Module {
	spec := tasks.TaskSpec[struct{}, struct{}]{
		Title:   "Umbrella",
		Effects: tasks.ReadOnly(),
		Perms:   tasks.Permissions{Start: rbac.Need("oswald:umbrella")},
		Resume:  tasks.ResumeFromZero,
		Run:     func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
	}
	return mod("oswald", func(r *module.Registrar) error {
		r.Tasks(tasks.Task(penguinKind, spec))
		return r.Err()
	}).with(func(m *module.Manifest) { m.TaskLedger = []tasks.Kind{penguinKind} })
}

// TestKernelTasks_DisabledModuleKeepsItsV1SnapshotUntilEnabledOnce (F5-18
// reach, UP-2): a snapshot an older build wrote (version 1: no View) is only
// rewritten by the boot that registers its kind, which a disabled module does
// not. Its runs therefore stay hidden from every caller (executor: a version-1
// snapshot is "unknown", closed) until the module is enabled once; that boot
// writes version 2, and the rule holds from then on, enabled or not. Killed by
// an upsert that leaves a v1 row alone for an enabled module (the enabling boot
// would keep the row closed). A disabled module registers no definition at all
// (its Register is not run), so taskDefinitions' own enabled check is not what
// holds the first half; the cell pins the observable result either way.
func TestKernelTasks_DisabledModuleKeepsItsV1SnapshotUntilEnabledOnce(t *testing.T) {
	v1 := []byte(`{"v":1,"shape":"task","title":"Patrol Gotham"}`)
	key := taskstore.DefKey(string(patrolKind))
	cfg, _ := taskConfig(t)
	boot := func(gothamEnabled bool, before func(k *Kernel)) []byte {
		t.Helper()
		g := gotham([]tasks.Kind{patrolKind}, patrol()).with(func(m *module.Manifest) { m.DefaultEnabled = gothamEnabled })
		c := cfg
		c.Modules = []module.Module{g, penguin()}
		k, err := newK(t, c)
		if err != nil {
			t.Fatalf("New (gotham enabled=%v): %v", gothamEnabled, err)
		}
		if before != nil {
			before(k)
		}
		raw, found, err := k.tasks.st.Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			raw = nil
		}
		if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
			t.Errorf("close: %v", err)
		}
		return raw
	}

	// An older build's v1 row, planted in the store the next boots open.
	boot(true, func(k *Kernel) {
		if err := k.commitTaskBatch(t.Context(), func(b *storage.Batch) error { return k.tasks.st.Set(b, key, v1) }); err != nil {
			t.Fatal(err)
		}
	})
	for i := range 2 {
		if got := boot(false, nil); string(got) != string(v1) {
			t.Fatalf("boot %d with the module disabled: snapshot %s, want the v1 row untouched %s", i+1, got, v1)
		}
	}
	got := boot(true, nil)
	if string(got) == string(v1) || len(got) == 0 {
		t.Fatalf("the boot that enables the module left snapshot %s, want it rewritten", got)
	}
	if want := `"view":{"kind":"default"}`; !strings.Contains(string(got), `"v":2`) || !strings.Contains(string(got), want) {
		t.Fatalf("snapshot after enabling once = %s, want version 2 with %s", got, want)
	}
	// Disabled again, the version-2 row stays: the rule it records is the rule.
	if again := boot(false, nil); string(again) != string(got) {
		t.Fatalf("a later boot with the module disabled changed the snapshot: %s -> %s", got, again)
	}
}
