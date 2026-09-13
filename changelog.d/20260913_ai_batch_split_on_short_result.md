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
