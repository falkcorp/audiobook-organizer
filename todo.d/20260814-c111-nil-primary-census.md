## C111 census: the nil `is_primary_version` population is 5,702 (all ungrouped) — "22,552 nils" was a misread

Full-library page-through on production 2026-08-14 (63,839 rows, `omitempty`
distinguishes nil from false), cross-tabbed against `version_group_id`:

| flag  | has VG | count  |
|-------|--------|--------|
| true  | yes    | 35,586 |
| false | yes    | 22,510 |
| false | **no** | **41** |
| nil   | no     | 5,702  |
| nil   | yes    | 0      |
| true  | no     | 0      |

- **The structure is clean:** every version-grouped book carries an explicit
  flag; every nil book is a groupless singleton. The long-quoted "22,552 nil
  books" was actually the explicit-FALSE population (22,510+41 ≈ today's
  22,551 `is_primary_version=false` count).
- **The index path counts nil as TRUE** — proven by arithmetic, not code
  reading: `is_primary_version=true` answers 41,288 = 35,586 explicit-true
  + 5,702 nil.
- **Correction to `20260814-c716-api-store-gap-resolved.md`:** the
  `show_quarantined=true` bug drops the 22,551 **explicit-false** books
  (it silently applies primary=true when the filter is unset), NOT the nils.
- **The 41 false/no-VG books are C314's exact population** — ungrouped books
  stuck at explicit false, invisible in every primary-only view, electable
  to primary trivially (they have no group to conflict with).

**D-2 semantic the data recommends: nil = true** (an ungrouped book is its
own primary). Unify:
- [ ] Make every raw `*bool` post-filter treat nil as true (matching
      `effectiveBoolFieldIndex{Default: true}`), or
- [ ] better: backfill explicit `true` onto the 5,702 nil rows (dry-run
      gated) so nil ceases to exist, then make nil a validation error at
      write time.
- [ ] Fix the 41 ungrouped-false rows to true in the same op (C314).
- [ ] Re-run this census as the post-fix verification: expected end state is
      exactly two populations (true, false+VG).
