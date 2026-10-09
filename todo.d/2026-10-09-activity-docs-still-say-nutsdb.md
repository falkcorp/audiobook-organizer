- [ ] **DOCS-ACTIVITY-BACKEND** Rewrite the live system docs that still describe
      the activity log as NutsDB with SQLite "disabled by default for
      pre-NutsDB deployments": `docs/system/storage.md` lines 147-172 (the
      "SQLite (opt-in legacy)" paragraph, line 163 is the sentence about the
      default), `docs/system/README.md:17`, `docs/system/architecture.md:37`
      and `:64`, `docs/system/components.md:143`,
      `.github/copilot-instructions.md:17`. Since 01-P1 (#3879, 2026-10-09)
      the default is Pebble and SQLite is the legacy opt-in until 01-P75
      removes it; `docs/architecture.md:18` and
      `docs/reference/config-api-shape.md` already say so. Found by the #3879
      review; the brief scoped docs to those two files, so this is the rest.
