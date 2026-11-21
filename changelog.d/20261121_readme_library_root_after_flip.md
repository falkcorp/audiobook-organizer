### Changed

#### README quick start no longer tells you to set AUDIOBOOK_ROOT_DIR

The Docker Compose quick start in `README.md` and `.github/README.md` told readers to set
`AUDIOBOOK_ROOT_DIR` in `.env`. After the settings flip that variable is retired and ignored with a
warning, so following the step left the library root unset. The quick start now says to set
`LIBRARY_HOST_DIR` (the host folder compose mounts at `/audiobooks`), then set the library root to
`/audiobooks` in the setup wizard or the Settings page. It also names the operator routes, `--dir` and
`ABO_CORE_LIBRARY_ROOTS`. Documentation only; merge after the settings flip.
