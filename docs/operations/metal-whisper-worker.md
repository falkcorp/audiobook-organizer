<!-- file: docs/operations/metal-whisper-worker.md -->
<!-- version: 1.6.0 -->
<!-- guid: c47a8e21-93b5-4d06-8f7a-1e6b2c9d5308 -->
<!-- last-edited: 2026-08-31 -->

# Metal Whisper worker (Mac)

Optional Whisper transcription capacity running on Apple Silicon via MLX.
It **adds capacity only**: when it is offline, production jobs stay durable and
queued, and scans do not depend on the Mac being up.

## Why this is a separate server

`scripts/whisper_server.py` runs faster-whisper, which runs on ctranslate2,
which has **no Metal/MPS backend**. Pointing it at a Mac does not fail — it
silently runs on CPU, roughly an order of magnitude slower, while reporting a
healthy `/health`. That silent-downgrade is the reason for a second server on a
different inference stack rather than a device flag on the first one.

| | `whisper_server.py` | `whisper_mlx_server.py` |
|---|---|---|
| Stack | faster-whisper / ctranslate2 | MLX |
| Device | CUDA, else CPU | Metal |
| Default bind | `0.0.0.0` | `127.0.0.1` (loopback) |
| Default port | 19847 | 19848 |
| VAD filtering | yes | **no** (see below) |

**Transcripts will not be byte-identical between the two.** The CUDA server
applies a tuned VAD filter; MLX Whisper has no equivalent, so this worker sends
the whole clip. Treat the two as interchangeable capacity for *finding* text,
not as producing identical strings.

## Install and run (loopback first)

`uv` handles dependencies from the script's inline metadata — no venv needed.

```bash
uv run scripts/whisper_mlx_server.py
# or pick a model explicitly:
uv run scripts/whisper_mlx_server.py mlx-community/whisper-small.en-mlx
```

First run downloads the model (~500 MB for `small.en`) to `~/.cache/huggingface`.

Environment:

| Variable | Default | Meaning |
|---|---|---|
| `WHISPER_MLX_MODEL` | `mlx-community/whisper-small.en-mlx` | model repo |
| `WHISPER_BIND` | `127.0.0.1` | bind address |
| `WHISPER_PORT` | `19848` | port |

## Verify it before exposing it

```bash
curl -s http://127.0.0.1:19848/health
# {"status":"ok","model":"...","batch_pipeline":true,"device":"metal","backend":"mlx"}
```

`batch_pipeline` **must** be present. The Go client (`supportsRemoteBatch` in
`internal/transcribe/remote.go`) decodes it as a `*bool` and enables the batch
endpoint when the pointer is non-nil; if the key is missing the worker silently
degrades to the slow per-file path with no error anywhere.

A real single-file canary:

```bash
curl -s -F "file=@/path/to/clip.wav" http://127.0.0.1:19848/transcribe
# {"text":"...","error":null}
```

And the batch contract — note the part filename is the **result key**:

```bash
curl -s -F "files=@/path/to/clip.wav;filename=book-123" \
        http://127.0.0.1:19848/transcribe-batch
# {"results":{"book-123":{"text":"...","error":null}}}
```

Run the contract tests (they stub inference, so they pass on any platform):

```bash
uv run --with fastapi --with httpx --with pytest --with python-multipart \
  pytest scripts/tests/test_whisper_mlx_server.py -v
```

Confirm the Go client contract is untouched:

```bash
GOTOOLCHAIN=go1.26.7 GOEXPERIMENT=jsonv2 go test ./internal/transcribe -count=1
```

## Measured on an M1 Max (32 GB), 2026-08-30

Validated end-to-end on loopback with `mlx-community/whisper-small.en-mlx`.

| | |
|---|---|
| Cold `/transcribe` (includes model download) | 16.9 s |
| Warm `/transcribe-batch`, 2 files, ~10.7 s of audio | **0.5 s** |
| Transcript accuracy | verbatim on both clips |
| Batch keys | echoed verbatim, including an em-dash and a comma |
| Thermal state after the run | no thermal or performance warning recorded (`pmset -g therm`) |

MLX reported `Device(gpu, 0)` at import, which is the device evidence.
**The 0.5 s figure is elapsed time and is not by itself proof of Metal
utilization** — a longer, more representative batch should be measured before
sizing this worker's concurrency in production.

## Running it persistently

`deploy/launchd/com.jdfalk.whisper-mlx.plist` is a launchd agent. It ships
**loopback-only** and restarts the worker on failure with a 30 s throttle, at
background priority so it does not compete with the desktop.

```bash
cp deploy/launchd/com.jdfalk.whisper-mlx.plist ~/Library/LaunchAgents/
# edit REPO_PATH and USERNAME in the copy first
launchctl load ~/Library/LaunchAgents/com.jdfalk.whisper-mlx.plist
curl -s http://127.0.0.1:19848/health
```

## Exposing it to the LAN — a separate, deliberate decision

