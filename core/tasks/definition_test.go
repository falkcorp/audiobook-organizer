// file: core/tasks/definition_test.go
// version: 1.0.0
// guid: 1173dc91-1e44-4449-bd9f-f6ef42138906
// last-edited: 2026-11-10

package tasks_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

func validCommon() tasks.Common {
	return tasks.Common{
		Title:   "Review the case files",
		Help:    "Reads every open case. Read-only.",
		Effects: tasks.ReadOnly(tasks.Res("gotham", "cases")),
		Perms:   tasks.Permissions{Start: rbac.Need("gotham:cases.view")},
		Resume:  tasks.ResumeFromZero,
	}
}

func writerCommon() tasks.Common {
	c := validCommon()
	c.Effects = tasks.Writes(tasks.Res("gotham", "cases"))
	c.Perms.Approve = rbac.Need("gotham:cases.manage")
	return c
}

func problemText(d tasks.Definition) string {
	var b strings.Builder
	for _, p := range tasks.Problems(d) {
		b.WriteString(p.Error())
		b.WriteString("\n")
	}
	return b.String()
}

func TestParallelism_ZeroInvalid(t *testing.T) {
	var zero tasks.Parallelism
	if err := zero.Validate(); err != tasks.ErrNoParallelism {
		t.Fatalf("zero Validate() = %v, want the bare ErrNoParallelism", err)
	}
	if zero.Workers() != 0 || zero.String() != "unset" {
		t.Fatalf("zero Workers %d String %q", zero.Workers(), zero.String())
	}
	if tasks.ErrNoParallelism.Error() != "tasks: parallelism not declared (use CPU, Fixed or Sequential)" ||
		tasks.ErrNoRunContext.Error() != "tasks: watermark needs a run context" {
		t.Fatal("a sentinel's message differs from 05-tasks §5T.3.2")
	}
	for _, p := range []tasks.Parallelism{tasks.CPU(), tasks.Fixed(4, "four lanes"), tasks.Sequential("one vault"), tasks.FromParam("workers", 2, 8)} {
		if err := p.Validate(); err != nil {
			t.Fatalf("%s: Validate() = %v", p, err)
		}
	}
	for _, p := range []tasks.Parallelism{tasks.Sequential(""), tasks.FromParam("", 2, 8), tasks.FromParam("workers", 2, 0)} {
		if err := p.Validate(); err != tasks.ErrNoParallelism {
			t.Fatalf("%s: Validate() = %v, want ErrNoParallelism", p, err)
		}
	}
}

func TestFixed_ClampAndReasonRequired(t *testing.T) {
	for _, c := range []struct {
		n, want int
	}{{0, 1}, {-3, 1}, {1, 1}, {4, 4}, {64, 64}, {65, 64}, {1000, 64}} {
		if got := tasks.Fixed(c.n, "rate budget").Workers(); got != c.want {
			t.Fatalf("Fixed(%d).Workers() = %d, want %d", c.n, got, c.want)
		}
	}
	if err := tasks.Fixed(4, "").Validate(); err != tasks.ErrNoParallelism {
		t.Fatalf("Fixed(4, \"\").Validate() = %v, want ErrNoParallelism", err)
	}
	if s := tasks.Fixed(4, "provider rate budget").String(); s != "fixed(4): provider rate budget" {
		t.Fatalf("String() = %q", s)
	}
	if s := tasks.Sequential("one vault door").String(); s != "sequential: one vault door" {
		t.Fatalf("String() = %q", s)
	}
	if tasks.CPU().String() != "cpu" || tasks.CPU().Workers() < 1 || !tasks.CPU().UsesCPUBudget() || tasks.Fixed(2, "x").UsesCPUBudget() {
		t.Fatal("CPU() rendering, workers or budget flag wrong")
	}
	if tasks.MaxFixed != 64 {
		t.Fatalf("MaxFixed = %d, want 64", tasks.MaxFixed)
	}
}

