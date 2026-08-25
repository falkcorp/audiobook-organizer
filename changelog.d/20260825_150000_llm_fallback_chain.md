### Added

- **A scan no longer loses its filename parsing when the AI service is
  unreachable.** Setting `llm_mode` to `openai-fallback-local` now makes the
  scan try the local AI backend when the remote one cannot be reached, instead
  of giving up on those books. That setting has existed for months but did
  nothing — it behaved exactly like the plain `openai` setting, so choosing it
  bought nothing.

  Being unreachable and being refused are treated differently, which is the
  point. A service that cannot be reached might be reachable somewhere else, so
  it is worth asking the other backend. A rejected request — a wrong or expired
  key, or an exhausted quota — is wrong everywhere, so it stops immediately
  rather than being re-asked once per batch for the rest of the scan.

  When no backend can answer, the scan finishes and moves on to its remaining
  steps rather than stalling, and the affected books keep the metadata derived
  from their filenames.
