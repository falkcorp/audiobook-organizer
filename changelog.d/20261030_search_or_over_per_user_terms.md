### Fixed

#### OR and grouped NOT over per-user terms are evaluated, not refused

A search, dynamic collection or smart playlist that put a per-user term
(`read_status`, `progress_pct`, `last_played`) under an `OR` with a sibling, or under a
`NOT` over a group, used to be evaluated as an `AND` (`title:Alpha OR read_status:finished`
found only Alpha books the viewer had finished) and, since the shape check landed, refused with
a 400. They are now answered as written for the signed-in viewer: each per-user term becomes an
index query over the viewer's own state rows (`search.MixedQuery`) and the index evaluates the
whole expression, so scoring, paging and the match count are unchanged. Plain conjunctions, a
term negated on its own and one field's alternation keep their existing path. The value checks
(unknown status, pattern, bad number or date) still apply, and `tag:` under an `OR` is still
refused. `search.CheckPerUserBoolean` and `search.ErrPerUserBoolean` are removed.
While the search index is starting up or rebuilding, the text-only fallback cannot evaluate
such a query and refuses it with a 400 rather than answering it as plain text.
