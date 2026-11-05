<!-- file: docs/agent-tasks/platform-core/F2-12-r2-corpus-stage1-r0-written.md -->
<!-- version: 1.1.4 -->
<!-- guid: dd68df55-0fe6-4120-9078-f5f927fbe5b4 -->
<!-- last-edited: 2026-11-05 -->

# F2-12 — R2 fixture corpus, stage 1: a committed script drives the R0 release binary through its API over a seeded store, and the R0 binary records the answers (effective config, schedules, search, ABS responses; reach is F4-07's)

| Field | Value |
|---|---|
| ID | F2-12 |
| Release / flip | R2, foundation PRs (F2); testdata and scripts only, no product code |
| Wave | W24 |
| Tier | **T3** (Opus / high): the oracle corpus every R2 PR migrates; its coverage decides what the cutover tests can prove, and it is regenerated only if R0 is re-cut |
| Depends on | OWNER-03 (and, transitively, OWNER-02: the R0 tag exists and is kept) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-12-r2-corpus-stage1
git -C "$REPO" fetch origin --tags
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

## Goal

Replace the previous-build oracle with one written **by the R0 binary itself** (09 §9.3 item 1; 14 E1, E18): a committed
Python script starts the R0 release binary on a fresh, seeded store, drives it **only through its own API** (HTTP routes,
and the CLI flags, env and config file it reads) to create every kind of row the R2 cutover steps M2–M6 read that R0's API
can write, stops it cleanly, and commits the store, its state directory and a manifest as testdata. A second committed
script boots the R0 binary on that store and records **its answers** (effective config, schedules, search results, ABS
responses) as testdata. Every later R2 PR migrates this corpus and compares against these answers.

Not this PR: the hand-built overlay for cases R0's API cannot write (F2-13), the production-shaped store (F2-14), the
cutover-suite harness (F2-15), the reach oracle (F4-07, `scripts/r2corpus/record_r0_reach.py`, which records reach over
this corpus).

## Spec to read first

