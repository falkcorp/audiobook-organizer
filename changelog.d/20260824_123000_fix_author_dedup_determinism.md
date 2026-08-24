### Fixed

#### Author dedup suggestions no longer change between identical scans

`FindDuplicateAuthors` walked its last-name buckets by ranging a Go map, which
has randomized iteration order. Its grouping is greedy — the first author of a
similar-name pair to be visited becomes the group's anchor and the other is
absorbed into it — so the *contents* of the suggested groups, not just their
order, differed from one scan to the next with no change to the underlying
library. Both grouping phases now iterate in sorted order, making the result a
function of the data alone.
