### Fixed

- **An error reply from the AI backend no longer marks books as parsed.** A
  batch reply with an `error` key next to `results`, or a wrapper holding an
  error object (`{"error": {...}}`, `{"error": [...]}`), was read as "nothing
  found", so every book in the batch was stamped as attempted and never
  AI-parsed again. `results` is now accepted only as the reply's only key, and
  every unwrapped object must be `null`, `{}` or a metadata object.
- **Model-written text can no longer abort the AI phase.** A reply the parser
  cannot decode now returns a typed `ReplyParseError`, which the
  permanent-failure check skips before it reads any text. Before, a filename
  echoed into a malformed reply that contained a marker such as
  `permission_error` stopped the whole phase and the parser chain. A malformed
  reply now falls through to the next backend and counts toward the normal
  three-failure limit.
- Reply excerpts in the logs also escape Unicode format characters and line
  and paragraph separators.
