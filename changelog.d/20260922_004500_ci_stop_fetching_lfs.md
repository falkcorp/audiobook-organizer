### Fixed

- CI no longer fetches Git LFS. Every job set `lfs: true` on checkout and pulled
  1.8 GB of LibriVox audio fixtures, on 23 checkout steps across 13 workflows —
  and not one of those workflows reads `testdata/audio`. A single pull-request
  push cost roughly 36 GB of LFS bandwidth, which exhausted the repository's LFS
  budget and failed every job at the checkout step, on pull requests and on
  `main` alike. Fixture-dependent tests will skip until the fixtures are fetched
  on demand, which is what their skip messages already describe
  ("LFS-on-demand; run git lfs pull to enable").
