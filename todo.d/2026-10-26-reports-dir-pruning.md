- [ ] **TODO-REPORTS-PRUNE** Prune `{library root}/.reports` — `backfill-file-hashes`
      writes one report per run named for its op ID (about 100 MB of TSV for a
      full-library run, and a rerun is a new op with a new file), and nothing ever
      removes them, so the directory grows without bound on the library volume.
      Decide a retention rule (keep the newest N per op kind, or by age), delete
      only regular files this op family wrote (never follow a symlink, never
      touch a `.pending` file of a live run), and log what was removed.
