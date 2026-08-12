- [ ] **`BookFile.Duration` is `int` seconds, so every per-track duration is truncated and
  `startOffset` drift compounds through a book.** Found by turning on value comparison in
  the ABS conformance suite (#2337), measured against the real *Odyssey* capture in
  `testdata/abs-fixtures/get_api_items_id.json`:

  ```
  oracle sum(audioFiles.duration) : 9975.431111
  int-truncated sum (ours)        : 9973          → 2.431 s short
  oracle startOffsets : [0, 1386.06, 2788.70, 4309.21, 6928.98, 8602.20]
  ours (int seconds)  : [0, 1386,    2788,    4308,    6927,    8600   ]
                                                       ↑ 2.200 s drift by track 6
  ```

  `startOffset` is cumulative, so error accumulates at roughly 0.4 s per track boundary —
  this item has 6 files but its own tags say the work is 24 parts, which would drift on
  the order of 10 s by the final track. A client that seeks using `startOffset` lands
  progressively further off the deeper into the book the listener is.

  `internal/database/store.go:696` — `Duration int`. There is no millisecond field on
  `BookFile` (`AcoustIDFingerprintDurationSec` immediately below it *is* `float64`, so
  sub-second precision is already understood to matter elsewhere in the same struct).
  `mapper.go:217` widens it back out with `DurationSec: float64(f.Duration)`, which cannot
  recover what the store never held.

  Not fixed alongside the conformance work by owner decision on 2026-08-12: changing a core
  production field's type touches the store, the mappers, the importers and needs a
  backfill, and had no business riding along with test-fixture changes. The affected
  conformance assertions carry **bounded** allowances (they still fail if a duration is
  wrong by more than the known truncation), so this stays visible rather than becoming
  permanently accepted.

- [ ] **`/api/me/sessions` reports `itemsPerPage` as the item count, not the page size.**
  `internal/server/handlers/abs/me.go:132` sets `ItemsPerPage: len(out)` alongside
  `NumPages: 1` — the endpoint does not paginate at all. The oracle returns
  `itemsPerPage=10, total=3, numPages=1`, i.e. the default page size with a short page.
  The sibling handlers in the same package get this right:
  `stats.go:108` and `stats.go:133` both use `queryInt(c, "itemsPerPage", 10)`.

  A client computing `numPages = ceil(total / itemsPerPage)` gets the right answer today
  only by accident, because `total` is also `len(out)`. Any client that trusts
  `itemsPerPage` as a page size — to decide whether to request a second page — is being
  told the wrong number.

  Not fixed with the conformance work because the honest fix is either to report the
  requested page size while still returning everything (self-inconsistent) or to actually
  paginate the endpoint (a live behaviour change for any user with more sessions than one
  page). That is a product call, not a test-fixture call. Found by #2337's value gate.

- [ ] **`deviceInfo.deviceType` is always `"unknown"`.** We never derive it from the
  User-Agent; the oracle capture reported `wearable`. Unlike `ipAddress`/`userAgent` —
  which the conformance normalizer treats as caller artifacts precisely because
  `me.go:127` *does* populate them for real — this one is a genuine unimplemented field,
  so it is named in an allowance rather than normalized away. Low priority; nothing is
  known to read it.

- [ ] **`publishedYear` loses the era: `Book.PrintYear` is an `int`, so the oracle's
  `"800BC"` comes back `"800"`.** ABS passes the raw date tag through. Same shape of loss
  as the duration truncation above — a typed column cannot hold what the tag said. The
  `publishedDecades` filter facet inherits it. Only visible on pre-CE material, so this is
  genuinely low priority, but it is the same class of bug and worth recording as such.

- [ ] **`timeBase` is hardcoded `"1/1000"` at `internal/server/handlers/abs/mapper.go:645`**
  where the oracle carries ffprobe's real stream `1/14112000`. We do not capture stream
  `time_base` at import, so there is nothing to map from. Owner decision 2026-08-12: allow
  it with a documented permanent allowance rather than add an ingest field and backfill for
  a value no client is known to divide by. Revisit only if a client turns out to use it.
