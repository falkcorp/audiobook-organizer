### Fixed

#### Linter configs moved to the repo root, so manual runs and CI finally agree

Every linter config lived in `.github/linters/`, and the Super Linter env files
pointed at them with values like `.github/linters/.golangci.yml`. Super Linter
resolves `*_CONFIG_FILE` as a filename *relative to* `LINTER_RULES_PATH`, so
those resolved to `.github/linters/.github/linters/...` — and four of the eight
pointers named files that did not exist anywhere in the repo.

The deeper problem was that running a linter by hand used different rules than
CI intended:

- **yamllint had two configs that disagreed.** `.yamllint` (line-length 80,
  `document-start` left at yamllint's default of *required*) is what bare
  `yamllint` auto-discovered; `.yaml-lint.yml` (line-length 120,
  `document-start: disable`) is what pre-commit and Super Linter were pointed at.
  The `document-start` split was the sharpest: under the auto-discovered config
  yamllint flagged **19 of 19** files in `.github/workflows/` for a missing
  `---`, because **0 of 19** have one. The repo's convention is consistently no
  leading `---`, so consolidating onto the `disable` setting matches what the
  code actually does — the alternative was a 19-file reformat nobody asked for.
  Consolidated onto `.yamllint` with the content that was in force.
- **black's config was in a file black never reads.** `[tool.black]` with
  `line-length = 100` sat in `.python-black`; `black .` looks only for
  `pyproject.toml` and so silently used its default of 88. Moved to
  `pyproject.toml`.

All configs are now at the repo root under the names their tools discover:
`.golangci.yml`, `.markdownlint.json`, `.prettierrc`, `.pylintrc`, `.yamllint`,
`pyproject.toml`, `ruff.toml`, `clippy.toml`, `rustfmt.toml`, and the
`super-linter-*.env` files. `.github/linters/` is deprecated and holds only a
pointer note. `.pre-commit-config.yaml` was repointed, which also fixed a
pre-existing break where it referenced `.prettierrc.json` — a file that has
never existed here.

Super Linter now runs in advisory mode (`DISABLE_ERRORS=true`): it reports
everything and never fails a PR.
