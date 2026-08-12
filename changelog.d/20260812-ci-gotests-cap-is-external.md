### Fixed

#### Corrected a wrong attribution in `ci.yml` about which CI job kept getting cancelled

`ci.yml` carried a comment claiming that raising `Coverage Floor (PR gate)` to
`timeout-minutes: 35` fixed the repeated `conclusion=cancelled` failures seen in
#2311 and #2315. It did not.

Those cancellations were on `Minimal CI / Go Tests (short, race)`, which is not
defined in this repository at all — the `Minimal CI /` prefix on a check name
means the job comes from a called workflow, here
`falkcorp/github-common/.github/workflows/reusable-ci-minimal.yml`. That job
capped at 20 minutes while running `go test -short -race -timeout 30m ./...`,
so the runner killed it ten minutes before Go's own timeout could print the
goroutine dump naming the stuck test.

Measured on #2319: `Go Tests (short, race)` cancelled at 20m16s, while
`Coverage Floor (PR gate)` — the job that had actually been raised — completed
successfully in 13m21s in the very same run.

The real fix is upstream in falkcorp/github-common#346; that workflow exposes no
timeout input, so it could not have been fixed from this repo. The `35` here
stays, because it is independently correct for this job (`make test-short` runs
with `-timeout 25m`), but the comment now states the invariant without claiming
credit for someone else's bug.
