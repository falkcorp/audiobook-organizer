<!-- Comments only — no behaviour change, so this fragment is deliberately a
     no-op rather than a changelog entry. See changelog.d/README.md.

     Library.tsx's URL-writer effect reads `searchParams` as a staleness guard
     without depending on it, which react-hooks/exhaustive-deps reported as a
     missing dependency. The warning's suggested fix — add it to the array — is
     a change to a race guard whose correctness depends on effect declaration
     order, so the warning was an invitation to undo #2271.

     Replaced with an explicit eslint-disable-line carrying the reason. The
     dependency array itself is byte-identical to before; the diff is comments.

     The alternative was measured rather than assumed: with `searchParams` added
     to the array, library-sidebar-filters.spec.ts ran 36/36 green on webkit. It
     was still not adopted, because that effect owns URL writes for the whole
     Library page and one spec file is not evidence about the rest of it. That
     measurement is recorded in the code comment so the next person does not
     have to redo it. -->
