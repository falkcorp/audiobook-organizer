<!-- file: docs/plans/2026-08-18-ci-lint-wiring-audit.md -->
<!-- version: 1.1.0 -->
<!-- guid: 2f6b8d43-9c15-4e70-a8b2-51d037e9c4a8 -->
<!-- last-edited: 2026-08-18 -->

# CI lint wiring — what actually runs today

Audit done before wiring the interface-width gate. Every claim below was checked against the
files, not assumed.

## Headline: `.golangci.yml` has never run in CI

`golangci-lint` appears in **zero** workflows — not in this repo's `.github/workflows/`, and not
in any ghcommon reusable workflow this repo calls:

| Workflow | golangci mentions |
|---|---|
| `audiobook-organizer/.github/workflows/**` | **0** |
| `ghcommon/reusable-ci-minimal.yml` | **0** |
| `ghcommon/reusable-ci.yml` | **0** |
| `ghcommon/reusable-security.yml` | **0** |

And `make ci` is `mocks-check staticcheck sdkguard test-all-short coverage-check-short` —
**staticcheck, not golangci-lint.** The only invocations are `make lint-errcheck` /
`lint-errcheck-full` (Makefile:593-597), which the Makefile comment says are "deliberately NOT
wired into `make ci`."

**Consequence for the width gate:** adding `interfacebloat` to `.golangci.yml` changes nothing on
its own. The config is a local, manual tool today. **A CI job that invokes golangci-lint is the
load-bearing part of the work, not the config edit.**

## Do we call a reusable CI job out of github-common? Yes — but it doesn't lint Go

`ci.yml:39` calls
`falkcorp/github-common/.github/workflows/reusable-ci-minimal.yml@85a29096`.

That reusable workflow's complete job list: **Go Vet & Build**, **Go Tests (short, race)**,
**Frontend Lint & Build**, **Frontend Unit Tests**. There is no Go lint step in it at all.

Note the local checkout at `~/repos/github.com/jdfalk/ghcommon` has a stale directory name — its
`origin` is `https://github.com/falkcorp/github-common.git`. Same repo, so the workflow refs are
correct; only the folder name is misleading.

## Super-linter: configured, referenced, and never executed

- `.github/linters/super-linter-{pr,ci}.env` exist here.
- **No workflow in this repo runs super-linter.** Only `.github/workflows/scripts/ci_workflow.py`
  mentions it (`load_super_linter_config`), and nothing invokes that path from a workflow.
- **No *reusable* ghcommon workflow runs it either.** `ghcommon/pr-automation.yml:130` does use
  `super-linter/super-linter@v8.6.0` — but that file has **no `workflow_call` trigger**, so it
  runs only inside ghcommon and cannot be called from here. `reusable-ci.yml` mentions
  `super-linter-*.env` only as a change-detection path filter, and has no super-linter step.

So super-linter has never bitched at anyone on this repo, and there is nothing to "turn on" —
it has to be added.

## The config pointers are broken independently of all the above

`.github/linters/super-linter-pr.env` — 4 of its 8 `*_CONFIG_FILE` entries point at files that do
not exist:

| Key | Points at | Reality |
|---|---|---|
| `GO_CONFIG_FILE` | `.github/linters/.golangci.yml` | **missing** — real file is at repo **root** |
| `MARKDOWN_CONFIG_FILE` | `.github/linters/.markdownlint.json` | **missing** — real file is at **root** |
| `JAVASCRIPT_PRETTIER_CONFIG_FILE` | `.github/linters/.prettierrc` | **missing** — real file is at **root** |
| `TYPESCRIPT_PRETTIER_CONFIG_FILE` | `.github/linters/.prettierrc` | **missing** — real file is at **root** |
| `PYTHON_ISORT_CONFIG_FILE` | `.github/linters/.isort.cfg` | **missing** — nowhere in repo |
| `PYTHON_BLACK_CONFIG_FILE` | `.github/linters/.python-black` | present in `.github/linters/` |
| `YAML_CONFIG_FILE` | `.github/linters/.yaml-lint.yml` | present in `.github/linters/` |
| `RUST_CONFIG_FILE` | `.github/linters/rustfmt.toml` | present in `.github/linters/` |

