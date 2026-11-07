<!-- file: docs/ci/woodpecker.md -->
<!-- version: 2.0.0 -->
<!-- guid: 2c8e5a14-9b3d-4f07-8e61-a4d0c7b2f913 -->
<!-- last-edited: 2026-11-07 -->

# Woodpecker CI: install runbook

This runbook is for the owner. It installs a self-hosted Woodpecker CI that
runs the same gates as `make ci`. The slow Go test packages run in parallel on
different machines, so local CI no longer loads the developer Mac. Nothing here
is installed by an agent. Every step runs by hand.

Placeholders used throughout (never commit the real values; this repo is public):

| placeholder | meaning |
|---|---|
| `coke.jdfalk.com` | the public CI hostname (real; public DNS) on the existing Cloudflare tunnel |
| `192.0.2.10` | the prod host (U0): Woodpecker server and the heavy Linux agent |
| `192.0.2.20` | the llm1 node (macOS, arm64) |
| `192.0.2.35` | the U1 node (Ubuntu, amd64) |
| `/srv/appdata` | the NVMe app-data area on U0 (not `/var/lib`, which is on the HDD pool) |

## Pipeline layout

**Woodpecker runs only what GitHub Actions cannot (owner decision 2026-11-07).**
Until then it duplicated almost every job GitHub already runs on each pull
request: the short race suite split three ways, the fixture shards, vet, the
repo guards and the ratchets. The repo is public, so GitHub minutes are free,
while every duplicate loaded our own servers and made noise on them. Two
workflows are left on pull requests:

| workflow | agent label | runs | why it is not on GitHub |
|---|---|---|---|
| `checks-lint` | `host=u0` | `staticcheck` (one step, plus a cache-trim step) | staticcheck is not part of any GitHub PR workflow (`nightly.yml` runs it, no PR job does) |
| `test-native-taglib` | `taglib=true` | `go test -tags native_taglib -race` of `internal/metadata`, `internal/tagger` and `internal/metafetch`: the cgo tag writers the production binary uses, which no other workflow compiles. `scripts/ci/build_taglib_static.py` builds TagLib 2.0.2 once into `$CI_CACHE_ROOT/taglib/` (keyed by the TagLib and utfcpp versions) and copies it to `third_party/taglib`; no coverage line | needs a TagLib built on the host and a host with ffmpeg, ffprobe and fpcalc |

**The PR gate is GitHub's.** The merge-blocking test checks are ci.yml's
`go-test-short-shards` (Go Tests (short, race), shards 1/4 to 4/4 and core/4)
and `Fixture Tests (no -short, race)` 1/3 to 3/3. Woodpecker's removed
workflows and their GitHub twins:

| removed workflow | GitHub twin |
|---|---|
| `test-rest`, `test-database`, `test-server-scanner` (the `make ci` short race suite split three ways) | ci.yml `go-test-short-shards`, which covers every package from `go list ./...` |
| `test-fixtures-1`, `-2`, `-3` (`make test-fixtures FIXTURE_SHARD=N/3`) | ci.yml `Fixture Tests (no -short, race)` 1/3 to 3/3, the same make target and the same `scripts/ci/fixture_weights.json` |
| `checks-build`: vet, vet-integration | ci.yml `Go Vet & Build` and `vet-integration` |
| `checks-build`: fmt-check, sdkguard, ops-check, types-check, bench-check | ci.yml `Repo Guards` |
| `checks-build`: pprof-check | ci.yml `Repo Guards` (the step was added on 2026-11-07, because nothing on GitHub ran it); core's `pprof-check` runs in `core-ci.yml`'s `make -C core ci` |
| `checks-build`: web tests | `frontend-ci.yml` |
| `checks-lint`: errcheck ratchet, deadcode ratchet, mocks-check | ci.yml `errcheck-ratchet`, deadcode ratchet and `mocks-check`; core's errcheck ratchet and mocks-check run in `core-ci.yml` |

`make ci-woodpecker` therefore no longer runs the whole of `make ci`. It
starts the two workflows above and waits for them; use `make ci` locally, or
rely on the GitHub checks, for the rest.

**Triggers.** Both workflows run on `pull_request` and on `manual` runs
(owner 2026-10-15). The repo's `allow_pr` setting on the Woodpecker server must
be on for `pull_request` events to start anything. A push to a branch with no
PR starts nothing; use `make ci-woodpecker` for that. `test-native-taglib`
runs on any `taglib=true` agent, U1 or u0-local (2026-10-17), so it does not
wait in the queue while U1 is busy or restarting between its day and night slot
counts. `make ci-woodpecker` follows the pull_request pipeline the push starts
instead of queueing a second, manual one for the same commit.

