// file: core/examples/hello/count_test.go
// version: 1.0.0
// guid: 1c150f6f-fc06-470d-acdd-2defe708fd72
// last-edited: 2026-11-11

package hello

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// helloService opens hello's own storage over MemFS (migrations included, so
// the seed row name/world=1 is there) and returns the service over its
// "name" keyspace, as Register builds it.
func helloService(t *testing.T) *service {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            "/batcave/hello.pebble",
		Owners:          []storage.OwnerSpec{{Owner: "hello", Spec: storageSpec(), Enabled: true}},
		Build:           "hello+test",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace("hello")
	if !ok {
		t.Fatal("hello has no keyspace")
	}
	return &service{names: ks.Sub("name")}
}

// TestHelloCount_Conformance is hello.count's registrability check (06 §6.12.3):
// the definition is constructed from a service over an in-memory keyspace and
// passes taskstest.Conformance. Running it through coretest's RunTask is F2-16's.
func TestHelloCount_Conformance(t *testing.T) {
	taskstest.Conformance(t, countTask(helloService(t)))
}

func TestHelloCount_DeclaresItself(t *testing.T) {
	d := countTask(helloService(t))
	if got := d.Kind(); got != "hello.count" {
		t.Fatalf("kind = %q, want hello.count", got)
	}
	c := tasks.CommonOf(d)
	if !c.Effects.Declared() || !c.Effects.IsReadOnly() {
		t.Fatalf("hello.count must be a declared read-only task: %+v", c.Effects)
	}
	if got := c.Perms.Start.Permissions(); len(got) != 1 || got[0] != PermAdmin {
		t.Fatalf("Perms.Start = %v, want only %s", got, PermAdmin)
	}
}

// runCount executes the definition's body over a FakeRun, the way the
// executor does (through the erased runner).
func runCount(t *testing.T, s *service) (CountResult, error) {
	t.Helper()
	exec, ok := countTask(s).(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	if !ok {
		t.Fatal("the definition exposes no executor runners")
	}
	decode, task, _, _ := exec.ExecutorRunners()
	if task == nil {
		t.Fatal("hello.count is not a Task")
	}
	p, err := decode(json.RawMessage("null"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := task.Run(taskstest.NewFakeRun(t.Context(), "hello.count").RC(), p)
	if err != nil {
		return CountResult{}, err
	}
	var out CountResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("result %s: %v", raw, err)
	}
	return out, nil
}

// TestHelloCount_SumsCounters: the seeded row plus the names the fixture
// adds; the task counts names and sums their counters.
func TestHelloCount_SumsCounters(t *testing.T) {
	s := helloService(t)
	got, err := runCount(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if want := (CountResult{Names: 1, Greetings: 1}); got != want {
		t.Fatalf("seeded count = %+v, want %+v", got, want)
	}

	for name, n := range map[string]string{"alfred": "3", "selina": "5", "harvey": "11"} {
		if err := s.names.Set(t.Context(), []byte(name), []byte(n)); err != nil {
			t.Fatal(err)
		}
	}
	got, err = runCount(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if want := (CountResult{Names: 4, Greetings: 20}); got != want {
		t.Fatalf("count = %+v, want %+v", got, want)
	}
}

// TestHelloCount_RefusesACorruptCounter: a counter that is not a decimal
// number fails the run, naming the key, rather than counting as zero.
func TestHelloCount_RefusesACorruptCounter(t *testing.T) {
	s := helloService(t)
	if err := s.names.Set(t.Context(), []byte("riddler"), []byte("riddle")); err != nil {
		t.Fatal(err)
	}
	_, err := runCount(t, s)
	if err == nil || !strings.Contains(err.Error(), "riddler") {
		t.Fatalf("err = %v, want a failure naming the key riddler", err)
	}
}
