### Fixed

- **RC cleanup now actually runs.** `cleanup-rc-releases.yml` had never
  succeeded once in 300 recorded runs (297 skipped, 3 failed). It passed
  `--arg` to `gh --jq`, which only real `jq` accepts, so every stable release
  died with `unknown command "base" for "gh release list"`. Old release
  candidates piled up unchecked — 268 of them, 180 for `v0.218.1` alone —
  which in turn broke two things downstream: `gh release list`'s default
  30-item cap could no longer see existing drafts (producing duplicate
  same-tag drafts), and goreleaser's `{{ .PreviousTag }}` resolved to an RC
  instead of the previous stable, so release notes diffed from the wrong base.
  The enumeration now uses `gh api --paginate` instead of a fixed `--limit`,
  which had its own truncation bug against this repo's 460+ releases.
  Added a `workflow_dispatch` entry point with a `dry_run` input so the
  delete path can be exercised safely before it is armed.
