<!-- file: docs/plans/storage-efficiency/TASK-A9.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3affcaa2-a70c-44b8-8097-7daeb7da62b2 -->
<!-- last-edited: 2026-10-03 -->

# TASK-A9: `scripts/deploy-cutover.sh`: save the pre-format binary, install, and wait for serving

Wave W3, in parallel with A7 (disjoint files). Start only after A4 (the
`--print-storage-format` flag and the `.storage-format` sidecar) has merged
to `main` and been deployed, and after A8 (the rehearsal sandbox) is done.
Model: sonnet. Reviewer: silent-failure-hunter.

## 1. Goal and why

**Goal.** One deploy step, used by every deploy, that:

1. after the new binary has been copied to the host and before it replaces
   the running one, asks it which storage format it supports (N, from
   `--print-storage-format`) and reads the store's stamp (S, from the A4
   sidecar `<DEPLOY_DB>.storage-format`);
   - N above S (a format-changing release): copy the running binary to
     `<DEPLOY_BIN>.pre-format-<N>`; if a file of that name already exists,
     stop with a non-zero exit and install nothing;
   - N equal to S (every routine deploy): no copy;
   - N below S: stop with a non-zero exit and install nothing (that build
     would refuse the store);
2. installs the binary (keeping today's `.prev` copy, which `make rollback`
   uses) and restarts the service;
3. polls the app and prints step and progress while it migrates;
4. exits 0 only when the app reports the new version, is not migrating, and
   the sidecar reads N;
5. exits non-zero on a `stopped` state (printing the reason) or on timeout.
   An unreachable endpoint counts toward the timeout, which also covers a
   crash loop.

`deploy-preflight` stays in front of it in every deploy target.

**Why.** The release B and C cut-overs are startup migrations (design 9).
The only way back from a bad one is the pre-migration checkpoint plus the
binary of the release before the cut-over. Today nothing keeps that binary:
the deploy template copies the running binary to `.prev` on every deploy
(`Makefile.local.example:82`, `:111`), so one routine deploy after a cut-over
overwrites it with a post-cut-over build. Saving it under a name that only a
format change creates, and refusing to overwrite it, keeps it until release E.
And a migration can take a long time, with the process up but not serving
(design 9, step 2d); a fixed `sleep 3` and one version check
(`Makefile.local.example:86-94`) cannot tell "migrating", "stopped" and
"crash-looping" apart.

**A gap this task works around (report it, do not fix it here).** The
template's post-deploy check curls `/api/v1/system/version`
(`Makefile.local.example:88`, `:117`), and no such route exists on `main`
(`grep -rn 'system/version' --include='*.go' internal cmd` prints nothing, and
`git log -S'system/version' -- internal/` finds no commit that ever had it).
Unmatched `/api` paths return 404, not the SPA page (`spa_fallback.go:56`,
`static_embed.go:41-48`), so `curl -f` fails and the template's check as
written cannot match; the owner's untracked `Makefile.local` may differ.
The version is served by `GET /api/v1/system/status`, behind
`settings.manage`. That is deliberate: `handlers/system/handler.go:149-162`
explains why no unauthenticated endpoint reports the build string. The script
therefore reads the version from `/api/v1/system/status` with a bearer token.
It also tries `/api/v1/system/version` first, because that is where the
release B status listener (B6b) answers while migrating; a 404 there means
"no listener" and is not an error. Whether B6b may report the real version
unauthenticated is an open design question for the owner; say so in the PR
body.

