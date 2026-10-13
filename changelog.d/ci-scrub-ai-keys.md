### Fixed

#### CI no longer calls paid AI providers when a host has an API key set

Every step on the heavy (local-backend) Woodpecker agents sources `scripts/ci/cache_env.sh` first, and that script now unsets `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `ACOUSTID_API_KEY`, `GOOGLE_BOOKS_API_KEY`, `HARDCOVER_API_TOKEN` and `WHISPER_LIVE_URL`. A Mac CI agent passed an OpenAI key to its steps, so the live OpenAI integration tests ran and failed on 429, blocking merges.
