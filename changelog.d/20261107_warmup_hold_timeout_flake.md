### Fixed

- The warmup-hold timeout test no longer fails when a busy CI agent is slow to start the operation after warmup ends. It now proves the hold leaves the timeout untouched by giving the operation a timeout shorter than the hold, so a counted hold still fails every time.
