<!-- file: core/integrations/ai/openai/testdata/dialects/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f9d4b86-5c13-4e70-a8b2-6d1e7c3f0a94 -->
<!-- last-edited: 2026-11-12 -->

# Dialect fixtures for aitest.Conformance

One directory per server that speaks the OpenAI wire: `openai`, `ollama`,
`llamacpp`, `whispercpp`. Each file is the body that server answers for one
route (`models`, `chat`, `embeddings`, `transcription`; `openai` also has the
batch files). A route a dialect has no file for answers 404, as that server
would.

These are written from each server's documented response shapes, NOT captured
from a running server: no Ollama, llama.cpp or whisper.cpp server was available
when they were written. They exercise what differs between the servers and
trips a strict decoder: fields a server adds (`system_fingerprint`, llama.cpp's
`timings` and model `meta`, whisper segment internals), fields it omits
(`prompt_tokens_details`), and an empty transcript. They do not measure what a
real server does. That is the live run (`MEDIACORE_LIVE_AI=1`, see
`conformance_test.go`), which 05-ai section 11 item 1 makes the source of truth
for structured-output support per server version. Replace a file with a capture
by saving the server's body for that route unchanged.

All names are Batman-themed on purpose (no real people, titles or libraries).