func TestFromParam_WorkersFor(t *testing.T) {
	p := tasks.FromParam("workers", 3, 100)
	for _, c := range []struct {
		params string
		want   int
	}{{``, 3}, {`{}`, 3}, {`{"workers": 5}`, 5}, {`{"workers": 0}`, 1}, {`{"workers": 500}`, 64}, {`{"workers": "x"}`, 3}} {
		if got := p.WorkersFor([]byte(c.params)); got != c.want {
			t.Fatalf("WorkersFor(%s) = %d, want %d", c.params, got, c.want)
		}
	}
	if got := tasks.FromParam("workers", 3, 4).WorkersFor([]byte(`{"workers": 9}`)); got != 4 {
		t.Fatalf("max not applied: %d", got)
	}
	if got := tasks.Fixed(7, "x").WorkersFor([]byte(`{"workers": 2}`)); got != 7 {
		t.Fatalf("Fixed read params: %d", got)
	}
}

// TestBatch_ZeroParallelismRefusedAtRegister: at F2-01 the constructor
// records the problem on the Definition (registration-time refusal is the
// kernel's, F2-16), and the problem wraps ErrNoParallelism.
func TestBatch_ZeroParallelismRefusedAtRegister(t *testing.T) {
	b := tasks.Batch("gotham.cases.sweep", tasks.BatchSpec[struct{}, int, struct{}]{
		Common: validCommon(),
		Source: func(*tasks.RC, struct{}) (tasks.Source[int], error) { return tasks.Source[int]{}, nil },
		Item:   func(*tasks.ItemRC, struct{}, int) error { return nil },
	})
	f := tasks.Fixer("gotham.cases.fix", tasks.FixerSpec[struct{}, int]{
		Common:     withApproval(writerCommon()),
		Candidates: func(*tasks.RC, struct{}) (tasks.Source[int], error) { return tasks.Source[int]{}, nil },
		Load:       func(*tasks.RC, struct{}, string) (int, bool, error) { return 0, false, nil },
		Evaluate:   func(*tasks.ItemRC, struct{}, int) (tasks.Row, error) { return tasks.Row{}, nil },
		Apply:      func(*tasks.ItemRC, tasks.Writer, struct{}, tasks.Row) error { return nil },
	})
	for _, d := range []tasks.Definition{b, f} {
		ps := tasks.Problems(d)
		if len(ps) != 1 || !errors.Is(ps[0], tasks.ErrNoParallelism) {
			t.Fatalf("%s problems = %v, want exactly one wrapping ErrNoParallelism", d.Kind(), ps)
		}
	}
}

func withApproval(c tasks.Common) tasks.Common {
	c.Approval = &tasks.Approval{PlanRequired: true}
	return c
}

func TestParseKind_Grammar(t *testing.T) {
	for _, s := range []string{"books.dedup.full-scan", "core.backup.run", "hello.count", "gotham.cases-2.review"} {
		k, err := tasks.ParseKind(s)
		if err != nil || string(k) != s {
			t.Fatalf("ParseKind(%q) = %q, %v", s, k, err)
		}
	}
	for _, s := range []string{"Books.x", "books", "books.a:b", "books..x", "", ".books.x", "books.x.", "books.9x", "books.-x", "books.x_y"} {
		if _, err := tasks.ParseKind(s); err == nil {
			t.Fatalf("ParseKind(%q) accepted", s)
		}
	}
	if m := tasks.Kind("books.dedup.full-scan").Module(); m != "books" {
		t.Fatalf("Module() = %q", m)
	}
}

