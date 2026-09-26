- [ ] **LLM-NODES-REMOTE-CI** Use the LLM nodes as a remote runner pool for local
      CI, so `make ci` no longer runs entirely on the Mac. More LLM nodes are
      coming, so no single host is hard-coded: the pool is whatever list of nodes
      is configured. On 2026-09-25 several agents ran `make ci` and `go test -race`
      in parallel on the Mac. Load hit about 150, and `internal/database` and
      `internal/server/handlers/abs` blew the 25-minute `test-short` timeout even
      though each passes on its own, so every agent's local gate read red for
      reasons unrelated to its change. Proposal: a `make ci-remote` that reads the
      node list (`CI_NODES` in `Makefile.local` or the environment; never committed,
      the repo is public) and syncs the worktree's commit to each node, by `git
      push` to a bare repo there or by `rsync`. It then shards the heavy legs across
      whichever nodes are free: split the Go `test-short -race` packages by
      measured runtime, while staticcheck, vet and frontend run on the Mac or a
      spare node. It streams each log back and returns a combined exit code. When
      no node is reachable it falls back to plain local `make ci`. Needs per node:
      the pinned Go toolchain (`go1.27.1`) and Node, a per-worktree checkout and
      build cache so parallel agents don't collide, and a lock or queue so two
      agents don't pile onto the same node. Must NOT starve a node's Ollama/GPU
      work: run at low priority (`nice`/`ionice`), skip or de-weight a node that
      is serving AI jobs, and check that AI throughput holds while it runs. New
      nodes should join by adding them to the list, with no code change.