There is a **second, form-level bug**: super-linter resolves `*_CONFIG_FILE` as a **filename
relative to `LINTER_RULES_PATH`** (default `.github/linters`), not as a repo-root path. Evidence:
ghcommon's own working `super-linter-pr.env` uses bare filenames — `GO_CONFIG_FILE=.golangci.yml`,
`MARKDOWN_CONFIG_FILE=.markdownlint.json` — with those files at its repo root. So even the three
"present" rows above are the wrong *form*: `.github/linters/.python-black` would resolve to
`.github/linters/.github/linters/.python-black`.

**Fix (matches the instruction "point at the right files in the root"):** set
`LINTER_RULES_PATH=.` and reduce every `*_CONFIG_FILE` to a bare filename, then make sure each
named file is actually at the repo root. `.isort.cfg` has to be created or its key dropped.

Caveat I'd rather state than hide: I verified the bare-filename convention from ghcommon's own
config, not from super-linter's documentation. It's strong evidence, not proof — the first CI run
confirms it.

## Also worth fixing while here: reusable-workflow SHA drift

`ci.yml` pins `85a29096` (2026-08-12); `frontend-ci.yml`, `nightly.yml`, `security.yml`,
and the burndown/release/triage refs all still pin `d0c3326b` (2026-07-05). Two versions of the
same repo in one CI surface.

## Verified while wiring it (all four proven by running the tool, not reasoned)

**1. The errcheck blocker — real, and solved.** A CI step running bare `golangci-lint run ./...`
against the root config would have exited non-zero on **927 errcheck findings** before
interfacebloat was ever consulted, i.e. permanently red on day one. Fix: the width step passes
`--enable-only interfacebloat,nolintlint`, which overrides the config's enable list.
**Measured: 0 errcheck findings from the width selector.**

**2. The Makefile needed the same treatment.** `make lint-errcheck` ran bare
`golangci-lint run ./...`, so adding linters to the config would have silently destroyed the
attributability the config header exists to protect. Both targets now pass
`--enable-only errcheck`. **Measured: 927 before and after — unchanged.**

**3. My "28 violations" figure was wrong; the real number is 34.** The 28 came from a scratchpad
config with **no `issues:` block**, so golangci-lint's default `max-same-issues: 3` silently
collapsed identical messages. The root config sets `max-same-issues: 0` precisely to stop that.
The instrument, not the codebase, produced the smaller number — the baseline must be generated by
the exact config CI runs, and **34** is that number.

**4. `nolintlint` would have arrived with 113 findings.** Enabling it repo-wide flagged **108
unexplained `//nolint:errcheck`**, 3 malformed, and 2 `//nolint:gosec` — all pre-existing Wave 0
territory. A gate that shows up with 113 findings gets switched off by whoever sees it next, so an
exclusion scopes the explanation requirement to width overrides only, with a comment saying to
delete it when Wave 0 closes. **Measured: nolintlint 0, interfacebloat 34.**

Reproduce: `make lint-width-full` (34) and `make lint-errcheck-full` (927).

## Recommendation

1. **Add `interfacebloat` + `nolintlint` to the root `.golangci.yml`** (as instructed). Guard the
   errcheck attributability via `--enable-only` selectors on every invocation (done and proven
   above), not by splitting the config into two files.
2. **Add an opt-in `go_lint` input to ghcommon's `reusable-ci-minimal.yml`**, defaulting **false**,
   that runs `golangci-lint`. Opt-in matters: the reusable workflow is shared, and defaulting it on
   would start linting every other repo that calls it with whatever config it happens to have.
   Then set `go_lint: true` here and re-pin `ci.yml` to the new SHA.
   **Correction — I now recommend the local job first, not the ghcommon PR.** Routing through
   `reusable-ci-minimal.yml` means the gate cannot be mutation-tested until a PR merges in a repo
   this work does not control *and* the SHA is re-pinned here. PR 1 requires five mutation cases
   (including a real `BookFileStore` 27 → 8 split) before the gate counts as verified, and a local
   job in `ci.yml` can run all five today. Prove it here, upstream it to
   `reusable-ci-minimal.yml` as an opt-in `go_lint` input afterwards.
3. **Fix the super-linter env pointers** (`LINTER_RULES_PATH=.` + bare filenames) regardless —
   they're broken today whether or not super-linter ever runs.
4. **Do not also run super-linter for Go.** Super-linter's Go mode *is* golangci-lint; running both
   double-reports the same findings and roughly doubles CI time for no extra coverage. Super-linter
   earns its place for Markdown/YAML/shell, not as a second Go linter.
5. Re-pin the drifted SHAs in one pass.
