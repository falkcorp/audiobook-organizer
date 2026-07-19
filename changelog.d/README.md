<!-- file: changelog.d/README.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8d3a1f26-4c7b-4e59-b0a2-6f1d9c8e5a34 -->
<!-- last-edited: 2026-07-16 -->

# Changelog fragments (`changelog.d/`)

`CHANGELOG.md` is **assembled** from the fragment files in this directory — you
do **not** edit `CHANGELOG.md` by hand. Every change that is worth recording
drops a small, uniquely-named Markdown fragment here instead. At release time
the automation folds all fragments into a new dated section of `CHANGELOG.md`
and deletes them.

This exists because many contributors and AI agents open PRs in parallel. If
everyone edited the single `## [Unreleased]` block in `CHANGELOG.md` directly,
every PR would collide. A fragment-per-change means PRs never touch the same
file, so there are no changelog merge conflicts.

> This is a focused revival of the retired JSON "doc-update" system, using the
> maintained open-source [`scriv`](https://scriv.readthedocs.io/) tool instead
> of bespoke scripts.

## Add a fragment

```bash
pip install scriv           # first time only (also in requirements.txt)
scriv create                # writes changelog.d/<timestamp>_<branch>.md
```

Open the generated file, uncomment the section(s) that apply, and fill them in.
You can also just create the file by hand following the format below. A CI check
fails any PR that changes code without adding a fragment.

## Format

A fragment is a slice of Markdown grouped under Keep a Changelog category
headings. Give each entry a `####` title and a short explanatory paragraph — a
changelog carries **more detail than a release note**: what changed, why, and
any impact or reproduction.

```markdown
### Fixed

#### `reusable-release.yml` — cleanup step no longer deletes the stable release

The cleanup step deleted the just-created stable release when its tag showed up
in the "drafts or prereleases" set. Added a guard that skips any tag matching
the new stable version.
```

- **Categories:** `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`,
  `Security` (Keep a Changelog).
- **Conventional-commit → category:** `feat` → Added, `fix` → Fixed,
  `perf`/`refactor` → Changed; deprecations → Deprecated, removals → Removed,
  security fixes → Security.
- One fragment per logical change. Use several category sections in one fragment
  only when a single change genuinely spans them.
- Fragments are **exempt from the file-header rule** — do not add the
  `file`/`version`/`guid` header (it would leak into `CHANGELOG.md`).

## How assembly works

- `scriv collect --version <tag>` (run by the shared release workflow
  `reusable-release.yml` on stable releases) inserts a new
  `## <version> — <date>` section at the `<!-- scriv-insert-here -->` marker in
  `CHANGELOG.md`, grouped by category, and removes the collected fragments.
- The PR fragment check comes from this repo's `changelog-check.yml` workflow.
  Both the check and the collect step activate for any repo that has this
  `changelog.d/scriv.ini`.
- Configuration lives in [`scriv.ini`](scriv.ini); the new-fragment scaffold is
  [`templates/new_fragment.md.j2`](templates/new_fragment.md.j2).
- GitHub **release notes** stay commit-based; this detailed changelog is the
  separate, richer artifact.
