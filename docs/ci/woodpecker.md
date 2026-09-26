<!-- file: docs/ci/woodpecker.md -->
<!-- version: 1.0.2 -->
<!-- guid: 2c8e5a14-9b3d-4f07-8e61-a4d0c7b2f913 -->
<!-- last-edited: 2026-09-26 -->

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
| `192.0.2.30` | the developer Mac |
| `/srv/appdata` | the NVMe app-data area on U0 (not `/var/lib`, which is on the HDD pool) |

## Pipeline layout

| workflow | agent label | runs | measured time |
|---|---|---|---|
| `test-database` | `host=u0`, `heavy=true` | `internal/database` alone, `-timeout 50m` | about 300 s on Linux (1500–2200 s on a loaded Mac) |
| `test-server-scanner` | `host=llm1` | `internal/server`, `internal/scanner`, `internal/server/handlers/abs` | about 620 s (abs); the packages run in parallel |
| `test-rest` | `host=mac` | every other package, including maintenance, registry and applygate | about 450–600 s |
| `checks` | `host=u0` | vet, staticcheck, errcheck ratchet, mocks-check, fmt-check, sdkguard, bench-check, web tests | about 5 min including tool install |
| `coverage` | `host=u0` | coverage floor across the three test workflows | seconds |

Placement follows two rules. First, packages whose tests decode audio (server,
scanner and the decode set in `test-rest`) run only on the Mac agents. The prod
host must not decode, and its Docker image has no ffmpeg. Second, the three
test workflows start together, so the wall time is roughly the slowest of them.
An ssh-based prototype (`scripts/ci_remote.py`) measured this layout at
**753 s** for a full run on 2026-09-26. A local `make ci` of the same commit
on the loaded Mac took **2099 s**, and `internal/database` hit its 25m timeout.

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

## 4. Agents

| agent | host | backend | labels | capacity |
|---|---|---|---|---|
| U0 | 192.0.2.10 | docker | `host=u0,heavy=true` | `WOODPECKER_MAX_WORKFLOWS=1` |
| llm1 | 192.0.2.20 | local | `host=llm1` | 1 |
| Mac | 192.0.2.30 | local | `host=mac` | 1 |

All agents share these settings:

```
WOODPECKER_SERVER=192.0.2.10:18734
WOODPECKER_AGENT_SECRET=<same as server>
WOODPECKER_AGENT_LABELS=host=<u0|llm1|mac>[,heavy=true]
```

### U0 (prod): capped and sliced

U0 is the production host, so its agent runs one workflow at a time, inside a
systemd slice that limits CPU and memory for everything the agent starts.

```ini
# /etc/systemd/system/woodpecker-ci.slice
[Slice]
CPUQuota=2400%        # at most 24 of 48 cores
MemoryMax=24G
CPUWeight=20          # lose to the app and to Ollama under contention
IOWeight=20
```

Run the agent as a systemd service with `Slice=woodpecker-ci.slice`. The step
containers are started by dockerd rather than by the agent, so they are not
children of the agent's cgroup. Put them in the same slice too. The cleanest
way is a second Docker daemon for CI, or `"cgroup-parent":
"woodpecker-ci.slice"` in a dedicated daemon's `daemon.json`. Do not set that
on the daemon that runs the app. Before relying on the limits, check with
`systemd-cgls` during a run that the step containers are under the slice.
Also set `WOODPECKER_MAX_WORKFLOWS=1`. The test and checks
workflows run in the `golang` image, which has no ffmpeg. That keeps the
host's no-decode rule, and a container's localhost is not the host's, so tests
that dial `localhost:8112` or `:8484` never reach the real services.

### llm1 and the Mac: local backend

These agents run steps directly on macOS (`WOODPECKER_BACKEND=local`). The
decode tests need the host's ffmpeg, ffprobe and fpcalc, and macOS cannot run
the pinned Linux images natively. The agent's environment must put the pinned
toolchains first on PATH:

```
PATH=$HOME/ci/opt/go/bin:$HOME/ci/opt/node/bin:$HOME/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin
GOTOOLCHAIN=go1.27.1
```

The local backend also needs the `plugin-git` binary on PATH for the clone
step. Get it from the woodpecker-ci/plugin-git releases, the version matching
the pinned image (2.10.1). Run the agent as a launchd user agent, not as root.

## 5. Secrets

Set these on the repo in the Woodpecker UI:

| secret | used by | value |
|---|---|---|
| `woodpecker_api_token` | `coverage` | Woodpecker personal API token (read access) |
| `cf_access_client_id` | `coverage` | Cloudflare Access service-token ID |
| `cf_access_client_secret` | `coverage` | Cloudflare Access service-token secret |

## Troubleshooting

- **A workflow sits in "pending".** No online agent has matching labels. Check
  the Agents page. The U0 agent runs only one workflow at a time, so `checks`
  and `coverage` queue behind `test-database`.
- **The coverage gate fails with "no CI-COVERAGE line".** That test workflow
  failed or never ran. Its own log shows why.
- **Webhooks fail with 403.** The GitHub `hooks` IP list in the WAF rule is
  stale. Refresh it (step 3.4).
- **The clone fails with an LFS budget error.** A workflow is missing
  `lfs: false` in its `clone` block.
