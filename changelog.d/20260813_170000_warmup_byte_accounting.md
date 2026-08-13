### Changed

#### Startup warmup now reports how many bytes it read, and how many it threw away

The memdb warmup log gained two fields, `phase_mb` and `discarded_mb`, alongside
the per-phase timings added earlier the same day.

Timing alone had taken the investigation as far as it could go. It established
that `book_files` is 89,357 ms of a 108,768 ms warmup — 82% — and that
`txn.Commit` is 13 ms, killing the theory that a single write transaction held
across all ten prefix scans was the cost. A production CPU profile then showed
61% of the phase inside `pebble.(*Iterator).Next`, nearly all of it in
`loadDataBlock`: Pebble is pulling a fresh sstable data block off disk for
almost every row, which is what happens when a data block holds only one or two
rows because the rows are enormous.

That points at `BookFile.AcoustIDFingerprint` — a `[]byte` stored inline in the
`book_file:` row, roughly 230 KB of raw chromaprint per two-hour file, which
`encoding/json` renders as ~307 KB of base64. `stripBookFileForMemdb` nils it
out the instant it has been decoded, so every one of those bytes is read,
decompressed, JSON-parsed and base64-decoded in order to be discarded.

Two very different fixes follow from that, and they are not compatible:

- if the blob really is most of the bytes, remove it from the row, which shrinks
  the work and pays out in five places at once (pread, cgo block decompression,
  block allocation, JSON, base64);
- if it is not, parallelize the scan, which divides the work without shrinking
  it.

The first is a large, data-loss-sensitive schema change. Rather than infer the
premise from a call graph, the warmup now measures it: `warmIter` sums the
length of every Pebble value it visits, and the `book_file:` callback charges
the base64-encoded length of the fields the projection is about to discard.
Cost is one integer add per key.

Two details are load-bearing and are pinned by tests. Discarded bytes are
counted in *encoded* length, because the scanned total counts raw JSON value
bytes — charging a decoded length against an encoded total would understate the
ratio by 4/3 and produce a number that looks precise and is wrong. And nonzero
megabyte totals round *up*, so a phase that read half a megabyte never prints as
`0` and becomes indistinguishable from a phase that read nothing.
