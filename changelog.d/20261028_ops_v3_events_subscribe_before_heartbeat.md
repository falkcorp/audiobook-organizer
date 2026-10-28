Fixed the operations event stream (`/api/v3/ops/events`) losing events published
right after its opening heartbeat. The broker's loop is started by the first
client and used to subscribe to the registry's event hub from a goroutine that
ran whenever it was first scheduled, so on a busy machine anything published
between the heartbeat and that moment was dropped (the hub skips subscribers
that are not registered yet). The subscription is now made before the first
client's stream writes its first byte. This also removes the cause of a flaky
`TestOpsV3_EventStreamLeavesOutWhatTheCallerMayNotRead`.
