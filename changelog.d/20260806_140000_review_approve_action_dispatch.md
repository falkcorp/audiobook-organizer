<!-- file: changelog.d/20260806_140000_review_approve_action_dispatch.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d9b71c6-58ea-4f27-9c04-6b1e2af8d035 -->
<!-- last-edited: 2026-08-06 -->

### Changed

- **Approving a review hold now runs the action a human chose, not the one its
  category implies.** Until now the review queue dispatched approve on
  `ReviewItem.Kind` — the shape the classifier saw. A Kind and the right action are
  not the same question: a `regroup.multidisc` hold means "these files sit in disc
  folders", which is true of three production holds whose members are each a
  full-length novel, because the disc and chapter branches of the classifier never
  evaluated its series guard. Approving those under Kind dispatch would have merged
  distinct novels through an apply path that hard-deletes the absorbed rows.

  Each hold now carries the classifier's `recommendedAction`, its reason, and the
  arithmetic behind it (member count, how many runtimes are known, how many are
  book-length, median and longest runtime, distinct title stems). Approve resolves
  an action — the explicit `{"action": "..."}` in the request body when a reviewer
  made a choice, otherwise the hold's own recommendation — and dispatches on that.

  🔑 **A typo is a 400, never a fallback.** Approving with an action outside the
  closed vocabulary (`combine`, `separate`, `version-group`, `duplicate-of`) is
  refused rather than quietly downgraded to the recommendation. Someone who meant
  "leave these six novels apart" and mistyped it must not be handed "combine".

  Three more refusals, all deliberate:

  - `insufficient-evidence` cannot be approved at all. It is the machine saying "I
    cannot tell", not a decision a human can pick, and the holds carrying it are
    exactly the ones with the least evidence.
  - Every hold written before this change decodes with no recommended action, which
    resolves to `insufficient-evidence`. Old holds are refused until a reviewer names
    an action explicitly — they are never dispatched to a merge on the strength of a
    field they do not carry. There is no backfill and no migration; a re-scan
    refreshes a still-pending hold's payload.
  - `duplicate-of` returns a "not implemented" error instead of marking the hold
    decided while doing nothing. "Decided" is sticky — a re-scan never re-offers a
    non-pending hold.

  `separate` needs no apply handler and never will: every member is already its own
  book, so the decision is a status transition and the queue's dedup-key idempotency
  is what keeps it decided across re-scans.

  Bulk approve uses **each item's own** recommendation rather than one action for the
  whole batch — a single action over a heterogeneous batch is precisely the footgun
  this change removes. Holds with no decidable action are reported in a `skipped`
  list with their reason instead of aborting the run.

  The four `Kind` strings are unchanged; they still describe shape and are still what
  the review UI maps. `review_apply_enabled` remains off, and approving with it off
  still records the decision without executing anything.

  ⚠️ **One widening worth knowing about.** Under Kind dispatch `regroup.ambiguous`
  had no apply handler and so could never merge anything. Keyed by action, an
  ambiguous hold whose evidence recommends `combine` now reaches the combine path.
  That is intended — the recommendation is evidence-backed where the Kind was only a
  shape, and it refuses `combine` unless a strict majority of members have a known
  runtime and those runtimes are short — but the gate is now the evidence, not the
  category.
