### Changed

#### Corrected the wording of the interval-plus-window schedule rule

The F5-14 pre-slice said a schedule with both `every` and `window` "never starts two runs for one due moment". That was too strong. A start that finds a run of the kind active folds into it under the kind's dedupe policy, so the default policy starts one run for one due moment; a policy that keys on something other than the parameters never folds into a running run, and a run that has already finished does not suppress the other trigger. The task spec (05-tasks, section 5T.3.8 and its history entry), the F5-14 seams file, and the comments in `tasks.ScheduleConfig` and the scheduler's effective-schedule code now say that. The spec and seams lists of books tasks that tick on an interval and also sit in the nightly window now include `purge_old_logs`, a sixth, which they had missed. No behaviour changed.
