- [ ] **`POST /ai/test-connection` tests OpenAI unconditionally — it can never
      validate a `llm_mode=local` backend.** `AIHandler.TestConnection`
      (`internal/server/handlers/ai.go:307`) builds
      `ai.NewOpenAIParser(&config.AppConfig, apiKey, true)` and calls
      `parser.TestConnection`, with no branch on `llm_mode` /
      `EffectiveLLMMode()`. On a local-backend install it dials api.openai.com,
      400s when `openai_api_key` is empty, and 500s when the key is set but the
      call fails — and in neither case has it touched the configured Ollama
      endpoint.

      **Cost, measured 2026-09-09.** Prod ran `llm_mode=local` against a base URL
      with a one-digit typo *and* a GPU that crashed every model load. Through
      both faults this endpoint returned 500, and that 500 was read twice as
      evidence about the local backend. It is not evidence about the local
      backend at all. The two things that did work: `GET /ai/backends/status`
      (reports `local_reachable` + `fallback_reason`) and actually running
      `library.ai-parse` and reading `progress_message`.

      Note the two are not interchangeable — `/ai/backends/status` only calls
      Ollama's `/api/tags`, which lists models without loading one, so it went
      `local_reachable: true` on hardware where every single inference aborted.
      A real connection test has to load a model, not enumerate them.

      Fix: branch on the effective LLM mode and, in local mode, test the
      configured local endpoint with a minimal generate/embed call rather than a
      list call. Failing that, rename it to `/ai/test-openai-connection` so the
      name stops making a promise the code does not keep. Either way the UI
      button that calls it needs to say which backend it tested.
