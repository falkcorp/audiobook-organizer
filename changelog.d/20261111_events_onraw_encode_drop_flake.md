### Fixed

- `TestOnRaw_EncodeDropLogsNoPayload` no longer fails intermittently under `-race`. It treated the encode-drop counter as proof the drop warning had been written, but the bus bumps the counter before it logs, so the test could read an empty log. It now waits for the warning itself before asserting its cause and that no payload value reached it. The no-payload assertion is unchanged.
