- [ ] **LIBRARY-SEARCH-DEADLINE** Library search (`internal/audiobooks/filter_compiled.go`)
      now shares the `querygrammar` size limits that #3908 added (256-byte values,
      100-instruction regex and glob programs), but it still has no time limit. A
      pattern under the size cap can cost a few hundred ms over the full library.
      For example, `(?:.?){30}zzz` is 65 instructions and took about 250 ms over
      40k short titles. Give library filtering the same detached, server-side
      evaluation deadline that the Review query got (`reviewQueryEvalDeadline`,
      checked every 256 rows). Return a clear 400 telling the user to simplify,
      never a partial list.
