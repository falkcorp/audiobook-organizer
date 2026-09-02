## `Book.author_name` is declared in TypeScript but the API never sends it

`web/src/services/api.ts` declares `author_name?: string` on the `Book` interface. The Go
`database.Book` has no such JSON tag — it marshals a nested `author` object — and the dedup
handler never sets the key. Because the field is optional, every read yields `undefined`
with no type error, and callers using `?? ''` swallow it silently.

This made the Dupes panel's client-side author search dead code for as long as it existed.
Found while moving that search server-side; the server now resolves author through the
author table, so the panel works, but the type still promises a field that never arrives.

- [ ] Decide: populate `author_name` server-side, or drop it from the TS interface
- [ ] Grep for other readers of `book.author_name` that are silently getting `undefined`
- [ ] Prefer whichever option makes the absence a compile error rather than an empty string
