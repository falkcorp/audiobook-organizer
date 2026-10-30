<!-- file: docs/ci/woodpecker.md -->
<!-- version: 1.17.3 -->
<!-- guid: 2c8e5a14-9b3d-4f07-8e61-a4d0c7b2f913 -->
<!-- last-edited: 2026-10-30 -->

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

**Triggers.** Every workflow runs on `pull_request` and on `manual` runs
(owner 2026-10-15). Between 2026-10-02 and 2026-10-15 they were manual-only,
because parallel pipelines loaded U1 and U0 together; the owner reversed that
so a PR gets CI without a LAN session queueing it. There is no global
one-at-a-time cap: each agent runs as many workflows as its own capacity
setting allows. The repo's `allow_pr` setting on the Woodpecker server must
be on for `pull_request` events to start anything. A push to a branch with no
PR starts nothing; use `make ci-woodpecker` for that. `test-native-taglib`
runs on any `taglib=true` agent, U1 or u0-local (2026-10-17), so it no longer
waits in the queue outside U1's CI window or while U1 is scaled down. `make ci-woodpecker` follows the
pull_request pipeline the push starts instead of queueing a second, manual
one for the same commit.

| workflow | agent label | runs | measured time |
|---|---|---|---|
| `test-database` | `heavy=true` | `internal/database` alone in 8 shards, `-timeout 50m` | see Sharding |
| `test-server-scanner` | `heavy=true` | `internal/server` and `internal/scanner` in 8 shards each, `internal/server/handlers/abs` in 6 | every shard under 55 s on U1 (538 s on llm1 with 4 each) |
| `test-rest` | `heavy=true` | every other package, including maintenance, registry and applygate | about 450–600 s |
| `test-fixtures-1`, `-2`, `-3` | `heavy=true` | three parallel workflows, one `make test-fixtures FIXTURE_SHARD=N/3 FIXTURE_TIMEOUT=25m` each (owner decision D59, 2026-10-27; contexts `ci/woodpecker/pr/test-fixtures-1` etc.): every package whose tests skip under `-short`, run without `-short`; no coverage line. Split because one run of every package was contended on llm1/mac (temp-file I/O and CPU), and the three sequential shards of 2026-10-18 summed to about 3400-3500 s on llm1, over their shared 55 min budget, so the last shard (`internal/server`) timed out. Each workflow has its own 60 min pipeline timeout and the full 25m `-timeout` per `go test` process: a package too heavy for one process (`internal/server`, `internal/plugins/maintenance`, `internal/audiobooks`, `internal/scanner`) is split by test name into several `go test -run` processes that land in different shards, run at most `FIXTURE_JOBS` (default: CPU count) test binaries at once. Shard weights are measured, not guessed: each run prints `::timing::<unit>=<seconds>` lines, and `scripts/ci/fixture_shard_weights.py --sample <the three shard logs> ...` rewrites `scripts/ci/fixture_weights.json` from a few green pipelines (see `docs/decisions/2026-10-30-fixture-shard-weights.md`) | not yet measured as parallel workflows. Single run: 22-34 min on llm1/mac, about 11 min on U1; sequential shards about 3400-3500 s on llm1 |
| `test-native-taglib` | `taglib=true` | `go test -tags native_taglib -race` of `internal/metadata`, `internal/tagger` and `internal/metafetch`: the cgo tag writers the production binary uses, which no other workflow compiles. `scripts/ci/build_taglib_static.py` builds TagLib 2.0.2 once into `$CI_CACHE_ROOT/taglib/` (keyed by the TagLib and utfcpp versions) and copies it to `third_party/taglib`; no coverage line | not yet measured |
| `checks-lint` | `host=u0` | staticcheck, errcheck ratchet, mocks-check | 566 s as one `checks` workflow with cold caches; see The CI cache |
| `checks-build` | `heavy=true` | vet, fmt-check, sdkguard, bench-check, pprof-check, web tests | split out of `checks` on 2026-09-29 |
| `coverage` | `host=u0` | coverage floor across the three test workflows | seconds |

Placement follows three rules. First, packages whose tests decode audio
(server, scanner and the decode set in `test-rest`) never run on the prod
host, which must not decode; its Docker image has no ffmpeg. Second, the test
workflows start together, so the wall time is roughly the slowest of them.
Third, the seven heavy workflows (`test-database`, `test-server-scanner`,
`test-rest`, `test-fixtures-1`, `-2`, `-3`, `checks-build`) are labelled `heavy=true`, not
`host=u1`, so either local-backend agent can take them: U1 and llm1 both carry
that label (owner decision 2026-10-11, so CI keeps working when U1 is paused at
night). U1 is the fast host (48 cores; under 55 s per shard), and llm1's 10
cores were the slowest (538 s for `test-server-scanner` with 4 shards each), so
expect llm1 runs to be slower and to compete with Whisper transcription.