`checks-lint` stays on `host=u0`; `u0` has no `heavy` label. No workflow
carries `heavy=true` any more, so the `heavy=true` labels on U1, llm1 and the
Mac are idle until a heavy workflow is added again.

Every workflow clones with `lfs: false`, because GitHub's LFS budget is
exhausted and no check reads LFS content. Every image is pinned by digest.

## 1. Server on U0

The server runs with Docker Compose. It listens for HTTP on the CI host's LAN
address, because the Cloudflare tunnel connectors run on three separate,
load-balanced tunnel nodes, not on the CI host; they reach it over the LAN,
the same way they reach the audiobook server.
gRPC for agents is on the LAN address, and its data lives on the NVMe app-data
area.

```yaml
# /srv/appdata/woodpecker/compose.yaml
services:
  woodpecker-server:
    image: woodpeckerci/woodpecker-server:v3    # pin by digest when installing
    restart: unless-stopped
    ports:
      - "192.0.2.10:18733:8000"     # HTTP: LAN; the three tunnel nodes connect here
      - "192.0.2.10:18734:9000"     # gRPC: LAN only, for agents; never tunnelled
    volumes:
      - /srv/appdata/woodpecker/data:/var/lib/woodpecker
    environment:
      WOODPECKER_HOST: https://coke.jdfalk.com
      WOODPECKER_OPEN: "false"
      WOODPECKER_ADMIN: <github-login>
      WOODPECKER_GITHUB: "true"
      WOODPECKER_GITHUB_CLIENT: <oauth client id>
      WOODPECKER_GITHUB_SECRET: <oauth client secret>
      WOODPECKER_AGENT_SECRET: <openssl rand -hex 32>
```

Keep the secrets in an `.env` file next to `compose.yaml`, mode 0600, and
never in the repo.

## 2. GitHub OAuth app

Create the app under GitHub → Settings → Developer settings → OAuth Apps → New
OAuth App, owned by the organization that owns the repo:

- Homepage URL: `https://coke.jdfalk.com`
- Authorization callback URL: `https://coke.jdfalk.com/authorize`

Put its client ID and secret into the server's `.env`. Log in once at
`https://coke.jdfalk.com` and activate the repo. Woodpecker then creates the
GitHub webhook, which points at `https://coke.jdfalk.com/api/hook`. Under the
repo settings in Woodpecker, set the pipeline path to `.woodpecker/`.

## 3. Ingress: Cloudflare tunnel and Access

