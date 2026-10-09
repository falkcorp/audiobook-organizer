- [ ] **CI-GO-CACHE-STALE** Give the other Go jobs in `ci.yml` a build cache
      that refreshes. The shared `Restore Go cache (manual)` step is keyed on
      `go.sum` alone, so after one job saves it for a `go.sum`, every later job
      hits the exact key and `actions/cache` never saves again; the entry then
      holds only the first saver's build objects (not `-race`). Found on 07-C3
      (PR #3882, 2026-10-09): each short-test shard recompiled the whole module
      graph race-instrumented, 9 min 18 s before the first test ran. The shard
      job got its own day-stamped race cache (`actions/cache/restore` +
      `cache/save` on `main` once a day); `Fixture Tests (no -short, race)`
      (8 min), `Errcheck Ratchet` (3.5 min), `Mock Freshness`, `Repo Guards` and
      `Minimal CI / Go Vet & Build` (5 min) still pay the same recompile every
      run. Done when each restores a cache that contains its own flavour of
      build objects and the day's second run of each is measurably shorter.
