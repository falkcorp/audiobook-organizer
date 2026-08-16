### Fixed

- **Library scans no longer get killed while they are still working.** The
  directory-discovery and directory-scanning phases of a scan reported no
  progress at all, so on a large import folder the scan looked identical to a
  hung process and the stuck-operation watchdog killed it after five minutes.
  The 2026-08-16 rescan died this way mid-walk of a folder holding 17,469
  books. Both phases now check in every 20 directories.

- **The scanner honors the configured LLM backend.** It previously built the
  cloud OpenAI parser whenever AI parsing was enabled, ignoring
  `ai_backend.llm_mode` entirely — so a deployment configured for a local
  Ollama endpoint, or for no LLM at all, still sent every scan batch to
  `api.openai.com`. Local, OpenAI, and disabled modes are now all respected,
  matching the `llmparser` service registration.
