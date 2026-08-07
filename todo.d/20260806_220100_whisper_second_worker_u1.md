<!-- file: todo.d/20260806_220100_whisper_second_worker_u1.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f31d4b8-92ae-4c07-b5f1-08e3a7d26c94 -->
<!-- last-edited: 2026-08-06 -->

- [ ] **Stand up a second Whisper worker on the spare CPU node.** Owner request
  2026-08-06. Host prepared, worker not built. (Host address and credentials are
  fleet-internal — see the private infra notes, not this repo.)

  **Why it is cheap to try:** the transcription backend is already a pluggable
  HTTP service — `WHISPER_REMOTE_URL` points at a faster-whisper instance on the
  GPU host. Adding a second worker is a deployment question, not a code change.
  `internal/transcribe/batch.go:51` reads a single URL today, so the only code work
  is fanning out across several endpoints.

  **The node as measured 2026-08-06:** Ubuntu 26.04, 48 cores, 251 GB RAM,
  **no GPU**. Its Tdarr node registers CPU-only with `transcodegpu: 0`, the Tdarr
  queue is **empty** (`table1Count: 0`), and both node processes sit at 0.0% CPU —
  so nothing needs stopping to free it. Python 3.14.3 with pip 25.1.1 and uv 0.12.2
  (both installed 2026-08-06).

  🔴 **CPU-only is the whole caveat.** faster-whisper with int8 quantisation on 48
  cores is real, but it is **not** a second GPU. **Benchmark against a real clip
  batch before promising throughput** — do not assume it halves the backfill.

  **Prefer an HTTP endpoint over the in-process `uv` path.** `whisper.go` also has
  `runPythonWhisper` (`uv run --with openai-whisper whisper`), and uv is now
  installed so that route works — but `batch.go:54-57` warns it loads the full
  model into RAM and *"reliably OOMs the server"* at batch sizes of 100–200. That
  warning was written about the **web-serving host**; the spare node has 251 GB and
  serves nothing, so the reasoning does not transfer directly. Even so, a second
  HTTP endpoint matches the existing interface, avoids the OOM class entirely, and
  needs no special batch sizing.

  **Point it at tier 3 first.** The lazy full sweep in
  [[per-file-intro-identity-signal]] has no deadline, which makes it the natural
  consumer for a slower worker — "slower than GPU" costs nothing there, while the
  decision-critical tiers keep the GPU.
