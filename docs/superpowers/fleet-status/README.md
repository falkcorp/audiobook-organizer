<!-- file: docs/superpowers/fleet-status/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: dbdb0be5-7ee7-4928-bc72-6147a5d78d5b -->
<!-- last-edited: 2026-08-22 -->

# Fleet Task Status

Each file here tracks the status of one fleet task. Agents write to their own
status file as they work. The orchestrator (main Claude session) reads all files
to know what is in-flight, done, or failed.

## File naming
`<task-id>-status.md` — e.g. `008-sec-4-path-injection-itunes-status.md`

## Status values
- `DONE` — merged to main
- `IN_PROGRESS` — agent is currently working on it
- `FAILED` — agent errored; reason in file
- `SKIPPED` — intentionally not done (see reason)

## Format
```
Status: DONE
PR: #<number>
Commit: <sha>
Merged: <date>
Notes: <anything notable>
```
