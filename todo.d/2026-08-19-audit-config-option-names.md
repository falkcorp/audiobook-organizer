## Config

- [ ] **Audit every config option name — the set has grown by accretion and the
      naming is inconsistent.** Prod's `/api/v1/config` currently returns **113**
      keys. They were added over a long period by different code paths, and nothing
      has ever reviewed them as a set, so the vocabulary drifted.

      The trigger: `write_back_metadata` is named for a whole subsystem but gates a
      single call site on the auto-fetch path, while the flag that actually controls
      tag writing on apply is `auto_write_tags_on_apply`. Reading the two together
      leads a reasonable person to the wrong conclusion about whether the app writes
      to their files. That one has its own entry above; this task is the sweep.

      **What to look for:**
      - *Scope lies* — a name broader than what it gates (the
        `write_back_metadata` class). Name the flag after its call site, not its
        subsystem.
      - *Asymmetric pairs* — two flags controlling the same behaviour on two paths
        that do not share a prefix (`..._on_fetch` / `..._on_apply`).
      - *Names that read as booleans but are not* — e.g. `organization_strategy`
        has the value `auto`, which sits next to `auto_organize` (a real bool)
        and invites exactly the confusion it caused today.
      - *Dead options* — a key that nothing reads. Verify by grepping the
        `AppConfig.<Field>` READ sites, not the field declaration: several keys are
        declared, bound to viper and persisted, yet never consulted. A key that
        cannot change behaviour should be deleted, not documented.
      - *Unclear units* — `auto_scan_debounce_seconds` is good; anything with a
        bare number and no unit suffix is not.

      **Method:** enumerate from the live `/api/v1/config` response, not from the
      struct — the struct carries fields the API does not expose and vice versa. For
      each key, find its READ sites and write down the one sentence that describes
      what it actually changes. Any key where that sentence does not match the name
      is a rename candidate.

      **Every rename needs the deprecated-alias migration** described in the
      `write_back_metadata` entry: live config is a persisted snapshot, so a bare
      rename silently reverts the setting to its default on next load.
