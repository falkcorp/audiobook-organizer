### Added

- The batch AI filename parse (`library.ai-parse`) now saves the release year
  the model returns instead of discarding it. It fills
  `audiobook_release_year` only when the book has none (a missing value or 0)
  and the field is not locked, the same gap-fill rule as the series position,
  and never overwrites an existing year. A year outside 1000 to next year is
  ignored and logged at Debug. Inside that range the model's year is taken as
  is, so it may be a copyright or recording year rather than the audiobook
  release year. The single-book AI parse endpoint is unchanged.
