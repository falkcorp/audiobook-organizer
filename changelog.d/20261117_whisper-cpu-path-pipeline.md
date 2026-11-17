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

#### The model default follows the device

With no `--model` the worker runs `large-v2` on CUDA and `base.en` on CPU. The
decode fingerprint of a GPU install (large-v2, float16, batched, batch 8) is
unchanged, pinned in a test as `850881e4bf06fae4`. **The result journal key still
changes once.** `/health` now also reports `"backend": "faster-whisper"`, and the
server keys the journal as `<backend>/<model>`, so an upgraded GPU worker's
journal moves from `large-v2` to `faster-whisper/large-v2` and the transcripts it
already held are redone once under the new key. There is no alias for the old key
in this change. A GPU install that was launched from main's `manage-whisper-server.py`
(which passed `base.en` and `float16`, not the supervisor's `large-v2`) now gets
`large-v2` by default: a new fingerprint, about 3 GB of VRAM, and a first-start model
download. Pass `--model base.en` to keep the old model. A GPU worker that falls back
to the CPU switches to `base.en`.

#### The startup resolution is pinned for the life of the worker

The first decode process to start resolves `auto` (device, model, compute type).
Every later process and every restart is started with those values explicitly and is
refused if it reports anything else, so a restart can never quietly turn a
`large-v2` GPU worker into a `base.en` CPU one and file `base.en` text under
`large-v2`'s journal key. A process that cannot load exactly the pinned values is a
start fault (`worker_misconfigured` on path jobs, `child_start_error` on `/health`),
not a fallback. With `--num-workers` above 1 every process serves the same pinned
resolution or the worker refuses to start. `/health` reports the pinned values and
the parent logs what each process loaded.

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
`whisper_decode_child.py`. A new characterization suite (`test_whisper_mlx_pins.py`)
was written against the Mac worker before the move; it passes unchanged except for
two pins that were deliberately edited. Three changes in the shared modules reach
the Mac worker: ffmpeg now runs with `-protocol_whitelist file`, the
`path_map_error` text on `/health` no longer quotes path-map entries, and a
request-deadline header of more than 12 digits is capped instead of answering 500.

#### Decode children are tied to the worker

A decode process ignores Ctrl-C and runs in its own session (POSIX), so stopping the
worker does not fail the in-flight clip as `decoder_crashed`; a clip still in flight
when the worker shuts the process down answers `decoder_killed`, as the Mac worker's
does. On Windows each process is put in a Job Object with kill-on-close, so it dies
with the worker even while stuck in a native call. Any exception between sending a
clip and reading its answer kills and restarts the process, and every request
carries an id the process echoes, so a stale reply is never read as the next clip's
transcript. The timeout environment variables must be finite and positive (the worker
exits at startup naming the variable otherwise), the computed deadline is capped at
3600 s (`WHISPER_TRANSCRIBE_MAX_TIMEOUT_S`) and the upload routes refuse bodies over
256 MiB (413): 256 MiB is about 2.3 hours of 16 kHz stereo PCM, far above an intro
clip; the cap keeps a huge upload from earning a multi-day deadline.

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
20-in-10-minutes breaker could never trip). A run counts as healthy only if it
answered `/health`, so a start that never became ready counts as a failure however
long it ran. The installer and
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
