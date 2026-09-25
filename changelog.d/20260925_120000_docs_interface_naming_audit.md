### Added

- **Interface naming consistency audit.** Documented every external interface
  in the codebase — HTTP routes, v2 operation definitions, JSON parameter and
  response field names, config keys, and Go `*Store` interface names — for
  naming inconsistencies, with file:line citations for each finding and a
  migration-cost estimate per class. No renames were made; see
  `docs/audits/2026-09-25-interface-naming-consistency.md`. Highlights:
  `/books/*` vs `/audiobooks/*` addressing the same resource; five different
  verbs (`merge`/`combine`/`link`/`split`/"merge as versions") for what are
  three underlying operations; `dismiss` vs `reject` vs `undo` for the same
  "reverse a decision" action; and a dry-run parameter convention that is
  inconsistent in a safety-relevant way — some v2 ops default to a live run
  when the dry-run flag is omitted, others default to a safe preview. Config
  keys and operation-ID lexical form (kebab-case, dot-namespace) were the one
  class found fully consistent.
