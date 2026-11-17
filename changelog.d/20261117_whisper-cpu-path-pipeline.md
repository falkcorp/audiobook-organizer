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
an error. CPU defaults to int8 with configurable model, threads
(`--cpu-threads`) and workers (`--num-workers`). `/health` reports the device
that actually loaded. The CPU worker decodes in-process, so it never answers
`transcribe_timeout` or `decoder_*`; see `docs/whisper-remote-extract.md`.

#### Windows path rules and a Windows machine id

The shared path code understands drive letters, UNC shares, case-insensitive
containment and long paths, and refuses server paths whose components Windows
cannot name (a colon or backslash, a trailing dot or space, `CON` or `NUL.m4b`)
as retryable `path_unmapped`. The machine id gains the Windows MachineGuid as a
source.

### Changed

#### Path pipeline and machine identity moved into shared modules

The backend-independent half of `whisper_mlx_server.py` now lives in
`scripts/whisper_path_pipeline.py` and `scripts/whisper_machine_id.py`, imported
by both workers. Deploy `whisper_server.py` together with those two files. A new
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
quotes its arguments. The installer and `manage-whisper-server.py` deploy all
three server files and no longer hard-code a user profile path or account name
(`WHISPER_WIN_USER` is now required by the manage script).

### Fixed

#### `whisper_server.py` pins `av<19`

faster-whisper 1.2.1 calls `av.open(..., metadata_errors=...)`, which PyAV 19
removed, and faster-whisper only requires `av>=11`, so an unpinned `uv run`
resolved av 19 and every decode failed with `open() got an unexpected keyword
argument 'metadata_errors'`.
