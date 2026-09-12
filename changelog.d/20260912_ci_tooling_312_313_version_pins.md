### Fixed

#### `security.yml` — npm dependency submission now resolves under Node 22, not 20.x (CI-03)

The `dependencies-npm` job pinned `node-version: '20.x'` while every other
workflow and `.github/repository-config.yml` (`versions.node: ['22']`) use 22.
Engine-dependent resolution (optional and platform dependencies) could make the
submitted npm dependency graph differ from the one CI builds. The job now uses
`'22'`, and the version-consistency gate below rejects any workflow
`node-version` that differs from `versions.node`.

#### Toolchain version-consistency check now fails on drift and covers every pin (CI-04)

The "Check version consistency" step in `test-action-integration.yml` truncated
`go.mod` to major.minor, compared it only against the first `go-version:` in
`ci.yml`, never read `.envrc`, the Dockerfiles or `.vscode/settings.json`, and
only emitted `::warning::`, so a patch-level Go drift reported itself and then
passed. It now runs `scripts/check_toolchain_versions.py`, which fails the job on
any mismatch: the `Makefile` `GOTOOLCHAIN` pin must equal `.envrc`, both
`.vscode` entries and every `FROM golang:` stage exactly (with matching image
digests); every workflow `go-version` and `repository-config.yml` `versions.go`
must equal the pin's major.minor; `go.mod`'s `go` directive must sit on the same
line at or below the pin, with no `toolchain` directive; and every workflow
`node-version` plus the frontend-config action output must equal `versions.node`.
The workflow's path filter now also covers those files, so a drift in any of
them triggers the check. Tests: `scripts/tests/test_check_toolchain_versions.py`.
