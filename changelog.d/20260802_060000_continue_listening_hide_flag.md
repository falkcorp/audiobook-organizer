<!-- file: changelog.d/20260802_060000_continue_listening_hide_flag.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e1b8a53-c027-4d96-b3f8-6015d9c2ae74 -->
<!-- last-edited: 2026-08-02 -->

### Fixed

- **"Remove from Continue Listening" had no effect on the Continue Listening shelf.**
  Phase 6 added a persisted `hideFromContinueListening` flag and the endpoints that
  set it, but `hasProgress` — which decides what goes on the `/personalized`
  Continue Listening shelf — only read the stored position and never consulted the
  flag. So the book reappeared on the next home-screen refresh and the feature looked
  broken, which is exactly how it was reported.

  Hiding deliberately **keeps** the position (the user tidied their shelf; they did
  not ask to lose their place), so the flag cannot be inferred from the absence of
  progress — it has to be read.

  Covered by a test asserting a hidden book leaves the shelf **and** keeps its
  `mediaProgress` entry, plus a companion test that `DELETE /api/me/progress/:id`
  removes the book from all three surfaces a client builds Continue Listening from
  (`/api/me`, `/api/me/progress`, `/personalized`).
