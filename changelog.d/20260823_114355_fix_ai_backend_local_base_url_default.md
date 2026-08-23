### Fixed

#### `ai_backend.local_base_url` no longer defaults to a developer's LAN IP

The fresh-install default for `ai_backend.local_base_url` was a hardcoded
address on one developer's LAN (`http://192.168.0.20:11434/v1`, their own
Ollama host). `ResetToDefaults()` repeated the same hardcoded value. Every
other install silently inherited it as a "configured" local endpoint:
`EffectiveLLMMode()` treats any non-empty `LocalBaseURL` as proof a local
backend is set up and selects local mode, so a fresh install on someone
else's network would try to reach a dead address instead of falling back to
OpenAI or disabled. Both defaults are now the empty string. Verified
`EffectiveLLMMode`/`EffectiveEmbeddingMode` fall through cleanly to the
non-local mode when `LocalBaseURL` is empty — added test coverage for that
fall-through and for the empty fresh-install default.

#### Deriving a paid LLM backend from a bare API key is no longer possible

Removing the hardcoded address made a second, latent problem live. With no
local endpoint configured, `EffectiveLLMMode()` fell through to
`AIBackendModeOpenAI` whenever an API key was present and any LLM consumer was
enabled — and `enable_ai_parsing` defaults to **true**. That is the exact shape
of the 2026-08-16 incident recorded in that function's own comment, where a
blank config silently selected a paid service and ran a library scan until the
account hit `credit_balance_exhausted`.

That fallback was deliberate and documented, but it had been effectively
unreachable on a default install: the hardcoded LAN address always won the
local branch first. Removing the address would have armed it for every install
with a key and no local endpoint.

Derivation now stops at `disabled`. Opting in to a paid backend is explicit
only — set `ai_backend.llm_mode = "openai"`, which returns before any
derivation runs. **This is a behaviour change** for any install that relied on
the key-plus-consumer fallback and had explicitly cleared
`ai_backend.local_base_url`; such installs must now set `llm_mode` to keep
using OpenAI. Two existing tests that asserted the old derivation were updated.

Not changed: `EffectiveEmbeddingMode()` has the same bare-key fallback shape
(`embedding.enabled` also defaults true). It reads `embedding.base_url`, which
already defaulted to empty, so this change does not affect it — the exposure
there is pre-existing and is left for its own task.
