`maintenance.transcribe-book-intros` runs again on a server that may not decode
audio. A new `remote_extract` mode, on by default when `ALLOW_SERVER_DECODE` is
unset and a Whisper worker advertises `path_pipeline`, sends each book's source
path to the worker, which cuts the first 90 seconds from its own NFS mount and
transcribes it; the server runs no ffmpeg and writes no clip cache, and the
silence retries (300 s clip, second audio file) go through the worker too.
`serverdecode.Check` now guards only the local-extraction path. The MLX worker
(`scripts/whisper_mlx_server.py`) gains `POST /transcribe-path` and
`/transcribe-path-batch`, driven by the new `WHISPER_PATH_MAP` variable (no
default), and `/health` reports `path_pipeline`, the mapped prefixes and
whether each mapped root is readable. The Go client gains
`transcribe.TranscribeByPathBatch` (same pool, journal and progress as the
upload path; `transcribePool` and `allocateJobs` are now generic over the job
type) and fails closed with a `TransportError`, deferring the page and writing
no status, when no endpoint qualifies or a mount is lost. Worker errors map to
`ffmpeg_error`, `source_file_missing` (only when the server cannot stat the file
either), a new non-durable `path_unmapped` status with its own
`stats:transcribe` counter, or the retryable `whisper_error`. See
`docs/whisper-remote-extract.md` for the mount and `WHISPER_PATH_MAP` setup.
