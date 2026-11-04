<!-- file: docs/agent-tasks/platform-core/PC-02-04-deploy-and-rollback-tooling.md -->
<!-- version: 1.0.0 -->
<!-- guid: 291d3cb6-2680-463f-9eef-7843324d234a -->
<!-- last-edited: 2026-11-04 -->

# PC-02-04 — Deploy and rollback tooling: `DEPLOY_READY_TIMEOUT`, ssh polling in `deploy-wait-ready.sh`, preflight sudoers and floor checks, binary history, `soak_record --deployed`, fake-ssh harness

| Field | Value |
|---|---|
| ID | PC-02-04 |
| Tier | **T2 Sonnet/high.** Shell and Python tooling, tested against a fake host on the CI runner; every decision is given below, and the guard decision itself is PC-02-01's script. |
| Wave | W02 (S0, release `S0`) |
| Depends on | PC-02-01 (the new `storage_format_guard.py` flags and the fast exits), PC-01-07 (`scripts/soak_record.py --deployed`) |
| Blocks | PC-02-06 (reuses `deploy-wait-ready.sh` and the fake-ssh idea) |
| Trailer | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §6.7 last sentences, §6.8, §7 |
| Spec | 09 S0 row "Deploy" and its "Done when" clauses for `test_make_rollback.py`, `test_deploy_wait_ready.py`, the floor refusal and the sudoers refusal; 09 §9.2.2 "Rollback tooling" and "Owner actions, all in S0"; 09 §9.3 "Boot-time budget" (`DEPLOY_READY_TIMEOUT`); 09 §9.2.1 "Soaks" (`--deployed`); 05-storage §5S.3.7 (the literal probe command) |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-02-04" -b platform-core/pc-02-04-deploy-tooling origin/main
cd "$REPO/../audiobook-organizer-pc-02-04"
git rebase origin/main
git worktree list
make web-deps
```

Preconditions (each must print what is shown, else STOP):

```bash
python3 scripts/storage_format_guard.py --help | grep -c -- '--prev-levels\|--prev-floor\|--restore-probe\|--mode'   # 4 or more
python3 scripts/soak_record.py --help | grep -c -- '--deployed'                                                     # 1 or more
```

## Exact files

Create: `scripts/tests/fake_ssh.py`, `scripts/tests/test_deploy_wait_ready.py`, `scripts/tests/test_make_rollback.py`.
Modify: `Makefile`, `Makefile.local.example`, `scripts/deploy-wait-ready.sh`, `scripts/deploy-preflight.sh`,
`scripts/tests/test_deploy_preflight.py`, `docs/system/deploy-and-gpu-ops.md`.

## Re-verify greps (expected counts at the planning checkout `51a2c2acd`)

```bash
grep -n '^rollback:' Makefile                                                    # 1 hit (line 896)
grep -n 'deploy-wait-ready.sh $(DEPLOY_URL) 300' Makefile                        # 1 hit (line 920)
grep -n 'storage_format_guard.py' Makefile                                       # 1 hit (line 908)
grep -n '^DEPLOY_DB   ?=' Makefile                                               # 1 hit (line 50)
grep -n 'deploy-wait-ready.sh $(DEPLOY_URL) 300' Makefile.local.example          # 2 hits (deploy, deploy-debug)
grep -n "sudo cp \$(DEPLOY_BIN) \$(DEPLOY_BIN).prev" Makefile.local.example      # 1 hit
grep -n '/api/v1/system/version' Makefile.local.example                          # 1 hit (no route serves it, 09 §9.7 P10)
grep -n 'Usage: deploy-wait-ready.sh BASE_URL \[TIMEOUT_SECONDS\]' scripts/deploy-wait-ready.sh  # 1 hit
grep -n '^set -eu' scripts/deploy-preflight.sh                                   # 1 hit
grep -n '### sudoers for `make rollback` (owner action)' docs/system/deploy-and-gpu-ops.md      # 1 hit (line 195)
```

The spec cites `Makefile:893-895` (variables), `Makefile:916` (the 300 s wait) and
`docs/system/deploy-and-gpu-ops.md:208-218` at `748fd787a`; at `51a2c2acd` the variables are at lines 46–58, the
wait at line 920 and the sudoers block at lines 208–225. W01 PRs change the Makefile before you start, so trust
the greps, not the line numbers. A zero-hit edit target: STOP.

## Exact changes

### `scripts/deploy-wait-ready.sh` (09 S0 "Deploy")

New usage line: `deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]`.

- With `SSH_HOST` and `UNIT`: every 5 s run
  `ssh "$SSH_HOST" systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus "$UNIT"` and parse the
  `Key=Value` lines. Remember `NRestarts` from the first poll.
  - `ExecMainStatus=78` → print `ERROR: the service exited 78: install the stepping-stone release first` to stderr,
    exit 3 (exit code coined here).
  - `ActiveState=failed` or `ActiveState=inactive`, or `NRestarts` above its first value → print the state and exit 1
    at once.
  - `ActiveState=activating` → progress; the timeout does not run.
  - `ActiveState=active` → from the first such poll, `TIMEOUT` seconds count for the URL check below; keep checking the
    unit on every poll.
- URL check (both modes): HEAD's loop: `"ready":true` → exit 0; `""` or `"ready":false` → keep polling; a 200 body
  without `ready` → exit 2.
- Without `SSH_HOST` (Docker, launchd): URL only, with the limit `max(TIMEOUT, 900)` seconds, documented in the
  header comment (value coined here).

### `Makefile`

- Add `DEPLOY_READY_TIMEOUT ?= 300` and `DEPLOY_STATE_DIR ?=` beside `DEPLOY_DB` with comments (09 §9.3).
- `rollback`: require `DEPLOY_STATE_DIR`; collect over ssh, with each stderr captured as today:
  `$(DEPLOY_BIN).prev --print-storage-levels` (and its exit code), `$(DEPLOY_BIN).prev --print-upgrade-floor` (and
  exit code), `sudo -n cat $(DEPLOY_DB).storage-levels`, `sudo -n cat $(DEPLOY_DB).storage-applied`,
  `test -d $(DEPLOY_DB)` (→ `--db-dir-state`), and the probe, literally (05-storage §5S.3.7):
  `sudo -n /usr/bin/find $(DEPLOY_STATE_DIR) -maxdepth 3 \( -path $(DEPLOY_STATE_DIR)/restore/pending.json -o -path '$(DEPLOY_STATE_DIR)/restore/*/inflight.json' -o -path '$(DEPLOY_STATE_DIR)/restore/*/inflight-checkpoint.json' \) -print`
  (stdout, stderr and exit code). Pass them all to `scripts/storage_format_guard.py --mode rollback` with
  `ROLLBACK_IGNORE_RESTORE="$(ROLLBACK_IGNORE_RESTORE)"` in its environment, keeping the existing flags.
- Replace `deploy-wait-ready.sh $(DEPLOY_URL) 300` with
  `deploy-wait-ready.sh $(DEPLOY_URL) $(DEPLOY_READY_TIMEOUT) $(DEPLOY_HOST) audiobook-organizer.service` and the
  echo text "(up to 300s)" with "(up to $(DEPLOY_READY_TIMEOUT)s after the unit leaves activating)".
- After the ready check: `python3 scripts/soak_record.py --deployed` with the arguments its `--help` documents
  (09 §9.2.1 "Soaks": "the `deploy` recipe and `make rollback` call it after their ready check").
- Keep: `.prev` copied over the binary and left unchanged (a second rollback copies the same `.prev` again).

### `Makefile.local.example` (`deploy` recipe)

- Test seams (names coined here; defaults reproduce today's behaviour):
  `DEPLOY_PREFLIGHT ?= bash scripts/deploy-preflight.sh origin/main`, `DEPLOY_BUILD ?= $(MAKE) build-linux VERSION=$(VERSION)`,
  `DEPLOY_ARTIFACT ?= dist/$(BINARY)-linux-amd64`, `DEPLOY_UPLOAD ?=` the upload path the recipe uses today.
- Add `DEPLOY_STATE_DIR := /path/to/state-dir` with a comment (09 §9.2.2 "Rollback tooling").
- After `scp` and `deploy-match-unit-type.sh`, and **before** `cp … .prev`: run
  `bash scripts/deploy-preflight.sh --store-check $(DEPLOY_UPLOAD)` (below); a refusal stops the recipe with nothing
  swapped (09 S0 "Deploy": the floor and levels check of the uploaded binary through the shared helper).
- Swap as today, then wait with `deploy-wait-ready.sh $(DEPLOY_URL) $(DEPLOY_READY_TIMEOUT) $(DEPLOY_HOST) audiobook-organizer.service`.
- Binary history (09 §9.3 step 2): read `binary_id` from `ssh $(DEPLOY_HOST) '$(DEPLOY_BIN) --version-json'`, then
  `sudo mkdir -p $(DEPLOY_BIN).d && sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).d/<binary_id>` and prune to the newest 10
  by mtime (`ls -1t | tail -n +11 | xargs -r rm -f` inside that directory).
- Post-check: replace the `/api/v1/system/version` read with `/health`'s `version` field (09 §9.6 row 20).
- Then `python3 scripts/soak_record.py --deployed` as in the Makefile.
- `deploy-debug`'s wait uses the same new arguments.

### `scripts/deploy-preflight.sh`

- Default mode (`[REMOTE/BRANCH]`, unchanged git check), then, when `DEPLOY_HOST` is set in the environment:
  1. warn (stderr, exit status unchanged) when `Makefile.local` exists and its `deploy:` recipe has no
     `DEPLOY_STATE_DIR` (09 S0 "Deploy");
  2. run `ssh "$DEPLOY_HOST" sudo -n -l` and refuse (exit 1) unless the output lists all four lines of 09 §9.2.2's
     owner action, matched as substrings: `$DEPLOY_DB.storage-levels`, `$DEPLOY_DB.storage-applied`,
     `/usr/bin/find $DEPLOY_STATE_DIR`, `journalctl -u audiobook-organizer.service`. The message names each
     missing line.
- New mode `--store-check NEW_BIN`: collect the same values as `rollback` with `NEW_BIN` as the candidate and run
  `storage_format_guard.py --mode deploy`; exit with its status.

### `docs/system/deploy-and-gpu-ops.md`

In the sudoers section add, in the same literal style, `/usr/bin/cat <DEPLOY_DB>.storage-levels`,
`/usr/bin/cat <DEPLOY_DB>.storage-applied`, the probe `find` exactly as in the Makefile, `/usr/bin/journalctl -u audiobook-organizer.service *`,
and the `mkdir`/`cp`/`rm` lines of the binary history; document `DEPLOY_READY_TIMEOUT`, `DEPLOY_STATE_DIR`,
`ROLLBACK_IGNORE_RESTORE=1` (passes a staged intent only, never an in-flight swap) and exit code 3 of
`deploy-wait-ready.sh`.

### `scripts/tests/fake_ssh.py` (the fake-ssh harness of 09 S0 "Deploy")

An executable Python script the tests install on `PATH` as `ssh` and `scp` (symlinks in a temp `bin/`). As `ssh`
it drops the host argument, appends the command to `$FAKE_SSH_LOG`, and runs it with `sh -c` locally; the temp
`bin/` also holds a fake `sudo` (strips `-n`/`-l` handling: `sudo -n -l` prints `$FAKE_SUDOERS`; any other call
runs the command, or prints `sudo: a password is required` and exits 1 when `FAKE_SUDO_DENY=1`) and a fake
`systemctl` (`show` prints the next line of `$FAKE_SYSTEMCTL_STATES`; other verbs append to the log and exit 0). As
`scp` it copies the local file to the path after the colon. Remote paths are temp paths.

## Code contract (interfaces fixed here)

- `scripts/deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]`: exit 0 ready; 1 timeout, `failed`/`inactive` or a
  restart; 2 a body without `ready`; 3 `ExecMainStatus=78` (stepping stone).
- `scripts/deploy-preflight.sh [REMOTE/BRANCH]` (git check, then the sudoers check when `DEPLOY_HOST` is set) and
  `scripts/deploy-preflight.sh --store-check NEW_BIN` (the guard on the uploaded binary); exit 0 pass, 1 refuse, 2 usage.
- Makefile variables: `DEPLOY_READY_TIMEOUT ?= 300`, `DEPLOY_STATE_DIR ?=`; `Makefile.local.example` seams
  `DEPLOY_PREFLIGHT`, `DEPLOY_BUILD`, `DEPLOY_ARTIFACT`, `DEPLOY_UPLOAD`.
- `scripts/storage_format_guard.py` is called with PC-02-01's flags only; this PR adds none to it.

## TDD tests to write first

`scripts/tests/test_deploy_wait_ready.py` (fake `ssh` and `systemctl`, a local `http.server` on 127.0.0.1):
activating for 3 polls then active and ready → exit 0; `ExecMainStatus=78` → exit 3 and the stepping-stone text;
`ActiveState=failed` → exit 1 within one poll; rising `NRestarts` → exit 1; no `SSH_HOST` → URL-only; a body without
`ready` → exit 2.

`scripts/tests/test_make_rollback.py` (09 S0 "Done when", each a test method; run `make rollback` and
`make -f Makefile -f Makefile.local.example deploy` with every `DEPLOY_*` variable on the command line):
S0 → HEAD rollback passes the guard, the probe passing on a state dir with no `restore/`; the `deploy` recipe
HEAD → S0 passes its probe; a second `make rollback` leaves `.prev` and the binary byte-identical to after the
first; `deploy-match-unit-type.sh` ran (the log holds `--print-capabilities`); the `deploy` recipe refuses a fake
new binary whose `--print-upgrade-floor` the store's `.storage-applied` does not meet, and nothing was swapped;
a staged `restore/pending.json` refuses, and passes with `ROLLBACK_IGNORE_RESTORE=1`; `restore/x/inflight.json`
refuses even with the override; `soak_record.py --deployed` was invoked after the ready check.

`scripts/tests/test_deploy_preflight.py` (extend): the preflight refuses a host whose `sudo -n -l` lacks one of the
four lines and names it; passes when all four are listed; warns on a `Makefile.local` deploy recipe without
`DEPLOY_STATE_DIR`.

## Acceptance

```bash
python3 -m unittest scripts/tests/test_deploy_wait_ready.py scripts/tests/test_make_rollback.py scripts/tests/test_deploy_preflight.py -v
                                              # every test "ok"; final line "OK"
grep -c 'deploy-wait-ready.sh $(DEPLOY_URL) 300' Makefile Makefile.local.example   # Makefile:0  Makefile.local.example:0
grep -c 'DEPLOY_READY_TIMEOUT' Makefile        # 2 or more
grep -c '/api/v1/system/version' Makefile.local.example   # 0
bash -n scripts/deploy-wait-ready.sh scripts/deploy-preflight.sh && echo syntax-ok   # syntax-ok
make ci; echo "exit=$?"                        # exit=0
```

## Out of scope

The staged restore itself (PC-03-06 makes the `deploy` recipe refuse on a staged restore through this probe);
`unit-lifecycle` (PC-02-06); editing `Makefile.local` or the host's sudoers (owner actions, OWNER-02).

## Rollback

Revert the PR; the owner's `Makefile.local` copy is unaffected until the owner copies the recipe again.

## Repo rules

- Worktree only; never `go work init`. Headers on every new or changed file (Python and shell use `#` comment
  headers); one `changelog.d/` fragment.
- Every commit ends with `Stage-Release: S0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- Python for anything non-trivial (CLAUDE.md "Scripts"); shell only for the existing scripts.
- `gh pr merge <n> --rebase` after the required checks pass; never squash or `--admin`.
