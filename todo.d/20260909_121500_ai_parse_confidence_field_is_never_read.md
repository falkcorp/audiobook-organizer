- [ ] **`ParsedMetadata.Confidence` is asked for, paid for, and never read —
      decide whether to gate saves on it or stop requesting it.** The
      `ParseBatch` system prompt in `internal/ai/openai_parser.go` instructs the
      model to return `"confidence": "high|medium|low"` and to "set confidence
      based on clarity of the filename structure", and `ParsedMetadata` declares
      the field. Nothing in `internal/ai/` or `internal/scanner/` ever reads it:
      grepping `Confidence` across both packages returns only the struct tag
      itself and the unrelated `dedup_review.go` type. Every parsed field is
      written regardless of what the model said about its own certainty.

      **⚠️ This is NOT a data-loss issue — do not read it as one.**
      `saveAIFieldsToPrimary` (`internal/scanner/ai_parse_async.go`) writes each
      field only when that field is empty — `row.Title == ""`,
      `row.AuthorID == nil`, `row.SeriesID == nil`, `row.SeriesSequence == nil`,
      `isBlankPtr(row.Narrator)`, `isBlankPtr(row.Publisher)` — and a user
      field-lock check fails closed on top of that. It is gap-fill: it cannot
      overwrite a title, author, series, sequence, narrator or publisher that
      already exists, and anything it fills can be corrected afterwards. The
      question here is "should we fill an empty field with a guess this weak?",
      not "is something being destroyed?".

      **It is live, though.** `enable_ai_parsing` is true in prod,
      `llm_mode=local`, and a library scan is running. `scanner.go` prefers to
      enqueue a `library.ai-parse` op per chunk (falling back to the inline
      `runAIBatchPhase` only when the enqueue fails), and that op saves through
      `saveAIFieldsToPrimary` against real book IDs. Before 2026-09-09 the phase
      failed every time — 81 consecutive zero-parse runs — so it wrote nothing
      and the missing filter cost nothing. The fixes landed that day made it
      work, so the accuracy below now reaches empty fields on real rows.

      **The field does predict correctness — it was worth measuring.** Same 40
      filenames as the 7B-vs-3B comparison, series-number accuracy broken out by
      the model's own self-reported confidence:

      | confidence | qwen2.5:7b-instruct | qwen2.5:3b-instruct |
      |---|---|---|
      | high | 1/1 (100%) | 1/1 (100%) |
      | medium | 10/13 (77%) | 5/7 (71%) |
      | low | 3/8 (38%) | 4/13 (31%) |

      Monotonic for both models. For the 7B, **5 of the 8 total errors sit in
      the `low` bucket**, so dropping `low` writes would remove 5 wrong values
      at the cost of 3 correct ones — a 2.6:1 trade in favour of filtering.
      Weigh it as "a wrong guess in a previously empty field vs. leaving that
      field empty", since gap-fill is all this path can do: nothing downstream
      distinguishes "the AI was unsure" from "the AI was confident and wrong",
      so a bad `low` write looks exactly like real metadata afterwards.

      Two options:

      1. **Gate the save on it.** Skip `low` fields, or route them to a review
         queue rather than the book row. Still needs one decision: should a
         low-confidence title mean "leave the existing title alone" or "don't
         save the row at all"? The numbers above say the gate is worth having;
         they do not say which of those two it should be.
      2. **Delete it from the prompt and the struct.** Defensible only if
         option 1 is rejected outright — on a CPU backend completion tokens are
         the entire cost model, and an unread field is paid for on every batch.

      Do NOT leave it as-is on the assumption that something downstream checks
      it. Nothing does, and the sample says something should.
