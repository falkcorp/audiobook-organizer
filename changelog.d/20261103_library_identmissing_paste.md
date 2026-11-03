### Fixed

- Web tests: `Library.identMissing.test.tsx` pastes the `ident:missing` query as one
  input event instead of typing it key by key. Each keystroke re-rendered the whole
  Library page, and the test hit vitest's 30 s timeout on a loaded CI runner.
  Locally it drops from about 1.5 s to 0.56 s.
