## CI / automation

- [ ] Decide whether the 22 `gha-*` repos (plus `magnet-handler`) should keep their
      classic branch protection. They all require PR reviews and share a
      `set-auto-merge` check, so they look like a deliberate template rather than
      drift — unlike audiobook-organizer, whose protection was removed 2026-08-20.
- [ ] Add a scheduled detect-only backstop for `auto-revert.yml`: if `main`'s tip has
      a failed gate run older than 30 minutes and no open auto-revert issue exists,
      file the issue. Covers the case where the `workflow_run` listener never fires
      (runner outage, cancelled run).
- [ ] `scripts/test_check_memory_leaks.py` is executed by no workflow. Either wire it
      into `repo-guards` next to the auto-revert selector tests, or delete it.
