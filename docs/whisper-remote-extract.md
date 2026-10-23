<!-- file: docs/whisper-remote-extract.md -->
<!-- version: 1.0.0 -->
<!-- guid: 38f7ba56-da3d-4ab2-8b9c-839df9cb5612 -->
<!-- last-edited: 2026-10-22 -->

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
  book -> /mnt/bigdata/books/.../01.m4b
  POST /transcribe-path-batch  ------------>  WHISPER_PATH_MAP: /mnt/bigdata -> ~/mnt/bigdata
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
mkdir -p ~/mnt/bigdata
mount_nfs -o vers=3,ro,soft,noresvport,nolocks <server-host>:/mnt/bigdata ~/mnt/bigdata
ls ~/mnt/bigdata | head          # must list the library
```

`<server-host>` is "the server" (the host that exports `/mnt/bigdata`). The
mount is read-only on purpose: the worker never writes to the library.

To mount it at every login and re-mount it if it drops, use a user LaunchAgent.
Save as `~/Library/LaunchAgents/com.jdfalk.bigdata-nfs.plist` (replace
`<server-host>`; the paths are absolute because launchd does not expand `~`):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.jdfalk.bigdata-nfs</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>mkdir -p /Users/jdfalk/mnt/bigdata &amp;&amp; { /sbin/mount | /usr/bin/grep -q ' on /Users/jdfalk/mnt/bigdata ' || /sbin/mount_nfs -o vers=3,ro,soft,noresvport,nolocks &lt;server-host&gt;:/mnt/bigdata /Users/jdfalk/mnt/bigdata; }</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>StartInterval</key><integer>300</integer>
  <key>StandardErrorPath</key><string>/Users/jdfalk/Library/Logs/bigdata-nfs.err</string>
</dict>
</plist>
```

`StartInterval` re-runs the script every 5 minutes; it does nothing while the
mount is present and re-mounts after a drop. Load it with
`launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.jdfalk.bigdata-nfs.plist`.

### 2. Give every worker `WHISPER_PATH_MAP`

```
WHISPER_PATH_MAP=/mnt/bigdata=/Users/jdfalk/mnt/bigdata
```

Format: comma-separated `server-path=mac-path` pairs. Both sides absolute, no
`..`, neither `/`. Longest server prefix wins. There is **no default**: unset
means the worker advertises `path_pipeline: false` and refuses every path job.
A malformed value does the same and says why in `/health` (`path_map_error`).

The workers are the launchd agents `com.jdfalk.whisper-mlx` (port 19848) and
`com.jdfalk.whisper-mlx-19849`, `-19850`, `-19851`. Add the variable to each
plist's `EnvironmentVariables` dictionary (do it in your own editor, or with
`/usr/libexec/PlistBuddy -c 'Add :EnvironmentVariables:WHISPER_PATH_MAP string /mnt/bigdata=/Users/jdfalk/mnt/bigdata' <plist>`).

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
`--whisper-path-map /mnt/bigdata=/Users/jdfalk/mnt/bigdata` whenever you re-run
it, or the variable is dropped.

### 3. Verify

On the Mac:

```bash
curl -s http://127.0.0.1:19848/health | python3 -m json.tool
```

Expect `"path_pipeline": true`, `"path_prefixes": ["/mnt/bigdata"]`, and
`"path_roots": [{"prefix": "/mnt/bigdata", "readable": true}]`. Check all four
ports, and check from the server through the tunnel too (`127.0.0.1:19848` ...
`19851` there).

`readable` is checked live with a directory listing. A shell in Terminal can
read the volume under Terminal's own privacy grant, which says nothing about
the launchd agent, so **trust the agent's `/health`**, not your shell. If it
says `readable: false` while `ls ~/mnt/bigdata` works, macOS is denying the
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
  before). A *timeout* is not a decode verdict and is recorded as the retryable
  `whisper_error`.
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
