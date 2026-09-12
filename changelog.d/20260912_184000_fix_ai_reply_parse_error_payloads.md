### Fixed

- **An error reply from the AI backend no longer marks books as parsed.** A
  batch reply with an `error` key next to `results`, or a wrapper holding an
  error object (`{"error": {...}}`, `{"error": [...]}`), was read as "nothing
  found", so every book in the batch was stamped as attempted and never
  AI-parsed again. `results` is now accepted only as the reply's only key, and
  every unwrapped object must be `null`, `{}` or a metadata object. An object
  whose `error` or `errors` key holds a value is an error even when it also
  has metadata keys, so `{"title": null, "error": "rate limited"}` no longer
  reads as "nothing found" and `{"title": "Solo", "error": "x"}` no longer
  saves the title. An empty `error` or `errors` value (`null`, `[]` or `{}`)
  next to metadata keys is ignored, so `{"title": "Solo", "error": null}` is a
  normal result; any other value, `""` included, is an error. A reply that is
  only `{"error": null}` or `{"errors": []}` is still an error. This applies
  to every result,
  including a bare object in a one-filename batch and the single-book
  parser's top-level object. Unrelated extra keys such as `filename` are
  still accepted.
- Metadata keys are matched without regard to case, as the JSON decoder does,
  so a reply using `"Title"` or `"Author"` is accepted again.
- **A reply that repeats a key is an error.** The JSON decoder keeps only the
  last copy of a repeated key, so key order decided the outcome:
  `{"results": [{"title": "A"}], "results": []}` read as "nothing found", and
  `{"title": "Solo", "error": "x", "error": null}` saved the title. A repeated
  `results`, `error`, `errors` or metadata key (compared without case, so
  `title` and `Title` count), or any other key spelled the same twice, now
  fails the reply, in the reply object and in every result.
- **Model-written text can no longer abort the AI phase.** A reply the parser
  cannot decode now returns a typed `ReplyParseError`, which the
  permanent-failure check skips before it reads any text. Before, a filename
  echoed into a malformed reply that contained a marker such as
  `permission_error` stopped the whole phase and the parser chain. A malformed
  reply now falls through to the next backend and counts toward the normal
  three-failure limit. The check walks wrapped and joined errors: reply text
  is never matched even when a caller wraps the error with `%w`, and a real
  permanent failure joined with a reply error is still permanent.
- Reply excerpts in the logs also escape Unicode format characters and line
  and paragraph separators.
