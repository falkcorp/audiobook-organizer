- [ ] **DEMO-RECORDING-BROKEN: `scripts/record_demo.js` fails at Phase 2 on `main` — the
      import path it POSTs is not on the allow-list.** The script writes its fixture into a
      temp directory and POSTs that `file_path` to `/api/v1/import/file`, which routes
      through `ImportFile` (`internal/server/handlers/filesystem.go`) →
      `ImportService.ImportFile` (`internal/importer/service.go`) →
      `fileops.ValidateUserPath` (`internal/fileops/service.go`). The allow-list
      (`defaultBrowseAllowPrefixes`) is `/home`, `/media`, `/mnt`, `/audiobooks`, `/data`,
      `/etc/audiobook-organizer`, plus `config.AppConfig.RootDir` and any registered import
      paths. **`/tmp` is not on it**, and `scripts/run_demo_recording.sh` starts the server
      with no `--dir` (so `RootDir` is empty) immediately after `/api/v1/system/reset` (so no
      import paths are registered). Result: `ErrPathNotAllowed` → HTTP 400 → the demo dies at
      Phase 2. This is pre-existing, not caused by #2798.
      **Two traps for whoever fixes it:** (a) since #2798 the script uses `os.tmpdir()`, which
      honours `TMPDIR` — on macOS that is `/var/folders/...`, NOT `/tmp`, so allow-listing
      `/tmp` alone will not fix it; (b) `mkdtempSync` creates the directory mode `0700`, so a
      server running as a different user (container, systemd unit) cannot read it even if the
      path is allowed. Prefer pointing the demo at a directory under an already-allowed prefix
      over widening the allow-list. Found by review on #2798.
