### Fixed

- The `embed_cover_art` setting now controls whether cover art is embedded into audio files during apply and write-back; it previously did nothing. The default is on, so nothing changes unless someone turns it off. A stored off value saved before the setting worked is reset to on once; turning it off after this release sticks.
