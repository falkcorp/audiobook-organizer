### Added

#### A Whisper transcription worker that actually runs on Apple Silicon

`scripts/whisper_mlx_server.py` is an optional Mac-only transcription worker
built on Apple MLX, speaking the exact protocol `internal/transcribe/remote.go`
already expects (`/health`, `/transcribe`, `/transcribe-batch`).

It exists as a second server rather than a device flag on the existing one for
a concrete reason: `scripts/whisper_server.py` runs faster-whisper on
ctranslate2, which has no Metal/MPS backend. Pointing it at a Mac does not
fail — it silently runs on CPU, roughly an order of magnitude slower, while
still serving a healthy `/health`. A silent downgrade is worse than a refusal,
so the Metal path gets its own inference stack.

The worker adds capacity only. It binds loopback by default (the CUDA server
binds `0.0.0.0`), listens on 19848 so both can run on one host, and is not
wired into `WHISPER_ENDPOINTS` by this change. When it is offline the Go client
treats it as absent capacity rather than permission to fall back, so queued
work stays durable and scans never depend on the Mac being up. `enable_ai_parsing`
is untouched and stays off.

Two behavioural notes for operators: transcripts are not byte-identical to the
CUDA server's, because that one applies a tuned VAD filter and MLX Whisper has
no equivalent; and the batch endpoint keys results by each part's filename
verbatim, which is the book ID the Go client looks up — normalising it in any
way makes the transcript unfindable.

Operations guide: `docs/operations/metal-whisper-worker.md`.
