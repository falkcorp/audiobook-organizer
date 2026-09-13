### Fixed

#### AI parse re-asks a batch in smaller pieces when the model returns the wrong number of results

`library.ai-parse` kept failing on the same batches: qwen2.5:7b-instruct
collapsed some 6-8 filename batches into a single object ("got 1 result(s) for
8 filename(s)"), did it again on every retry, and the whole batch was rejected
because results are matched to filenames by position. The AI phase now splits a
batch that fails with exactly that error (a new typed `ai.ResultCountError`) in
half and re-asks each half, down to single files. Books that parse are saved;
a book that still fails on its own is reported as a failed book with the reason
and is left for the next run. Transport errors, timeouts, other reply errors
and permanent failures are unchanged and never split. Extra calls per batch are
bounded at 2n-2 (14 for a batch of 8), made sequentially inside the batch's
worker with the usual 2s pause, so the model host never sees more requests in
flight than `parse_batch_workers`.

A file that fails on its own is recorded durably (a path-keyed give-up
marker in the raw KV store, with the last reason). After
`maxAIParseSingleFileFailures` (3) failed runs the batch AI parse skips it, so
a poisoned batch costs at most 3 x (2n-1) calls over its lifetime (45 at a
batch of 8) instead of 2n-1 on every run forever. Renaming or moving the file
makes it eligible again; the interactive `POST /ai/parse-filename` never
consults the marker. Skipped and newly given-up counts appear in the AI parse
summary.
