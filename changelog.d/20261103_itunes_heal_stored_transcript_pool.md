### Changed

- iTunes heal's transcription layer now reads the intro transcript the intro-transcribe ops already stored (the first file's row, then the book's) and only otherwise sends a 30-second clip to the self-hosted Whisper pool. The in-process `uv run openai-whisper` backend and the paid OpenAI `whisper-1` fallback are removed; a pool outage counts as a transcription failure for that candidate and stores nothing. A stored `[SILENCE]` sentinel is treated as "known silent": the candidate is left unscored with no pool call.