The worker binds loopback by default. Exposing it is its own change:

1. Start with `WHISPER_BIND=0.0.0.0` (the server logs a warning when you do).
2. Add a macOS firewall rule permitting **only** the production host on 19848.
   Do not open it to the whole subnet.
3. Verify reachability *from production* before touching any config.
4. Only then add it to `WHISPER_ENDPOINTS` as a **low-concurrency** member, in
   a separate production-config change.

There is no authentication on this endpoint. Loopback-only is the safe default
and the LAN step should be treated as granting transcription access to anything
that can reach the port.

## Keep AI parsing off

`enable_ai_parsing` stays `false`. This worker is transcription capacity; it is
not a reason to re-enable AI parsing, and doing so would couple scan behaviour
to the Mac being up. (A local Ollama rung for *parsing* is a separate design —
see the unfinished LLM fallback chain, stages 2–4, in `TODO.md`.)

## Rollback

Stop the worker and leave it loopback-only. Remove its entry from
`WHISPER_ENDPOINTS`; queued production work is not deleted. The Go client
treats an unavailable endpoint as absent capacity, **not** as permission to
fall back to an in-process worker — so removing it slows transcription down
rather than changing any result.

## Reaching the worker from the server

The worker binds `127.0.0.1` and is published to the server over a reverse SSH
tunnel, so the server reaches it at `http://127.0.0.1:19848` and **nothing is
exposed on the LAN**. Access is authenticated by the existing SSH key, and the
server's sshd binds the forwarded port to loopback only (`GatewayPorts` defaults
to `no`).

Two launchd agents, both in `deploy/launchd/`:

| Agent | Does |
|---|---|
| `com.jdfalk.whisper-mlx.plist` | Runs the worker, bound to `127.0.0.1:19848` |
| `com.jdfalk.whisper-tunnel.plist` | Holds `ssh -N -R 19848:127.0.0.1:19848` to the server |

Install (replace the placeholders — the templates carry no host or username):

```sh
LA=~/Library/LaunchAgents
sed -e "s#REPO_PATH#$PWD#g" -e "s#USERNAME#$USER#g" \
    deploy/launchd/com.jdfalk.whisper-mlx.plist    > $LA/com.jdfalk.whisper-mlx.plist
sed -e "s#USERNAME#$USER#g" -e "s#PROD_HOST#<server>#g" \
    deploy/launchd/com.jdfalk.whisper-tunnel.plist > $LA/com.jdfalk.whisper-tunnel.plist
launchctl load -w $LA/com.jdfalk.whisper-mlx.plist
launchctl load -w $LA/com.jdfalk.whisper-tunnel.plist
```

### The tunnel must own its SSH connection

`com.jdfalk.whisper-tunnel.plist` passes `ControlMaster=no` and
`ControlPath=none`, and this is load-bearing. With a `ControlMaster auto` entry
in `~/.ssh/config` — which is a common setup — `ssh -R` attaches to a shared
multiplexed connection belonging to whatever interactive session opened it,
registers the forward there, and **exits 0**. The tunnel then works perfectly,
and dies when that unrelated session's `ControlPersist` window closes. launchd
sees the clean exit and, with `KeepAlive`, respawns straight back into the same
borrowed socket. A tunnel whose lifetime is not its own looks identical to a
healthy one right up until it disappears.

Verify the agent owns its connection, and that `KeepAlive` actually restores it:

```sh
pgrep -fl "ssh -N -o ControlMaster=no"     # must match; a bare `ssh -N -R` does not
kill -9 $(pgrep -f "ssh -N -o ControlMaster=no")
sleep 35 && pgrep -fl "ssh -N -o ControlMaster=no"   # a DIFFERENT pid
```

From the server, the worker should answer on loopback:

```sh
curl -s http://127.0.0.1:19848/health
# {"status":"ok","device":"metal","batch_pipeline":true,...}
```

## Routing work by capability ("tier routing")

Each endpoint has a set of capability **labels**. Work goes only to endpoints
whose set contains **every** label in `WHISPER_REQUIRES`. Surviving endpoints are
then ordered by `priority` exactly as before — so labels *filter* the candidates
and priority *orders* them. A "tier" is a required-label set; there is no routing
table.

```jsonc
// WHISPER_ENDPOINTS
[
  {"url": "http://gpu-box:19847", "priority": 1,  "concurrency": 4, "require_gpu": true},
  {"url": "http://mac:19848",     "priority": 50, "concurrency": 1,
   "capabilities": ["local", "quiet-hours"]}
]
```

```sh
WHISPER_REQUIRES=gpu          # any GPU box
WHISPER_REQUIRES=gpu,local    # only a GPU box that is also declared local
WHISPER_REQUIRES=             # empty = any endpoint (the default)
```

### Labels come from two places, and they are not interchangeable

**Measured** labels are derived from `/health` and cannot be declared by an
operator: `gpu`, `cpu`, `batch`, and the specific backend (`cuda`, `metal`,
`mps`, `rocm`, `hip`). Both the family and the backend are derived, so a
requirement can be as broad as `gpu` or as narrow as `metal`.

