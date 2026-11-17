<!-- file: docs/whisper-remote-extract.md -->
<!-- version: 1.8.0 -->
<!-- guid: 38f7ba56-da3d-4ab2-8b9c-839df9cb5612 -->
<!-- last-edited: 2026-11-17 -->

# Whisper intro transcription with Mac-side extraction

`maintenance.transcribe-book-intros` used to cut each book's first 90 seconds
with ffmpeg **on the server**. Since 2026-10-09 the server refuses that
(`serverdecode.Check`, standing rule: audio decoding runs on the Macs only), so
a live run failed immediately with "audio decoding is not allowed on this
server".

The fix keeps the rule and moves the cut to the Mac. The server sends a
Whisper worker the **path** of the book's first audio file and a clip length;
the worker reads the file through its own NFS mount of the library, cuts the
clip with its own ffmpeg, and transcribes it. The server does one `stat` per
book and decodes nothing.

```
server (no decode)                         Mac worker (ports 19848-19851)
  book -> /srv/library/Gotham/Wayne/01.m4b
  POST /transcribe-path-batch  ------------>  WHISPER_PATH_MAP: /srv/library -> ~/mnt/library
  {"jobs": {id: {path, seconds}}}             ffmpeg -ss 0 -t 90 ... -> 16 kHz mono WAV
                                              mlx whisper -> text
  {"results": {id: {text, error}}}  <-------
```

## What you have to do on the Mac

Two things, once: mount the library, then tell the workers how server paths
map onto the mount.

### 1. Mount the library as your login user (no root)

NFSv4 fails for a non-root mount with "Invalid argument"; NFSv3 works.

```bash
mkdir -p ~/mnt/library
mount_nfs -o vers=3,ro,soft,noresvport,nolocks,timeo=10,retrans=3,deadtimeout=30 <server-host>:/srv/library ~/mnt/library
ls ~/mnt/library | head          # must list the library
```

`<server-host>` is "the server" (the host that exports `/srv/library`). The
mount is read-only on purpose: the worker never writes to the library.

The timeout options are not optional. `soft` makes a read on an unreachable
server fail with an I/O error instead of blocking forever, and `timeo=10` (1 s,
in tenths of a second) and `retrans=3` bound each request's retries. `deadtimeout`
(seconds; `man mount_nfs`) works differently from what "fails until the server
answers" suggests: once the mount has stayed unresponsive that long, it is marked
dead and **forcibly unmounted**. Soft read-only mounts get `deadtimeout=60` by
default; `30` here shortens it. After that the mountpoint is an **empty local
directory**, not a failing mount.

The worker accounts for that: a mapped root only counts as readable while it is
still a mount point (`os.path.ismount`), so a forcibly unmounted share shows as
`readable: false` in `/health` and each job answers `path_unmapped`, instead of
an empty listing turning every file into `not_found`. Consequences to know:

- The mapped local root must be the mount point itself (`~/mnt/library`), not a
  subdirectory of it. A map that deliberately points at a plain directory needs
  `WHISPER_PATH_REQUIRE_MOUNT=0` in the worker's environment.
- Recovery after a forced unmount is the LaunchAgent below: it re-mounts within
  its 5-minute interval, and the worker needs no restart.
- Without these options a dropped server can leave ffmpeg and the health check
  hanging on the mount. The 30 s figure is the documented meaning of the option;
  it has not been measured here by pulling the server away mid-read. The worker's
  own `WHISPER_EXTRACT_TIMEOUT` (120 s per clip) is the backstop either way.

To mount it at every login and re-mount it if it drops, use a user LaunchAgent.
Save as `~/Library/LaunchAgents/com.example.library-nfs.plist` (replace
`<server-host>`; the paths are absolute because launchd does not expand `~`):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.example.library-nfs</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>mkdir -p /Users/<you>/srv/library &amp;&amp; { /sbin/mount | /usr/bin/grep -q ' on /Users/<you>/srv/library ' || /sbin/mount_nfs -o vers=3,ro,soft,noresvport,nolocks,timeo=10,retrans=3,deadtimeout=30 &lt;server-host&gt;:/srv/library /Users/<you>/srv/library; }</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>StartInterval</key><integer>300</integer>
  <key>StandardErrorPath</key><string>/Users/<you>/Library/Logs/library-nfs.err</string>
