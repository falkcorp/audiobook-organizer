### Fixed

- **The cached-metadata listing now honours `limit` and `offset`.** Asking it for
  five entries returned all 40,485 of them in a 7.35 MB response: the endpoint
  accepted the paging parameters and then ignored them, so it looked supported
  from the outside and nothing pointed at it. It now returns the page that was
  asked for, and reports the size of the whole matching set alongside it so a
  caller can still say how much there is to page through.

  Filtering happens before paging, which is the part that had to be got right:
  a book's review status is not stored with its cached candidates, so asking for
  "five books awaiting review" has to work out which books those are before it
  can count to five. Asking for a page of pending books now returns a full page
  of pending books rather than however many of the first five happened to
  qualify.

- **Rows are now returned in a stable order.** The listing was sorted by fetch
  time alone, and entries written by the same batch share a timestamp, so rows
  that tied could come back in a different order on each call — enough to hand a
  client the same row twice while skipping another as it paged through.
