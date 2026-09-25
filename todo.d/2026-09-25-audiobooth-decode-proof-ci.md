- [ ] **AUDIOBOOTH-DECODE-CI** Wire `make audiobooth-decode` into CI. The Go half
      (`TestAudioBoothFixtures_ReplayEveryAppRequest`) already runs in `make ci`,
      but the Swift decode only runs on a Mac with Swift 6.2+. Options: a macOS
      runner, or a `swift:6.2` Linux container. The Linux option is untested:
      swift-corelibs-foundation must compile the staged AudioBooth models, and its
      `URLComponents` must encode queries the way the Go replay assumes (it leaves
      `+` literal). Also decide whether CI should fail when the regenerated
      fixtures differ from the committed ones. They churn today, because session
      ids and timestamps are per-run.
- [ ] **ABS-COLLAPSESERIES** `GET /api/libraries/:id/items?collapseseries=1` is
      ignored: two books in one series come back as two rows and no
      `collapsedSeries` element is emitted (found by the AudioBooth decode proof,
      fixture `tests/audiobooth-decode/fixtures/items_collapse_series.json`).
      AudioBooth sends it when "Collapse series in library" is on
      (`LibraryPageModel.swift:338`). Implement it with the `Book.CollapsedSeries`
      shape the app decodes (`id`, `name`, `numBooks`, `libraryItemIds` required),
      then drop the manifest row's `vacuous` note and add a `nonEmpty` check.
