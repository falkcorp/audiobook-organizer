### Fixed

#### CI no longer calls paid AI providers when a host has an API key set

Every Go step in the Woodpecker workflows sources `scripts/ci/cache_env.sh` first, and that script now unsets `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `ACOUSTID_API_KEY`, `GOOGLE_BOOKS_API_KEY` and `HARDCOVER_API_KEY`. A Mac CI agent passed an OpenAI key to its steps, so the live OpenAI integration tests ran and failed on 429, blocking merges.
