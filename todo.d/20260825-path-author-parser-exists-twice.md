## The path→author parser exists twice, the copies have diverged, and only one of them runs

`internal/scanner` and `internal/metadata` each carry a complete copy of the
filename/directory author parser: `extractFromFilename` / `extractInfoFromPath`,
`parseFilenameForAuthor`, `extractAuthorFromDirectory`, `looksLikePersonName`.

**`internal/metadata` runs first.** `internal/scanner` calls its own
`extractInfoFromPath` only when `Author` is still empty (`scanner.go:1446`), and
by that point metadata has already populated it. So on the ordinary path, the
scanner's copy is dead code.

**This already caused a shipped-and-caught defect.** PR #2888 fixed the
`Unknown Author` laundering in the scanner copy only. It was **inert** — it never
executed on the path that produces the bug — and worse, it would have opened the
AI nomination gate, called the LLM, and discarded the answer, since
`runAIBatchPhase` only fills fields that are still empty. A review pass caught it
before merge. See `docs/audits/2026-08-25-unknown-author-feedback-loop.md`.

**The copies genuinely differ in behaviour**, so this is a correctness issue, not
tidiness. Measured 2026-08-25 on
`.../Unknown Author/Pratchett 036/Pratchett 036 - Unknown Author.mp3`:

- `internal/metadata`'s `extractAuthorFromDirectory` validates the directory name
  and rejects `Pratchett 036`.
- `internal/scanner`'s does not, and returns it — attributing the book to its own
  title.

Same input, different author, depending on which copy got there first.

- [ ] Collapse the two into one parser (its own package, as
      `internal/authorname` and `internal/trackseq` already are), consulting
      `authorname.IsPlaceholder`, and delete both copies.
- [ ] Reconcile the divergent directory validation deliberately rather than
      picking one by accident — the metadata behaviour is the safer of the two.
- [ ] Add a conformance test over a shared corpus, in the shape of
      `internal/trackseq`'s, so the two cannot drift again if they are not fully
      merged.

Related: `todo.d/20260825-directory-fallback-reads-title-as-author.md` — the
directory fallback's positional assumption is wrong under the organizer's
`<root>/<author>/<title>/<file>` layout, and should be settled as part of this.
