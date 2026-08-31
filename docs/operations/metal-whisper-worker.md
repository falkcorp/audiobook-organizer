<!-- file: docs/operations/metal-whisper-worker.md -->
<!-- version: 1.3.0 -->
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
