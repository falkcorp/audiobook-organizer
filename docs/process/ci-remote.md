<!-- file: docs/process/ci-remote.md -->
<!-- version: 1.0.0 -->
<!-- guid: 64ab6c3c-1ce6-48a4-8e4a-0c95ada9ab46 -->
<!-- last-edited: 2026-09-26 -->

# `make ci-remote`: local CI on a runner pool

`make ci-remote` runs the same gates as `make ci`, but it sends the heavy legs
to a pool of remote nodes instead of running everything on your machine. It
exists because several agents running `make ci` at once on one Mac pushed the
load to about 150. Packages that pass on their own then hit the 25-minute
`test-short` timeout, so every agent's gate read red for reasons unrelated to
its change.

The implementation is `scripts/ci_remote.py`, and its unit tests are in
`scripts/test_ci_remote.py`.

```bash
make ci-remote                                    # test HEAD (tree must be clean)
make ci-remote CI_REMOTE_ARGS=--allow-dirty       # snapshot uncommitted work into a temp commit
python3 scripts/ci_remote.py --ref origin/main    # test another commit
python3 scripts/ci_remote.py --dry-run            # probe nodes and print the plan; run nothing
python3 scripts/ci_remote.py --quiet              # stream only failure lines
```

## What it runs

The gate set is the same as `make ci`, and so is the verdict:

| `make ci` piece | ci-remote leg | where |
|---|---|---|
| `test-short` (`go test ./... -short -race -coverprofile -covermode=atomic -timeout 25m`) | `test-go-NN` / `test-decode-NN`: the same flags, one shard per package subset | any node; decode shards only on a node that may decode |
| `vet` (test-short prerequisite) | `vet` (`make vet`) | any node |
| `staticcheck`, `sdkguard`, `bench-check`, `fmt-check` | same make targets | any node |
| `web-test` | `npm ci --prefix web` + `make web-test` | any node |
| `lint-errcheck-ratchet` | `errcheck-ratchet` (the same script, which pins GOOS=linux) | any node |
| `mocks-check` | `mocks-check` | this machine (mockery is only installed on developer machines) |
| `coverage-check-short` | the same target, run on the **merged** coverage profile of all shards | this machine |

The shards cover every package from `go list ./...`, including packages with no
tests. Since Go 1.22 those count toward the coverage total, so leaving them out
would change the number. Each shard's profile covers a disjoint set of
packages, and the merge concatenates them into `coverage.out` before
`coverage-check-short` runs. A `package-census` row turns red if any listed
package printed no result line.

The nodes can differ from your machine, and from each other, in GOOS. The
build-tagged files (`*_linux.go` and `*_darwin.go`) then differ as well, so the
merged coverage total can move by a small amount depending on which OS ran
which package. Pass/fail is not affected.

Legs run from one shared queue. A node's workers take the heaviest leg that
node may run. A decode-capable node takes the decode shards first, because no
other node can run them. Shards are balanced by measured runtime using
longest-processing-time-first. The timings are kept per GOOS in
`<primary checkout>/.git/ci-remote/timings.json`, because `internal/server` is
roughly 15x slower on macOS's temp filesystem than on Linux. That file is
shared by every worktree and is never committed. On the first run the script
falls back to a size heuristic (bytes of `*_test.go`).

Output:

- Each leg streams its log with a `[leg@node]` prefix.
- Full logs, `plan.json`, `summary.json` and the merged `coverage.out` go to
  `.ci-remote/<sha>/`, which is gitignored.
- The run ends with a table of leg, node, duration and result. The exit code
  is non-zero if any leg failed.
- If no node is usable (unreachable, missing tools, or a `prod` node above its
  load threshold), the script falls back to plain local `make ci`. Pass
  `--no-fallback` to fail instead.

## CI_NODES

The node list is **never committed**, because the repo is public. It comes from
the first of these that sets it:

1. the `CI_NODES` environment variable (or `--nodes`)
2. `Makefile.local` in the current checkout
3. `Makefile.local` in the primary checkout, which the script finds through
   `git rev-parse --git-common-dir`. Worktrees need no copy of their own.

