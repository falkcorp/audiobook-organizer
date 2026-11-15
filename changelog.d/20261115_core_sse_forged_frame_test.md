### Fixed

#### `core/internal/sse` — forged-frame test no longer fails when the timestamp holds 999

`TestServe_FramesAreOneDataLine` flagged any output containing `999` as a forged frame id, but the stream-opened frame carries a nanosecond timestamp that sometimes contains those digits, so the test failed by chance on unrelated PRs. It now parses the output as SSE lines and fails only on a real forged field, and runs each case on a fixed clock whose nanoseconds are all 9s.
