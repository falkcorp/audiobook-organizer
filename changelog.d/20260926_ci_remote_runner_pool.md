### Added

#### `make ci-remote` runs local CI across the configured runner nodes

`make ci-remote` (`scripts/ci_remote.py`) runs the same gates as `make ci`, but
sends the heavy legs to the nodes listed in `CI_NODES`, instead of running
everything on the developer's Mac. Several agents running `make ci` at once had
pushed the Mac's load to about 150 and made packages time out that pass on their
own.

- It pushes the commit to each node's bare repo by explicit refspec and checks
  it out into a job directory for that run. Each node shares one Go build cache
  and one module cache.
- `go test -short -race` is split by package and balanced by runtime measured
  per OS. Vet, staticcheck, sdkguard, bench-check, fmt-check, the errcheck
  ratchet and the web tests run as separate legs.
- The coverage profiles from all shards are merged before `coverage-check-short`
  runs.
- Tests that decode audio are never sent to a `nodecode` node. Those nodes run
  with a PATH that has no ffmpeg, ffprobe or fpcalc.
- A `prod` node runs at the lowest CPU and I/O priority on at most half its
  cores, and is skipped when its load is high. A node whose local Ollama is
  serving gets half its slots.
- Slot locks on each node make parallel agents queue instead of piling on.
- When no node is usable, it falls back to local `make ci`.

Setup and troubleshooting are in `docs/process/ci-remote.md`.