</dict>
</plist>
```

`StartInterval` re-runs the script every 5 minutes; it does nothing while the
mount is present and re-mounts after a drop. Load it with
`launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.example.library-nfs.plist`.

### 2. Give every worker `WHISPER_PATH_MAP`

```
WHISPER_PATH_MAP=/srv/library=/Users/<you>/srv/library
```

Format: comma-separated `server-path=mac-path` pairs. Both sides absolute, no
`..`, neither `/`. Longest server prefix wins. There is **no default**: unset
means the worker advertises `path_pipeline: false` and refuses every path job.
A malformed value does the same and says why in `/health` (`path_map_error`).

The workers are the launchd agents `com.jdfalk.whisper-mlx` (port 19848) and
`com.jdfalk.whisper-mlx-19849`, `-19850`, `-19851`. Add the variable to each
plist's `EnvironmentVariables` dictionary (do it in your own editor, or with
`/usr/libexec/PlistBuddy -c 'Add :EnvironmentVariables:WHISPER_PATH_MAP string /srv/library=/Users/<you>/srv/library' <plist>`).

Then **reload** each agent. launchd reads a plist only when the job is
bootstrapped, so `kickstart` alone restarts the worker with the *old*
environment:

```bash
for label in com.jdfalk.whisper-mlx com.jdfalk.whisper-mlx-19849 \
             com.jdfalk.whisper-mlx-19850 com.jdfalk.whisper-mlx-19851; do
  launchctl bootout   gui/$(id -u)/$label 2>/dev/null
  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/$label.plist
