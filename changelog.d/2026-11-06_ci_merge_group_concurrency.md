### Fixed

- CI: merge-queue runs of `ci.yml` get a concurrency group of their own and are never cancelled in progress. The queue reuses one `gh-readonly-queue/main/pr-<n>-<base>` ref per entry, and the shared per-ref group with `cancel-in-progress: true` let a second run or a re-run attempt on that ref cancel the running one ("Canceling since a higher priority waiting request ... exists"). The queue counts a cancelled required check as failed, so it dropped #4394 twice. Pull-request and push runs keep the per-ref cancel.
