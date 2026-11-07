### Fixed

- Tests: `TestOpsV3Events_RoleLoweredMidStreamStopsTheRuns` no longer fails intermittently under load. It took its "before the demotion" baseline while a Repairs run event from the broker's same flush was still queued, then counted that event as delivered after the demotion. The test now settles the stream deterministically (two ordered plain-run events, so the event queue is provably drained) before and after lowering the role; the assertion is unchanged and the stream code is untouched.