func TestScheduleFromConfig_KeyForm(t *testing.T) {
	task := func(s *tasks.Schedule) tasks.Definition {
		c := validCommon()
		c.Schedule = s
		return tasks.Task("gotham.cases.review", tasks.TaskSpec[struct{}, struct{}]{
			Common: c,
			Run:    func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
		})
	}
	for _, key := range []string{"core.backup.schedules.create", "modules.books.scheduler.schedules.library_scan_full", "core.ai.schedules.batch_poll"} {
		s := tasks.ScheduleFromConfig(key)
		if p := tasks.Problems(task(s)); len(p) != 0 {
			t.Fatalf("key %q: problems %v", key, p)
		}
		if s.ConfigKey() != key {
			t.Fatalf("ConfigKey() = %q", s.ConfigKey())
		}
	}
	for _, key := range []string{
		"core.backup.schedules.create-now",       // hyphenated segment
		"core.backup.create",                     // no .schedules.
		"core.backup.nightly.create",             // schedules segment missing
		"plugins.books.scheduler.schedules.scan", // owner neither core nor modules.<id>
		"gotham.backup.schedules.create",         // owner neither core nor modules.<id>
		"modules.books.schedules.scan",           // modules.<id> without a subsystem
		"core.schedules.schedules.x",             // subsystem named schedules
		"core.Backup.schedules.create",           // upper case
		"core.backup.schedules." + strings.Repeat("a", 49),
		"",
	} {
		if p := problemText(task(tasks.ScheduleFromConfig(key))); !strings.Contains(p, "schedule key") {
			t.Fatalf("key %q: no schedule-key problem recorded (%q)", key, p)
		}
	}
}

func TestSchedule_ModeAndBuilders(t *testing.T) {
	writerTask := func(s *tasks.Schedule) string {
		c := writerCommon()
		c.Schedule = s
		return problemText(tasks.Task("gotham.cases.close", tasks.TaskSpec[struct{}, struct{}]{
			Common: c, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
		}))
	}
	if p := writerTask(tasks.ScheduleFromConfig("modules.gotham.cases.schedules.close")); !strings.Contains(p, "Live() or Preview()") {
		t.Fatalf("a scheduled writer without a mode passed: %q", p)
	}
	if p := writerTask(tasks.ScheduleFromConfig("modules.gotham.cases.schedules.close").Live()); p != "" {
		t.Fatalf("Live() writer schedule: %q", p)
	}
	if p := writerTask(tasks.ScheduleFromConfig("modules.gotham.cases.schedules.close").Live().Preview()); !strings.Contains(p, "both") {
		t.Fatalf("Live and Preview accepted: %q", p)
	}
	if p := writerTask(tasks.ScheduleFromConfig("modules.gotham.cases.schedules.close").Live().Params(func() {})); !strings.Contains(p, "Params") {
		t.Fatalf("unencodable Params accepted: %q", p)
	}
	s := tasks.Every(time.Minute).OnStart().When(func() bool { return false }).Params(map[string]int{"n": 1}).Preview().Missed(tasks.SkipMissed)
	if s.Interval() != time.Minute || !s.RunsOnStart() || s.Open() || string(s.RunParams()) != `{"n":1}` ||
		!s.IsPreview() || s.IsLive() || s.OnMissed() != tasks.SkipMissed || s.ConfigKey() != "" {
		t.Fatal("builder state not recorded")
	}
	if p := writerTask(tasks.Every(0).Live()); !strings.Contains(p, "positive") {
		t.Fatalf("Every(0) accepted: %q", p)
	}
}

