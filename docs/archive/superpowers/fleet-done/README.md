<!-- file: docs/archive/superpowers/fleet-done/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2b6654c1-44fb-4ebd-b5eb-87af83b9f8de -->
<!-- last-edited: 2026-08-11 -->

# Fleet tasks — completed (archived 2026-08-11)

28 fleet workstreams whose status file records `Status: DONE` with a merged commit or PR.
Each is a **pair**: the brief in [`tasks/`](tasks/) and its outcome in [`status/`](status/),
sharing a numeric prefix (`005-sec-1-safepath-package.md` ↔
`005-sec-1-safepath-package-status.md`).

Moved from `docs/superpowers/fleet-tasks/` and `docs/superpowers/fleet-status/`. **Zero files
outside those two directories referenced any of them**, so the move broke no links — verified
by grepping every basename repo-wide before the move.

## What stayed live

The 10 `NOT_STARTED` pairs remain in `docs/superpowers/fleet-{tasks,status}/`, because for
unexecuted work the brief is the only surviving record and `TODO.md` does not track them:

`024-ops-1-11-async-embed-batch-api` · `028-acoustid-dedup-1` · `029-acoustid-compare-1` ·
`030-arch-4-10-service-tests` · `031-arch-4-12-isp-interfaces` · `032-arch-4-13-itunes-extract` ·
`033-arch-6-4-itl-partial-export` · `034-arch-7-1-tag-policies` · `035-arch-7-9-itunes-rebuild` ·
`036-fe-1-16-resizable-columns`

`fleet-status/README.md` also stayed — it documents the status-file convention. Note that it
contains a `Status: DONE` example, so a naive `grep -l 'Status:.*DONE' fleet-status/*.md`
matches it and would sweep the explanation of the system into the archive along with the
system. The move used verified brief↔status pairing instead.

## Contents

- 28 briefs in `tasks/`
- 28 status files in `status/`
- 56 files total; 27 of them carry no `<!-- file: -->` header (they never had one), so no
  header rewriting was needed beyond `013-rate-5-bulk-rating.md`.
