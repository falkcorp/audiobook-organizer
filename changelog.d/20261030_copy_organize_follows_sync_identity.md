### Fixed

- Re-organizing a book by COPY now carries the listener's Audiobookshelf sync id,
  per-file download ids, every user's position and book state onto the new primary
  copy (`Service.CreateOrganizedVersion` calls the new `merge.FollowVersionHandover`
  after the copy's rows are written and before the original is demoted). A book
  partway through no longer drops off Continue Listening and the visible copy no
  longer starts at zero. Positions saved without a sync id (the iTunes position
  backfill) move too. A failed follow is logged and never fails the organize.
