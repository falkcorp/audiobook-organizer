<!-- file: docs/agent-tasks/platform-core/PC-08-03-release-callers-build-web.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6afd75a4-6a26-476e-8b4d-bd9c7c178c27 -->
<!-- last-edited: 2026-11-04 -->

# PC-08-03 — Release callers call build_web.sh

**Full title:** Release callers: both `pre-build-script`s call `build_web.sh`; `frontend-enabled` settled from
the owner's reading of `reusable-release.yml`.

| Field | Value |
|---|---|
| ID | PC-08-03 |
| Tier | **T1 (Sonnet, medium).** Two workflow edits whose shape is chosen by a recorded owner reading; this brief maps each recorded choice to exact edits. |
| Wave | W08 (UI-0, release `UI0`) |
| Depends on | PC-08-02, OWNER-06 (its recorded reading of `reusable-release.yml`) |
| Trailer | `Stage-Release: UI-0 1` |
| Normative digest | `docs/plans/platform-core/digests/UI-0.md` (behaviour rule 6) |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-08-03 -b ci/pc-08-03-release-callers origin/main
cd "$REPO/../audiobook-organizer-pc-08-03"
git rebase origin/main
git worktree list
```

Use `git push` for workflow files, never the MCP contents API (CLAUDE.md "GitHub Operations").

## Step 0 — the owner's record (STOP if absent)

```bash
grep -rn -i -A6 'reusable-release.yml' docs/plans/platform-core/stage-ledger/   # OWNER-06 records it (its brief, step 11)
```

The record must state, for `reusable-release.yml` at the SHA in `.github/ghcommon-ref.txt`:
(1) what `frontend-enabled: true` does (does its frontend half run `npm ci` or a build in `web/`?), and
(2) the owner's choice, exactly one of:

- **Choice A** — set `frontend-enabled: false`, with a sentence saying where Node comes from for
  `pre-build-script` (for example "the Go half's runner image has Node 26" or "a setup-node step the reusable
  workflow runs when `pre-build-script` is set");
- **Choice B** — keep the reusable frontend half and use a github-common pre-install input, naming the input,
  the new github-common SHA and the github-common PR that adds it;
- **Choice C** — the frontend half does **not** install or build `web/`: only the `pre-build-script` change is
  needed.

If the record is missing, or names none of A, B, C, STOP: this is an owner checkpoint (09 §9.2.1) and no agent
may read `reusable-release.yml` in its place.

## Re-verify (HEAD anchors; the spec cites 748fd787a with the same lines)

```bash
cat .github/ghcommon-ref.txt                                  # HEAD: 861d4e83ccf918c9354a2d5f5685068f655cdf58
grep -n 'reusable-release.yml@\|frontend-enabled\|pre-build-script' .github/workflows/prerelease.yml
#   HEAD: :34 uses: …/reusable-release.yml@861d4e83…; :40 frontend-enabled: true; :51 pre-build-script: 'cd web && npm ci && npm run build'
grep -n 'reusable-release.yml@\|frontend-enabled\|pre-build-script' .github/workflows/release-prod.yml
#   HEAD: :45 uses: …@861d4e83…; :52 frontend-enabled: true; :58 pre-build-script: 'cd web && npm ci && npm run build'
ls scripts/ci/build_web.sh core/web/package.json              # both listed (PC-01-06, PC-08-02)
```

Two exact `pre-build-script` hits and two `frontend-enabled: true` hits are required; any other count, STOP.

## Exact files and edits

In both `.github/workflows/prerelease.yml` and `.github/workflows/release-prod.yml`:

1. Replace `pre-build-script: 'cd web && npm ci && npm run build'` with
   `pre-build-script: 'sh scripts/ci/build_web.sh deps && cd web && npm run build'`.
2. By the owner's choice:
   - **A:** `frontend-enabled: true` → `frontend-enabled: false`, and add a comment line above it quoting the
     record's Node-source sentence.
   - **B:** keep `frontend-enabled: true`; add the named pre-install input with the value
     `sh scripts/ci/build_web.sh deps`; change both `uses: …/reusable-release.yml@<sha>` to the record's new SHA
     (and its trailing comment's date and PR); write the same SHA into `.github/ghcommon-ref.txt`. Every other
     caller pinned to the old SHA in `.github/workflows/` moves with it (CLAUDE.md: move the pin together):
     `grep -rln '861d4e83ccf918c9354a2d5f5685068f655cdf58' .github/` lists them; update every hit.
   - **C:** no further edit.
3. Bump both workflow headers (`# version:` patch, `# last-edited:`). `.github/ghcommon-ref.txt` has no header.

