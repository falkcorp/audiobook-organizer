<!-- file: docs/agent-tasks/platform-core/R0-01-toolchain-bump-go1-27-3.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7189c037-92b9-4266-ac53-c6fb370da014 -->
<!-- last-edited: 2026-11-05 -->

# R0-01 — Toolchain bump to go1.27.3: every pinned copy, the prose copies, both image digests and the two stale Dependabot rules, in one PR

| Field | Value |
|---|---|
| ID | R0-01 |
| Release | R0 (the first PR of R0; 09 §9.4 R0 Content (2); 13 §13.2.2) |
| Tier | **T0** (Haiku / low): a mechanical edit of the guarded pin copies, proved by the guard script `scripts/check_toolchain_versions.py` |
| Wave | W01 |
| Depends on | none |
| Branch | `toolchain/go1.27.3` |
| Commit title | `toolchain: go1.27.3` (13 §13.2.2 names the PR this way) |

**Gate:** plan, then execute autonomously in your own worktree and PR. This is a text edit. Do not change any
Go code, any `go.mod` `go` directive, or any line this brief does not name. If anything below does not match
what you see, STOP and report; do not improvise.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>                         # the path of your audiobook-organizer clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/r0-01-toolchain" -b toolchain/go1.27.3 origin/main
cd "$REPO/.worktrees/r0-01-toolchain"
git rebase origin/main
npm ci --prefix web
```

Never edit the primary checkout or `main` (CLAUDE.md "Worktree Discipline"). Never run `go work init` (CLAUDE.md).

## Goal

Move the Go toolchain pin from `go1.27.2` to `go1.27.3` in every place that carries it, together, in one PR
(13 §13.2.1, §13.2.2): the 34 machine-checked copies, the 9 prose copies, the 2 distinct image digests (one
`golang:1.27.3-alpine` digest used on both Dockerfile `FROM` lines, one `golang:1.27.3-bookworm` digest on the
Woodpecker image line), and delete the two stale Dependabot ignore rules. Leave every "not a pin" line alone.
The `go 1.27.0` directive in `go.mod` does **not** change (13 §13.2.1 "Directive policy").

## Files (modify only these)

`Makefile`, `.envrc`, `.vscode/settings.json`, `Dockerfile`, `Dockerfile.build-cgo`,
`.github/workflows/ci.yml`, `.github/workflows/binary-smoke.yml`, `.github/workflows/codeql.yml`,
`.github/workflows/e2e.yml`, `.github/workflows/frontend-ci.yml`, `.github/workflows/nightly.yml`,
`.github/workflows/ratchet-lower.yml`, `.github/workflows/security.yml`,
`.github/workflows/vulnerability-scan.yml`, `.woodpecker/checks-build.yaml`, `.woodpecker/checks-lint.yaml`,
`.woodpecker/test-database.yaml`, `.woodpecker/test-fixtures-1.yaml`, `.woodpecker/test-fixtures-2.yaml`,
`.woodpecker/test-fixtures-3.yaml`, `.woodpecker/test-native-taglib.yaml`, `.woodpecker/test-rest.yaml`,
`.woodpecker/test-server-scanner.yaml`, `scripts/ci_remote.py`, `CLAUDE.md`, `agents/go-specialist.md`,
`skills/project-context/SKILL.md`, `Makefile.local.example`, `.github/codeql/README.md`,
`.github/dependabot.yml`; plus one new fragment `changelog.d/<date>_toolchain_go1_27_3.md` (no header).

## Re-verify before editing (expected counts at the base commit)

Run each command. If any count differs, STOP and report the command and its output.

```bash
# 1. Every 1.27.2 outside docs/ and CHANGELOG.md: expect 57
git grep -n -E '1\.27\.2' -- . ':!docs' ':!CHANGELOG.md' | wc -l
# 2. No 1.27.3 yet: expect 0
git grep -n -E '1\.27\.3' -- . ':!docs' ':!CHANGELOG.md' | wc -l
# 3. The canonical pin: expect exactly "43:export GOTOOLCHAIN := go1.27.2"
grep -n '^export GOTOOLCHAIN := ' Makefile
# 4. Workflow go-version copies: expect 17
grep -n "go-version: '1\.27\.2'" .github/workflows/*.yml | wc -l
# 5. Woodpecker GOTOOLCHAIN copies: expect 9
grep -n 'GOTOOLCHAIN: go1\.27\.2$' .woodpecker/*.yaml | wc -l
# 6. Dockerfile FROM lines with the alpine digest: expect 2
grep -n '^FROM golang:1\.27\.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS go-builder$' Dockerfile Dockerfile.build-cgo | wc -l
# 7. Woodpecker image line: expect 1 (checks-lint.yaml:50)
grep -n '&go_image golang:1\.27\.2-bookworm@sha256:' .woodpecker/checks-lint.yaml | wc -l
# 8. The guard passes on the base: expect the last line "Toolchain versions consistent" and exit 0
python3 scripts/check_toolchain_versions.py; echo "exit $?"
# 9. The two stale Dependabot rules: expect 2 lines (33 and 86 at the base)
grep -n 'dependency-name: "golang"\|dependency-name: "vite"' .github/dependabot.yml
```

At the spec's evidence commit the two historical Makefile comments were at `Makefile:463,507`
(13 §13.2.2 "Not a pin"); at HEAD they are at `Makefile:467,511`. Every other anchor in 13 §13.2.2 is unchanged at
HEAD.

## Steps

1. **Resolve the two image digests** (13 §13.2.2: "must be resolved at bump time"):

   ```bash
   ALPINE=$(docker buildx imagetools inspect golang:1.27.3-alpine | awk '/^Digest:/{print $2; exit}')
   BOOKWORM=$(docker buildx imagetools inspect golang:1.27.3-bookworm | awk '/^Digest:/{print $2; exit}')
   echo "$ALPINE"; echo "$BOOKWORM"
   ```

   Each must print `sha256:` followed by exactly 64 lowercase hex characters (check:
   `echo "$ALPINE" | grep -cE '^sha256:[0-9a-f]{64}$'` prints `1`; same for `$BOOKWORM`). If docker is missing,
   the command fails, or either value does not match, **STOP and report**. Never invent or reuse a digest.

2. **Canonical pin, direnv, editor, remote CI script:**

   ```bash
   sed -i 's/^export GOTOOLCHAIN := go1\.27\.2$/export GOTOOLCHAIN := go1.27.3/' Makefile
   sed -i 's/^export GOTOOLCHAIN=go1\.27\.2$/export GOTOOLCHAIN=go1.27.3/' .envrc
   sed -i 's/"GOTOOLCHAIN": "go1\.27\.2"/"GOTOOLCHAIN": "go1.27.3"/g' .vscode/settings.json
   sed -i 's/^GO_TOOLCHAIN = "go1\.27\.2"/GO_TOOLCHAIN = "go1.27.3"/' scripts/ci_remote.py
   ```

3. **GitHub Actions (17 lines in 9 files):**

   ```bash
   sed -i "s/go-version: '1\.27\.2'/go-version: '1.27.3'/" .github/workflows/ci.yml \
     .github/workflows/binary-smoke.yml .github/workflows/codeql.yml .github/workflows/e2e.yml \
     .github/workflows/frontend-ci.yml .github/workflows/nightly.yml .github/workflows/ratchet-lower.yml \
     .github/workflows/security.yml .github/workflows/vulnerability-scan.yml
   ```

   This leaves the comment `ci.yml:525` ("go1.27.2 writes version 5") unchanged, as required.

4. **Woodpecker (9 GOTOOLCHAIN lines, 1 image line, 1 prose comment):**

   ```bash
   sed -i 's/GOTOOLCHAIN: go1\.27\.2$/GOTOOLCHAIN: go1.27.3/' .woodpecker/*.yaml
   sed -i "s|&go_image golang:1\.27\.2-bookworm@sha256:[0-9a-f]\{64\}|\&go_image golang:1.27.3-bookworm@${BOOKWORM}|" .woodpecker/checks-lint.yaml
   sed -i 's/runs gofmt 1\.27\.2,/runs gofmt 1.27.3,/' .woodpecker/checks-build.yaml
   ```

   This leaves `checks-lint.yaml:73` ("cannot read go1.27.2 export data") unchanged, as required.

5. **Dockerfiles (tag and digest on the `FROM` line, and the comment directly above it):**

   ```bash
   for f in Dockerfile Dockerfile.build-cgo; do
     sed -i "s|^FROM golang:1\.27\.2-alpine@sha256:[0-9a-f]\{64\} AS go-builder$|FROM golang:1.27.3-alpine@${ALPINE} AS go-builder|" "$f"
     sed -i "s|^# SHA pinned 2026-10-09 (golang:1\.27\.2-alpine manifest-list)\.|# SHA pinned $(date +%Y-%m-%d) (golang:1.27.3-alpine manifest-list).|" "$f"
   done
   ```

6. **Prose copies (one line each):**

   ```bash
   sed -i 's/every build path pins `go1\.27\.2`/every build path pins `go1.27.3`/' CLAUDE.md
   sed -i 's/toolchain pinned `go1\.27\.2` everywhere/toolchain pinned `go1.27.3` everywhere/' agents/go-specialist.md
   sed -i 's/toolchain pinned go1\.27\.2 in/toolchain pinned go1.27.3 in/' skills/project-context/SKILL.md
   sed -i 's/(`export GOTOOLCHAIN := go1\.27\.2`)/(`export GOTOOLCHAIN := go1.27.3`)/' Makefile.local.example
   sed -i 's/GOTOOLCHAIN=go1\.27\.2/GOTOOLCHAIN=go1.27.3/g' .github/codeql/README.md
   ```

   Do **not** touch the "not a pin" lines of 13 §13.2.2: `Makefile:467,511` (HEAD), `ci.yml:525`,
   `.woodpecker/checks-lint.yaml:73`, `internal/diag/flight/flight.go:628`, `flight_test.go:494`,
   `internal/pathutil/protected.go:52`, `scripts/check-errcheck-ratchet.sh:121`,
   `scripts/check-interface-width.sh:128`, `scripts/setup-mockery.sh:11`, the file-header versions `1.27.2` in
   `internal/audiobooks/audiobook_service_unit_test.go:2` and `internal/config/persistence_test.go:2`, and
   `scripts/check_toolchain_versions.py:39`, `scripts/tests/test_check_toolchain_versions.py:31`.

7. **Dependabot, two stale rules** (13 §13.2.2 "Two stale Dependabot rules"). In `.github/dependabot.yml`:
   - delete the five lines 30–34 (the docker entry's whole `ignore:` block: the `ignore:` key, the two comment
     lines "Go 1.27 does not compile this repository …", `- dependency-name: "golang"` and
     `versions: [">=1.27"]`). The docker entry then ends with its `labels:` list;
   - delete the ten lines 80–89 of the npm entry (the six comment lines starting "Hard-pin the build toolchain.
     vite 8 …", and the two entries `vite` `>=8.0.0` and `@vitejs/plugin-react` `>=5.0.0`). Keep the
     `"*"` semver-major rule above them and the jsdom rule below them.

   Both deletions in one command (sed numbers lines of the input, so the two ranges are the base line numbers;
   run it once only, after re-verify item 9 printed lines 33 and 86):

   ```bash
   sed -i '80,89d;30,34d' .github/dependabot.yml
   ```

   Check: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/dependabot.yml'))" && echo ok` prints
   `ok`, and `grep -c 'dependency-name: "golang"\|dependency-name: "vite"\|dependency-name: "@vitejs/plugin-react"' .github/dependabot.yml`
   prints `0`.

8. **File headers.** In every file you changed that has a header, bump the patch of `version:` by one and set
   `last-edited:` to today's date (`.standards/instructions/file-headers.md`). The files with headers are:
   `Makefile`, `.envrc`, `Dockerfile`, `Dockerfile.build-cgo`, the 9 workflows, the 9 Woodpecker files,
   `scripts/ci_remote.py`, `CLAUDE.md`, `agents/go-specialist.md`, `skills/project-context/SKILL.md` (front-matter
   `version:`), `Makefile.local.example`, `.github/codeql/README.md`, `.github/dependabot.yml`.
   `.vscode/settings.json` has no header; leave it without one.

9. **Changelog fragment** `changelog.d/<YYYY-MM-DD>_toolchain_go1_27_3.md`, no header (CLAUDE.md fragment rule),
   in the format of `changelog.d/README.md`, one line: "Go toolchain pin moved from go1.27.2 to go1.27.3 in every
   copy, with re-resolved image digests; two stale Dependabot ignore rules removed."

10. **Preconditions of the bump PR** (13 §13.2.4, §13.2.3: run, do not assume). Run in the worktree:

    ```bash
    make test                       # full backend suite with -race
    make fixtures && make test-fixtures
    make ci
    ```

    All three must exit 0. If any fails, STOP and report the failing package and output; do not edit code.

## Code contract

None: this PR changes no Go, TypeScript or Python code. The guard script is unchanged (its core-related
changes, 13 §13.2.2 items 1–4, belong to R1).

## Tests to write first

None. The owned gate is 13's pin check, `scripts/check_toolchain_versions.py` (09 §9.4.1 row 1), which already
exists and must pass after the bump.

## Acceptance (commands and expected output)

```bash
git grep -n -E '1\.27\.2' -- . ':!docs' ':!CHANGELOG.md' ':!changelog.d' | wc -l   # expect 14 (the "not a pin" lines only)
git grep -n -E '1\.27\.3' -- . ':!docs' ':!CHANGELOG.md' ':!changelog.d' | wc -l   # expect 43 (34 pins + 9 prose)
grep -n '^export GOTOOLCHAIN := go1.27.3$' Makefile                   # expect 1 hit, line 43
grep -n '^go 1.27.0$' go.mod                                          # expect 1 hit (unchanged)
python3 scripts/check_toolchain_versions.py; echo "exit $?"            # last lines: "Toolchain versions consistent", "exit 0"
python3 -m unittest scripts.tests.test_check_toolchain_versions 2>&1 | tail -1   # expect "OK"
grep -c "@${ALPINE} AS go-builder" Dockerfile Dockerfile.build-cgo     # expect 1 and 1
grep -c "golang:1.27.3-bookworm@${BOOKWORM}" .woodpecker/checks-lint.yaml   # expect 1
make test; echo "exit $?"                                             # expect exit 0
make test-fixtures; echo "exit $?"                                    # expect exit 0
make ci; echo "exit $?"                                               # expect exit 0
```

Every required check of 09 §9.2.1 that runs on this PR is green on an up-to-date branch before merge.

## Out of scope

- The guard's core changes (tier 3 reading `core/go.mod`, the `core/Makefile` parity rule, the no-literal check;
  13 §13.2.2 items 1–4): R1-01.
- `go.mod` `go` directive, `tools/*/go.mod`, `.github/repository-config.yml` (`'1.27'`) and
  `.github/workflow-config.yaml` (13 §13.2.2 "Unchanged by a patch bump").
- Any Go code change, any dependency bump, any other Dependabot rule.

## Rollback

Revert the single commit (`git revert <sha>` in a new PR under the same checks). Nothing persists outside the
repository.

## Repo rules

- File headers bumped on every changed file that has one; `changelog.d/` fragments carry no header.
- Conventional commit, title `toolchain: go1.27.3`, body listing the groups changed, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 defines no release or flip trailer (09 §9.2.1); add none.
- `gh pr create`, then after every required check is green: `gh pr merge <n> --rebase`. Never squash, never
  `--admin` past a required check.
- Remove the worktree after the merge: `git -C "$REPO" worktree remove "$REPO/.worktrees/r0-01-toolchain"`.
