### Fixed

- Authority evidence for author credits (follow-ups to the #3741 review):
  - The `authority_evidence_enabled` flag is now read under the config lock, so
    a settings change can no longer race a credit resolve.
  - The background load of the authority lists is tracked by the server, so
    shutdown waits for it before the database closes. No new load starts once
    shutdown has begun.
  - With the flag on, the lists start loading at server start instead of on
    the first credit resolve.
  - Turning the flag off releases the in-memory lists. A resolve with the flag
    off never starts a load.
  - Once a credit part has weak evidence, the resolver skips the remaining
    sources that can only give weak evidence. This saves a book listing and a
    history read per part. A source that can give strong evidence is still
    asked.
  - An authority source that knows a name but has no entry for it gives weak
    evidence instead of crashing.
