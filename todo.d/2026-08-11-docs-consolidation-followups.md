- [ ] 📚 **Docs consolidation follow-ups (from the 2026-08-11 inventory).** Full evidence in
      [`docs/audits/2026-08-11-docs-inventory.md`](docs/audits/2026-08-11-docs-inventory.md).
      Six items that a docs pass could not decide:

      1. **Resolve the two prod-run contradictions.** `TODO.md:4988` says the dedup prod
         drain was never executed; `docs/operations/pending-prod-actions.md:26` says it ran
         2026-07-18 (9,074→1,311). Same split on T04: `TODO.md:5311` unchecked vs
         `docs/dedup/STATUS.md:78-86` "EXECUTED ON PRODUCTION". Purgeable drifts 7,878 vs
         7,891. **Each record makes the other unfalsifiable** — only the owner knows which
         run actually happened. This is the ONLY thing blocking `dedup-pipeline-hardening`
         from being archivable.
      2. **Union-merge `docs/openapi.yaml` into `docs/api/openapi.json`.** They are two
         independently hand-maintained specs, neither generated. JSON has 117 paths the YAML
         lacks; **YAML has 25 the JSON lacks** (`/auth/login|logout|me|sessions*`,
         `/ai/scans*`). Picking a winner loses real surface.
      3. **Decide the 11 UNCERTAIN docs** (list in the inventory §4).
      4. **Classify `docs/system/**` (9) and `docs/architecture/**` (9)** — needed to settle
         whether the top-level architecture docs duplicate them.
      5. **Make `run-sweep.sh` fail loudly on a package it cannot parse.** It discovers work
         via `find -name 'TASK-*.md'` and 4 of 10 live packages have none, so it emits
         nothing — indistinguishable from "nothing to do".
      6. **Write headers for the CURRENT files still missing them** (the 76 fleet files are
         archived; the remainder are live docs).
