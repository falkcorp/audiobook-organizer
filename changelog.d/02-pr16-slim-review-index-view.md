### Changed

- The Review page's metadata index (`GET .../metadata/cache/review?view=index`) no longer carries each candidate's `score_breakdown` or `category_tags`; the evidence panel already reads the breakdown from the per-page `ids=` detail rows. On the 40,000-book synthetic benchmark (39,017 reviewable rows, an 8-step breakdown per candidate) the index response drops from 84.1 MB to 22.4 MB.
- While a review page's full rows are still being fetched, the evidence panel shows a loading note instead of claiming the candidate has no recorded derivation; a failed fetch shows a distinct "could not be loaded" note.
