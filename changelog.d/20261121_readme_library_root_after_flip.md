### Changed

#### README quick start no longer tells you to set AUDIOBOOK_ROOT_DIR

The Docker Compose quick start in `README.md` and `.github/README.md` told readers to set
`AUDIOBOOK_ROOT_DIR` in `.env`. After the settings flip that variable is retired and ignored with a
warning, so following the step left the library root unset. The quick start now says to set
`LIBRARY_HOST_DIR` (the host folder compose mounts at `/audiobooks`), then set the library root to
`/audiobooks` in the setup wizard or the Settings page. It also names the operator routes, `--dir` and
`ABO_CORE_LIBRARY_ROOTS`. Documentation only; merge after the settings flip.

#### .env.example names only current variables, and a test keeps it that way

`.env.example` carried 27 variables the flip retired. Eight of them (`ROOT_DIR`, `DATABASE_PATH`,
`BASIC_AUTH_ENABLED`, `BASIC_AUTH_USERNAME`, `BASIC_AUTH_PASSWORD`, `ENABLE_AUTH`, `ACOUSTID_API_KEY`,
`WHISPER_CLIP_CACHE_DIR`) stop the service from starting when set; the rest are ignored with a warning.
The basic-auth block was the worst: it said "set all three to protect the UI", but compose now passes only
the `ABO_CORE_HTTP_BASIC_GATE_*` names, so a user following the template got no gate. Every variable is now
its current `ABO_` name from `deploy/retired-env-names.txt`, body limits are converted from MiB to bytes,
the four provider base-URL overrides (which have no replacement) are gone, and `ROOT_DIR` became
`LIBRARY_HOST_DIR`, matching the compose mount. The comments say which variables compose passes into the
container. `scripts/tests/test_env_example_current_names.py` fails when the template names any retired
variable, commented-out lines included. It runs in the existing `unittest discover -s scripts` CI step.

#### seed command no longer points at a retired variable

`audiobook-organizer seed` told you to "pass --dir or set ROOT_DIR". `ROOT_DIR` now stops the service from
starting, so following the message broke boot. The error and the command help now point at `--dir` or the
library root setting (`ABO_CORE_LIBRARY_ROOTS`). This is the only part of the change that touches the binary.
