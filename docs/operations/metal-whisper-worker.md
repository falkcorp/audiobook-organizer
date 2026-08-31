<!-- file: docs/operations/metal-whisper-worker.md -->
<!-- version: 1.0.0 -->
<!-- guid: c47a8e21-93b5-4d06-8f7a-1e6b2c9d5308 -->
<!-- last-edited: 2026-08-30 -->

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
