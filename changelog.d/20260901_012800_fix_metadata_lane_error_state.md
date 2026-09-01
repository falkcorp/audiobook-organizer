### Fixed

- **The metadata review lane no longer reports a failed load as an empty queue.**
  A failed request to the cached-review endpoint was swallowed entirely — no
  error state, no toast, not even a console line — so a 500, a hung request and a
  genuinely empty cache all rendered the same screen: "No metadata matches to
  review. Search providers from the Metadata menu to find some." That advice
  cannot help when the server is down. The lane now carries an error, the panel
  renders it in an Alert with a **Retry** button (matching the dupes and regroup
  lanes), and the spine shows a loading state while a request is in flight
  instead of the empty copy.