U1 and llm1 score the same for a `heavy=true` workflow (an exact `heavy` match
plus the `repo` and `org-id` wildcards), so the server gives each heavy
workflow to whichever of them has a free slot, effectively at random. llm1 can
therefore take a heavy workflow while U1 is online and idle. To force a host,
pause the other agent.

*Option, not done:* to prefer U1, relabel llm1's agent `host=llm1,heavy=*`
(`WOODPECKER_AGENT_LABELS`; this needs the llm1 LaunchDaemon plist edited with
sudo and the agent restarted). The `*` scores lower than U1's exact `heavy`
match, so llm1 would take only overflow when U1 has no free slot, or
everything while U1 is paused. No workflow changes, and llm1 still cannot get
`checks-lint` (`host: u0`).

`checks-lint` stays on `host=u0`; `u0` has no `heavy` label, so heavy work can
never be routed to the prod host. An ssh-based prototype
(`scripts/ci_remote.py`) measured an earlier layout at **753 s** for a full run on 2026-09-26; a local
`make ci` of the same commit on the loaded Mac took **2099 s**.

### Sharding

`go test` runs packages in parallel but a package's tests in one process, one
after another unless a test calls `t.Parallel()`, so a workflow takes as long
as its slowest package. `scripts/ci/go_test_shards.py` lists a package's
top-level tests, deals them into N shards balanced by the recorded durations
in `scripts/ci/go_test_timings.json`, runs each shard as its own
`go test -run '^(...)$'` process, and merges the shards' coverage profiles
block by block so the CI-COVERAGE sum is unchanged. Separate processes keep
the tests' shared globals as isolated as before.

Measured on U1 (`-short -race -covermode=atomic`), one process vs. sharded:

| package | one process | sharded |
|---|---|---|
| `internal/database` | 265 s | 46 s (8 shards) |
| `internal/server/handlers/abs` | 253 s | 54 s (6 shards) |

Refresh the timings file when the shard estimates printed at the start of a
run drift far from the measured shard times: run the package with
`go test -json` and keep each top-level test's `Elapsed`.

Every workflow clones with `lfs: false`, because GitHub's LFS budget is
exhausted and no test reads LFS content. Every image is pinned by digest.

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
| llm1 | 192.0.2.20 (macOS arm64) | local | `host=llm1,heavy=true` | 2 | LaunchDaemon, `UserName` = the CI user |
| U1 | 192.0.2.35 (Ubuntu amd64) | local | `host=u1,heavy=true,taglib=true` | 1 | systemd `woodpecker-agent.service`, `User=woodpecker` |
| u0-local | 192.0.2.10 (prod, Ubuntu amd64) | local | `host=u0-local,taglib=true` | 1 | systemd `woodpecker-agent-local.service`, `User=woodpecker` |
| mac | the owner's Mac (macOS arm64) | local | `host=mac,heavy=true` | 1 | `run-agent.sh` under `env -i`, started by hand (not a launchd job); gRPC goes through an ssh tunnel to U0 because macOS Local Network blocks the agent binary |

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

No workflow targets `host=llm1` by name, but it carries `heavy=true`, so it
runs the seven heavy workflows whenever it has a free slot, whether or not U1 is
busy or paused; see Pipeline layout (which also covers preferring U1). Its tools (Homebrew Go, node/npm, ffmpeg, ffprobe, fpcalc,
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

U1 runs the `heavy=true` workflows (`test-rest`, `test-database`,
`test-server-scanner`, `test-fixtures-1`/`-2`/`-3`, `checks-build` and, shared with
u0-local, `test-native-taglib`) with the local
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
  `/tank/ci/tools/node` symlink that `checks-build` puts on PATH when the
  directory exists (llm1 uses the node on its PATH). To upgrade,
  unpack the new version beside it and repoint the symlink.
- **gofmt:** `/usr/bin/gofmt` is Ubuntu's older Go; `checks-build` puts the
  pinned toolchain's `$(go env GOROOT)/bin` first so `fmt-check` matches.
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
  the Agents page. U0 and U1 run 1 workflow at a time and llm1 runs 2. A
  `heavy=true` workflow waits when both U1 and llm1 are paused or offline.
- **The coverage gate fails with "no CI-COVERAGE line".** That test workflow
  failed or never ran. Its own log shows why.
- **Webhooks fail with 403.** The GitHub `hooks` IP list in the WAF rule is
  stale. Refresh it (step 3.4).
- **The clone fails with an LFS budget error.** A workflow is missing
  `lfs: false` in its `clone` block.
