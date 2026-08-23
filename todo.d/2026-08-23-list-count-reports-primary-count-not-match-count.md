- [ ] **`/api/v1/audiobooks` reports the PRIMARY count as `count` whenever no
      filter is set — off by 14,986 on production.** Second consumer of the same
      `CountPrimaryBooks` substitution already tracked above for
      `audiobook_organizer_books_total`. Measured 2026-08-23 against
      the production server:

      | query | `count` | actual stream |
      |---|---:|---:|
      | `?limit=1` | 56,725 | 56,725 ✅ |
      | `?limit=1&show_quarantined=true` | **41,741** | **56,727** ❌ |

      Asking to *include* quarantined books makes the reported total **drop by
      14,984**, which is backwards — the superset reports fewer rows than the
      subset. The stream is fine: a 250-row page fetched with
      `show_quarantined=true` held **240 non-primary books**. Only the counter
      is wrong.

      **Mechanism.** `buildAudiobookListResponse`
      (`internal/server/audiobooks_helpers.go`) sets
      `filters.ExcludeQuarantined = true` when `show_quarantined` is absent,
      then tests `hasFilters := filters.IsPrimaryVersion != nil ||
      filters.ExcludeQuarantined || ...`. So the flag that is set *by omitting*
      a parameter is itself counted as a filter, and the branch selects a
      different **counter**, not a different predicate:

      - omit → `hasFilters=true` → `CountAudiobooksFiltered` → correct
      - `show_quarantined=true` → `hasFilters=false` → `CountAudiobooks` →
        `store.CountPrimaryBooks()` → *"primary, **non-deleted** books"*

      `CountAudiobooks` (`internal/audiobooks/service_single.go`) is documented
      as *"the total count of audiobooks"* but delegates straight to
      `CountPrimaryBooks`. The name and the doc comment both promise a total and
      neither delivers one — that mismatch is the actual defect, and it is why
      the same wrong number reached two unrelated consumers.

      **Why it matters beyond a wrong number.** This is the exact "count !=
      items" defect the comment directly above `hasFilters` was written to
      prevent (*"Dropping quarantined rows AFTER pagination made a 500-page
      return fewer than 500 and made count != items"*). It was fixed on the
      predicate axis and reintroduced on the counter axis. Any client paging
      until `len(fetched) >= count` silently truncates at 41,741 — which is not
      hypothetical: `tools/cmd/orphan-nonprimary-census` did exactly that until
      #2809, and its `-min-expected` positive control only guards the low end,
      so a 14,986-book truncation would have passed it.

      **Fix direction.** Make the promise match the delivery rather than
      patching the call site:
      - rename `CountAudiobooks` → `CountPrimaryAudiobooks` (and fix the doc
        comment) so no future caller reads "total" and gets "primary"; then
      - give the unfiltered list branch a counter that actually counts the rows
        it is about to stream, and
      - decide whether the Prometheus gauge above wants the primary count (then
        reword its help text) or a true total (then repoint it).
      A regression test should assert `count == len(items)` for an unpaginated
      request on both the `show_quarantined` and default paths — the two
      currently disagree and nothing catches it.
