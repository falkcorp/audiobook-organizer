// file: core/tasks/taskstest/conformance_test.go
// version: 1.0.1
// guid: 02f74c7d-36d4-4265-952a-e0ab50353814
// last-edited: 2026-11-11

package taskstest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// recorder is a tester that records each failure under its subtest path.
type recorder struct {
	path   string
	fails  *[]string
	logged *[]string
}

func newRecorder() recorder { return recorder{fails: new([]string), logged: new([]string)} }

func (r recorder) Helper() {}
func (r recorder) Logf(format string, args ...any) {
	*r.logged = append(*r.logged, r.path+": "+fmt.Sprintf(format, args...))
}
func (r recorder) Errorf(format string, args ...any) {
	*r.fails = append(*r.fails, r.path+": "+fmt.Sprintf(format, args...))
}
func (r recorder) Run(name string, f func(tester)) bool {
	f(recorder{path: strings.TrimPrefix(r.path+"/"+name, "/"), fails: r.fails, logged: r.logged})
	return true
}

func common() tasks.Common {
	return tasks.Common{
		Title:   "Catalogue Arkham inmates",
		Help:    "Counts the inmates. Read-only.",
		Effects: tasks.ReadOnly(tasks.Res("arkham", "inmates")),
		Perms:   tasks.Permissions{Start: rbac.Need("arkham:inmates.view")},
		Resume:  tasks.ResumeFromZero,
	}
}

func source(*tasks.RC, struct{}) (tasks.Source[string], error) {
	return tasks.Source[string]{Items: []string{"joker", "harley"}}, nil
}

func fixer(kind tasks.Kind, approval *tasks.Approval) tasks.Definition {
	c := common()
	c.Effects = tasks.Writes(tasks.Res("arkham", "inmates"))
	c.Perms.Approve = rbac.Need("arkham:inmates.manage")
	c.Approval = approval
	return tasks.Fixer(kind, tasks.FixerSpec[struct{}, string]{
		Common:      c,
		Candidates:  source,
		Load:        func(*tasks.RC, struct{}, string) (string, bool, error) { return "", false, nil },
		Evaluate:    func(*tasks.ItemRC, struct{}, string) (tasks.Row, error) { return tasks.Row{}, nil },
		Apply:       func(*tasks.ItemRC, tasks.Writer, struct{}, tasks.Row) error { return nil },
		Parallelism: tasks.Sequential("one cell block at a time"),
	})
}

func batch(kind tasks.Kind, par tasks.Parallelism) tasks.Definition {
	return tasks.Batch(kind, tasks.BatchSpec[struct{}, string, struct{}]{
		Common:      common(),
		Source:      source,
		Item:        func(*tasks.ItemRC, struct{}, string) error { return nil },
		Parallelism: par,
	})
}

// TestConformance_SelfTest runs Conformance over one valid definition of each
// shape and three broken ones, expecting failures only for the broken ones,
// each under the rule it breaks.
func TestConformance_SelfTest(t *testing.T) {
	task := tasks.Task("arkham.inmates.count", tasks.TaskSpec[struct{}, int]{
		Common: common(),
		Run:    func(*tasks.RC, struct{}) (int, error) { return 0, nil },
	})
	valid := []tasks.Definition{
		task,
		batch("arkham.inmates.sweep", tasks.Fixed(4, "four guards")),
		fixer("arkham.inmates.reassign", &tasks.Approval{PlanRequired: true}),
	}
	r := newRecorder()
	conformance(r, valid...)
	if len(*r.fails) != 0 {
		t.Fatalf("valid definitions failed: %v", *r.fails)
	}

	noResume := common()
	noResume.Resume = 0
	broken := []tasks.Definition{
		tasks.Task("arkham.inmates.escape", tasks.TaskSpec[struct{}, int]{
			Common: noResume,
			Run:    func(*tasks.RC, struct{}) (int, error) { return 0, nil },
		}),
		batch("arkham.inmates.riot", tasks.Parallelism{}),
		fixer("arkham.inmates.release", nil),
	}
	r = newRecorder()
	conformance(r, append(slices.Clone(valid), broken...)...)
	want := map[string]bool{
		"arkham.inmates.escape/" + AssertNoProblems:        false,
		"arkham.inmates.escape/" + AssertRequiredCommon:    false,
		"arkham.inmates.riot/" + AssertNoProblems:          false,
		"arkham.inmates.riot/" + AssertParallelismDeclared: false,
		"arkham.inmates.release/" + AssertNoProblems:       false,
		"arkham.inmates.release/" + AssertFixerApproval:    false,
	}
	for _, f := range *r.fails {
		path, _, _ := strings.Cut(f, ": ")
		if _, ok := want[path]; !ok {
			t.Errorf("unexpected failure %q", f)
			continue
		}
		want[path] = true
		for _, v := range valid {
			if strings.HasPrefix(path, string(v.Kind())+"/") {
				t.Errorf("a valid definition failed: %q", f)
			}
		}
	}
	for path, hit := range want {
		if !hit {
			t.Errorf("no failure recorded under %s", path)
		}
	}

	r = newRecorder()
	conformance(r, task, task)
	if !slices.ContainsFunc(*r.fails, func(f string) bool { return strings.HasPrefix(f, AssertUniqueKinds+":") }) {
		t.Fatalf("duplicate kinds not failed: %v", *r.fails)
	}
	r = newRecorder()
	conformance(r)
	if len(*r.fails) != 1 {
		t.Fatalf("an empty Conformance did not fail: %v", *r.fails)
	}
}

// TestConformance_PassesOnTestingT runs the exported entry point over valid
// definitions on a real *testing.T.
func TestConformance_PassesOnTestingT(t *testing.T) {
	Conformance(t, batch("arkham.inmates.sweep", tasks.CPU()), fixer("arkham.inmates.reassign", &tasks.Approval{PlanRequired: true}))
}