```make
# Makefile.local
CI_NODES ?= user@node-a.example:max=2 user@node-b.example:prod,nodecode,max=2
```

Entries are space-separated `user@host[:flag,flag]`. An unknown flag is an
error, so a typo in `nodecode` cannot quietly allow decoding on a node that is
banned from it.

| flag | meaning |
|---|---|
| `max=N` | At most N concurrent CI jobs on this node **across all agents**, enforced by N slot locks on the node. Default 1. |
| `nodecode` | The node must never run audio decode, encode or fingerprint work. Decode packages are never scheduled there. As a second guard, every leg on the node runs with a PATH that has no `ffmpeg`, `ffprobe` or `fpcalc`, so a test the scanner missed skips instead of decoding. A leg refuses to start (exit 70) if any of the three is still reachable. |
| `prod` | The production host. Every leg runs under `nice -n 19` plus `ionice -c3` when available. `GOMAXPROCS` and `go test -p` are capped so that all slots together use at most half the cores. The node is skipped when `load1 > cores × 0.5` (`CI_REMOTE_PROD_MAX_LOAD_FRAC`) or when MemAvailable is below 8 GB (`CI_REMOTE_PROD_MIN_MEM_GB`). |

Every node runs legs under `nice -n 19` (and `ionice -c3` on Linux), so CI never
competes with Ollama or GPU work at equal priority. A node whose **local** Ollama
is serving is de-weighted to half its slots. "Serving" means a loaded model's
`expires_at` moved between two `/api/ps` samples taken 3 s apart, or the Ollama
processes are using at least 20% CPU. A loaded model by itself does not count,
because keep-alive holds it resident for hours. A `127.0.0.1:11434` that is only
a tunnel to another host does not count either: the probe looks at `/api/ps`
only when an `ollama` process runs on the node itself. A non-prod node with
`load1 > 1.5 × cores` is also de-weighted to half its slots.

Which packages need a decoder is found by scanning each package's `*_test.go`
files for `exec.LookPath("ffmpeg"|"ffprobe"|"fpcalc")`,
`exec.Command[Context](…"ffmpeg"…)`, or a hard-coded `/usr/bin/ffmpeg`-style
path. Every run prints the list and the placement of each shard. Per-package
placement is saved in `summary.json`.

A node is decode-capable when it is not `nodecode` and all three tools are on
its CI PATH. On macOS nodes the CI PATH includes `/opt/homebrew/bin` and
`/usr/local/bin`. A non-interactive ssh session does not include them, and
without them every decode test would skip. If no node is decode-capable, the
decode shards run on this machine.

## How a node is used

The node user's home holds everything under `~/ci`:

```
~/ci/audiobook-organizer.git   bare repo; each run pushes <sha> to refs/ci-jobs/<runid>
~/ci/jobs/<sha>-<runid>/       git worktree of the bare repo, one per run per node
~/ci/cache/{go-build,gomod,npm}  shared per node (GOCACHE, GOMODCACHE, npm cache)
~/ci/locks/slots/slot-<i>/     mkdir slot locks (owner file inside)
~/ci/opt/{go,node}/            pinned toolchains
~/go/bin/{staticcheck,golangci-lint}
```

Each leg runs as one `ssh node bash -s`. The leg:

1. Takes a free slot lock with `mkdir`, or exits 75. The runner then requeues
   the leg so another node can take it, and backs off.
2. Refreshes the lock's mtime once a minute. A lock whose heartbeat stopped
   more than 10 minutes ago is treated as dead and taken over.
3. Puts `~/ci/opt/go/bin:~/ci/opt/node/bin:~/go/bin` first on PATH and sets
   `GOTOOLCHAIN=go1.27.1`, the per-node `GOCACHE` and `GOMODCACHE`, and
   `GOMAXPROCS` on prod nodes.
4. Runs the command at low priority in the job dir. Go shards stream their
   coverage profile back between markers.

When the run passes, the job dir and the ref are removed. When it fails, the
job dir is marked `.ci-remote-failed`, and the node keeps the newest 3 failed
job dirs.

## Setting up a new node

The runner changes nothing outside `~/ci`, `~/go/bin` and the caches. You need
key-based ssh from each developer machine (`BatchMode=yes` is used, so there
are no password prompts), plus `git`, `make`, `bash` and `curl` on the node.

