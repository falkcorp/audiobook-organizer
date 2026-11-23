### Fixed

- The notify delivery test `TestDeliver_PublisherCtxCancelDoesNotCancelDelivery` no longer fails under load. It waited for the fake channel to record a call and then read what the send had seen of its context, but the fake records the call before it runs the send, so the read could land first and report a missing context. It now waits for the send to report what it saw. Test only; delivery code is unchanged.
