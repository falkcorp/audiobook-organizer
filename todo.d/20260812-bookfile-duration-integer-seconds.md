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

- [ ] **`deviceInfo.deviceType` is always `"unknown"`, and the capture cannot tell us what
  it should be.** `play.go:307` defaults it to `"unknown"` and then echoes whatever the
  client sent (`play.go:315`), so a client that supplies `deviceType` is already handled —
  the gap is only that we never *derive* it. Real ABS derives it from the User-Agent, and
  the oracle answered `"wearable"` for a request whose body carried only `clientName` and
  `deviceId`.

  **Blocked on evidence, not effort: 0 of 28 fixtures record request headers at all**, so
  the User-Agent that produced `"wearable"` is not preserved anywhere. Inferring a
  UA→deviceType rule from one output with no recorded input is exactly the single-sample
  mistake that produced the retracted `tagTrack` finding. Unblocking this means teaching
  the capture harness in `testdata/abs-oracle/` to record request headers and re-capturing;
  the derivation is then a small, testable mapping. Low priority regardless — nothing in
  the client contract reads `deviceType`; it is diagnostic, shown in the sessions list.

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
