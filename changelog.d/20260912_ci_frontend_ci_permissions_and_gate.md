### Security

#### `frontend-ci.yml` — permission ceiling narrowed to `contents: read` (CI-02)

The Frontend CI workflow granted `contents`, `actions`, `packages`, `id-token`
and `attestations` write plus `checks: write`, with no stated reason, and that
set was the ceiling for every job of the external `falkcorp/github-common`
reusable CI workflow it calls. At the pinned SHA that reusable workflow, and
the cache workflow it nests, declare `contents: read` on every job, so the
wider grants were never used. The workflow now grants `contents: read` only,
with a comment recording why and what to re-check when the pin moves.

### Fixed

#### `frontend-ci.yml` — frontend coverage can no longer be skipped silently (CI-06)

The `Frontend Build and Test` job runs only when the external
`gha-get-frontend-config` action reports `has-frontend == 'true'`. If that
output came back empty or unexpected, the job was skipped, and a skipped job
reads as green, so a PR could pass with no frontend build or test run. A new
`Frontend CI Gate` job always runs (unless the run is cancelled) and fails
unless config detection succeeded, `has-frontend` is exactly `true`, and the
frontend job succeeded.