func TestConstructors_NeverPanicAndCollectProblems(t *testing.T) {
	task := tasks.Task("", tasks.TaskSpec[int, int]{})
	batch := tasks.Batch("x", tasks.BatchSpec[int, int, int]{ChunkSize: -1, ItemTimeout: -1, OnItemError: 9,
		PartitionBy: func(int) string { return "" }, ExclusiveRefs: func(int) []tasks.Resource { return nil }})
	fixer := tasks.Fixer("gotham.cases.fix", tasks.FixerSpec[int, int]{Common: tasks.Common{Effects: tasks.ReadOnly()}})
	for _, c := range []struct {
		d    tasks.Definition
		want []string
	}{
		{task, []string{"kind is empty", "Title is required", "Effects not declared", "Perms.Start is required", "Resume is required", "Task has no Run"}},
		{batch, []string{"needs <moduleID>.<name>", "Parallelism unset", "Batch has no Source", "Batch has no Item",
			"both PartitionBy and ExclusiveRefs", "ChunkSize -1", "ItemTimeout", "unknown OnItemError"}},
		{fixer, []string{"Effects must be Writes", "needs Approval", "Fixer has no Candidates", "Fixer has no Load",
			"Fixer has no Evaluate", "Fixer has no Apply", "Parallelism unset"}},
	} {
		got := problemText(c.d)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s (%s): problem %q missing from\n%s", c.d.Kind(), c.d.Shape(), w, got)
			}
		}
	}
	if ps := tasks.Problems(nil); len(ps) != 1 {
		t.Fatalf("Problems(nil) = %v", ps)
	}
	if problemText(fixer) != problemText(tasks.Fixer("gotham.cases.fix", tasks.FixerSpec[int, int]{Common: tasks.Common{Effects: tasks.ReadOnly()}})) {
		t.Fatal("problem order is not stable")
	}
}

func TestConstructors_CommonRules(t *testing.T) {
	run := func(mut func(*tasks.Common)) string {
		c := validCommon()
		mut(&c)
		return problemText(tasks.Task("gotham.cases.review", tasks.TaskSpec[struct{}, struct{}]{
			Common: c, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
		}))
	}
	for name, c := range map[string]struct {
		mut  func(*tasks.Common)
		want string
	}{
		"resume_zero":        {func(c *tasks.Common) { c.Resume = 0 }, "Resume is required"},
		"resume_unknown":     {func(c *tasks.Common) { c.Resume = 9 }, "Resume is required"},
		"yields_not_cont":    {func(c *tasks.Common) { c.Yields = true }, "Yields requires ResumeContinue"},
		"timeout_over_24h":   {func(c *tasks.Common) { c.Timeout = 25 * time.Hour }, "Timeout"},
		"progress_negative":  {func(c *tasks.Common) { c.ProgressTimeout = -1 }, "ProgressTimeout"},
		"bad_key":            {func(c *tasks.Common) { c.ConcurrencyKey = "nokey" }, "ConcurrencyKey"},
		"bad_notwhile":       {func(c *tasks.Common) { c.NotWhile = []tasks.Kind{"Bad"} }, "NotWhile"},
		"bad_priority":       {func(c *tasks.Common) { c.Priority = 9 }, "Priority"},
		"approval_no_perm":   {func(c *tasks.Common) { c.Approval = &tasks.Approval{} }, "Perms.Approve"},
		"approval_neg_age":   {func(c *tasks.Common) { c.Approval = &tasks.Approval{MaxPlanAge: -1} }, "MaxPlanAge"},
		"empty_ready":        {func(c *tasks.Common) { c.Ready = []tasks.ReadySignal{""} }, "Ready"},
		"bad_lane":           {func(c *tasks.Common) { c.Lanes = []tasks.LaneID{"decode"} }, "Lanes"},
		"debounce_negative":  {func(c *tasks.Common) { c.Debounce = &tasks.Debounce{Window: -1} }, "Debounce"},
		"debounce_wait_low":  {func(c *tasks.Common) { c.Debounce = &tasks.Debounce{Window: time.Minute, MaxWait: time.Second} }, "MaxWait"},
		"bad_notify":         {func(c *tasks.Common) { c.Notify = 7 }, "Notify"},
		"cooperative_zero":   {func(c *tasks.Common) { c.Cancel = tasks.Cooperative(0) }, "positive grace"},
		"uninterruptible":    {func(c *tasks.Common) { c.Cancel = tasks.Uninterruptible("", 0) }, "needs a reason"},
		"writes_none":        {func(c *tasks.Common) { c.Effects = tasks.Writes() }, "names no resource"},
		"ref_without_kind":   {func(c *tasks.Common) { c.Effects = tasks.ReadOnly(tasks.Res("gotham", "")) }, "needs a module and a kind"},
		"deletes_read_only":  {func(c *tasks.Common) { c.Effects = tasks.ReadOnly().AlsoDeletes(tasks.Res("gotham", "cases")) }, "AlsoDeletes on a ReadOnly"},
		"perms_start_absent": {func(c *tasks.Common) { c.Perms = tasks.Permissions{} }, "Perms.Start"},
	} {
		if got := run(c.mut); !strings.Contains(got, c.want) {
			t.Errorf("%s: problem %q missing from %q", name, c.want, got)
		}
	}
	ok := func(mut func(*tasks.Common)) {
		if got := run(mut); got != "" {
			t.Errorf("valid Common reported %q", got)
		}
	}
	ok(func(c *tasks.Common) { c.Resume = tasks.ResumeContinue; c.Yields = true })
	ok(func(c *tasks.Common) { c.Timeout = tasks.MaxTimeout; c.ConcurrencyKey = "core.storage.exclusive" })
	ok(func(c *tasks.Common) { c.Cancel = tasks.Uninterruptible("one atomic rename", time.Minute) })
	ok(func(c *tasks.Common) { c.Debounce = &tasks.Debounce{}; c.Lanes = []tasks.LaneID{"books.audio-decode"} })
}