09 §9.3 item 1 (the whole paragraph), §9.2.3 (the steps M1–M8 and what each moves), §9.4 R2 "Verification", §9.7 (P-rows:
what production holds); 05-storage §5S.3.13 "Tests" (`TestCutoverOnR0Corpus`), §5S.9.1 (the families each step moves);
05-config §1.12.2 "Oracle and tests" (stage 1: what the config oracle records); 05-tasks §5T.9 `TestCutover_TasksFixtureFromR0`
(the run states the corpus should hold) and the "(ABO)" rows; 05-authn §10 (identity cutover tests over the R0-written
fixture); 05-rbac §12 `TestIdentityCutover_ReachOracle` (reach is F4-07's); 14 E1, E18.

## Inputs (owner-provided; STOP if missing)

- The **R0 tag** that OWNER-02 cut and kept. OWNER-02 records it in the tracking issue as the line
  `R0 tag for F2-12: <tag>`; read it from there (`gh issue view <tracking issue> --comments`). If that line is missing,
  STOP and report. `git tag -l` must list the tag. This PR commits it as `ci/r0_release.txt` (below), so from this PR
  on the repo records it and no later row needs it handed over.
- The R0 release binary for linux/amd64, fetched from that tag's GitHub release (`gh release download <tag> --pattern
  '*linux*amd64*'`), checked with `./audiobook-organizer --version` (and `--version-json` if R0 has it). If the release has
  no such asset, STOP: building at the tag is not the same binary (09 §9.3 says "the R0 release binary").

## Base state

```bash
R0_TAG=<the tag from OWNER-02's tracking-issue line>
git tag -l | grep -c "^$R0_TAG$"                          # 1, else STOP
test ! -e ci/r0_release.txt && echo absent                 # "absent" (created here)
test -d internal/books && echo ok                          # "ok" (R1-15), else STOP
test ! -e internal/books/testdata/r2corpus && echo absent  # "absent"
test ! -e scripts/r2corpus && echo absent                  # "absent"
git check-ignore -v --no-index internal/books/testdata/r2corpus/stage1/audiobooks.pebble/000002.log internal/books/testdata/r2corpus/stage1/state/.encryption_key internal/books/testdata/r2corpus/stage1/library.bleve/x internal/books/testdata/r2corpus/stage1/logs/x.log internal/books/testdata/r2corpus/stage1/x.db   # 5 lines at ac4368bdb: audiobooks.pebble/, .encryption_key, library.bleve/, logs/, *.db (each one ignored today)
ls scripts/abs_capture_fixtures.py                         # exists at ac4368bdb (an existing ABS capture script to learn from)
grep -n 'db-census' internal/server/handlers/diagnostics.go | head -2   # the census route 09 §9.7 P1 uses (read-only)
```

## Files

Create: `scripts/r2corpus/write_r0_corpus.py`, `scripts/r2corpus/record_r0_answers.py`,
`internal/books/testdata/r2corpus/stage1/` (the store directory and its state directory, as the R0 binary leaves them),
`internal/books/testdata/r2corpus/answers/`, `internal/books/testdata/r2corpus/manifest.json`, `ci/r0_release.txt`
(the R0 tag; format below). Add tests for the scripts
under `scripts/tests/` (`test_write_r0_corpus.py`, `test_record_r0_answers.py`; names coined here) and the Go manifest
test `internal/books/r2corpus_manifest_test.go`. Modify: `.gitignore` (the negation block below). Python, not shell
(CLAUDE.md rule 4). No other Go change.

### `ci/r0_release.txt`: the R0 tag, in the repo

Create it with exactly these six lines, `<TAG>` replaced by OWNER-02's tag, and a trailing newline (the same shape
as F3-08's `ci/prefreeze_release.txt`):

```text
# file: ci/r0_release.txt
# version: 1.0.0
# guid: <fresh guid from python3 -c 'import uuid;print(uuid.uuid4())'>
# last-edited: <today, YYYY-MM-DD>
# The R0 release (09 §9.3 item 1): the tag whose release binary wrote R2's fixture corpus. One tag line; lines starting with '#' are comments.
<TAG>
```

The reader rule is the one F3-08's file uses: the tag is the single line that is neither blank nor starts with `#`
(`grep -v '^#' ci/r0_release.txt | grep -v '^[[:space:]]*$'`); never `cat` the file. Both scripts read the tag from
this file by default (a `--tag` override is for a re-cut only), fail unless exactly one tag line is present, and
download that tag's asset. `manifest.json`'s `r0_tag` is that tag. If R0 is ever re-cut, the regenerating PR updates
this file in the same commit as the corpus.

### `.gitignore`: commit the store, not a store-shaped hole

The root `.gitignore` ignores, at any depth, `*.log`, `logs/`, `*.db` (and `-wal`/`-shm`/`-journal`), `*.pebble/` and
`audiobooks.pebble/`, `*.bleve/` and `library.bleve/`, `.encryption_key`, `.bootstrap-token` and `*.api-token`. Pebble
names its WAL files `NNNNNN.log`, ABO's default store directory is `audiobooks.pebble` (`cmd/root.go`), and ABO's clean
close syncs the WAL without flushing the memtable (`internal/database/pebble_store.go`, `closeStore`), so on a corpus this small
the rows can live only in the WAL. `git add` skips ignored files without an error. Append this block **at the end** of
`.gitignore` (the last matching pattern wins, so it must follow every pattern above):

```gitignore
# R2 fixture corpus (F2-12): the R0 binary's store, WAL, index, logs and fixture key are testdata.
!/internal/books/testdata/r2corpus/**
/internal/books/testdata/r2corpus/**/.bootstrap-token
/internal/books/testdata/r2corpus/**/*.api-token
/internal/books/testdata/r2corpus/**/.claude/
```

The first line re-includes every file and directory under the corpus (WAL `*.log`, `*.pebble/` and `audiobooks.pebble/`,
`library.bleve/`, `.encryption_key`, `*.db`); the other three keep the auth-token files ignored. The writer deletes the
state directory's `.bootstrap-token` after it stops R0 (the token is a one-boot credential; nothing replays it). The block
covers F2-13's `overlay/` and `blockers/` stores too. Verify before you commit: `git status --porcelain --ignored
internal/books/testdata/r2corpus/ | grep -c '^!!'` prints `0`, and `git ls-files internal/books/testdata/r2corpus/ | grep -cE
'\.(log|sst)$'` prints at least 1.

## What the corpus must contain (coverage contract)

For each family 05-storage §5S.9.1 says a cutover step moves or rewrites, at least one row **R0's API can create**,
plus the edge values listed. Build the list from §5S.9.1 and §9.2.3 at your base; the minimum is:

| Step | Rows to create through R0's API (examples, not limits) |
|---|---|
| M2 config and secrets | settings saved through `PUT /api/v1/config` covering every class of key the rename table moves, secret-bearing fields set (the blob's plaintext secrets and HEAD's encrypted rows), a gap-fill `config.yaml` beside the store, env-set values for at least one key per layer |
| M3 identity | users (active, disabled, admin, editor, viewer, `fp-worker`), API keys (scoped, empty-scope, every-atom, admin-owned), invites and the invite allow-list, sessions; OAuth identities only if R0's API can create them (else F2-13) |
| M4 tasks | runs of every shape R0 can start: queued, finished (succeeded, failed, canceled), paused executor, schedules edited through the task routes, a stand-down taken by a scan; the states R0 cannot hold at a clean stop (running, stopping) are F2-13's |
| M5 log journal | system-log rows produced by normal operation |
| M6 integrations | integration fields in the settings blob, a webhook configured, import roots, AI jobs only if a fake endpoint lets R0 create them |
| books (stay in place) | library data from a small fixture audio set (use the repo's existing test fixtures), playlists, reading progress and ABS sessions through the ABS API, so `TestCutoverLeavesOtherFamiliesByteIdentical` has bytes to compare |

`manifest.json` (format coined here): `{"r0_tag", "r0_binary_sha256", "written_at", "stage1": [{"case", "family",
"step", "how", "store"}], "overlay": [], "blockers": [], "stores": {"<store>": [{"path", "size", "sha256"}]},
"answers": [{"name", "file", "what"}]}`. `store` is a repo-relative directory (stage 1's is
`internal/books/testdata/r2corpus/stage1`, holding the store directory and its state directory); every `stage1`,
`overlay` and `blockers` entry carries one. `stores` maps each such directory to **every** file under it, with its
size in bytes and its sha256, paths relative to the store directory. F2-13 appends to `overlay` and `blockers` and adds its
stores to `stores`; never rewrite stage 1 entries.

## Answers (what `record_r0_answers.py` records, each as a file under `answers/`)

- **Effective config:** R0's own view of every key (the config route's response, env, YAML and stored values included),
  the input of 05-config's oracle `TestConfigCutover_EffectiveConfigUnchanged`.
- **Schedules:** R0's task and maintenance-window listings (the input of the schedule oracle).
- **Search:** results of a fixed query list over the corpus' library (the search oracle).
- **ABS responses:** the responses of the ABS conformance requests on the corpus (reuse the request list of the existing
  ABS suite/capture script).

Each answer file records the request that produced it. Answers are recorded by booting R0 **on a copy** of the committed
store, never on the committed store itself (the committed bytes must stay as written).

**API keys in answer files are a placeholder.** A recorded request or response that carries an API key's secret (the
`Authorization` or `X-API-Key` header, a mint response's key field) writes the placeholder `<api-key:NAME>` (format coined
here; `NAME` is the fixture key's fixed name) instead of the value. A replay re-mints the key through R0's API on its copy of
the store and substitutes it. The installed pre-commit hook rejects any added line matching `abk_[A-Za-z0-9]{16,}`
(`scripts/setup-git-hooks.sh`); its message suggests `--no-verify`. **Never use `--no-verify`.** If the hook fires, a key
was not redacted: fix the recorder.

## Invariants

1. Stage 1 is written only through R0's API, flags, env and config file. No direct store write, no ABO Go code linked.
2. Credentials are generated fixture values, never real ones; the manifest says so. No host name, address or user name of a
   real installation appears anywhere.
3. The scripts are deterministic in what they create (fixed names, fixed order); timestamps may differ between runs.
4. The committed store is the R0 binary's clean-shutdown output; record its size in the manifest and keep the whole
   corpus small (state the budget you chose in the PR body).
5. Regenerating is a manual act (`--regenerate` flag); CI only reads the committed corpus.

## Tests

- `scripts/tests/test_write_r0_corpus.py`: the script's case list covers every step row of the coverage table (static
  check against `manifest.json`); a dry mode lists the API calls without a binary.
- `scripts/tests/test_record_r0_answers.py`: every answer file named in `manifest.json` exists and parses, and no answer
  file matches `abk_[A-Za-z0-9]{16,}` (every key is the placeholder).
- `TestR2Corpus_ManifestMatchesFiles` (name coined here, Go, `internal/books/r2corpus_manifest_test.go`): every file the
  manifest names exists; every stage-1 case names a family; it walks **all three** of `stage1`, `overlay` and `blockers`
  (both empty at this PR) and asserts that each entry's `store` exists and is a key of `stores`; for each store, the files on
  disk under it equal its file list exactly (none missing, none extra) with matching size and sha256; stage 1's store lists at
  least one WAL (`*.log`) or SST (`*.sst`) file under its store directory and the `.encryption_key` file; the manifest's
  `r0_tag` equals the one tag line of `ci/r0_release.txt` (same reader rule). Put the
  per-store comparison in a helper (`verifyStoreFiles(t, dir, files)`, name coined here) in the same file: F2-14's
  measurement test, in the same package, calls it on the store it copies.

## Acceptance

```bash
python3 scripts/r2corpus/write_r0_corpus.py --help                         # exit 0
python3 -m pytest scripts/tests/test_write_r0_corpus.py scripts/tests/test_record_r0_answers.py -q   # all passed
python3 -c "import json;m=json.load(open('internal/books/testdata/r2corpus/manifest.json'));print(len(m['stage1'])>0, len(m['answers'])>=4)"   # True True
go test ./internal/books/ -run TestR2Corpus_ManifestMatchesFiles -count=1   # ok
git status --porcelain --ignored internal/books/testdata/r2corpus/ | grep -c '^!!'   # 0 (nothing under the corpus is ignored)
git ls-files internal/books/testdata/r2corpus/ | grep -cE '\.(log|sst)$'      # >=1 (the WAL or an SST is committed)
git ls-files internal/books/testdata/r2corpus/ | grep -c '/\.encryption_key$' # 1
grep -v '^#' ci/r0_release.txt | grep -v '^[[:space:]]*$' | grep -cE '^v[0-9]+\.[0-9]+\.[0-9]+$'   # 1 (the R0 tag)
grep -rcE 'abk_[A-Za-z0-9]{16,}' internal/books/testdata/r2corpus/answers/ | grep -vc ':0$'   # 0
du -sh internal/books/testdata/r2corpus/                                    # within the budget stated in the PR body
make ci                                                                     # exit 0
```

## STOP conditions

- No R0 release asset for linux/amd64, the tag is missing, or OWNER-02's tracking-issue line naming it is missing.
- A family a cutover step moves cannot be written through R0's API: do not write it by hand here; list it for F2-13.
- A row would need a real credential or a real host name.
- The pre-commit hook rejects the commit: fix the redaction; never `--no-verify`.

## Out of scope

The overlay and blocker corpora (F2-13); the production-shaped store (F2-14); every Go cutover test (F2-15, the flips);
the reach oracle (F4-07).

## Rollback

Revert the PR (testdata and scripts only).

## Repo rules

File headers on the scripts, `ci/r0_release.txt` (the `#` header above), `.gitignore` (bump its `version` and `last-edited`, keep its guid) and the manifest (JSON cannot carry a comment header: add a `"_header"` object with `file`,
`version`, `guid`, `last-edited` as the first key, and say so in the PR body); conventional commit
(`test(r2): R0-written fixture corpus stage 1 and its answers`) with `Co-Authored-By: Claude <noreply@anthropic.com>`;
`gh pr merge <n> --rebase`; `changelog.d/` fragment. Never commit `.api-token`, `.bootstrap-token` or `.claude/.credentials/`
(the pre-commit hook blocks them), and never commit with `--no-verify`.
