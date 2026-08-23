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
