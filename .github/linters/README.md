<!-- file: .github/linters/README.md -->
<!-- version: 2.0.0 -->
<!-- guid: 7a3c2e19-4b8d-4f06-9c21-8e5f30d7b642 -->
<!-- last-edited: 2026-08-18 -->

# Linter configuration — MOVED TO THE REPO ROOT (2026-08-18)

The linter configs that used to live in this directory are now at the **repo root**:

| File | Now at |
|---|---|
| `.markdownlint.json` | repo root (was already there) |
| `.yaml-lint.yml` | repo root |
| `.python-black` | repo root |
| `.pylintrc` | repo root |
| `ruff.toml` | repo root |
| `clippy.toml` | repo root |
| `rustfmt.toml` | repo root |
| `super-linter-pr.env`, `super-linter-ci.env` | repo root |

## Why

Super Linter resolves every `*_CONFIG_FILE` as a **filename relative to
`LINTER_RULES_PATH`**, not as a repo-root path. The env files here carried values like
`.github/linters/.golangci.yml`, which resolved to
`.github/linters/.github/linters/.golangci.yml` — and four of them named files that did not
exist anywhere. Moving the configs to the root and setting `LINTER_RULES_PATH=.` makes every
pointer a bare filename that resolves, and matches the layout `falkcorp/github-common` uses
for its own configs.

`.golangci.yml` and `.prettierrc` were already at the root, which is part of how the split
went unnoticed: the pointers looked plausible and the files were simply somewhere else.

This directory is kept only for this note. Do not add new configs here.
