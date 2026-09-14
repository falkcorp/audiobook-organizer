### Fixed

#### Applying metadata one book at a time no longer freezes for a minute per click

- Each metadata apply stands the library scan down. When the apply finished, the scan was re-queued at once, restarted, and reloaded all 156,952 works (~57s on production) before it checked for cancellation. The next click canceled it and had to wait for that reload to finish, so every click cost about a minute and the scan never got past its first folder.
- The scan's startup is now cancelable: the scan-cache load, the dirty-folder check and the works load all stop within a few milliseconds of a cancel, so a stand-down parks the scan promptly. Each startup phase also logs how long it took.
- A restarted scan reuses the previous run's works lookup map instead of reloading it, as long as no work row changed in between. Every writer of a work row (create, update, delete, prefix wipe, reset) bumps a works generation counter, and a map that doesn't match it is reloaded. An idle map is dropped after 30 minutes.
- After the last apply releases the scan, the re-queue now waits for a grace period (`scan_standdown_grace_seconds`, default 45). An apply that arrives inside the grace starts immediately, with no scan to park. The persisted stand-down marker is still cleared when the last apply finishes, so if the server restarts during the grace the startup resume sweep re-queues the parked scan.
