### Wire `WithRequestTimeout` to config — a cold model load is 84% of the 30s budget

`internal/ai/embedding_client.go`'s `defaultRequestTimeout` is a hardcoded
`30 * time.Second`. `WithRequestTimeout` already exists and already clamps `0`
back to that constant, so the plumbing is half-built — it is just never fed
from config the way `ai_backend.parse_batch_timeout_seconds` now is.

Measured 2026-09-09 on the Mac M1 Max that prod's `embedding.base_url` now
points at:

| bge-m3 request | wall |
|---|---|
| cold load, nothing in the OS page cache | **25.28 s** |
| same call, file cached | 1.2 s |
| warm n=1 / n=16 / n=64 | 0.09 s / 0.28 s / 1.04 s |

So the steady-state headroom is large (a full 64-input `embedChunkSize` costs
1.04 s against 30 s) and the risk is entirely in the **first** call after Ollama
evicts the model. Default Ollama keep-alive is 5 minutes; the Mac is currently
set to `OLLAMA_KEEP_ALIVE=30m`, which makes eviction rare but not impossible,
and that setting does **not** survive a reboot (see below).

This is deliberately filed rather than fixed — no production path is failing,
and the 25.28 s figure was a one-time first-load from disk that did not
reproduce on reload. Raising the constant for every backend would be the wrong
fix. The right one is to plumb it, so a slow-disk or cold-start backend can be
given a longer budget without changing the OpenAI path.

- [ ] Add an `embedding.request_timeout_seconds` config key, resolve it the way
      `config.ResolveAIParseBatch` resolves the parse pair (0 = default, with a
      sane ceiling), and pass it through to `WithRequestTimeout` at the
      construction sites.
- [ ] While there: `OLLAMA_KEEP_ALIVE=30m` on the Mac was set with
      `launchctl setenv`, because `brew services` **regenerates**
      `~/Library/LaunchAgents/homebrew.mxcl.ollama.plist` on every start and
      silently discarded a `PlistBuddy` edit to it. `launchctl setenv` does not
      survive a reboot, so keep-alive reverts to 5 minutes on the Mac's next
      boot. Needs a durable mechanism (login item, or a brew-services override
      that survives regeneration) if we keep depending on warm models.