## 2. Setup

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
git log --oneline origin/main | grep -m1 -i 'storage_format'   # must print A4's commit; if not, stop
git worktree add ../aorg-storage-a9-deploy-cutover -b feat/storage-a9-deploy-cutover origin/main
cd ../aorg-storage-a9-deploy-cutover
npm ci --prefix web
```

- Do NOT run `go work init`.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch.

## 3. Read before editing

- `Makefile.local.example`, all of it: the warnings about `GOTOOLCHAIN` and
  pipes (`:19-48`), the deploy variables (`:50-60`, plus `DEPLOY_DB` from
  A4), and both deploy targets (`deploy` `:70-95`, `deploy-debug` `:97-124`).
- `scripts/deploy-preflight.sh`.
- `Makefile:725-736` (`rollback`, with A4's guard) and A4's
  `scripts/storage_format_guard.py`: the same sidecar and flag, read the same
  way.
- `internal/server/handlers/system/handler.go:149-200` (`HealthCheck`,
  `GetSystemStatus`) and `internal/sysinfo/service.go:62-66` (the `version`
  field of the status response).
- `scripts/test_ci_remote.py:1-30`: how a script's unit test loads the script
  (`importlib`) with no network and no ssh. CI runs
  `python3 -m unittest discover -s scripts -p 'test_*.py'`
  (`.github/workflows/ci.yml:310`).
- Design `docs/design/2026-10-03-storage-efficiency-design.md`, section 9:
  step 2a (the listener's JSON: state, step, progress, held count, stop
  reason, ETA), step 2d (stop states keep the process up) and the restore
  paragraph.

## 4. Re-verify anchors

1. `grep -n 'sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).prev' Makefile.local.example` → `82`, `111`.
2. `grep -n 'curl -ksf $(DEPLOY_URL)/api/v1/system/version' Makefile.local.example` → `88`, `117`.
3. `grep -n 'bash scripts/deploy-preflight.sh origin/main' Makefile.local.example` → `72`, `100`.
4. `grep -rn 'system/version' --include='*.go' internal cmd` → nothing.
5. `grep -n 'protected.GET("/system/status"' internal/server/wire_system_routes.go` → `28:`.
6. `grep -n 'json:"version"' internal/sysinfo/service.go` → `64:` (the
   `Version` field of `SystemStatus`).
7. `grep -n "unittest discover -s scripts -p 'test_\*.py'" .github/workflows/ci.yml` → `310:`.
8. `grep -n 'print-storage-format' main.go` → A4's flag (one line).

## 5. Steps

1. **`scripts/deploy_cutover.py` (new).** Python 3, standard library only,
   `#` header lines after the shebang. Arguments (all required unless a
   default is given): `--host`, `--bin` (DEPLOY_BIN), `--db` (DEPLOY_DB),
   `--url` (DEPLOY_URL), `--staged` (the uploaded binary's path on the host),
   `--expected-version` (the `git describe` string), `--token-file` (a local
   file whose first line is a bearer token with `settings.manage`),
   `--restart-cmd` (default `sudo systemctl restart audiobook-organizer.service`),
   `--timeout` seconds (default 1800 until B9 measures the migration; then
   the rehearsal duration x 2, passed by the caller), `--interval` seconds
   (default 5), `--insecure` (skip TLS verification; prod uses a self-signed
   certificate, the template's `curl -k`).
   - Structure it as a class that takes three injected callables:
     `run(argv) -> (rc, stdout, stderr)` for ssh, `http_get(url, headers) -> (status, body)`,
     and `clock` (`now()`, `sleep(s)`). `main()` wires the real ones
     (`subprocess.run(["ssh", host, remote_cmd])` with every path passed
     through `shlex.quote`; `urllib.request` with an `ssl` context). The
     tests replace all three.
   - **Read N and S.** `ssh host '<staged> --print-storage-format'`; the
     output must be one integer, else exit 4 ("new binary did not report a
     storage format"). `ssh host 'cat <db>.storage-format'`; must be one
     integer, else exit 4 ("cannot read the store's storage format"); there
     is no override.
   - **Decide.** N < S → exit 4, nothing installed. N > S → target
     `<bin>.pre-format-<N>`: `ssh host 'test -e <target>'` succeeding → exit
     4 ("refusing to overwrite <target>"), nothing installed; otherwise
     `sudo cp <bin> <target>`, then confirm `<target> --print-storage-format`
     prints S (the saved binary opens the store as it is now); a mismatch
     removes nothing and exits 4. N == S → no copy.
   - **Install.** Exactly today's step:
     `sudo cp <bin> <bin>.prev 2>/dev/null; sudo mv <staged> <bin> && <restart-cmd>`.
     A non-zero exit is exit 5.
   - **Poll** until `--timeout`:
     1. `GET <url>/api/v1/system/version` with no credentials. A 200 with a
        JSON object that has `migrating: true` is the B6b listener: print
        `state`, `step`, `progress`, `held`, `eta` when any of them changed
        since the last print. `state == "stopped"` → print `stop_reason` and
        exit 2. A 404 or 401, or a body that is not JSON, means "no
        listener"; go on to (2).
     2. `GET <url>/api/v1/system/status` with `Authorization: Bearer <token>`.
        A 200 whose `version` contains `--expected-version` → read the
        sidecar again; equal to N → print "serving at storage format N" and
        exit 0; not equal → keep polling (the app may not have written it
        yet). A 200 with another version → keep polling (old process still
        up). Any other status, a connection error or a timeout of the request
        itself → keep polling.
     3. `sleep(--interval)`.
     On timeout print the last state seen and exit 3. Never print the token.
2. **`scripts/deploy-cutover.sh` (new).** Under 20 lines (repo rule 4):
   `set -euo pipefail`, then
   `exec python3 "$(dirname "$0")/deploy_cutover.py" "$@"`. `#` header lines
   after the shebang. It exists so `Makefile.local` calls one stable name.
3. **`scripts/test_deploy_cutover.py` (new).** `unittest`, loaded like
   `test_ci_remote.py`, no network and no ssh. Fakes for `run`, `http_get`
   and the clock. Cases:
   - `test_format_change_saves_pre_format_binary`: N=2, S=1, no target →
     `sudo cp` to `.pre-format-2` happens before the `mv`.
   - `test_routine_deploy_makes_no_copy`: N=1, S=1 → no `.pre-format-*`
     command at all.
   - `test_existing_pre_format_refuses_and_installs_nothing`: N=2, S=1,
     target exists → exit 4, no `mv`, no restart.
   - `test_older_binary_refuses`: N=1, S=2 → exit 4, no `mv`.
   - `test_unreadable_formats_refuse`: non-integer N, then missing sidecar →
     exit 4 each, no `mv`.
   - `test_unreachable_endpoint_times_out_nonzero`: every request raises a
     connection error → exit 3 after the fake clock passes `--timeout`.
   - `test_stopped_state_exits_nonzero`: the listener answers
     `{"migrating": true, "state": "stopped", "stop_reason": "held > 1%"}` →
     exit 2 and the reason is printed.
   - `test_serving_at_n_exits_zero`: listener 404, status 200 with the
     expected version, sidecar N → exit 0.
   - `test_old_version_then_new`: status shows the old version for three
     polls, then the new one → exit 0, no early success.
   - `test_token_never_printed`: the token string does not appear in stdout
     or stderr in any case above.
4. **`Makefile.local.example`.**
   - Add `DEPLOY_TOKEN_FILE ?=` with a comment: a local file whose first line
     is an API token with `settings.manage`, used by the post-deploy wait.
     Never commit the token.
   - In both `deploy` and `deploy-debug`, keep `deploy-preflight`, the build
     and the `scp`. Replace the install line, the `sleep 3` and the version
     `case` (`:81-95`, `:110-124`) with one call:
     `@bash scripts/deploy-cutover.sh --host $(DEPLOY_HOST) --bin $(DEPLOY_BIN) --db $(DEPLOY_DB) --url $(DEPLOY_URL) --staged /home/USER/audiobook-organizer --expected-version "$$(git describe --tags --always)" --token-file $(DEPLOY_TOKEN_FILE) --insecure`.
     While the app reports `migrating: true` the script prints the status
     instead of "Roll back with: make rollback"; keep that final hint only
     on exit 0.
   - Update the file's header comment block to say deploys go through the
     script, and bump its header.
5. **`docs/system/deploy-and-gpu-ops.md`.** One short section: what the
   script does, its exit codes (0 serving, 2 stopped, 3 timeout, 4 refused
   before install, 5 install failed), that a format-changing deploy leaves
   `<bin>.pre-format-<N>` which is kept until release E, and that the owner
   adds the same call to the untracked `Makefile.local`. Bump the header.

## 6. Do not touch

- `Makefile` (the committed `rollback` target and its guard are A4's).
- Any Go code. In particular, do not add an unauthenticated version route;
  report the gap (section 1).
- `scripts/deploy-preflight.sh`, `scripts/storage_format_guard.py`.
- The untracked `Makefile.local`: the owner edits it.

## 7. Tests

- `python3 -m unittest discover -s scripts -p 'test_deploy_cutover.py' -v`:
  every case in step 3 passes.
- One real routine deploy to the A8 sandbox. The sandbox's bind mounts are
  private to its namespace, so every path the script reads over ssh must be
  the host-visible one: `--db` is the clone's path
  (`$DATA_CLONE_MNT/<store basename>`, from A8), never the prod store path
  (that would read prod's sidecar, which also says 1, and pass on the wrong
  file); `--bin` is the `SANDBOX_BIN` path; `--url` uses port 8485.
  `sandbox-run.sh` runs in the foreground (`exec unshare`), so
  `--restart-cmd` is: stop the instance on 8485
  (`pkill -f 'serve .*--port 8485'`, as `sandbox-reset.sh` does), then start
  `sandbox-run.sh` in the background with `nohup ... &`. Do not run
  `sandbox-reset.sh` as part of it. The deploy exits 0, prints "serving at
  storage format 1", and `ls <SANDBOX_BIN>.pre-format-*` finds nothing.
- Not here, because they need release B (they are B9 pass criteria): a
  `.pre-format-<N>` file from a real format change, and a non-zero exit on a
  forced 1% stop while polling the B6b listener.

## 8. Verify

```bash
python3 -m py_compile scripts/deploy_cutover.py scripts/test_deploy_cutover.py
python3 -m unittest discover -s scripts -p 'test_*.py' -v
bash -n scripts/deploy-cutover.sh
grep -n 'scripts/deploy-cutover.sh' Makefile.local.example   # two lines, one per deploy target
```

## 9. Deliverables

- Version headers on the three new files (`#` header after the shebang;
  guids from `uuidgen | tr A-Z a-z`); bump `Makefile.local.example` and
  `docs/system/deploy-and-gpu-ops.md`.
- Fragment `changelog.d/<YYYYMMDD>_storage_a9_deploy_cutover.md`, no header,
  `### Added`, one `####` entry for the script and the new
  `DEPLOY_TOKEN_FILE` variable.
- `git diff origin/main | grep -nE "ab""k_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}"`
  prints nothing. Use `192.0.2.x` placeholders in docs.
- Commit, for example
  `feat(deploy): deploy-cutover script saves the pre-format binary and waits for serving`,
  ending with:

  ```
  Co-Authored-By: <model name> <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/feat/storage-a9-deploy-cutover"`
- `gh pr create --base main --head feat/storage-a9-deploy-cutover`. The body
  states the `/api/v1/system/version` gap and the open question about B6b,
  and that the owner must add `DEPLOY_TOKEN_FILE` and the script call to
  `Makefile.local`. Do NOT merge.

## 10. Exit criteria and report

- [ ] All ten unit cases pass, and the CI discovery command runs them.
- [ ] A format change saves `<bin>.pre-format-<N>` before the `mv`; a routine
      deploy makes no copy; an existing target refuses with nothing
      installed.
- [ ] Unreachable → exit 3 at the timeout; `stopped` → exit 2 with the
      reason; serving at N → exit 0.
- [ ] One routine sandbox deploy exits 0 and leaves no `.pre-format-*` file.
- [ ] `Makefile.local.example` calls the script from both deploy targets,
      with `deploy-preflight` still first.
- [ ] PR open, not merged.

Report:

```
TASK-A9 report
head sha: <sha>
PR: <url>
files changed: <list>
unit tests: <n>/10 PASS (<time>)
sandbox routine deploy: exit <code>, output tail <3 lines>, pre-format files: <none | list>
gap reported: /api/v1/system/version absent on main; version read from /api/v1/system/status
owner action: add DEPLOY_TOKEN_FILE and the deploy-cutover call to Makefile.local
not done / deviations: <list or "none">
```
