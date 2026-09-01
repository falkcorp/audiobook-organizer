### Fixed

- **The regroup review panel's counts stay honest under a server-side search.**
  Three numbers had been derived on the assumption that searching never left the
  browser, and pushing the term to the server made each of them wrong: the
  "N pending" chip showed the match count instead of the queue (measured: "1
  pending" over 728 holds), every bucket raised a warning-coloured "partial view"
  chip on any search that narrowed anything, and the browser's own filter could
  subtract rows the server had matched — including holds found by a word from the
  recommendation sentence the reviewer reads on the row, which the browser does
  not index. The search box's label and helper text, which still told reviewers it
  matched "the loaded page only", now say it searches the queue.
