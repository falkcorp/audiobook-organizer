Added a run-state adapter and a task-run row mapper for the Activity frame
(`web/src/components/activity/coreRunStatus.ts`, `taskRunRow.ts`). Nothing imports
them yet; F9-03 binds them when the shell's tasks pane replaces the legacy store.
