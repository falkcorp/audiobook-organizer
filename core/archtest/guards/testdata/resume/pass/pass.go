// file: core/archtest/guards/testdata/resume/pass/pass.go
// version: 1.0.2
// guid: a7ceb0e0-5b04-43c9-a357-ae4606d85c31
// last-edited: 2026-11-10

// Package pass holds ResumeContinue Task definitions that each reach a
// checkpoint by one clause, plus definitions the guard ignores.
package pass

import "github.com/falkcorp/mediacore/tasks"

type np = struct{}

func common(r tasks.ResumePolicy) tasks.Common {
	return tasks.Common{Title: "t", Help: "h", Resume: r}
}

var cont = tasks.Common{Title: "t", Help: "h", Resume: tasks.ResumeContinue}

type worker struct{}

func (worker) run(rc *tasks.RC, p np) (np, error) { return np{}, save(rc) }

func save(rc *tasks.RC) error { return rc.Checkpoint(1) }

var defs = []tasks.Definition{
	// Direct checkpoint in a literal Run.
	tasks.Task("gotham.direct", tasks.TaskSpec[np, np]{Common: tasks.Common{Resume: tasks.ResumeContinue},
		Run: func(rc *tasks.RC, p np) (np, error) { return np{}, rc.Checkpoint(1) }}),
	// Common's fields as promoted fields (Go 1.27), checkpointing.
	tasks.Task("gotham.promoted", tasks.TaskSpec[np, np]{Resume: tasks.ResumeContinue,
		Run: func(rc *tasks.RC, p np) (np, error) { return np{}, rc.Checkpoint(1) }}),
	// Through a method value and a helper two levels down; Common from a var.
	tasks.Task("gotham.method", tasks.TaskSpec[np, np]{Common: cont, Run: worker{}.run}),
	// A RunItems loop with a Watermark; Common from a helper.
	tasks.Task("gotham.watermark", tasks.TaskSpec[np, np]{Common: common(tasks.ResumeContinue),
		Run: func(rc *tasks.RC, p np) (np, error) {
			_, err := tasks.RunItems(rc, tasks.CPU(), []int{1}, func(ic *tasks.ItemRC, i int) error { return nil },
				tasks.ItemsOptions[int]{Watermark: "arkham"})
			return np{}, err
		}}),
	// Not ResumeContinue: never checked.
	tasks.Task("gotham.from-zero", tasks.TaskSpec[np, np]{Common: common(tasks.ResumeFromZero),
		Run: func(rc *tasks.RC, p np) (np, error) { return np{}, nil }}),
	// Reviewed from-zero restart.
	tasks.Task("gotham.reviewed", tasks.TaskSpec[np, np]{Common: cont, //archtest:resume-from-zero the dirty set is the resume state
		Run: func(rc *tasks.RC, p np) (np, error) { return np{}, nil }}),
	// A Batch passes by construction.
	tasks.Batch("gotham.batch", tasks.BatchSpec[np, int, np]{Common: cont, Parallelism: tasks.CPU()}),
}
