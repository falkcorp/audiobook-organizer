### Fixed

- A transcription dispatch cancelled while the endpoint gate was probing no longer carries on as if nothing happened. The cut-short probe left a remembered (canary-failed) lone whisper machine unrecognised, so the dispatch sent it every job unasked and returned no error; it now returns the cancellation and sends nothing. This also removes an intermittent failure of `TestSingle_ACancelDuringTheCooldownWaitReturnsTheCancellation` on loaded CI agents.