**Declared** labels are whatever an operator puts in `capabilities` for things no
probe can see — `local`, `unmetered`, `quiet-hours`, `fast`.

A declared label that collides with the measured namespace is **dropped and
logged**, never trusted. This is deliberate: the previous `kind: "gpu"` field let
an operator assert an endpoint was a GPU box and be believed, which is why it
never controlled anything and has now been removed. If you want an endpoint
treated as a GPU, it has to *prove* it on `/health`.

> **`kind` is gone.** It was informational only — written into config, read by
> nothing. An existing `"kind": "gpu"` in `WHISPER_ENDPOINTS` is now an ignored
> key, not an error; delete it and use `require_gpu` or `capabilities`.

### Failure behaviour

Matching is **conjunctive and fail-closed**:

- An empty requirement set means *any endpoint* — the historical behaviour.
- An endpoint whose `/health` cannot be read satisfies **nothing**, so it is
  refused whenever any label is required (distinct from cooldown, which means
  "this failed, retry later").
- If no endpoint qualifies, transcription returns a transport error naming the
  required set, each refused endpoint, what it was missing, and what it offered —
  so a typo'd label reads differently from a genuinely unqualified box.

## Refusing a worker that is not actually on a GPU

A Whisper worker on the wrong silicon does not fail. `ctranslate2` (what
faster-whisper runs on) has CUDA and CPU backends and nothing else, so on a
machine with an AMD card — or an Apple machine running the *non*-MLX server —
it loads happily on CPU and serves healthy 200s about ten times slower than
expected. Nothing in the library surfaces that; it reads as "transcription is
slow lately."

Set `require_gpu` on the endpoint to refuse it instead:

```json
[
  {"url": "http://gpu-box:19847", "priority": 1,  "concurrency": 4, "require_gpu": true},
  {"url": "http://mac:19848",     "priority": 50, "concurrency": 1, "require_gpu": true}
]
```

The gate reads the `device` field from `/health` and accepts only an explicit
allow-list: `cuda`, `metal`, `mps`, `rocm`, `hip`. It is **fail-closed** — an
endpoint is refused when

- `/health` cannot be read at all,
- `/health` reports no `device` (a `whisper_server.py` older than 2.9.0 — upgrade
  the worker, or leave `require_gpu` off for it), or
- the device is anything not on the list, including `cpu` and `auto`.

Refusals are logged per endpoint. If *every* endpoint is refused, transcription
returns a transport error naming each one and its reason, which is deliberately
distinct from the "no whisper endpoints configured" error — the fix for one is a
config typo, and for the other it is the hardware.

> **AMD note.** There is no setting that makes `whisper_server.py` use an AMD GPU;
> ctranslate2 has no ROCm backend. Using AMD silicon needs a different engine
> (e.g. whisper.cpp + Vulkan) behind the same three-endpoint shim this document
> describes for MLX. `rocm`/`hip` are on the allow-list so such a worker is not
> refused once it exists — not as a claim that faster-whisper supports AMD.

## Throttling: three knobs, in the order you should reach for them

| Setting | Scope | Default | What it means |
|---|---|---|---|
| endpoint `concurrency` | one server | 1 | Max **simultaneous requests** to that server. Since 2026-08-31 this is a real cap, enforced at the request; before that it was only an allocation weight and did not limit anything. |
| `whisper_max_in_flight` | whole pool | 0 (unlimited) | Ceiling on total simultaneous requests across **every** endpoint. Not the sum of the per-endpoint caps — use it when you have capacity but don't want it all in use. |
| `whisper_batch_size` | request shape | 16 | Files per `/transcribe-batch` call. **Smaller spreads work across the pool**; larger cuts HTTP overhead on servers that batch for real. |

`whisper_batch_sleep_ms` still exists but is a blind timer from the CUDA era. Prefer the
caps above; it is set to 0 in production.

Two things to know before you tune these:

- An omitted or `0` endpoint `concurrency` means **1**, not unlimited — unlike
  `whisper_max_in_flight`, where `0` does mean unlimited.
- Changing either cap takes effect **at restart**. A live change is logged and the
  established cap kept: swapping a live semaphore installs an empty one, which would
  remove the cap while every signal still reported it working.

## Do NOT demote the worker process

The launchd agent must not set `ProcessType=Background` or `Nice`. On Apple Silicon,
background QoS confines the worker to the **efficiency cores** (2 of 10 on an M1 Max).
Measured on one machine, same 90s clip, same model, same minute:

| | Median |
|---|---|
| normal QoS | **4.92 s** |
| `ProcessType=Background` | **>240 s** (timed out) |

It also inverts with scale: four background-QoS workers contend for the same two E-cores
and delivered *less* aggregate throughput than a single worker. To keep the machine
responsive under load, send fewer jobs (the caps above) rather than demoting the process.