1. **One public hostname.** Add `coke.jdfalk.com` to the existing Cloudflare
   tunnel, with service `http://192.0.2.10:18733` (the CI host's LAN address).
   The tunnel has three connector nodes that load-balance, none of them on the
   CI host, so every connector must be able to reach that LAN address. The
   port is open to the LAN like the audiobook server's; from outside, the
   tunnel (behind Access) is the only way in.
2. **The Access app covers the whole hostname.** Create a Cloudflare Access
   self-hosted application for `coke.jdfalk.com`, with the owner's identity as
   the Allow policy. It protects the UI, the API and the OAuth login and
   callback (`/authorize`).
3. **Bypass for the webhook only.** Add a second Access application, or a
   path-scoped policy, for `coke.jdfalk.com/api/hook` with the action
   **Bypass**. GitHub cannot send Access service-token headers, so the webhook
   path must skip Access. Nothing else gets a bypass.
4. **WAF rule for the webhook path.** Add a WAF custom rule on the zone:

   ```
   (http.host eq "coke.jdfalk.com" and starts_with(http.request.uri.path, "/api/hook")
     and not ip.src in $github_hooks)  →  Block
   ```

   `$github_hooks` is a Cloudflare IP list filled from the `hooks` array of
   `https://api.github.com/meta`. To refresh it, run
   `curl -s https://api.github.com/meta | jq -r '.hooks[]'` and replace the
   list's contents. GitHub changes these ranges rarely, but check monthly and
   whenever webhook deliveries start failing with 403. The second layer is
   Woodpecker's signed per-repo hook token: a request from an allowed IP
   without a valid signature is still rejected.
5. **Agents never use the tunnel.** Agents connect to the gRPC port
   `192.0.2.10:18734` over the LAN only. Do not add a tunnel route for 18734.
6. **Automation from outside the LAN.** Create a Cloudflare Access service
   token and a Woodpecker personal API token. Send
   `CF-Access-Client-Id` / `CF-Access-Client-Secret` together with
   `Authorization: Bearer <woodpecker token>`, and store all three next to
   the existing credentials, never in the repo. The Access app needs a
   "Service Auth" policy that allows that token. The webhook does not use it.
   The `coverage` workflow uses the same three values (see Secrets).

## 4. Agents (as deployed 2026-09-26; U1 added 2026-09-29; mac agent added 2026-10-12)

| agent | host | backend | labels | parallel workflows | runs as |
|---|---|---|---|---|---|
| U0 | 192.0.2.10 | docker | `host=u0` | 1 | swarm service `woodpecker_woodpecker-agent` |
| llm1 | 192.0.2.20 (macOS arm64) | local | `host=llm1,heavy=true` | 3 (`WOODPECKER_MAX_WORKFLOWS` in the plist, 2026-11-06) | LaunchDaemon, `UserName` = the CI user |
| U1 | 192.0.2.35 (Ubuntu amd64) | local | `host=u1,heavy=true,taglib=true` | 6 by day, 1 at night (see below) | systemd `woodpecker-agent.service`, `User=woodpecker` |
| u0-local | 192.0.2.10 (prod, Ubuntu amd64) | local | `host=u0-local,taglib=true` | 1 | systemd `woodpecker-agent-local.service`, `User=woodpecker` |
| mac | the owner's Mac (macOS arm64) | local | `host=mac,heavy=true` | 1 | `run-agent.sh` under `env -i`, started by hand (not a launchd job); gRPC goes through an ssh tunnel to U0 because macOS Local Network blocks the agent binary |

**U1 day and night slots (owner, 2026-11-06).** U1 runs around the clock, with
6 parallel workflows inside its daytime windows and 1 outside them, so the fans
stay quiet at night and during the afternoon nap gap. The windows, in the owner's
local time, are Mon-Fri 09:00-13:00 and 17:00-19:30, and Sat-Sun 09:00-12:00
and 14:30-19:30. Until 2026-11-06 U1 was paused outside those windows instead.
`WOODPECKER_MAX_WORKFLOWS` is read only when the agent starts, and a restart
kills running jobs. So the schedule job on the owner's Mac
(`~/.local/bin/ci-agent-schedule.py`, launchd `com.jdfalk.ci-agent-schedule`,
every 5 minutes) handles each switch over several runs. It pauses U1
(`no_schedule`), waits for its running jobs to finish, rewrites
`/etc/systemd/system/woodpecker-agent.service.d/max-workflows.conf` and
restarts the agent, then unpauses it once the API reports the new capacity.
The switch down to 1 slot starts 15 minutes before each window closes, so a
running job has time to finish. The job logs to
`~/.local/state/u1-ci-window.log`.

All agents use `WOODPECKER_SERVER=192.0.2.10:18734` and the same
`WOODPECKER_AGENT_SECRET` as the server.

### U0 (prod): the server stack

The server and the U0 agent run as one Docker swarm stack,
`~/ai/woodpecker/woodpecker-compose.yaml`, deployed with
`docker stack deploy -c woodpecker-compose.yaml woodpecker`. Secrets are in
`~/ai/woodpecker/woodpecker.env` (mode 600): `WOODPECKER_GITHUB_CLIENT`,
`WOODPECKER_GITHUB_SECRET`, `WOODPECKER_AGENT_SECRET`, `WOODPECKER_ADMIN`.
Images are pinned by digest. The server publishes 18733 (HTTP) and 18734
(gRPC) in host mode.

U0 is the production host, so its agent is capped. The live value was changed
with `docker service update` on 2026-10-11; if the stack file still says 2, a
stack redeploy puts it back -- update the stack file too:

```
WOODPECKER_MAX_WORKFLOWS=1   # owner 2026-10-11 (was 2); set live with `docker service update --env-add`
WOODPECKER_BACKEND_DOCKER_LIMIT_CPU_QUOTA=800000     # 8 CPUs per step container
WOODPECKER_BACKEND_DOCKER_LIMIT_MEM=17179869184      # 16 GiB per step container
WOODPECKER_BACKEND_DOCKER_VOLUMES=/mnt/cache/woodpecker-cache:/ci-cache
```

That bounds CI at about 16 CPUs and 32 GiB of the host's 48 cores.
`checks-lint`, the only workflow file in `.woodpecker/` labelled `host=u0`,
runs in the `golang` image, which has no ffmpeg, so the host's no-decode rule
holds for this agent. The one exception on this host is `test-native-taglib`
on the separate u0-local agent below.

### u0-local: local backend on prod (`test-native-taglib` only)

`test-native-taglib` links on Linux only, and until 2026-10-17 U1 was the only
Linux local-backend agent, so the workflow waited whenever U1 was outside its
CI window or scaled down for fan noise. The owner ruled that tests must never
wait on U1, so the prod host runs a second agent next to the swarm one. The
swarm agent cannot run the workflow: on the docker backend `image: bash` is a
container image, not a shell.

- **Labels:** `host=u0-local,taglib=true` and nothing else. Never `heavy=true`
  (it would pull every heavy workflow onto prod) and never `host=u0` (it would
  pull `checks-lint`, whose `image: golang...` the local backend would try to
  exec as a shell). The workflow's metafetch tests decode audio; that is an
  owner-approved exception to the prod no-decode rule for this one workflow.
- **Bounds:** `WOODPECKER_MAX_WORKFLOWS=1`, `Nice=10`, `CPUQuota=800%`,
  `MemoryMax=16G`: the same caps the swarm agent puts on each step container.
- **Runs as:** system user `woodpecker`, HOME `/srv/ci`, which is its own ZFS
  dataset (`nvmecache/woodpecker-local`, `quota=60G`), separate from the swarm
  agent's `nvmecache/woodpecker-cache`. `cache_env.sh` finds `/srv/ci/cache`.
- **Install layout:** `/srv/ci/woodpecker/bin/{woodpecker-agent,plugin-git}`,
  copied from U1 (same version, sha256 checked); work dir
  `/srv/ci/woodpecker/work`; agent config `/srv/ci/woodpecker/agent.conf`.
  `scripts/ci/update_woodpecker_agents.py` does not cover this agent yet; copy
  U1's binary again after upgrading U1.
- **Toolchain:** apt `golang-go` as the bootstrap, as on U1. Snap's `go` (the
  login user's) refuses a HOME outside `/home`, and the local backend gives
  every pipeline a fresh HOME. Also `cmake`, `zlib1g-dev`, `build-essential`,
  and the host's ffmpeg, ffprobe and fpcalc.
- **Service:** `/etc/systemd/system/woodpecker-agent-local.service`, talking to
  the server at `127.0.0.1:18734`, healthcheck on `127.0.0.1:18736`. Logs:
  `journalctl -u woodpecker-agent-local`.
- **Its own agent token, not the shared secret.** The agent was created on the
  server first (`POST /api/agents`, agent id 7) and authenticates with that
  agent's token, kept in the root-only `/etc/woodpecker-agent-local.env`
  (mode 600). A step runs as the agent's user and can read the agent's
  environment, so a hostile pull request could read the token; this way it
  can only impersonate u0-local, and deleting agent 7 revokes it without
  touching the other agents.
- **Sandbox.** The local backend runs pull-request code directly on the host,
  so the unit confines it: `ProtectSystem=strict` with only `/srv/ci`
  writable, `ProtectHome=yes` (the stack's `woodpecker.env` and every other
  login file), `ProtectProc=invisible`, `PrivateTmp`, `PrivateDevices`,
  `NoNewPrivileges`, and `InaccessiblePaths=` over `/mnt` (the library and the
  swarm agent's cache), `/opt`, `/srv/tftp` and the app's and docker's
  `/var/lib` directories. Loopback is not blocked, because the tests start
  `httptest` servers on 127.0.0.1, so a step can still reach the prod
  services' ports; they keep their own authentication. What stops a hostile
  pull request from getting this far is `require_approval: forks` (see The CI
  cache); keep it.

**The CI cache.** The docker backend creates `/woodpecker` as a volume for
each workflow and deletes it when the workflow ends, so caches kept there
never outlived a run: the then-single `checks` workflow re-downloaded and
rebuilt its linters and built the repo cold every time (566 s on
2026-09-29). The agent therefore mounts one host directory,
`/mnt/cache/woodpecker-cache`, into every step container at `/ci-cache`, and
`checks-lint.yaml` keeps GOCACHE, GOMODCACHE, GOBIN and the staticcheck and
golangci-lint caches (under `XDG_CACHE_HOME`) there.

- **Isolated dataset.** It is its own ZFS dataset on the NVMe pool, with a
  hard quota, and nothing else on the host uses it:

  ```
  zfs create -o quota=120G -o compression=zstd -o atime=off \
    -o mountpoint=/mnt/cache/woodpecker-cache nvmecache/woodpecker-cache
  # marker checked by checks-lint's first step (root-owned directory)
  docker run --rm -v /mnt/cache/woodpecker-cache:/c <golang image> \
    sh -c 'echo nvmecache/woodpecker-cache > /c/.woodpecker-cache-dataset'
  ```

  `checks-lint`'s `cache-trim` step keeps the Go build cache near 30 GiB, well
  under the quota.
- **Mount guard.** `checks-lint`'s first step fails unless
  `/ci-cache/.woodpecker-cache-dataset` exists. A bind mount alone proves
  nothing: if the dataset is not mounted, docker creates an empty directory
  at the host path, on the root filesystem and outside the quota, and the
  caches would grow there unnoticed.
- **No Trusted flag.** The mount is set on the agent, not in a pipeline file,
  so the repo does not need Woodpecker's Trusted flag. Trusted volumes would
  let any pipeline file, including one on a pull-request branch, mount any
  host path on the prod host (`/`, `docker.sock`). Keep the flag off.
- **What a shared cache does expose.** The cache is writable and outlives the
  run, so a pipeline can change what later pipelines run: the tool binaries
  in `/ci-cache/bin` and the build cache entries. A pull-request pipeline
  runs that branch's code, so this is only as safe as the rule on who can
  start one. The repo has `require_approval: forks`: a pull request from a
  fork waits for a maintainer's approval before any agent runs it. Keep that
  setting. The shared `/tank/ci/cache` on U1 and `/Users/jdfalk/ci/cache` on llm1 have the same exposure.

### llm1: local backend (takes `heavy=true` workflows)

No workflow targets `host=llm1` by name, and since 2026-11-07 none carries
`heavy=true` (see Pipeline layout), so it has no pipeline work to take. Its tools (Homebrew Go, node/npm, ffmpeg, ffprobe, fpcalc,
python3, golangci-lint, make) must be on the agent's PATH. Its cache root is
`/Users/jdfalk/ci/cache/{gomod,go-build,npm}`. This agent runs steps directly
on macOS (`WOODPECKER_BACKEND=local`). The decode tests need the host's ffmpeg, ffprobe and fpcalc, and macOS has no
Docker backend.

- **Install layout:** `~/ci/woodpecker/bin/{woodpecker-agent,plugin-git}`. The agent is darwin/arm64 from the woodpecker releases (v3.19.0 since 2026-10-10); plugin-git is 2.10.1.
- **Logs:** `~/ci/woodpecker/agent.log`.
- **Step workspaces:** `~/ci/woodpecker/work`.

The plist puts the pinned toolchains first on PATH. `GOTOOLCHAIN=go1.27.2` is
set per step, so a host with an older go fetches the exact version.

**On the local backend, a step's `image:` is the shell that runs its
`commands` (use `image: bash`), not a container image.** A container image name
there makes the agent try to exec it: `exec: "golang:...": executable file not
found in $PATH`.

**macOS Local Network privacy.** A launchd-started agent that runs as a
normal user gets `dial tcp ...:18734: connect: no route to host` until it is
allowed under System Settings → Privacy & Security → Local Network →
`woodpecker-agent`. This also applies to a LaunchDaemon with `UserName` set. The
same binary started from an ssh session connects fine, which is how to tell
this apart from a firewall. Do not run the agent as root to avoid the prompt:
pipelines would run as root.

### U1: local backend on Linux

U1 runs `test-native-taglib` (shared with u0-local; the `heavy=true` workflows
it used to run were removed on 2026-11-07) with the local
backend, like llm1, so the decode tests use the host's ffmpeg, ffprobe and
fpcalc (`apt install ffmpeg libchromaprint-tools`). `build-essential` supplies
the C compiler `-race` needs, and `golang-go` is only a bootstrap:
`GOTOOLCHAIN=go1.27.2` fetches the pinned version. `test-native-taglib` also
needs `cmake` and `zlib1g-dev` (the static `libz.a`) installed (`apt install
cmake zlib1g-dev`) and, for the first build only, network access to github.com;
it stores the static TagLib it builds under `/tank/ci/cache/taglib/`. That
workflow is labelled `taglib=true`, which only the Linux agents (U1 and
u0-local) carry, because the `#cgo LDFLAGS` in
`internal/metadata/taglib_cgo.go` (`-lgcc_eh`) link on Linux only: llm1 and mac
(macOS) would fail at link time. U1 gets the label from the drop-in
`/etc/systemd/system/woodpecker-agent.service.d/labels.conf`.

- **Runs as:** the unprivileged system user `woodpecker` (no sudo). Pipelines
  execute pull-request code, so the agent never runs as a login user.
- **Install layout:** `/tank/ci/woodpecker/bin/{woodpecker-agent,plugin-git}`
  (agent v3.19.0 since 2026-10-10, plugin-git 2.10.1; linux/amd64). `/tank/ci` is its own ZFS dataset and the
  user's HOME.
- **Caches:** `/tank/ci/cache/{go-build,gomod,npm}`. The workflows do not
  hard-code a cache path: each step sources `scripts/ci/cache_env.sh`, which
  uses the first existing root of `/tank/ci/cache` (U1) and
  `/Users/jdfalk/ci/cache` (llm1) and exports `GOMODCACHE`, `GOCACHE` and
  `npm_config_cache` from it. The local backend does pass the agent's
  environment, so per-agent env vars would work too. The script is preferred
  because the paths live in the repo, and because it fails fast when no cache
  root exists: a missing agent env var would silently fall back to the per-run
  HOME cache, the disk-filling mode.
- **Node (web tests):** the official `node-v26.10.0-linux-x64` tarball, checked
  against the release's `SHASUMS256.txt`, unpacked in `/tank/ci/tools` with a
  `/tank/ci/tools/node` symlink that `checks-build` put on PATH when the
  directory exists (llm1 uses the node on its PATH). No current workflow
  needs node; kept for a future heavy workflow. To upgrade,
  unpack the new version beside it and repoint the symlink.
- **gofmt:** `/usr/bin/gofmt` is Ubuntu's older Go; the removed `checks-build`
  put the pinned toolchain's `$(go env GOROOT)/bin` first so `fmt-check` matched.
- **Step workspaces:** `/tank/ci/woodpecker/work`.
- **Service:** `/etc/systemd/system/woodpecker-agent.service`, with the agent
  secret in the root-only `/etc/woodpecker-agent.env` (mode 600). Logs:
  `journalctl -u woodpecker-agent`.

### Upgrading the U1 and llm1 agents

U1 and llm1 run a plain binary, not a container, so a server upgrade leaves
them behind. `scripts/ci/update_woodpecker_agents.py` brings them to the
server's version (or `--version X`): it pauses each agent, waits for its
running tasks, swaps in the checksum-verified release binary (the old one
stays as `woodpecker-agent.<old>`), restarts it, and passes only once the
server reports the new version. A host that never checks back in is rolled
back. `--dry-run` reports versions only. U0's agent is upgraded with the
swarm stack instead.

### Running CI from a workstation

`make ci-woodpecker` pushes HEAD, starts a manual pipeline for the branch
through the API, waits, prints every step (with the tail of each failed step's
log), and exits non-zero unless the pipeline succeeded. It reads
`WOODPECKER_URL` (the server's LAN address; the public host is behind Access)
and `WOODPECKER_TOKEN` from the environment or from the gitignored
`.claude/.credentials/woodpecker-api.env`.

## 5. Secrets

Set these on the repo in the Woodpecker UI:

| secret | used by | value |
|---|---|---|
| `woodpecker_api_token` | `coverage` | Woodpecker personal API token (read access) |
| `cf_access_client_id` | `coverage` | Cloudflare Access service-token ID |
| `cf_access_client_secret` | `coverage` | Cloudflare Access service-token secret |

## Troubleshooting

- **A workflow sits in "pending".** No online agent has matching labels. Check
  the Agents page. U0 runs 1 workflow at a time, U1 runs 6 by day and 1 at
  night, and llm1 runs 3. A `heavy=true` workflow waits when U1, llm1 and mac
  are all busy, paused or offline. While U1 is paused it is switching slot
  counts, which waits for its running jobs to finish.
- **The coverage gate fails with "no CI-COVERAGE line".** That test workflow
  failed or never ran. Its own log shows why.
- **Webhooks fail with 403.** The GitHub `hooks` IP list in the WAF rule is
  stale. Refresh it (step 3.4).
- **The clone fails with an LFS budget error.** A workflow is missing
  `lfs: false` in its `clone` block.
