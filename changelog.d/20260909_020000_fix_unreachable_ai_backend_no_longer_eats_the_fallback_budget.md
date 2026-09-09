### Fixed

- **An AI backend that is down no longer prevents the fallback backend from
  being tried.** The filename-parsing chain exists so that one unreachable LLM
  does not cost a scan its metadata, and it decides whether to try the next
  backend by how much of the request's 30-second budget is left. But a dial that
  failed — connection refused, no route to host, DNS failure — was being retried
  three times with 2s and 8s of backoff between attempts, which used the whole
  budget up. Every remaining backend was then recorded as "skipped, no time
  left" without being asked, so the chain built precisely for an unreachable
  remote was defeated by the retry loop underneath it.

  Failures that never established a connection are now reported immediately
  instead of retried: there is no partial state to lose, and the right retry for
  "the backend is down" is the next backend or the next run, not ten seconds of
  waiting that the fallback needs. A dead host now costs about five seconds
  rather than thirty, leaving the chain enough budget to reach every rung.

  This is deliberately kept distinct from a permanently-refused request (a
  revoked key, an exhausted quota), which still stops the whole phase — an
  unreachable host says nothing about whether the request itself is valid.
