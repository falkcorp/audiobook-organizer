### Fixed

- **The four ABS write fixtures nothing was reading are now asserted, Content-Type
  included.** `patch_api_me_progress_id`, `patch_api_me_progress_batch_update`,
  `delete_api_me_progress_id` and `delete_api_me_item_id_bookmark_time` were captured
  from the real server and then referenced by zero tests for as long as they existed.
  Their routes were exercised — but those tests assert the status code and read the
  value back out of the store; only one looked at the response body, and none looked at
  the header.

  All six plain-text routes answer `200 OK` as `text/plain`, and the header is the part
  a client acts on first: gin takes Content-Type from the render call, so "improving"
  `respondPlainOK` into `c.JSON` would flip it to `application/json` while leaving the
  status at 200 and the body at `OK` — passing every test that existed and breaking
  every client that picks its decoder from the header. Verified by making exactly that
  change: all six sites fail, on the header alone, with the body untouched.

  Each request is now driven from the fixture's own recorded request rather than a
  hand-typed one, so the test cannot stop being the request that produced the recorded
  response. The recorded ETag is deliberately not asserted, and the reason is in the
  helper: all six captures carry the identical value because Express derives a weak
  ETag from the body and every one of these bodies is the same two bytes. It identifies
  the payload, not the resource, and nothing revalidates a response to a mutation.
