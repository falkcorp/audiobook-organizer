### Fixed

- A transcribe test (`TestIdentity_AMismatchDropsTheCachedProbe`) failed now and then in CI with "the probe was not cached". The per-endpoint cooldown registry is shared by the whole test process and keyed by URL, and test servers reuse ports, so a fake worker could be handed a port an earlier test had left benched; a benched endpoint is never cached. Every fake worker the probe-cache tests use now starts with a clean cooldown, and a regression test hands a server a deliberately benched port. No production code changed.
