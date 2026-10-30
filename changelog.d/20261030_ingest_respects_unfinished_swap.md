### Fixed

- A download ingested into a book whose version swap did not finish (a version left in
  `swapping_out` / `swapping_in`, so the book has no active version) is now added as an `alt`
  version, with an Info log naming the stuck swap's version ids. It used to become a second
  active version, which made the later resume of the swap refuse with `SWAP_RESUME_REFUSED`
  and left the book needing a hand repair. `ResumeVersionSwap` itself needed no change: it
  already tolerates an `alt` version.