## Code contract and tests

The contract is the two `with:` inputs of each caller (`pre-build-script`, `frontend-enabled`) and, for choice B,
the input the owner names. No unit test exists for a reusable-workflow call; the verification below is the test.

## Verification (09 UI-0 "Done when": a dry-run RC build that leaves `git status` clean)

`prerelease.yml` runs only on push to `main` and has no dry-run input, so do this locally from a clean clone of
the branch, which is the same command sequence the reusable workflow runs:

```bash
CLONE=$(mktemp -d) && git clone --quiet --branch ci/pc-08-03-release-callers "$REPO" "$CLONE" && cd "$CLONE"
sh -c 'sh scripts/ci/build_web.sh deps && cd web && npm run build'     # exit 0
git status --porcelain                                                  # empty
test -f web/dist/index.html && echo dist-ok                             # dist-ok
command -v goreleaser >/dev/null && goreleaser build --snapshot --clean --single-target   # exit 0 when goreleaser is installed
```

The CI-side dry run (an RC through `prerelease.yml`) is the first RC cut after merge; OWNER-09 records it
(Planner notes).

## Acceptance

```bash
grep -c "pre-build-script: 'sh scripts/ci/build_web.sh deps && cd web && npm run build'" .github/workflows/prerelease.yml .github/workflows/release-prod.yml   # 1 and 1
grep -c "cd web && npm ci" .github/workflows/prerelease.yml .github/workflows/release-prod.yml     # 0 and 0
python3 -c "import yaml,sys;[yaml.safe_load(open(f)) for f in sys.argv[1:]];print('yaml-ok')" .github/workflows/prerelease.yml .github/workflows/release-prod.yml   # yaml-ok
make ci                                                                  # exit 0
```

Choice B only: `grep -rn '861d4e83ccf918c9354a2d5f5685068f655cdf58' .github/` prints nothing and
`cat .github/ghcommon-ref.txt` prints the new SHA.

## Out of scope

The CI callers `ci.yml`, `frontend-ci.yml`, `nightly.yml` (`run-frontend: false`, done at S0 by PC-01-06);
editing github-common itself (the owner's PR there, for choice B).

## Rollback

Revert the commit: the release callers return to `cd web && npm ci && npm run build`, which fails once `web/`
depends on the unbuilt `file:` package, so revert together with PC-08-02 or not at all.

## Repo rules

- Commit `ci(release): build core/web before web in both release callers (UI-0)`, body ending with
  `Stage-Release: UI-0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- Pin GitHub Action references to SHAs (CLAUDE.md). `gh pr merge <n> --rebase`; never squash; never `--admin`.

## Planner notes

- **No CI dry-run path exists.** 09 UI-0 asks for "a dry-run RC build through `prerelease.yml`", but the
  workflow has only a `push` trigger. The brief verifies the identical command locally and leaves the real RC to
  the owner; the owner may instead add a temporary `workflow_dispatch` with `draft: true`, which would be its own
  reviewed change.
- **OWNER-06's brief records only what `frontend-enabled` does,** not the owner's choice between
  `frontend-enabled: false`, a pre-install input with a ref bump, or no change (09 UI-0 "Preconditions": "owner
  decides"). This brief STOPs without the choice; OWNER-06 should record it too.
- **Trailer after `release: UI0`:** same ambiguity as PC-08-02's note on check (b).
