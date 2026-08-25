## Finish the LLM fallback chain — stages 2 through 4

Stage 1 landed: `parserChain` in `internal/scanner/ai_parser_chain.go`, wired
from `newAIParser` so `llm_mode=openai-fallback-local` builds a real chain
instead of silently behaving as plain `openai`. Unreachable falls through;
permanently-refused does not.

What is NOT done, in the order it should be done:

- [ ] **Stage 2 — make the local rung start a backend.** Today the local rung's
      `ensure` only constructs a client against an already-running endpoint; if
      nothing is listening it declines. `internal/tools/ollama_daemon.go` already
      has start-on-demand, adopt-across-restarts and stop-when-idle, but it is
      wired only for embeddings. Reuse it, and add a refcount so
      `StopWhenIdle` cannot kill a daemon another consumer adopted.
- [ ] **Stage 3 — durable deferral.** When no rung answers, the candidates are
      currently just left unparsed; the only thing that re-nominates them is a
      human running another scan. Record them as owed a parse.
      🚨 **The scan-cache stamp must not be written for work that was only
      promised.** The stamp is what tells the next scan the file is settled, so
      stamping a deferred book converts a temporary outage into permanent data
      loss — it is never re-nominated by any path, ever. The existing abort path
      is already correct here (it stamps only inside the success branch); Stage 3
      must preserve that property deliberately, not by accident. A test must
      assert the stamp is ABSENT after a fully-deferred phase, and be
      mutation-checked by writing the stamp anyway.
      The persistence needs a store method — `internal/database` is another
      session's lane, so specify the shape and ask rather than writing it.
- [ ] **Stage 4 — poll for the remote and drain what is owed.**
      🚨 **An in-memory ticker's ceiling is process uptime.** This deployment
      restarted 146 times in 30 days, so a long-interval ticker fires zero times
      while logging a perfectly healthy schedule. Persist a `last_probed_at` row
      and compute "is it due?" from that on every startup and tick — never from
      time-since-process-start.
      Drain via the existing `library.ai-parse` operation and
      `saveAIFieldsToPrimary`, NOT the scan's `saveBook`: organize may have moved
      or demoted the row in the meantime.

Design notes and the full test strategy are in the worktree's `PLAN.md`
(`feat/llm-fallback-chain`).

Deliberately deferred: auto-pulling a local model. A multi-GB download mid-scan
is a decision, not a fallback. If it is ever added it needs its own explicit
setting, defaulting off.