done
```

Once the variable is already in the plist (for example after pulling a new
`whisper_mlx_server.py`), a plain restart is enough:

```bash
launchctl kickstart -k gui/$(id -u)/com.jdfalk.whisper-mlx
launchctl kickstart -k gui/$(id -u)/com.jdfalk.whisper-mlx-19849   # and -19850, -19851
```

`scripts/setup_mac_ai_worker.py` rewrites these plists from scratch, so pass
`--whisper-path-map /srv/library=/Users/<you>/srv/library` whenever you re-run
it, or the variable is dropped.

It also gives `--host-index 0` the server-side ports `19848..`, the same port the
older `deploy/launchd/com.jdfalk.whisper-tunnel.plist` hard-codes: never run a Mac
on that plist and a different Mac at host index 0, or the two race for one server
port and the server reaches a different Mac through the same URL (see "One server
port, one Mac" in `docs/operations/metal-whisper-worker.md`).

### 3. Verify

On the Mac:

```bash
curl -s http://127.0.0.1:19848/health | python3 -m json.tool
```

Expect `"path_pipeline": true`, `"path_prefixes": ["/srv/library"]`, and
`"path_roots": [{"prefix": "/srv/library", "readable": true}]`. Check all four
ports, and check from the server through the tunnel too (`127.0.0.1:19848` ...
`19851` there).

`readable` is checked live with a directory listing. A shell in Terminal can
read the volume under Terminal's own privacy grant, which says nothing about
the launchd agent, so **trust the agent's `/health`**, not your shell. If it
says `readable: false` while `ls ~/mnt/library` works, macOS is denying the
agent access to the network volume (System Settings, Privacy and Security).

## How the op behaves

`remote_extract` is an op parameter:

| value | meaning |
|---|---|
| omitted (default) | **remote** when this host may not decode (`ALLOW_SERVER_DECODE` unset, i.e. production) and some Whisper endpoint advertises `path_pipeline`; otherwise local |
| `true` | remote; the run errors if no endpoint can serve it (never silently local) |
| `false` | local ffmpeg, which needs `ALLOW_SERVER_DECODE=1` as before |

`extract_only` is always local: it rebuilds the server's clip cache, which a
worker cannot fill. Remote mode skips the clip cache entirely. A dry run (the
default) names the mode it would use.

Only the local path calls `serverdecode.Check`. The silence retries (a 300 s
clip, then the second audio file) go through the worker as well, so a
remote-mode run never decodes on the server.

### Failure behaviour (everything fails closed)

- **No endpoint advertises `path_pipeline`, or a mapped root is unreadable
  (mount lost):** the pool refuses the endpoint with that reason, the page is
  **deferred**, and **no book status is written**. Fix the mount; the next run
  picks the books up.
- **Mount lost between the health probe and a job:** the worker answers that job
  `path_unmapped`. The server then stats the file itself. If it cannot see it
  either the book is `source_file_missing`; if it can, the book is
  **`path_unmapped`**, a new status that is not durable and is retried next run.
- **Worker's ffmpeg cannot decode the file:** `ffmpeg_error` (durable, as
  before), but only when the file is still readable afterwards. A nonzero ffmpeg
  exit is re-checked: if the mapped root can no longer be listed or the file is
  gone the worker answers `path_unmapped`, and if ffmpeg's error text names an
  I/O fault or timeout (`Input/output error`, `Operation timed out`, `Stale file
  handle`) it answers `ffmpeg_timeout`. A *timeout* is not a decode verdict and
  is recorded as the retryable `whisper_error`.
- **Container refused by the format whitelist** (an AIFF renamed `.m4b`, an
  unusual file): the worker answers `container_rejected`, which is this worker's
  policy and not a verdict about the file, so it is recorded as the retryable
  `whisper_error` (never the durable `ffmpeg_error`). A corrupt file that is in a
  whitelisted container is still `ffmpeg_error`.
- **Worker has no ffmpeg on its PATH:** the worker answers
  `worker_misconfigured`. That is a fault of the worker, not of the book: the
  server writes no status and counts the book **deferred**.
- **Planted playlists:** ffmpeg is started with `-format_whitelist` listing the
  audio containers only, so a `.m3u8`, `.sdp` or concat "audio file" dropped in
  the share cannot make ffmpeg's playlist demuxers open other paths on the Mac.
- **A silence retry that fails:** the `[SILENCE]` marker, which makes
  `only_missing` skip a book for good, is written only when the retries came
  back as successful empty results. A retry that hit a transport error, got no
  result, or met a misconfigured worker writes nothing and the book is deferred.
  A retry that returned a real per-book verdict (for example `ffmpeg_error` on
  the 300 s clip) is classified like any other worker error.
- **Path outside every mapped prefix, containing `..`, or resolving outside its
  root through a symlink:** refused per job, same handling as above.

`stats:transcribe` gains a `path_unmapped` counter. The result journal keys
path jobs by the file's path, size, mtime and clip length plus the worker's
model, decode and extract fingerprints, so a replaced file is never served an
old transcript and upload-path entries are unaffected.

### Known limitation

If workers in one pool were given **different** path maps, a job can be sent to
a worker that does not map its prefix and come back `path_unmapped`. All four
current workers share one map, which is the supported setup.

## A non-Mac worker: Windows or Linux, on CPU or GPU

`scripts/whisper_server.py` serves the same path pipeline as the Mac worker, on
CPU or on a CUDA GPU, so a Windows box (or any non-Mac machine) can take the
`path_pipeline` jobs above. The wire contract is the Mac worker's: the same
`/transcribe-path-batch` request and result shapes, the same error codes, the
same `/health` fields. The path pipeline, its path-safety rules and the `/health`
body are **shared code** (`scripts/whisper_path_pipeline.py`,
`scripts/whisper_machine_id.py`) that both workers import, so they cannot drift.

**Deploy all four files together:** `whisper_server.py`,
`whisper_path_pipeline.py`, `whisper_machine_id.py` and `whisper_decode_child.py`,
in one folder. The Windows supervisor refuses to start without the three modules.

### Who can reach the worker (read this before binding wider)

**The worker has no authentication.** It binds `127.0.0.1` by default, exactly like
the Mac worker, and the server reaches it through an SSH tunnel. A worker bound
wider (`--host 0.0.0.0` or an interface address; `-BindHost` in the supervisor and
installer) is an explicit opt-in, and what it exposes is this: **any host that can
reach the port can ask the worker to transcribe any file under its mapped roots and
read the transcript back**, and can occupy its decode slots. It logs a warning at
start when bound beyond loopback. If a LAN worker is what you need, firewall the
port to the server only. Real authentication for both workers (a shared-secret
header the shared module checks and the Go client sends) is filed as a follow-up
and not part of this change.

`/health` no longer echoes the path-map entries when the map is invalid: the error
names the entry's position ("entry 2 is not server-path=local-path") and never
quotes its text, because `/health` is unauthenticated.

**What an error answer says, on both workers.** Nothing a failure said. Every error
that leaves either worker in an HTTP body, a per-clip `error` on the upload and path
routes and the body of an unexpected 500 alike, is a stable code followed by either one
fixed sentence (a code with a single reason, `whisper_error: transcription failed on this
worker`) or one word from a closed set for that code (a code with several reasons). One
function (`wire_error` in `scripts/whisper_path_pipeline.py`) builds both and takes no
free text, so a path, an ffmpeg stderr line or a library's exception message cannot reach
it. The sets, from the raise sites:

| Code | Words |
|---|---|
| `ffmpeg_error` | `invalid_data`, `moov_missing`, `no_audio_stream`, `unsupported_codec`, `truncated_file`, `permission_denied`, `empty_clip`, `invalid_wav`, `spawn_failed`, `other` |
| `ffmpeg_timeout` | `extract_timeout`, `io_fault`, `os_io_error` |
| `path_unmapped` | `not_absolute`, `dot_dot`, `not_under_prefix`, `root_unreadable`, `symlink_escape`, `path_map_unset`, `path_map_invalid`, `mapping_failed`, `source_unreadable` |
| `path_unrepresentable` | `bad_character`, `component_too_long`, `reserved_name`, `trailing_dot_or_space` |
| `bad_request` | `job_not_object`, `path_invalid`, `path_too_long`, `seconds_invalid` |
| `decoder_killed` | `killed_by_signal`, `not_reaped` |
| `decoder_crashed` | `died_on_clip`, `wrong_answer` |
| `worker_misconfigured` | `exited_during_start`, `ffmpeg_missing`, `model_load_failed`, `pinned_model_mismatch`, `process_start_failed`, `protocol_error`, `start_timeout` |

`not_found`, `whisper_error`, `container_rejected`, `transcribe_timeout`,
`request_abandoned` and `internal_error` have one reason each and carry a fixed sentence.
ffmpeg's stderr is mapped onto the `ffmpeg_error` words by a short ordered table of
phrases (`moov atom not found` is `moov_missing`, `Invalid data found` is `invalid_data`,
and so on) with `other` for anything unmatched, so which line ffmpeg printed never decides
what is sent. The detail that used to be in the answer (the exception, ffmpeg's last line,
the `OSError`, the exit code of a dead decode process) is written to the worker's log at
the moment the answer is made, and a refused batch is logged once per request with the job
count, grouped by code and word with the first detail of each group, not once per job.

**What the server stores.** The Go client reads the code and nothing after it to choose a
status (`transcribe.PathErrorCode` cuts on the first `: `). The `transcribe_error` it
stores is the text after the code for `ffmpeg_error` (the status is already `ffmpeg_error`,
so the stored detail is the word alone, e.g. `invalid_data`), and the whole
`<code>: <word>` for every other code (e.g. `path_unmapped: root_unreadable`,
`decoder_crashed: died_on_clip`). When the worker could not reach a file the server cannot
stat either, the stored detail is the server's own `source file not found: <path>`. Nothing
on the Go side parses a word.

`/health` reports `ffmpeg` as `true`/`false` (found or not), never where it was found. The
path map's error and `device_reason` were already fixed text. `identity.model` (also
`/health` `model`) is the `--model` value only when that is a hub id; when it is an
existing directory or an absolute path (POSIX, a Windows drive or UNC path, or a leading
`~`) it is `local:` plus the first 16 hex digits of the SHA-256 of the value, and the worker
logs the raw value once at startup so the label can be traced. The decode fingerprint still
hashes the raw value, so settings that differ only in the model's location still differ.
One limit: FastAPI's own 422 for a request that is not a JSON object echoes the caller's
own input back to the caller.

What guards this: `test_whisper_server_cpu.py` and `test_whisper_mlx_pins.py` drive every
error route with planted paths and stderr and look for them in the body; those runtime
tests are the guarantee. `test_whisper_wire_text_guard.py` is a cheaper tripwire that
parses every `scripts/whisper_*.py` and fails on a dict `error`/`detail`, an
`HTTPException` detail (keyword or positional), the content of a `*Response`, a
`_note_start` argument, or a `ChildFaultError`/`PathJobError` category that is not built in
the sanctioned shape (a `wire_error(...)` call, a fixed string, or a `CAT_*` constant). It
does not prove other shapes safe (a body assembled in a variable, a key other than
`error`/`detail`), which is why the runtime tests exist.

### Choosing the device

```
uv run scripts/whisper_server.py --device auto   # default: CUDA if it loads, else CPU
uv run scripts/whisper_server.py --device cpu    # never touches CUDA
uv run scripts/whisper_server.py --device cuda   # GPU or an error
```

Every flag has an environment variable (`WHISPER_DEVICE`, `WHISPER_MODEL`,
`WHISPER_COMPUTE_TYPE`, `WHISPER_CPU_THREADS`, `WHISPER_NUM_WORKERS`,
`WHISPER_BATCH_SIZE`, `WHISPER_HOST`, `WHISPER_PORT`); a flag beats its variable.

- `auto` uses CUDA when a device is found **and** the model loads on it, and
  otherwise runs on the CPU. That fallback is logged at **WARNING** ("whisper GPU
  FALLBACK ...") and is visible in `/health` (`fallback: true`, with
  `device_reason`): a worker you expected to be a GPU worker that quietly became a
  slow CPU one is exactly what should be noticed. `device_reason` is one of a few
  fixed phrases (`requested cpu`, `auto: CUDA probe failed`, `auto: no CUDA device
  found`, ...): `/health` is unauthenticated, so the exception behind a failed probe
  or load is in the worker log, never on `/health`. Use `cpu` on a machine whose GPU
  is gone or flaky: there is nothing to probe, so nothing to fail.
- `cuda` is strict. If the model cannot load on the GPU the process exits 1 with the
  reason; it never quietly serves from the CPU.
- `/health` reports the device that **actually loaded** (`device`), and
  `requested_device` is what you asked for (`auto`, `cpu` or `cuda`), always. The
  device and compute type the load first tried are `attempted_device` and
  `requested_compute_type`. Also `device_source`, `device_reason`, `fallback`,
  `num_workers` and `path_flavor` (`nt` or `posix`: how this worker judges path
  components, which the server can use to route files a Windows worker cannot name).

### The model follows the device

With no `--model`, the worker picks by the device it ends up on: **`large-v2` on
CUDA, `base.en` on CPU**, and logs which and why. An existing GPU install therefore
keeps the model it always ran and produces the **same decode fingerprint** as before
this change (pinned in `test_whisper_server_cpu.py`: `850881e4bf06fae4` for
large-v2, float16, batched, batch size 8). **The result journal key still changes
once**: `/health` now sends `"backend": "faster-whisper"`, so the server keys the
journal `faster-whisper/large-v2` instead of `large-v2`, and an upgraded GPU worker's
earlier transcripts are redone one time under the new key (no alias is provided). A
GPU install launched from `manage-whisper-server.py` as it was on main (which passed
`base.en` and `float16`) now defaults to `large-v2`: a new fingerprint and about 3 GB
of VRAM; pass `--model base.en` to keep the old model. An `auto` worker that falls back from CUDA to CPU re-resolves to `base.en`, because
`large-v2` on a CPU would not keep up; pass `--model` to override either way.

### CPU defaults

| setting | default on CPU | note |
|---|---|---|
| compute type | `int8` (reported by ctranslate2 as `int8_float32`) | `float16` does not exist on CPU; `--device cpu` with it is a usage error |
| model | `base.en` | `small.en` is the next step up in quality and several times slower |
| `--cpu-threads` | logical cores minus 2, at least 1, at most 8 | leaves room for the OS, ffmpeg and `/health` |
| `--num-workers` | 1 | decode **processes**, each holding its own copy of the model (memory scales with it); raise only on a machine with many more cores than threads |

These are starting points, **not measurements**: nothing here was benchmarked on a
particular desktop CPU. Measure one dry-run dispatch (below) and adjust. The path
pipeline only ever transcribes the first 90 seconds of a book, which is what makes
a CPU worker worth running at all.

Changing the model, compute type or decode path changes `decode_fingerprint`,
which re-keys that worker's result journal: transcripts journalled under the old
settings are not served for the new ones. Threads and workers do not change it.

### Every answer names the worker that produced it

The server journals a transcript under the model and fingerprints of the worker that
decoded it. It used to take them from a `/health` probe that can be 30 seconds old,
and a worker process that was replaced since (its supervisor restarted it and
`--device auto` resolved differently) would have had its text filed under the old
model. So `/transcribe`, `/transcribe-batch`, `/transcribe-path` and
`/transcribe-path-batch` now carry a top-level `identity` object (`model`, `backend`,
`decode_fingerprint`, `extract_fingerprint`), read from the worker's startup pin by
the same function `/health` uses, so the two cannot disagree and a restarted child
cannot change it. The server files each result under the identity of the answer that
carried it. When the identity differs from the probe it logs a warning and drops that
endpoint's cached probe (whether or not the call has a journal), so the next dispatch
asks `/health` again. An `identity` is complete or it is not used: it needs a model, a
backend and a decode fingerprint (and, for a path result, an extract fingerprint), none
of them blank. A partial one is logged, the cached probe is dropped, and that answer's
results are NOT journalled at all; they are not filed under the probe's identity either,
because a row keyed on less than the whole identity would be served to a worker that
decodes differently. A worker too old to send `identity` is journalled under its
`/health` identity as before, and that is logged once per endpoint. One limit: the journal is looked up, before a request is
sent, with the identity the last probe gave, so a result is never filed under the
wrong model but a lookup in the first dispatch after a replacement can still miss or
hit under the old one. The Mac worker does not send `identity` yet.

### A hung decode is killed, not waited on

faster-whisper decodes inside a native call Python cannot interrupt. The worker
therefore loads the model in a **child process** (`scripts/whisper_decode_child.py`,
started with the `spawn` method, the only one Windows has) and the parent enforces a
deadline per clip. A decode that misses it is killed and a fresh child is started
for the next clip, so the worker recovers by itself. The first child to start
**pins** the model, device, compute type and batched mode for the life of the worker
(batched is part of the decode fingerprint: a batched pipeline that failed to build
would otherwise decode by beam search and file that text under the batched worker's
fingerprint): every later child and restart is started with those values explicitly and refused (a start fault,
not a fallback) if it reports anything else, so a restart cannot turn a `large-v2`
GPU worker into a `base.en` CPU one. `/health` reports the pinned values and the log
line `decode process N ready: model=... device=... compute_type=...` follows every
start. A request id travels with each clip and any exception between send and
receive kills the child, so a stale answer is never taken for the next clip's:

- `transcribe_timeout`: no result within the deadline (the child was killed);
- `decoder_crashed`: the child died on the clip (a segfault, an exception that took
  the process down); the exit code is in the worker log;
- `decoder_killed`: the child was killed from outside (an operator, the out-of-memory
  killer; POSIX only, since Windows reports no signal, so a process killed from
  outside reads as `decoder_crashed` there);
- `worker_misconfigured`: the child could not start (the model failed to load); the
  worker retries on a later request and shows `child_start_error` in `/health` until
  it can. `/health` and the job answer are both reachable without authentication, so
  both carry only a fixed code and a short category word (`model_load_failed`,
  `start_timeout`, `exited_during_start`, `process_start_failed`,
  `pinned_model_mismatch`, `protocol_error`, and `ffmpeg_missing` for a job; the job
  error reads
  `worker_misconfigured: model_load_failed`); the full error text, which can hold a
  path or a library's exception, goes to the worker log only. A worker is
  also reported busy (`busy_seconds`) while a child is being started for a request.

These are the Mac worker's codes with the same meaning, so the server treats them
identically. The Mac worker's own child (`InferenceChild`) is built on POSIX file
descriptors and process groups, and only the contract is shared; its start faults now
carry the same category words, so its `child_start_error` and job answers are the
fixed text too, not the message they used to be.

The per-clip deadline is `max(300 s, 8 x the clip's audio length)`, capped at 3600 s
(`WHISPER_TRANSCRIBE_MAX_TIMEOUT_S`; a clip of ten minutes of audio would otherwise
earn 4800 s and a huge upload days). Upload bodies are limited to 256 MiB (413),
which is about 2.3 hours of 16 kHz mono 16-bit PCM or about 1.2 hours of stereo.
The timeout variables must be finite and above zero, or the worker exits at startup
naming the variable. The child ignores Ctrl-C and, on Windows, lives in a Job Object
that kills it with the worker (if the worker could not put a child in the job, the
child is not protected that way and `/health` says `child_unprotected: true`);

Stopping the worker: the child ignores Ctrl-C, and the worker kills it only in
`engine.shutdown()`, which runs after the web server has returned. The web server is
started with its default graceful shutdown, which (as far as the code shows; not
tried with a hung decode) waits for requests in flight, so a stop during a hung decode
can wait up to that clip's deadline (at most 3600 s) unless the worker is stopped a
second time. No clip answers `decoder_killed` because of a stop. A worker killed
outright (the scheduled task stopped, `Stop-Process`) gives its caller a dropped
connection and no error, and on Windows the Job Object ends the child with it; on POSIX a child stuck in a native call that holds the
GIL is not reaped if the worker is killed (the orphan watch is a Python thread). `/health`
reports `busy_seconds` and `busy_limit_seconds` as before. The `300` and `8` are
generous and **unbenchmarked**: tune them (`WHISPER_TRANSCRIBE_MIN_TIMEOUT_S`,
`WHISPER_TRANSCRIBE_TIMEOUT_FACTOR`) once a real CPU has been timed. The upload endpoints
(`/transcribe`, `/transcribe-batch`) get the same length-derived limit and also
honour the request-deadline header, so a client that has given up does not keep a
decode slot. A child that is still loading its model has 300 s after a restart; the
first start has an hour, because it can include the model download.

`--num-workers N` now means N decode processes, so the old worry that the
underlying library is not thread-safe at `num_workers > 1` does not arise: nothing
shares a model across threads.

### Windows setup

1. Install `uv` (`winget install --id astral-sh.uv`) and ffmpeg
   (`winget install Gyan.FFmpeg`; the path pipeline cuts clips with it, and
   `/health` shows `ffmpeg` when it is found).
2. Make the library reachable from the Windows account that runs the worker, and
   prefer a **UNC path** (`\\arkham\audio\lib`) to a mapped drive letter: a scheduled
   task does not see drives mapped in another logon session.
3. Set the path map. The **server** side is the production server's POSIX path;
   the **local** side is a drive path or a UNC share:

   ```
   WHISPER_PATH_MAP=/srv/library=\\arkham\audio\lib
   WHISPER_PATH_MAP=/srv/library=X:\library
   ```

   The local side must not be relative, drive-relative (`X:dir`), a bare drive
   (`X:`, which means "the current directory on X"), a bare `\\host` with no share,
   contain `..`, or start with `\\?\`. A drive path (`X:\library`), a drive root
   (`X:\`) or a share (`\\host\share`) is accepted.
4. Install and start the supervised task (from your clone; it copies the four
   server files and the supervisor into your profile folder):

   ```powershell
   powershell -ExecutionPolicy Bypass -File scripts\install-whisper-supervisor.ps1 `
       -Device cpu -PathMap "/srv/library=\\arkham\audio\lib"
   ```

   Unset options take the defaults above, including `-BindHost 127.0.0.1`. To serve
   the LAN add `-BindHost 0.0.0.0` and read "Who can reach the worker" first.
   The supervisor restarts a crashed server with a doubling delay (5 s up to
   5 min), starts the count over after a run that answered `/health` and then stayed
   up two minutes or more, and **gives up after 8 crashes in a row**
   (`-MaxCrashStreak`; about 10 minutes of waiting, the delays being 5, 10, 20, 40, 80,
   160 and 300 s, plus however long each start ran before failing: a start that hangs
   in a model load for an hour counts once and adds the hour), logging FATAL and
   exiting 1. A run that never answered `/health` never resets the count. It
   logs to `whisper-supervisor.log` in the profile folder.
   (The earlier "20 restarts in 10 minutes" breaker could never trip: the backoff
   fits at most 7 restarts into any ten minutes.)

`scripts/manage-whisper-server.py` is an alternative deploy over WinRM (`--deploy`,
`--restart`, `--status`). It copies the four files in 1200-byte chunks and checks
the SHA-256 of each, because WinRM carries a script on a command line of at most
8191 characters, so a 47 KB file cannot go in one command (the largest command it
sends is about 5 KB). `--status` is a plain HTTP request and needs no Windows
account. The port is `WHISPER_PORT` (default 19847) for the health check, for
finding the old process, and for the launched server.

### Windows path rules

The server's paths are POSIX, so this is where a Windows worker can differ:

- the check for "still a mount point" is POSIX-only; on Windows a lost share shows
  as `readable: false` because listing the root fails;
- a server path with a component Windows cannot name (a `:` or `\` in a file name,
  a trailing dot or space, a reserved device name such as `CON` or `NUL.m4b`, or a
  name over 255 characters) answers **`path_unrepresentable`**, a code of its own,
  distinct from `path_unmapped` (outside the mapped prefix, `..`, a symlink out of
  the root). The book exists on the server, so the server retries it every run and
  never records a failure, but **this worker cannot transcribe it**; it needs a
  worker whose filesystem can name the file. Today the Go side treats an unknown
  code as an ordinary retryable worker error, so the book is retried on whichever
  worker comes next; routing it deliberately by the worker's `path_flavor` is a
  filed follow-up. The safety rule itself is not loosened;
- containment under the mapped root is case-insensitive, as NTFS and SMB are;
- paths longer than about 240 characters are handed to ffmpeg in the `\\?\` form;
- ffmpeg is started with `-protocol_whitelist file`, so a planted file cannot make
  it open a network URL.

### Verify

1. On the worker: `curl.exe -s http://127.0.0.1:19847/health`. Expect
   `"path_pipeline": true`, `"device": "cpu"` (or `cuda`), and every mapped root
   `"readable": true` in `path_roots`. If `readable` is `false` the account running
   the worker cannot list the share: fix that first.
2. From the server through the SSH tunnel (a loopback worker answers nowhere else).
3. One dry-run dispatch of `maintenance.transcribe-book-intros` (a dry run is the
   default): it names the endpoints it would use and the mode. Then run it for a
   handful of books and look at the transcripts and the time per book before
   turning the worker loose.

An endpoint configured with `require_gpu` refuses a CPU worker (its `/health`
`device` is not in the GPU allow-list). Leave `require_gpu` off for a CPU worker.

### Not verified

Nothing in this section has been run on a real Windows machine with a real GPU or a
real desktop CPU. What was run: the shared pipeline and server tests on macOS and
Linux (a fake backend that hangs, crashes and exits, in a real spawned child), the
same tests on a `windows-latest` GitHub runner (`whisper-windows.yml`), a CPU model
(`tiny.en`) end to end on macOS, and the supervisor's breaker with a fake `uv` under
PowerShell for macOS. Not run: a Scheduled Task, WinRM, a UNC share, NTFS name
rules, CUDA.

### Request limits (both workers)

Added with the shared pipeline, well above anything the server sends: at most 1024
jobs and 512-character keys per `/transcribe-path-batch` (422), a path of at most
4096 characters (a per-job `bad_request`), and a request body of at most 8 MiB
(413). A request-deadline header longer than 12 digits is capped to the maximum
rather than failing the request.