func TestConstructors_ValidDefinitions(t *testing.T) {
	task := tasks.Task("gotham.cases.review", tasks.TaskSpec[struct{}, int]{
		Common: validCommon(),
		Run:    func(*tasks.RC, struct{}) (int, error) { return 0, nil },
	})
	batch := tasks.Batch("gotham.cases.sweep", tasks.BatchSpec[struct{}, int, int]{
		Common:      validCommon(),
		Source:      func(*tasks.RC, struct{}) (tasks.Source[int], error) { return tasks.Source[int]{Items: []int{1}}, nil },
		Item:        func(*tasks.ItemRC, struct{}, int) error { return nil },
		Parallelism: tasks.FromParam("workers", 2, 8),
	})
	fixer := tasks.Fixer("gotham.cases.fix", tasks.FixerSpec[struct{}, int]{
		Common:      withApproval(writerCommon()),
		Candidates:  func(*tasks.RC, struct{}) (tasks.Source[int], error) { return tasks.Source[int]{}, nil },
		Load:        func(*tasks.RC, struct{}, string) (int, bool, error) { return 0, false, nil },
		Evaluate:    func(*tasks.ItemRC, struct{}, int) (tasks.Row, error) { return tasks.Row{}, nil },
		Apply:       func(*tasks.ItemRC, tasks.Writer, struct{}, tasks.Row) error { return nil },
		Parallelism: tasks.Sequential("one vault"),
	})
	for _, c := range []struct {
		d     tasks.Definition
		shape tasks.Shape
		par   string
	}{{task, tasks.ShapeTask, "unset"}, {batch, tasks.ShapeBatch, "param(workers, def 2, max 8)"}, {fixer, tasks.ShapeFixer, "sequential: one vault"}} {
		if p := tasks.Problems(c.d); p != nil {
			t.Fatalf("%s: %v", c.d.Kind(), p)
		}
		if c.d.Shape() != c.shape || tasks.ParallelismOf(c.d).String() != c.par || tasks.CommonOf(c.d).Title == "" {
			t.Fatalf("%s: shape %s parallelism %s", c.d.Kind(), c.d.Shape(), tasks.ParallelismOf(c.d))
		}
	}
	if tasks.CommonOf(nil).Title != "" || tasks.ParallelismOf(nil).Validate() == nil {
		t.Fatal("nil Definition accessors")
	}
}
