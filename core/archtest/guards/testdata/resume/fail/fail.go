// file: core/archtest/guards/testdata/resume/fail/fail.go
// version: 1.0.2
// guid: 922e1b1a-dd7e-4c5e-999d-0dcb29c00aa9
// last-edited: 2026-11-10

// Package fail holds ResumeContinue Task definitions the guard reports, one
// per failing clause.
package fail

import "github.com/falkcorp/mediacore/tasks"

type np = struct{}

var cont = tasks.Common{Title: "t", Help: "h", Resume: tasks.ResumeContinue}

func defs(c tasks.Common, spec tasks.TaskSpec[np, np]) []tasks.Definition {
	return []tasks.Definition{
		tasks.Task("joker.none", tasks.TaskSpec[np, np]{Common: cont, // want: reaches no rc.Checkpoint
			Run: func(rc *tasks.RC, p np) (np, error) { return np{}, nil }}),
		tasks.Task("joker.no-watermark", tasks.TaskSpec[np, np]{Common: cont, // want: reaches no rc.Checkpoint
			Run: func(rc *tasks.RC, p np) (np, error) {
				_, err := tasks.RunItems(rc, tasks.CPU(), []int{1}, func(ic *tasks.ItemRC, i int) error { return nil },
					tasks.ItemsOptions[int]{Watermark: ""})
				return np{}, err
			}}),
		tasks.Task("joker.blank-watermark", tasks.TaskSpec[np, np]{Common: cont, // want: reaches no rc.Checkpoint
			Run: func(rc *tasks.RC, p np) (np, error) {
				_, err := tasks.RunItems(rc, tasks.CPU(), []int{1}, func(ic *tasks.ItemRC, i int) error { return nil },
					tasks.ItemsOptions[int]{Watermark: "  "})
				return np{}, err
			}}),
		tasks.Task("joker.param-common", tasks.TaskSpec[np, np]{Common: c}), // want: Resume cannot be read
		tasks.Task("joker.spec-var", spec),                                  // want: not a literal
		tasks.Task("joker.no-reason", tasks.TaskSpec[np, np]{Common: cont, //archtest:resume-from-zero
			// want-above: needs the reason
			Run: func(rc *tasks.RC, p np) (np, error) { return np{}, nil }}),
		tasks.Task("joker.stale", tasks.TaskSpec[np, np]{Common: cont, //archtest:resume-from-zero it used to restart
			// want-above: delete its
			Run: func(rc *tasks.RC, p np) (np, error) { return np{}, rc.Checkpoint(1) }}),
	}
}
