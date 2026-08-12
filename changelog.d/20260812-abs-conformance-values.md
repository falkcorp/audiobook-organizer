### Changed

- **The ABS conformance suite now checks values, not just shape.** All 22 golden-fixture
  assertions ran with `conformance.Options{IgnoreExtra: true}` and no `CompareValues`,
  which compared key presence and type and nothing else — a handler that returned every
  correct key with entirely wrong data passed. The suite read as a conformance gate and
  gated almost nothing.

  Ten endpoints already met the oracle exactly and are now pinned to it: ping, status,
  libraries, series, authors, narrators, paginated authors, and the three bookmark
  routes.

- **Strictness is now the default, and weakening it is what costs effort.** The previous
  arrangement had this backwards: a newly added endpoint got the weak check for free and
  turning it up required someone to remember. The twelve call sites that cannot meet the
  oracle yet must now name themselves via `assertConformantPending` **and give a written
  reason** — an empty reason fails the test. Those twelve are the remaining work, they
  are enumerated in the source rather than in a tracking doc, and the count cannot drift
  upward quietly.

  Their failures are not one problem. Eight are fixture drift: the fake library's
  multi-file book is synthetic (six identical 1662 s tracks, 2049–2054 byte files,
  `timeBase 1/1000`) where the oracle captured a real recording of *The Odyssey* (six
  distinct durations summing to 9975.48, files of 11–21 MB, `timeBase 1/14112000`).
  Four are identity divergences we may well intend to keep — `user.type`, `Source`,
  `permissions.upload`, `permissions.createEreader`.

  Two are neither, and would have stayed invisible under shape-only checking: we emit
  the raw ID3 `TRCK` value `4/24` in `metaTags.tagTrack` where AudiobookShelf normalizes
  it to `4`, and we send the tag title for an audio track where AudiobookShelf sends the
  filename. Both are live mapper behaviour rather than test data.
