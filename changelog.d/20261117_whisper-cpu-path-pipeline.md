### Added

#### Non-Mac Whisper worker serves the path pipeline, on CPU or GPU

`scripts/whisper_server.py` now answers `/transcribe-path` and
`/transcribe-path-batch` (the server sends a file path, the worker maps it
through `WHISPER_PATH_MAP`, cuts the first seconds with ffmpeg and transcribes
the clip), with the same request and result shapes, error codes and `/health`
fields as the Mac worker, including `backend`, `machine_id`, `busy_seconds` and
the mapped roots' readability. `--device auto|cpu|cuda` (`WHISPER_DEVICE`) picks
the device: `auto` uses CUDA when it loads and otherwise logs one line and runs
on the CPU, `cpu` never touches CUDA, and an explicit `cuda` that cannot load is
an error; an `auto` fallback from CUDA to CPU is logged at WARNING and shown in
`/health` (`fallback`, `requested_device`, `device_reason`). CPU defaults to int8
with configurable model, threads (`--cpu-threads`) and decode processes
(`--num-workers`). `/health` reports the device that actually loaded.

#### The non-Mac worker kills a hung decode

The model now loads in a spawned child process and the parent enforces a per-clip
deadline (`max(300 s, 8 x audio length)`, unbenchmarked), killing a child that
misses it and starting a fresh one. It answers the Mac worker's
`transcribe_timeout`, `decoder_crashed` and `decoder_killed`, so a hung decode no
longer wedges the worker until someone restarts it. The upload endpoints get the
same length-derived limit and honour the request-deadline header.

#### The model default follows the device (an existing GPU install is unchanged)

With no `--model` the worker runs `large-v2` on CUDA and `base.en` on CPU. An
existing GPU install keeps large-v2 and the same decode fingerprint as before
(pinned in a test), so its result journal is not re-keyed. A GPU worker that falls
back to the CPU switches to `base.en`.

#### The non-Mac worker binds loopback unless told otherwise

`whisper_server.py` binds `127.0.0.1` by default, like the Mac worker, and logs a
warning when bound wider. The worker has no authentication: a worker bound to the
LAN (`--host 0.0.0.0`, `-BindHost` in the supervisor and installer) lets any host
that can reach the port obtain transcripts of files under its mapped roots, so a
LAN worker is an explicit opt-in and should be firewalled to the server. This
changes the old default of `0.0.0.0`: a worker the server reaches directly over
the LAN must now be started with `-BindHost 0.0.0.0` (or reached through an SSH
tunnel). `/health` no longer quotes path-map entries in `path_map_error`.

#### Windows path rules and a Windows machine id

The shared path code understands drive letters, UNC shares, case-insensitive
containment and long paths, and refuses server paths whose components Windows
cannot name (a colon or backslash, a trailing dot or space, `CON` or `NUL.m4b`)
with their own code, `path_unrepresentable` (the Go side treats an unknown code as
a retryable worker error, so the book is retried), distinct from `path_unmapped`.
A bare drive letter (`X:`) is refused as a local root. The machine id gains the
Windows MachineGuid as a source.

### Changed

#### Path pipeline and machine identity moved into shared modules

The backend-independent half of `whisper_mlx_server.py` now lives in
`scripts/whisper_path_pipeline.py` and `scripts/whisper_machine_id.py`, imported
by both workers. Deploy `whisper_server.py` together with those two files and
`whisper_decode_child.py`. A new
characterization suite (`test_whisper_mlx_pins.py`) was written against the
Mac worker before the move and passes unchanged after it.

#### Request limits on the path routes (both workers)

At most 1024 jobs and 512-character keys per `/transcribe-path-batch` (422), a
path of at most 4096 characters (per-job `bad_request`) and a request body of at
most 8 MiB (413). The server's own requests are far below each limit.

#### Windows supervisor takes `-Device` and backs off on crashes

`whisper-supervisor.ps1` no longer defaults to `float16` (which the CPU cannot
run); it takes `-Device`, `-CpuThreads`, `-NumWorkers` and `-PathMap`, restarts
with a doubling delay, refuses to start when a sibling module is missing, and
quotes its arguments (including a `-PathMap` ending in a backslash), takes
`-BindHost`, and gives up after 8 crashes in a row (`-MaxCrashStreak`; the old
20-in-10-minutes breaker could never trip). The installer and
`manage-whisper-server.py` deploy all four server files and no longer hard-code a
user profile path or account name (`WHISPER_WIN_USER` is needed only by
`--deploy` and `--restart`). `manage-whisper-server.py` copies files in chunks
with a SHA-256 check (a 47 KB file cannot go in one WinRM command), uses one port
(`WHISPER_PORT`, default 19847; it used 8000) and launches loopback-bound.

#### ffmpeg runs with `-protocol_whitelist file` (both workers)

The clip cutter may only read local files. The extract fingerprint is unchanged.

### Fixed

#### `whisper_server.py` pins `av<19`

faster-whisper 1.2.1 calls `av.open(..., metadata_errors=...)`, which PyAV 19
removed, and faster-whisper only requires `av>=11`, so an unpinned `uv run`
resolved av 19 and every decode failed with `open() got an unexpected keyword
argument 'metadata_errors'`.