The versions below match the pool as of 2026-09-26: go1.27.1, node v26.7.0,
staticcheck v0.8.1 (honnef.co/go/tools) and golangci-lint v2.13.1. The commands
were reconstructed from `go version -m` of the installed binaries. Pick the
tarball that matches the node's OS and architecture.

```bash
# on the node
mkdir -p ~/ci/opt ~/ci/bin

# Go (linux-amd64 shown; use darwin-arm64 on an Apple Silicon Mac)
curl -fsSL https://go.dev/dl/go1.27.1.linux-amd64.tar.gz | tar -C ~/ci/opt -xz
#   -> ~/ci/opt/go/bin/go

# Node (linux-x64 shown; use darwin-arm64 on an Apple Silicon Mac)
curl -fsSL https://nodejs.org/dist/v26.7.0/node-v26.7.0-linux-x64.tar.xz | tar -C ~/ci/opt -xJ
mv ~/ci/opt/node-v26.7.0-linux-x64 ~/ci/opt/node
#   macOS: curl -fsSL https://nodejs.org/dist/v26.7.0/node-v26.7.0-darwin-arm64.tar.gz | tar -C ~/ci/opt -xz
#          mv ~/ci/opt/node-v26.7.0-darwin-arm64 ~/ci/opt/node

# Linters, built with the pinned toolchain into ~/go/bin
export PATH="$HOME/ci/opt/go/bin:$PATH" GOTOOLCHAIN=go1.27.1
go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1

# The bare repo each run pushes into
git init --bare ~/ci/audiobook-organizer.git
```

A decode-capable node also needs `ffmpeg`, `ffprobe` and `fpcalc` (chromaprint)
on the CI PATH, for example from Homebrew on macOS or the distro packages on
Linux. A `nodecode` node needs nothing extra. Its decoders are filtered out of
the PATH that CI uses.

Then add the node to `CI_NODES` in your `Makefile.local` and check the result
with `python3 scripts/ci_remote.py --dry-run`. The node table shows each node's
GOOS, cores, load, whether it may decode, how many slots it gets and why. No
code change is needed.

## Troubleshooting

- **A node shows `unreachable`.** Run `ssh -o BatchMode=yes user@node true`.
  The runner never prompts, so a key that needs a passphrase or a host key not
  yet in `known_hosts` shows up here.
- **A node shows `missing tools: go`.** `~/ci/opt/go/bin/go` is missing or not
  executable. `make` or `git` missing shows up the same way.
- **A node shows `no ~/ci/audiobook-organizer.git`.** Run the `git init --bare`
  step above.
- **A leg keeps printing `all N slots held by other runs; requeued`.** Other
  agents hold the node's slots. `cat ~/ci/locks/slots/slot-*/owner` on the node
  shows who holds them. A lock whose owner died expires 10 minutes after its
  last heartbeat. If you are sure a lock is dead you can `rmdir` it by hand, but
  do not do this while its owner's leg is still running.
- **A leg fails with exit 70 on a nodecode node.** The PATH filter found a
  decoder it could not hide, for example one installed after the job dir was
  prepared. Nothing was decoded. Rerun.
- **A decode test skipped on a decode-capable node.** Check that the node has
  `ffmpeg`, `ffprobe` and `fpcalc` on the CI PATH shown above, not just in an
  interactive shell's PATH.
- **The remote result differs from local `make ci`.** Compare the per-package
  status in `.ci-remote/<sha>/summary.json` with your local run. Build-tagged
  (`*_linux.go`/`*_darwin.go`) code runs only on its own OS. Failed job dirs
  are kept on the node under `~/ci/jobs/` so you can rerun the failing command
  there by hand.
- **Ctrl-C.** This kills the local ssh sessions. A remote leg that is already
  running can finish in the background. Its slot lock expires once the
  heartbeat stops, and its job dir is left in place for inspection.
- **Leftover job dirs from killed runs.** They sit under `~/ci/jobs/` without a
  `.ci-remote-failed` marker. Remove them with
  `git -C ~/ci/audiobook-organizer.git worktree remove --force <dir>`.
