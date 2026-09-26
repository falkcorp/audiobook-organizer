- [ ] **LLM1-REMOTE-CI** Use `llm1` as a remote runner for local CI, so `make ci`
      no longer runs entirely on the Mac. On 2026-09-25 several agents ran `make ci` and
      `go test -race` in parallel on the Mac. Load hit about 150, and
      `internal/database` and `internal/server/handlers/abs` blew the 25-minute
      `test-short` timeout even though they pass on their own, so every agent's
      local gate read red for reasons unrelated to its change. Proposal: a
      `make ci-remote` (or `CI_HOST=llm1 make ci`) that syncs the worktree's
      commit to a checkout on llm1 (`git push` to a bare repo there, or `rsync`
      of the tree) and runs some or all of the gate there, streaming the log
      back and returning its exit code. Split options: run the Go `test-short
      -race` leg on llm1 while staticcheck/vet/frontend run on the Mac, or shard
      packages across both. It needs a pinned Go toolchain (`go1.27.1`) and Node
      on llm1, a per-worktree build cache and checkout so parallel agents don't
      collide, and a queue or lock so two agents don't run the same heavy leg at
      once. Must NOT touch llm1's Ollama/GPU workload: run at low priority
      (`nice`/`ionice`), and check whether AI jobs slow down while it runs.
      Keep private hostnames and IPs out of the committed Makefile. Read the
      host from `Makefile.local` or an env var.
